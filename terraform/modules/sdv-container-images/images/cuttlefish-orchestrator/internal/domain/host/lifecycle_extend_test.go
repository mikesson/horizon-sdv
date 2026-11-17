// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package host

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func reservedHost(expires time.Time) *Host {
	return &Host{
		ID: "h1", Status: StatusRunning, Origin: OriginUser,
		Lifecycle: HostLifecycle{Mode: LifecycleModeEphemeral, TTLSeconds: 3600, ExpiresAt: &expires},
	}
}

// An extend counts from the later of now and the current expiry. Counting from
// now would cut a lease that device raises had already pushed past it.
func TestExtendIsMonotonic(t *testing.T) {
	t.Parallel()
	now := time.Now()

	ahead := now.Add(10 * time.Hour)
	h := reservedHost(ahead)
	if err := h.Extend(time.Hour, now); err != nil {
		t.Fatal(err)
	}
	if want := ahead.Add(time.Hour); !h.Lifecycle.ExpiresAt.Equal(want) {
		t.Errorf("extend of a lease 10h out = %v, want %v (from the lease, not from now)", h.Lifecycle.ExpiresAt, want)
	}

	lapsed := now.Add(-time.Hour)
	h = reservedHost(lapsed)
	if err := h.Extend(time.Hour, now); err != nil {
		t.Fatal(err)
	}
	if want := now.Add(time.Hour); !h.Lifecycle.ExpiresAt.Equal(want) {
		t.Errorf("extend of a lapsed lease = %v, want %v (from now, never still in the past)", h.Lifecycle.ExpiresAt, want)
	}
}

// A host the reaper already marked deleting is being torn down. Accepting an
// extend would answer 200 for a lease that ends with the deprovision.
func TestExtendRefusesADeletingOrDeletedHost(t *testing.T) {
	t.Parallel()
	for _, status := range []Status{StatusDeleting, StatusDeleted} {
		expires := time.Now().Add(time.Hour)
		h := reservedHost(expires)
		h.Status = status

		err := h.Extend(time.Hour, time.Now())
		if !errors.Is(err, ErrInvalidState) {
			t.Fatalf("extend of a %s host = %v, want ErrInvalidState", status, err)
		}
		if !h.Lifecycle.ExpiresAt.Equal(expires) {
			t.Errorf("refused extend of a %s host moved expires_at to %v", status, h.Lifecycle.ExpiresAt)
		}
	}
}

// A protected host's nil expiry is the protected encoding. Writing a timestamp
// there would make the reaper's listing see an ephemeral-looking lease.
func TestExtendRefusesAProtectedHost(t *testing.T) {
	t.Parallel()
	h := reservedHost(time.Now())
	h.Lifecycle = HostLifecycle{Mode: LifecycleModeProtected, ProtectedDeviceCount: 2}

	err := h.Extend(time.Hour, time.Now())
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("extend of a protected host = %v, want ErrInvalidState", err)
	}
	if !strings.Contains(err.Error(), "protected by 2 pinned devices") {
		t.Errorf("refusal %q does not name the pinned devices", err)
	}
	if h.Lifecycle.ExpiresAt != nil {
		t.Errorf("refused extend wrote expires_at = %v on a protected host", h.Lifecycle.ExpiresAt)
	}
}

// Only a reserved host takes an extend: the fleet keeper, collapse and batch
// settlement own the other classes' leases and would overwrite it. The refusal
// names the class and points at the device commands.
func TestExtendRefusesManagedClasses(t *testing.T) {
	t.Parallel()
	cases := map[Class]*Host{
		ClassFleet:     {Origin: OriginPool},
		ClassDedicated: {Origin: OriginAuto, Exclusive: true},
		ClassBatch:     {Origin: OriginBatch, BatchID: "b1"},
	}
	for class, h := range cases {
		expires := time.Now().Add(time.Hour)
		h.ID, h.Status = "h1", StatusRunning
		h.Lifecycle = HostLifecycle{Mode: LifecycleModeEphemeral, ExpiresAt: &expires}
		err := h.Extend(time.Hour, time.Now())
		if !errors.Is(err, ErrInvalidState) {
			t.Errorf("%s: extend = %v, want ErrInvalidState", class, err)
			continue
		}
		for _, want := range []string{string(class), "cfctl device extend", "cfctl device pin"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: refusal %q is missing %q", class, err, want)
			}
		}
		if !h.Lifecycle.ExpiresAt.Equal(expires) {
			t.Errorf("%s: refused extend moved the lease", class)
		}
	}

	// A user's exclusive host is reserved and takes the extend.
	h := reservedHost(time.Now().Add(time.Hour))
	h.Exclusive = true
	if err := h.Extend(time.Hour, time.Now()); err != nil {
		t.Errorf("extend of a user's exclusive host refused: %v", err)
	}
}

func TestExtendRefusesATerminalHost(t *testing.T) {
	t.Parallel()
	h := reservedHost(time.Now().Add(time.Hour))
	h.Status = StatusDeleted
	if err := h.Extend(time.Hour, time.Now()); !errors.Is(err, ErrInvalidState) {
		t.Errorf("extend of a deleted host = %v, want ErrInvalidState", err)
	}
}

// The horizon: a new expiry plus the grace must not pass the VM's stop time,
// and an unobserved VM (nil) passes.
func TestExtendHonorsTheVMHorizon(t *testing.T) {
	t.Parallel()
	now := time.Now()
	expires := now.Add(time.Hour)
	terminates := now.Add(2 * time.Hour)

	h := reservedHost(expires)
	h.VMTerminatesAt = &terminates
	err := h.Extend(time.Hour, now)
	horizon, ok := errors.AsType[*VMHorizonError](err)
	if !ok || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("extend whose grace passes the VM stop = %v, want a *VMHorizonError matching ErrInvalidState", err)
	}
	if !horizon.TerminatesAt.Equal(terminates) || !strings.Contains(err.Error(), "Restarting the host resets") {
		t.Errorf("refusal %q must give the stop time and say a restart resets it", err)
	}
	if !h.Lifecycle.ExpiresAt.Equal(expires) {
		t.Errorf("refused extend moved the lease to %v", h.Lifecycle.ExpiresAt)
	}

	h = reservedHost(expires)
	if err := h.Extend(1000*time.Hour, now); err != nil {
		t.Errorf("extend with no observed VM stop refused: %v", err)
	}
}
