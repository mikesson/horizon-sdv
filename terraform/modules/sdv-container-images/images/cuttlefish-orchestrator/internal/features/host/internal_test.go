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
	"errors"
	"log/slog"
	"strings"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// TestMarkDeprovisionStalled verifies that an exhausted deprovision leaves the
// host in "deleting" with a Stalled condition, and never "error", so
// host.reconcile keeps re-driving the idempotent delete (GCE has no
// delete-failed state; "error" would make a user-deleted host retryable).
func TestMarkDeprovisionStalled(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := NewHostRepository(db.Client)
	svc := NewHostService(HostServiceConfig{}, nil, nil, repo, slog.Default())

	h := &host.Host{ID: "hdf-1", Name: "hdf", Zone: "us-central1-a", MaxDevices: 4, Status: host.StatusDeleting, DesiredState: host.DesiredAbsent}
	if err := repo.Create(ctx, h); err != nil {
		t.Fatalf("seed host: %v", err)
	}

	svc.markDeprovisionStalled(ctx, h.ID, errors.New("boom"))

	got, err := repo.Get(ctx, h.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != host.StatusDeleting {
		t.Errorf("status = %q, want deleting (a failed delete is transient, not terminal)", got.Status)
	}
	if got.StalledReason == "" {
		t.Error("expected a Stalled condition recording the teardown failure")
	}
	if got.ErrorMessage != "" {
		t.Errorf("a stalled teardown must not set the error condition, got %q", got.ErrorMessage)
	}

	// Missing host: no-op, no panic.
	svc.markDeprovisionStalled(ctx, "does-not-exist", errors.New("boom"))
}

// Incarnation 0 must keep the legacy unsuffixed name (live row/VM pairs stay
// matched across the incarnation migration); later incarnations append -i<N>,
// respect GCE's 63-character limit, and NEVER truncate into the uuid8
// identity segment: the budget comes out of the user name.
func TestInstanceNameIncarnation(t *testing.T) {
	t.Parallel()
	const id = "12345678-1234-1234-1234-123456789012"

	legacy := host.InstanceName("myhost", id, 0)
	if legacy != "cf-myhost-12345678" {
		t.Errorf("incarnation 0 must be the legacy name, got %q", legacy)
	}
	if got := host.InstanceName("myhost", id, 3); got != "cf-myhost-12345678-i3" {
		t.Errorf("incarnation suffix wrong: %q", got)
	}
	long := host.InstanceName(strings.Repeat("x", 80), id, 12)
	if len(long) > 63 {
		t.Errorf("name exceeds GCE 63-char limit: %d %q", len(long), long)
	}
	if !strings.HasSuffix(long, "-12345678-i12") {
		t.Errorf("truncation must preserve the full uuid8 segment and suffix: %q", long)
	}
	if empty := host.InstanceName("", id, 2); empty != "cf-12345678-i2" {
		t.Errorf("nameless host form wrong: %q", empty)
	}
}
