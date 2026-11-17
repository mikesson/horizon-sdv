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

package hostreconciler

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/option"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/tests/harness/fakegce"
)

// lostRecorder wraps the sync fake to capture MarkHostLost calls.
type lostRecorder struct {
	*syncHostRepo
	mu   sync.Mutex
	lost []string
}

func (r *lostRecorder) MarkHostLost(ctx context.Context, id, reason string) error {
	r.mu.Lock()
	r.lost = append(r.lost, id)
	r.mu.Unlock()
	return r.syncHostRepo.MarkHostLost(ctx, id, reason)
}

// TestSyncZoneLostHostGating pins the lost-host decision against a live fake
// GCE boundary: only a host whose status expects a VM AND whose instance a
// fresh point read confirms gone may be marked lost. In particular, a VM the
// zone LISTING omits (a stale or degraded listing) but a point read still
// finds must never be declared lost. The pre-fix code marked every such host
// deleted.
func TestSyncZoneLostHostGating(t *testing.T) {
	fake := fakegce.New()
	t.Cleanup(fake.Close)
	ctx := t.Context()

	client, err := compute.NewInstancesRESTClient(ctx,
		option.WithEndpoint(fake.URL()), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	// One real VM that the listing omits even though it exists.
	if _, err := client.Insert(ctx, &computepb.InsertInstanceRequest{
		Project: "p", Zone: "z",
		InstanceResource: &computepb.Instance{Name: new("vm-alive")},
	}); err != nil {
		t.Fatal(err)
	}
	fake.OmitFromListings("vm-alive")

	repo := &lostRecorder{syncHostRepo: newSyncHostRepo()}
	for _, h := range []*host.Host{
		{ID: "h-gone", Zone: "z", Status: host.StatusRunning, GCEInstanceName: "vm-gone"},
		{ID: "h-provisioning", Zone: "z", Status: host.StatusProvisioning, GCEInstanceName: "vm-not-yet"},
		{ID: "h-deleting", Zone: "z", Status: host.StatusDeleting, GCEInstanceName: "vm-torn-down"},
		{ID: "h-preempted", Zone: "z", Status: host.StatusPreempted, GCEInstanceName: "vm-was-spot"},
		{ID: "h-listed-out", Zone: "z", Status: host.StatusRunning, GCEInstanceName: "vm-alive"},
	} {
		repo.Hosts[h.ID] = h
	}

	svc := New(Config{
		ProjectID:      "p",
		Zones:          []string{"z"},
		InstanceLabels: map[string]string{"managed-by": "test"},
	}, client, repo, slog.Default())

	if err := svc.SyncZone(ctx, "z"); err != nil {
		t.Fatalf("SyncZone: %v", err)
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if !slices.Equal(repo.lost, []string{"h-gone"}) {
		t.Errorf("MarkHostLost calls = %v, want exactly [h-gone]", repo.lost)
	}
	for id, want := range map[string]host.Status{
		"h-provisioning": host.StatusProvisioning,
		"h-deleting":     host.StatusDeleting,
		"h-preempted":    host.StatusPreempted,
		"h-listed-out":   host.StatusRunning,
	} {
		if got := repo.Hosts[id].Status; got != want {
			t.Errorf("host %s status = %s, want %s untouched", id, got, want)
		}
	}
}

// lostBudgetHarness builds a sweep against a fake GCE that knows no
// instances, so every seeded running host's VM reads as definitively gone.
// The lost-mark budget is then the only thing between the sweep and marking
// them all.
func lostBudgetHarness(t *testing.T, repo *lostRecorder, maxMarks int) *Service {
	t.Helper()
	fake := fakegce.New()
	t.Cleanup(fake.Close)
	client, err := compute.NewInstancesRESTClient(t.Context(),
		option.WithEndpoint(fake.URL()), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return New(Config{
		ProjectID:            "p",
		Zones:                []string{"z"},
		InstanceLabels:       map[string]string{"managed-by": "test"},
		MaxLostMarksPerCycle: maxMarks,
	}, client, repo, slog.Default())
}

func seedGoneHosts(repo *lostRecorder, n int) {
	for i := range n {
		id := "h-" + string(rune('a'+i))
		repo.Hosts[id] = &host.Host{
			ID: id, Zone: "z", Status: host.StatusRunning,
			GCEInstanceName: "vm-" + id,
		}
	}
}

// TestLostMarkBudgetIsShared pins the budget's durable form: the gate counts
// committed lost marks (the tombstone evidence CountLostMarksSince reads), so
// marks another concurrently-sweeping instance already made (modeled by
// pre-seeding the mark times) consume THIS instance's budget too. The old
// per-process counter saw an empty budget here and marked away, doubling the
// cap exactly during a leadership handoff.
func TestLostMarkBudgetIsShared(t *testing.T) {
	repo := &lostRecorder{syncHostRepo: newSyncHostRepo()}
	repo.lostMarkTimes = []time.Time{time.Now(), time.Now()} // the other leader's marks
	seedGoneHosts(repo, 2)
	svc := lostBudgetHarness(t, repo, 2)

	if err := svc.SyncZone(t.Context(), "z"); err != nil {
		t.Fatalf("SyncZone: %v", err)
	}
	if len(repo.lost) != 0 {
		t.Errorf("MarkHostLost calls = %v, want none: the shared budget is already spent", repo.lost)
	}
	if got := svc.remediationDeferred.Load(); got != 2 {
		t.Errorf("remediation_deferred = %d, want 2", got)
	}
}

// The budget still meters a single sweep: each committed mark is durable
// evidence the next candidate's count sees, so a sweep stops at the cap.
// Marks older than the window age out and free the budget again.
func TestLostMarkBudgetMetersAndAges(t *testing.T) {
	repo := &lostRecorder{syncHostRepo: newSyncHostRepo()}
	// Stale marks from long ago must not count against today's budget.
	repo.lostMarkTimes = []time.Time{time.Now().Add(-time.Hour)}
	seedGoneHosts(repo, 4)
	svc := lostBudgetHarness(t, repo, 2)

	if err := svc.SyncZone(t.Context(), "z"); err != nil {
		t.Fatalf("SyncZone: %v", err)
	}
	if got := len(repo.lost); got != 2 {
		t.Errorf("MarkHostLost calls = %d (%v), want exactly the budget of 2", got, repo.lost)
	}
	if got := svc.remediationDeferred.Load(); got != 2 {
		t.Errorf("remediation_deferred = %d, want 2 (the deferred remainder)", got)
	}
}

// No evidence, no destruction: when the budget count itself fails, the sweep
// defers correction rather than marking blind. Without the count it cannot
// tell one lost VM from a mass anomaly.
func TestLostMarkBudgetCountFailureDefers(t *testing.T) {
	repo := &lostRecorder{syncHostRepo: newSyncHostRepo()}
	repo.countLostErr = errors.New("spanner unavailable")
	seedGoneHosts(repo, 1)
	svc := lostBudgetHarness(t, repo, 2)

	if err := svc.SyncZone(t.Context(), "z"); err != nil {
		t.Fatalf("SyncZone: %v", err)
	}
	if len(repo.lost) != 0 {
		t.Errorf("MarkHostLost calls = %v, want none on a failed budget read", repo.lost)
	}
	if got := svc.remediationDeferred.Load(); got != 1 {
		t.Errorf("remediation_deferred = %d, want 1", got)
	}
}

// TestSweepOrphanVMs pins the orphan direction: a managed instance no
// live host row claims is detected, and deleted only when enabled, while
// claimed, young (grace window), and unlabeled instances stay untouched. A
// tombstone-claimed instance (deleted-status row) counts as an orphan:
// nothing will ever manage it again.
func TestSweepOrphanVMs(t *testing.T) {
	fake := fakegce.New()
	t.Cleanup(fake.Close)
	ctx := t.Context()

	client, err := compute.NewInstancesRESTClient(ctx,
		option.WithEndpoint(fake.URL()), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	labels := map[string]string{"managed-by": "test"}
	fake.PlantOrphan("vm-claimed", "z", labels, time.Hour)
	fake.PlantOrphan("vm-orphan", "z", labels, time.Hour)
	fake.PlantOrphan("vm-young", "z", labels, time.Minute)
	fake.PlantOrphan("vm-foreign", "z", nil, time.Hour)
	fake.PlantOrphan("vm-tombstoned", "z", labels, time.Hour)

	repo := newSyncHostRepo()
	repo.Hosts["h1"] = &host.Host{ID: "h1", Zone: "z", Status: host.StatusRunning, GCEInstanceName: "vm-claimed"}
	repo.Hosts["h2"] = &host.Host{ID: "h2", Zone: "z", Status: host.StatusDeleted, GCEInstanceName: "vm-tombstoned"}

	// Dry-run (the default): both orphans detected, nothing deleted.
	svc := New(Config{ProjectID: "p", InstanceLabels: labels}, client, repo, slog.Default())
	if err := svc.sweepOrphanVMs(ctx); err != nil {
		t.Fatalf("sweepOrphanVMs: %v", err)
	}
	if got := svc.orphanVMs.Load(); got != 2 {
		t.Errorf("orphans detected = %d, want 2 (vm-orphan + vm-tombstoned)", got)
	}
	if got := len(fake.Live()); got != 5 {
		t.Errorf("live instances after dry-run = %d, want all 5 untouched", got)
	}

	// Deletion enabled: exactly the two orphans go; everything else survives.
	svc = New(Config{ProjectID: "p", InstanceLabels: labels, DeleteOrphanVMs: true}, client, repo, slog.Default())
	if err := svc.sweepOrphanVMs(ctx); err != nil {
		t.Fatalf("sweepOrphanVMs (delete): %v", err)
	}
	live := fake.Live()
	slices.Sort(live)
	if want := []string{"vm-claimed", "vm-foreign", "vm-young"}; !slices.Equal(live, want) {
		t.Errorf("live instances = %v, want %v", live, want)
	}
	if got := svc.orphanVMsDeleted.Load(); got != 2 {
		t.Errorf("orphans deleted = %d, want 2", got)
	}
}

// installLabels are one install's managed labels, as internal/app builds them.
func installLabels(deployment string) map[string]string {
	return map[string]string{
		"managed-by":     "cloud-cuttlefish-orchestrator",
		"cco-deployment": deployment,
	}
}

// TestOrphanSweepLeavesAnotherDeploymentAlone pins the second-install case: a
// VM with the managed name prefix and managed-by label, but labeled for a
// different deployment in the same project, belongs to that install and is
// never this install's orphan, even with deletion enabled. Judged on
// managed-by alone it would be deleted.
func TestOrphanSweepLeavesAnotherDeploymentAlone(t *testing.T) {
	fake := fakegce.New()
	t.Cleanup(fake.Close)
	ctx := t.Context()

	client, err := compute.NewInstancesRESTClient(ctx,
		option.WithEndpoint(fake.URL()), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	fake.PlantOrphan("cf-theirs", "z", installLabels("other"), time.Hour)
	fake.PlantOrphan("cf-ours", "z", installLabels("cco"), time.Hour)

	svc := New(Config{ProjectID: "p", InstanceLabels: installLabels("cco"), DeleteOrphanVMs: true},
		client, newSyncHostRepo(), slog.Default())
	if err := svc.sweepOrphanVMs(ctx); err != nil {
		t.Fatalf("sweepOrphanVMs: %v", err)
	}
	if got := svc.orphanVMs.Load(); got != 1 {
		t.Errorf("orphans detected = %d, want 1 (cf-ours only)", got)
	}
	if live := fake.Live(); !slices.Equal(live, []string{"cf-theirs"}) {
		t.Errorf("live instances = %v, want [cf-theirs]", live)
	}
}

// TestDriftAdoptsTerminatedWithoutDeploymentLabel pins that labels never gate
// drift. A host created before the cco-deployment label existed carries only
// managed-by, and its row must still adopt an out-of-band stop. If the drift
// listing required the full label set, the VM would drop out of the listing,
// the point read would find it, and the row would stay running.
func TestDriftAdoptsTerminatedWithoutDeploymentLabel(t *testing.T) {
	fake := fakegce.New()
	t.Cleanup(fake.Close)
	ctx := t.Context()

	client, err := compute.NewInstancesRESTClient(ctx,
		option.WithEndpoint(fake.URL()), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if _, err := client.Insert(ctx, &computepb.InsertInstanceRequest{
		Project: "p", Zone: "z",
		InstanceResource: &computepb.Instance{
			Name:   new("cf-old-host"),
			Labels: map[string]string{"managed-by": "cloud-cuttlefish-orchestrator"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Stop(ctx, &computepb.StopInstanceRequest{
		Project: "p", Zone: "z", Instance: "cf-old-host",
	}); err != nil {
		t.Fatal(err)
	}

	repo := newSyncHostRepo()
	repo.Hosts["h-old"] = &host.Host{ID: "h-old", Zone: "z", Status: host.StatusRunning, GCEInstanceName: "cf-old-host"}

	svc := New(Config{ProjectID: "p", Zones: []string{"z"}, InstanceLabels: installLabels("cco")},
		client, repo, slog.Default())
	if err := svc.SyncZone(ctx, "z"); err != nil {
		t.Fatalf("SyncZone: %v", err)
	}
	if got := repo.Hosts["h-old"].Status; got != host.StatusStopped {
		t.Errorf("host status = %s, want %s adopted from TERMINATED", got, host.StatusStopped)
	}
}
