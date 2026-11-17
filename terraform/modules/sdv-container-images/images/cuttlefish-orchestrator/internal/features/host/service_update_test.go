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

package hostfeature

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// countingHostRepo records how many times a write was attempted, so a no-op
// capacity change can be shown to write nothing rather than write the same
// value back (which would land an audit entry per save).
type countingHostRepo struct {
	*atomicHostRepo
	mutations int
}

func (f *countingHostRepo) Mutate(ctx context.Context, id string, fn func(*host.Host) error, extra ...*spanner.Mutation) (*host.Host, error) {
	f.mutations++
	return f.atomicHostRepo.Mutate(ctx, id, fn, extra...)
}

// live is the number of INSTANCE slots the host's live devices desire, which is
// what max_devices bounds, and never a count of device rows.
func newCapacityService(seed *host.Host, live int64, liveErr error) (*HostService, *countingHostRepo) {
	repo := &countingHostRepo{atomicHostRepo: newAtomicHostRepo(seed)}
	svc := &HostService{hostRepo: repo, logger: slog.New(slog.DiscardHandler)}
	if live >= 0 || liveErr != nil {
		svc.liveInstanceCounter = func(context.Context, string) (int64, error) { return live, liveErr }
	}
	return svc, repo
}

func runningHost(maxDevices int64) *host.Host {
	return &host.Host{
		ID: testHostID, Name: "h", Zone: "z",
		Status: host.StatusRunning, MaxDevices: maxDevices,
	}
}

func TestUpdateMaxDevicesRaises(t *testing.T) {
	t.Parallel()
	// No live-device counter configured: raising must not need one.
	svc, repo := newCapacityService(runningHost(4), -1, nil)

	got, err := svc.UpdateMaxDevices(t.Context(), testHostID, 8)
	if err != nil {
		t.Fatalf("UpdateMaxDevices: %v", err)
	}
	if got.MaxDevices != 8 {
		t.Errorf("MaxDevices = %d, want 8", got.MaxDevices)
	}
	if repo.Hosts[testHostID].MaxDevices != 8 {
		t.Errorf("persisted MaxDevices = %d, want 8", repo.Hosts[testHostID].MaxDevices)
	}
}

// TestUpdateMaxDevicesRefusesToStrandDevices is the invariant: the devices
// already on the host would otherwise sit above a ceiling that says they
// cannot exist.
func TestUpdateMaxDevicesRefusesToStrandDevices(t *testing.T) {
	t.Parallel()
	svc, repo := newCapacityService(runningHost(8), 3, nil)

	_, err := svc.UpdateMaxDevices(t.Context(), testHostID, 2)
	if !errors.Is(err, host.ErrInvalidState) {
		t.Fatalf("err = %v, want host.ErrInvalidState", err)
	}
	if repo.mutations != 0 {
		t.Errorf("refused change still wrote %d times", repo.mutations)
	}
}

func TestUpdateMaxDevicesLowersToExactlyTheLiveCount(t *testing.T) {
	t.Parallel()
	svc, _ := newCapacityService(runningHost(8), 3, nil)

	got, err := svc.UpdateMaxDevices(t.Context(), testHostID, 3)
	if err != nil {
		t.Fatalf("UpdateMaxDevices: %v", err)
	}
	if got.MaxDevices != 3 {
		t.Errorf("MaxDevices = %d, want 3", got.MaxDevices)
	}
}

// TestUpdateMaxDevicesCountsLiveDevicesOnly pins the gate to the live
// population rather than the host's own counter, which includes error rows that
// back no CVD: a host whose count is stale-high must still shrink.
func TestUpdateMaxDevicesCountsLiveDevicesOnly(t *testing.T) {
	t.Parallel()
	h := runningHost(8)
	h.CurrentDeviceCount = 6
	svc, _ := newCapacityService(h, 1, nil)

	if _, err := svc.UpdateMaxDevices(t.Context(), testHostID, 2); err != nil {
		t.Fatalf("UpdateMaxDevices: %v", err)
	}
}

func TestUpdateMaxDevicesNoOpWritesNothing(t *testing.T) {
	t.Parallel()
	svc, repo := newCapacityService(runningHost(4), 0, nil)

	if _, err := svc.UpdateMaxDevices(t.Context(), testHostID, 4); err != nil {
		t.Fatalf("UpdateMaxDevices: %v", err)
	}
	if repo.mutations != 0 {
		t.Errorf("unchanged capacity wrote %d times", repo.mutations)
	}
}

func TestUpdateMaxDevicesRejectsTerminalHost(t *testing.T) {
	t.Parallel()
	h := runningHost(4)
	h.Status = host.StatusDeleted
	svc, repo := newCapacityService(h, 0, nil)

	if _, err := svc.UpdateMaxDevices(t.Context(), testHostID, 8); !errors.Is(err, host.ErrInvalidState) {
		t.Fatalf("err = %v, want host.ErrInvalidState", err)
	}
	if repo.mutations != 0 {
		t.Errorf("terminal host was written %d times", repo.mutations)
	}
}

func TestUpdateMaxDevicesRequiresACounterToLower(t *testing.T) {
	t.Parallel()
	svc, _ := newCapacityService(runningHost(8), -1, nil)

	if _, err := svc.UpdateMaxDevices(t.Context(), testHostID, 2); err == nil {
		t.Fatal("lowering without a live-device counter must fail, not assume zero")
	}
}
