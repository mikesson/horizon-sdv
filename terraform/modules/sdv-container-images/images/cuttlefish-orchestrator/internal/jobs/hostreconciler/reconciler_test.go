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
	"log/slog"
	"slices"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"cloud.google.com/go/compute/apiv1/computepb"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
)

// syncHostRepo embeds the shared host fake and adds the Repository
// batch extras. lostMarkTimes mirrors the durable evidence the real
// repository counts (tombstoned_at of lost marks); pre-seeding it models
// marks another instance committed.
type syncHostRepo struct {
	*hosttest.FakeRepo
	lostMarkTimes []time.Time
	countLostErr  error // returned by CountLostMarksSince when set
}

func newSyncHostRepo() *syncHostRepo {
	return &syncHostRepo{FakeRepo: hosttest.NewFakeRepo()}
}

func (m *syncHostRepo) MarkHostLost(_ context.Context, id, reason string) error {
	if h, ok := m.Hosts[id]; ok {
		h.Status = host.StatusDeleted
		h.ErrorMessage = reason
	}
	m.lostMarkTimes = append(m.lostMarkTimes, time.Now())
	return nil
}

func (m *syncHostRepo) CountLostMarksSince(_ context.Context, since time.Time, _ string) (int64, error) {
	if m.countLostErr != nil {
		return 0, m.countLostErr
	}
	var n int64
	for _, ts := range m.lostMarkTimes {
		if ts.After(since) {
			n++
		}
	}
	return n, nil
}

func (m *syncHostRepo) MarkPreempted(_ context.Context, id, opID string) (*host.Host, error) {
	h, ok := m.Hosts[id]
	if !ok {
		return nil, host.ErrNotFound
	}
	// Mirror the real repository's in-transaction validation.
	if !h.Status.CanTransitionTo(host.StatusPreempted) {
		return nil, &host.InvalidStateError{
			Current:  string(h.Status),
			Expected: string(host.StatusPreempted),
		}
	}
	h.SetPreempted(opID)
	return h, nil
}

func (m *syncHostRepo) BatchUpdateGCEState(_ context.Context, updates []host.GCEStateUpdate) (int64, error) {
	var applied int64
	for _, u := range updates {
		// Mirror the CAS semantics of the real repository: a row whose status
		// moved since the plan was computed is skipped, not clobbered.
		if h, ok := m.Hosts[u.ID]; ok && h.Status == u.PrevStatus {
			h.Status = u.Status
			h.IPAddress = u.IP
			h.InternalIP = u.InternalIP
			applied++
		}
	}
	return applied, nil
}

func (m *syncHostRepo) ListActiveZones(_ context.Context) ([]string, error) {
	seen := make(map[string]bool)
	var zones []string
	for _, h := range m.Hosts {
		if h.Status != host.StatusDeleted && !seen[h.Zone] {
			seen[h.Zone] = true
			zones = append(zones, h.Zone)
		}
	}
	return zones, nil
}

// fakeResync records the reconciler filings the sync enqueues. active names
// resources that already hold a live (pending or leased) task, which the
// level re-file must leave alone.
type fakeResync struct {
	filed  []string // resource IDs, in filing order
	active map[string]bool
}

func (f *fakeResync) EnqueueResync(_ context.Context, _, resourceID string, _ time.Duration) error {
	f.filed = append(f.filed, resourceID)
	return nil
}

func (f *fakeResync) HasActive(_ context.Context, _, resourceID string) (bool, error) {
	return f.active[resourceID], nil
}

// Tests

func TestService_StartStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hostRepo := newSyncHostRepo()
		logger := slog.Default()

		config := Config{
			ProjectID:      "test-project",
			Zones:          []string{}, // empty fleet -> no zones -> no GCE calls
			SyncInterval:   100 * time.Millisecond,
			InstanceLabels: map[string]string{"managed-by": "test"},
		}

		// Created without a GCE client: with no active zones the cycle never
		// touches GCE. This test is about the lifecycle, not the sync.
		svc := New(config, nil, hostRepo, logger)

		ctx := t.Context()
		svc.Start(ctx)
		svc.Start(ctx) // idempotent no-op

		// Stop joins the loop; a second Stop is a no-op.
		svc.Stop()
		svc.Stop()
	})
}

func TestService_HasRequiredLabels(t *testing.T) {
	t.Parallel()
	hostRepo := newSyncHostRepo()
	logger := slog.Default()

	config := Config{
		ProjectID:    "test-project",
		Zones:        []string{"us-central1-a"},
		SyncInterval: 1 * time.Minute,
		InstanceLabels: map[string]string{
			"managed-by": "orchestrator",
			"env":        "prod",
		},
	}

	svc := New(config, nil, hostRepo, logger)

	tests := []struct {
		name     string
		labels   map[string]string
		expected bool
	}{
		{
			name:     "All required labels present",
			labels:   map[string]string{"managed-by": "orchestrator", "env": "prod", "extra": "label"},
			expected: true,
		},
		{
			name:     "Missing managed-by label",
			labels:   map[string]string{"env": "prod"},
			expected: false,
		},
		{
			name:     "Wrong value for managed-by",
			labels:   map[string]string{"managed-by": "other", "env": "prod"},
			expected: false,
		},
		{
			name:     "Empty labels",
			labels:   map[string]string{},
			expected: false,
		},
		{
			name:     "Nil labels",
			labels:   nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := svc.hasRequiredLabels(tt.labels)
			if result != tt.expected {
				t.Errorf("hasRequiredLabels() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestExtractInstanceName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		targetLink string
		expected   string
	}{
		{
			name:       "Full GCE target link",
			targetLink: "https://www.googleapis.com/compute/v1/projects/test-project/zones/us-central1-a/instances/cf-my-host-abc123",
			expected:   "cf-my-host-abc123",
		},
		{
			name:       "Instance name with hyphens",
			targetLink: "https://www.googleapis.com/compute/v1/projects/proj/zones/zone/instances/cf-test-host-12345678",
			expected:   "cf-test-host-12345678",
		},
		{
			name:       "Just instance name (no URL structure)",
			targetLink: "cf-simple-host",
			expected:   "", // Function expects URL with /instances/ path
		},
		{
			name:       "Empty string",
			targetLink: "",
			expected:   "",
		},
		{
			name:       "Trailing slash",
			targetLink: "https://www.googleapis.com/compute/v1/projects/proj/zones/zone/instances/cf-host/",
			expected:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractInstanceName(tt.targetLink)
			if result != tt.expected {
				t.Errorf("extractInstanceName(%q) = %q, want %q", tt.targetLink, result, tt.expected)
			}
		})
	}
}

func TestPreemptionAutoRecovery(t *testing.T) {
	t.Parallel()
	hostRepo := newSyncHostRepo()
	spot := &host.Host{
		ID: "spot-1", Name: "s", Zone: "us-central1-a",
		GCEInstanceName:   "cf-s-abc",
		Status:            host.StatusRunning,
		ProvisioningModel: host.ProvisioningModelSpot,
	}
	hostRepo.Hosts[spot.ID] = spot

	recovery := &fakeResync{}
	auditLog := &fakeAuditLog{}
	s := New(Config{}, nil, hostRepo, slog.Default(),
		WithAuditLogger(auditLog),
		WithPreemptionRecovery(recovery))

	if err := s.handlePreemptions(t.Context(), "us-central1-a",
		map[string]string{"cf-s-abc": "op-1"}); err != nil {
		t.Fatalf("handlePreemptions: %v", err)
	}

	if spot.Status != host.StatusPreempted {
		t.Errorf("status = %q, want preempted", spot.Status)
	}
	if len(recovery.filed) != 1 || recovery.filed[0] != "spot-1" {
		t.Errorf("recovery invoked with %v, want [spot-1]", recovery.filed)
	}
	if len(auditLog.entries) != 1 || auditLog.entries[0].Action != audit.ActionHostPreempted {
		t.Fatalf("audit entries = %+v, want one host.preempted", auditLog.entries)
	}
	if got := auditLog.entries[0].Metadata["recovery"]; got != "recovery_enqueued" {
		t.Errorf("recovery disposition = %v, want recovery_enqueued", got)
	}
	if got := auditLog.entries[0].Metadata["operation_id"]; got != "op-1" {
		t.Errorf("operation_id = %v, want op-1", got)
	}
}

// A host already sitting in preempted with NO live recovery task is re-filed
// by the level check. That is the case where the mark's enqueue failed or the
// breaker suspended that cycle, with the operation evidence long aged out of
// the lookback (the map carries no entry for it). A preempted host whose
// recovery task is still live must be left alone.
func TestPreemptionRecoveryRefiledForStrandedHost(t *testing.T) {
	t.Parallel()
	hostRepo := newSyncHostRepo()
	stranded := &host.Host{
		ID: "spot-stranded", Name: "s1", Zone: "us-central1-a",
		GCEInstanceName:   "cf-s1-abc",
		Status:            host.StatusPreempted,
		ProvisioningModel: host.ProvisioningModelSpot,
		PreemptionCount:   1,
	}
	owned := &host.Host{
		ID: "spot-owned", Name: "s2", Zone: "us-central1-a",
		GCEInstanceName:   "cf-s2-abc",
		Status:            host.StatusPreempted,
		ProvisioningModel: host.ProvisioningModelSpot,
		PreemptionCount:   1,
	}
	hostRepo.Hosts[stranded.ID] = stranded
	hostRepo.Hosts[owned.ID] = owned

	recovery := &fakeResync{active: map[string]bool{owned.ID: true}}
	s := New(Config{}, nil, hostRepo, slog.Default(),
		WithPreemptionRecovery(recovery))

	// No evidence at all: the events aged out of the 30m lookback.
	if err := s.handlePreemptions(t.Context(), "us-central1-a", map[string]string{}); err != nil {
		t.Fatalf("handlePreemptions: %v", err)
	}
	if len(recovery.filed) != 1 || recovery.filed[0] != stranded.ID {
		t.Errorf("re-filed %v, want exactly [%s]", recovery.filed, stranded.ID)
	}

	// A capped host stays manual.
	stranded.PreemptionCount = int64(defaultMaxPreemptionRecoveries) + 1
	recovery.filed, recovery.active[stranded.ID] = nil, false
	if err := s.handlePreemptions(t.Context(), "us-central1-a", map[string]string{}); err != nil {
		t.Fatalf("handlePreemptions: %v", err)
	}
	if len(recovery.filed) != 0 {
		t.Errorf("capped host re-filed %v, want none", recovery.filed)
	}
}

// fakeAuditLog captures the entries the sync audits.
type fakeAuditLog struct {
	entries []audit.Entry
}

func (f *fakeAuditLog) Log(_ context.Context, entry audit.Entry) error { f.LogAsync(entry); return nil }
func (f *fakeAuditLog) LogAsync(entry audit.Entry)                     { f.entries = append(f.entries, entry) }
func (f *fakeAuditLog) Query(context.Context, audit.ListFilter) ([]audit.Entry, error) {
	return nil, nil
}

func (f *fakeAuditLog) GetResourceHistory(context.Context, audit.ResourceType, string) ([]audit.Entry, error) {
	return nil, nil
}

func (f *fakeAuditLog) GetUserActivity(context.Context, string, int) ([]audit.Entry, error) {
	return nil, nil
}
func (f *fakeAuditLog) Close() error { return nil }

// fakeZoneHealth records the preemption-storm cells the breaker writes.
type fakeZoneHealth struct {
	recorded []string // "zone/family/metric"
}

func (f *fakeZoneHealth) RecordFailure(_ context.Context, zone, family, metric, _ string) (*gcecatalog.ZoneHealth, error) {
	f.recorded = append(f.recorded, zone+"/"+family+"/"+metric)
	return &gcecatalog.ZoneHealth{Zone: zone, MachineFamily: family, BindingMetric: metric}, nil
}
func (f *fakeZoneHealth) ClearCell(context.Context, string, string) error { return nil }
func (f *fakeZoneHealth) ListCells(context.Context) ([]*gcecatalog.ZoneHealth, error) {
	return nil, nil
}

func (f *fakeZoneHealth) ListCapacityCells(context.Context, []string) ([]*gcecatalog.ZoneHealth, error) {
	return nil, nil
}

// A tripped zone recovery breaker graduates into the placement plane: one
// preemption-storm zone_health cell per affected machine family, so new Spot
// capacity stops landing in the reclaiming zone. Hosts are still marked
// preempted; recovery stays suspended for the cycle.
func TestZoneBreakerWritesPreemptionStormCells(t *testing.T) {
	t.Parallel()
	hostRepo := newSyncHostRepo()
	preempted := map[string]string{}
	for i := range 4 {
		name := "cf-s" + strconv.Itoa(i)
		machineType := "n1-standard-8"
		if i == 0 {
			machineType = "g2-standard-8"
		}
		hostRepo.Hosts[name] = &host.Host{
			ID: name, Name: name, Zone: "us-central1-a",
			GCEInstanceName:   name,
			Status:            host.StatusRunning,
			ProvisioningModel: host.ProvisioningModelSpot,
			MachineType:       machineType,
		}
		if i < 3 { // 3 of 4 preempted: over the 0.4 fraction
			preempted[name] = "op-" + strconv.Itoa(i)
		}
	}

	zoneHealth := &fakeZoneHealth{}
	auditLog := &fakeAuditLog{}
	recovery := &fakeResync{}
	s := New(Config{}, nil, hostRepo, slog.Default(),
		WithZoneHealth(zoneHealth),
		WithAuditLogger(auditLog),
		WithPreemptionRecovery(recovery))

	if err := s.handlePreemptions(t.Context(), "us-central1-a", preempted); err != nil {
		t.Fatalf("handlePreemptions: %v", err)
	}

	slices.Sort(zoneHealth.recorded)
	want := []string{"us-central1-a/g2/preemption", "us-central1-a/n1/preemption"}
	if !slices.Equal(zoneHealth.recorded, want) {
		t.Errorf("recorded cells = %v, want %v", zoneHealth.recorded, want)
	}
	if len(recovery.filed) != 0 {
		t.Errorf("breaker must suspend auto-recovery; recovery.filed %v", recovery.filed)
	}
	if len(auditLog.entries) != len(preempted) {
		t.Errorf("audited %d preemptions, want %d", len(auditLog.entries), len(preempted))
	}
	for _, e := range auditLog.entries {
		if got := e.Metadata["recovery"]; got != "suspended_zone_breaker" {
			t.Errorf("recovery disposition = %v, want suspended_zone_breaker", got)
		}
	}
	for name := range preempted {
		if hostRepo.Hosts[name].Status != host.StatusPreempted {
			t.Errorf("host %s status = %q, want preempted (marking is not suspended)", name, hostRepo.Hosts[name].Status)
		}
	}
}

// Below the breaker threshold no storm cell is written. Individual
// preemptions are recovery-path business, not placement memory.
func TestNoStormCellBelowBreakerThreshold(t *testing.T) {
	t.Parallel()
	hostRepo := newSyncHostRepo()
	hostRepo.Hosts["cf-solo"] = &host.Host{
		ID: "cf-solo", Name: "cf-solo", Zone: "us-central1-a",
		GCEInstanceName:   "cf-solo",
		Status:            host.StatusRunning,
		ProvisioningModel: host.ProvisioningModelSpot,
		MachineType:       "n1-standard-8",
	}

	zoneHealth := &fakeZoneHealth{}
	s := New(Config{}, nil, hostRepo, slog.Default(), WithZoneHealth(zoneHealth))

	if err := s.handlePreemptions(t.Context(), "us-central1-a",
		map[string]string{"cf-solo": "op-1"}); err != nil {
		t.Fatalf("handlePreemptions: %v", err)
	}
	if len(zoneHealth.recorded) != 0 {
		t.Errorf("no cells expected below the breaker threshold, got %v", zoneHealth.recorded)
	}
}

// The stateless lookback re-reads the same recent operations every cycle, so
// a host already recovery.filed from an event (running again, LastPreemptionID set)
// must not be re-preempted by that same operation ID, while a new
// preemption (new ID) is processed.
func TestPreemptionDedupesByOperationID(t *testing.T) {
	t.Parallel()
	hostRepo := newSyncHostRepo()
	spot := &host.Host{
		ID: "spot-3", Name: "s3", Zone: "us-central1-a",
		GCEInstanceName:   "cf-s3",
		Status:            host.StatusRunning, // recovery.filed after op-1
		ProvisioningModel: host.ProvisioningModelSpot,
		LastPreemptionID:  "op-1",
	}
	hostRepo.Hosts[spot.ID] = spot

	recovery := &fakeResync{}
	s := New(Config{}, nil, hostRepo, slog.Default(),
		WithPreemptionRecovery(recovery))

	// Same operation re-observed: no-op.
	if err := s.handlePreemptions(t.Context(), "us-central1-a",
		map[string]string{"cf-s3": "op-1"}); err != nil {
		t.Fatalf("handlePreemptions: %v", err)
	}
	if spot.Status != host.StatusRunning || len(recovery.filed) != 0 {
		t.Fatalf("re-observed operation must be a no-op; status=%q recovery.filed=%v", spot.Status, recovery.filed)
	}

	// A new preemption of the recovery.filed host: processed.
	if err := s.handlePreemptions(t.Context(), "us-central1-a",
		map[string]string{"cf-s3": "op-2"}); err != nil {
		t.Fatalf("handlePreemptions: %v", err)
	}
	if spot.Status != host.StatusPreempted || spot.LastPreemptionID != "op-2" {
		t.Errorf("new operation must preempt; status=%q lastID=%q", spot.Status, spot.LastPreemptionID)
	}
	if len(recovery.filed) != 1 {
		t.Errorf("recovery invoked %d times, want 1", len(recovery.filed))
	}
}

// A dying Spot VM is often OBSERVED before it is understood: the drift
// writer adopts running->stopping/stopped from the VM's STOPPING/TERMINATED
// window before the ops scan matches the preemption. The detector must be
// able to reclassify from those states. Without it, whichever observation
// landed first foreclosed recovery and the host degraded to lost (audit
// F-A2).
func TestPreemptionReclassifiesDriftAdoptedStop(t *testing.T) {
	t.Parallel()
	for _, from := range []host.Status{host.StatusStopping, host.StatusStopped} {
		t.Run(string(from), func(t *testing.T) {
			t.Parallel()
			hostRepo := newSyncHostRepo()
			spot := &host.Host{
				ID: "spot-drift", Name: "sd", Zone: "us-central1-a",
				GCEInstanceName:   "cf-sd",
				Status:            from,
				ProvisioningModel: host.ProvisioningModelSpot,
			}
			hostRepo.Hosts[spot.ID] = spot

			recovery := &fakeResync{}
			s := New(Config{}, nil, hostRepo, slog.Default(),
				WithPreemptionRecovery(recovery))

			if err := s.handlePreemptions(t.Context(), "us-central1-a",
				map[string]string{"cf-sd": "op-1"}); err != nil {
				t.Fatalf("handlePreemptions: %v", err)
			}
			if spot.Status != host.StatusPreempted {
				t.Errorf("status = %q, want preempted (drift adoption must not foreclose recovery)", spot.Status)
			}
			if len(recovery.filed) != 1 {
				t.Errorf("recovery invoked %d times, want 1", len(recovery.filed))
			}
		})
	}
}

// spotAbsenceIsDeletion pins the vanished-Spot classification: preemption
// evidence (preempted op or system-principal delete) is never deletion; an
// external-principal delete is; no evidence defers up to missingSpotGrace
// before degrading to the lost path.
func TestSpotAbsenceClassification(t *testing.T) {
	t.Parallel()
	spot := &host.Host{
		ID: "spot-a", GCEInstanceName: "cf-a",
		Status: host.StatusRunning, ProvisioningModel: host.ProvisioningModelSpot,
	}

	t.Run("preemption op evidence", func(t *testing.T) {
		t.Parallel()
		s := New(Config{}, nil, newSyncHostRepo(), slog.Default())
		s.zoneEvidence = map[string]instanceEvidence{"cf-a": {preemptOpID: "op-1"}}
		if s.spotAbsenceIsDeletion(spot) {
			t.Error("preempted-op evidence must never classify as deletion")
		}
	})
	t.Run("system delete evidence", func(t *testing.T) {
		t.Parallel()
		s := New(Config{}, nil, newSyncHostRepo(), slog.Default())
		s.zoneEvidence = map[string]instanceEvidence{"cf-a": {deleteOpID: "op-2", systemDelete: true}}
		if s.spotAbsenceIsDeletion(spot) {
			t.Error("system-principal delete is the Spot reclaim tail, not user deletion")
		}
	})
	t.Run("external delete evidence", func(t *testing.T) {
		t.Parallel()
		s := New(Config{}, nil, newSyncHostRepo(), slog.Default())
		s.zoneEvidence = map[string]instanceEvidence{"cf-a": {deleteOpID: "op-3", externalDelete: true}}
		if !s.spotAbsenceIsDeletion(spot) {
			t.Error("an external-principal delete is user intent and must classify as deletion")
		}
	})
	t.Run("no evidence defers then degrades", func(t *testing.T) {
		t.Parallel()
		s := New(Config{}, nil, newSyncHostRepo(), slog.Default())
		if s.spotAbsenceIsDeletion(spot) {
			t.Fatal("first unexplained sighting must defer")
		}
		if s.spotAbsenceIsDeletion(spot) {
			t.Fatal("within the grace it must still defer")
		}
		s.missingSpotSince[spot.ID] = time.Now().Add(-missingSpotGrace - time.Minute)
		if !s.spotAbsenceIsDeletion(spot) {
			t.Fatal("past the grace the absence degrades to the lost path")
		}
		if _, tracked := s.missingSpotSince[spot.ID]; tracked {
			t.Error("resolved entries must be pruned")
		}
	})
}

// isSystemPrincipal matches exactly: misreading a human or service-account
// delete as a preemption would resurrect a host its owner intended gone.
func TestIsSystemPrincipal(t *testing.T) {
	t.Parallel()
	for user, want := range map[string]bool{
		"system":            true,
		"System@google.com": true,
		"":                  false,
		"alice@example.com": false,
		"cuttlefish-orchestrator@example-project.iam.gserviceaccount.com": false,
	} {
		if got := isSystemPrincipal(user); got != want {
			t.Errorf("isSystemPrincipal(%q) = %v, want %v", user, got, want)
		}
	}
}

// A preemption event racing a user delete must not interrupt the deletion:
// preempted-over-deleting would pass CanRetry and auto-recovery would
// re-provision a host the user just deleted (racing its deprovision task).
func TestPreemptionNeverInterruptsDeletion(t *testing.T) {
	t.Parallel()
	hostRepo := newSyncHostRepo()
	spot := &host.Host{
		ID: "spot-4", Name: "s4", Zone: "us-central1-a",
		GCEInstanceName:   "cf-s4",
		Status:            host.StatusDeleting, // user delete won the race
		ProvisioningModel: host.ProvisioningModelSpot,
	}
	hostRepo.Hosts[spot.ID] = spot

	recovery := &fakeResync{}
	s := New(Config{}, nil, hostRepo, slog.New(slog.DiscardHandler),
		WithPreemptionRecovery(recovery))

	if err := s.handlePreemptions(t.Context(), "us-central1-a",
		map[string]string{"cf-s4": "op-9"}); err != nil {
		t.Fatalf("handlePreemptions: %v", err)
	}

	if spot.Status != host.StatusDeleting {
		t.Errorf("status = %q, want deleting (preemption must not interrupt deletion)", spot.Status)
	}
	if len(recovery.filed) != 0 {
		t.Errorf("auto-recovery must not run for a deleting host, got %v", recovery.filed)
	}
}

func TestPreemptionRecoveryRespectsLimit(t *testing.T) {
	t.Parallel()
	hostRepo := newSyncHostRepo()
	spot := &host.Host{
		ID: "spot-2", Name: "s2", Zone: "us-central1-a",
		GCEInstanceName:   "cf-s2",
		Status:            host.StatusRunning,
		ProvisioningModel: host.ProvisioningModelSpot,
		PreemptionCount:   defaultMaxPreemptionRecoveries, // next preemption pushes it over the limit
	}
	hostRepo.Hosts[spot.ID] = spot

	recovery := &fakeResync{}
	s := New(Config{}, nil, hostRepo, slog.Default(),
		WithPreemptionRecovery(recovery))

	if err := s.handlePreemptions(t.Context(), "us-central1-a",
		map[string]string{"cf-s2": "op-2"}); err != nil {
		t.Fatalf("handlePreemptions: %v", err)
	}

	if len(recovery.filed) != 0 {
		t.Errorf("recovery should be skipped past the limit, got %v", recovery.filed)
	}
	if spot.Status != host.StatusPreempted {
		t.Errorf("host should still be marked preempted, got %q", spot.Status)
	}
}

func TestPreemptionHandling(t *testing.T) {
	t.Parallel()
	hostRepo := newSyncHostRepo()

	// Add a Spot VM host
	spotHost := &host.Host{
		ID:                "spot-host-1",
		Name:              "my-spot-host",
		Zone:              "us-central1-a",
		GCEInstanceName:   "cf-my-spot-host-abc123",
		Status:            host.StatusRunning,
		ProvisioningModel: host.ProvisioningModelSpot,
		PreemptionCount:   0,
	}
	hostRepo.Hosts[spotHost.ID] = spotHost

	// Simulate preemption
	operationID := "operation-12345"
	spotHost.SetPreempted(operationID)

	// Verify preemption was recorded
	if spotHost.Status != host.StatusPreempted {
		t.Errorf("expected status preempted, got %s", spotHost.Status)
	}
	if spotHost.PreemptionCount != 1 {
		t.Errorf("expected preemption count 1, got %d", spotHost.PreemptionCount)
	}
	if spotHost.LastPreemptionID != operationID {
		t.Errorf("expected last preemption ID %s, got %s", operationID, spotHost.LastPreemptionID)
	}
	if spotHost.PreemptedAt == nil {
		t.Error("expected preempted_at to be set")
	}

	// Verify host can be retried
	if !spotHost.CanRetry() {
		t.Error("expected preempted host to be retryable")
	}

	// Verify status can transition to provisioning (retry)
	if !spotHost.Status.CanTransitionTo(host.StatusProvisioning) {
		t.Error("expected preempted status to allow transition to provisioning")
	}

	// Verify instance name is tracked correctly
	instanceName := extractInstanceName(
		"https://www.googleapis.com/compute/v1/projects/test-project/zones/us-central1-a/instances/" + spotHost.GCEInstanceName,
	)
	if instanceName != spotHost.GCEInstanceName {
		t.Errorf("extracted instance name %q does not match host %q", instanceName, spotHost.GCEInstanceName)
	}
}

func TestSpotVMIdentification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		provisioningModel host.ProvisioningModel
		isSpot            bool
	}{
		{
			name:              "Standard VM",
			provisioningModel: host.ProvisioningModelStandard,
			isSpot:            false,
		},
		{
			name:              "Spot VM",
			provisioningModel: host.ProvisioningModelSpot,
			isSpot:            true,
		},
		{
			name:              "Empty provisioning model (defaults to standard)",
			provisioningModel: "",
			isSpot:            false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &host.Host{
				ID:                "test-host",
				ProvisioningModel: tt.provisioningModel,
			}
			if h.IsSpotVM() != tt.isSpot {
				t.Errorf("IsSpotVM() = %v, want %v", h.IsSpotVM(), tt.isSpot)
			}
		})
	}
}

func TestPlanGCEUpdate(t *testing.T) {
	t.Parallel()
	newInstance := func(status, natIP, internalIP string) *computepb.Instance {
		inst := &computepb.Instance{Status: &status}
		if natIP != "" || internalIP != "" {
			ni := &computepb.NetworkInterface{NetworkIP: &internalIP}
			if natIP != "" {
				ni.AccessConfigs = []*computepb.AccessConfig{{NatIP: &natIP}}
			}
			inst.NetworkInterfaces = []*computepb.NetworkInterface{ni}
		}
		return inst
	}

	tests := []struct {
		name       string
		dbStatus   host.Status
		dbVMStatus string // raw state already observed; "" = never observed
		dbHOReady  bool   // ho_reachable latch set: a RUNNING VM derives running, not starting
		dbIP       string
		dbInternal string
		inst       *computepb.Instance
		wantChange bool
		wantStatus host.Status
		wantClass  driftClass
	}{
		{
			// The raw observation lands even when the enum write is
			// blocked: first sight of the VM records vm_status, status kept.
			name:       "blocked enum still records first raw observation",
			dbStatus:   host.StatusDeleting,
			dbIP:       "34.1.2.3",
			dbInternal: "10.0.0.1",
			inst:       newInstance("RUNNING", "34.1.2.3", "10.0.0.1"),
			wantChange: true,
			wantStatus: host.StatusDeleting,
			wantClass:  driftBlocked,
		},
		{
			// The pre-fix bug: any host with an IP was rewritten every cycle.
			name:       "no drift means no update",
			dbStatus:   host.StatusRunning,
			dbVMStatus: "RUNNING",
			dbHOReady:  true,
			dbIP:       "34.1.2.3",
			dbInternal: "10.0.0.1",
			inst:       newInstance("RUNNING", "34.1.2.3", "10.0.0.1"),
			wantChange: false,
			wantClass:  driftNone,
		},
		{
			name:       "IP drift updates keeping status",
			dbStatus:   host.StatusRunning,
			dbVMStatus: "RUNNING",
			dbHOReady:  true,
			dbIP:       "34.1.2.3",
			dbInternal: "10.0.0.1",
			inst:       newInstance("RUNNING", "34.9.9.9", "10.0.0.1"),
			wantChange: true,
			wantStatus: host.StatusRunning,
			wantClass:  driftBenign,
		},
		{
			// The reconciler repeats the device controller's promotion: once the
			// ho_reachable latch is set, a RUNNING VM derives running, so a host
			// left in starting is adopted to running here too.
			name:       "starting with HO ready adopted to running",
			dbStatus:   host.StatusStarting,
			dbHOReady:  true,
			dbIP:       "",
			dbInternal: "10.0.0.1",
			inst:       newInstance("RUNNING", "", "10.0.0.1"),
			wantChange: true,
			wantStatus: host.StatusRunning,
			wantClass:  driftAdopted,
		},
		{
			name:       "external stop observed as stopped",
			dbStatus:   host.StatusRunning,
			dbIP:       "34.1.2.3",
			dbInternal: "10.0.0.1",
			inst:       newInstance("TERMINATED", "34.1.2.3", "10.0.0.1"),
			wantChange: true,
			wantStatus: host.StatusStopped,
			wantClass:  driftAdopted,
		},
		{
			// The pre-fix bug: a deleting host's VM still reports RUNNING and
			// the sync wrote running back, breaking the deprovision flow.
			name:       "deleting host never resurrected by GCE state",
			dbStatus:   host.StatusDeleting,
			dbVMStatus: "RUNNING", // already observed; nothing new to record
			dbIP:       "34.1.2.3",
			dbInternal: "10.0.0.1",
			inst:       newInstance("RUNNING", "34.1.2.3", "10.0.0.1"),
			wantChange: false,
			wantClass:  driftBlocked,
		},
		{
			name:       "invalid transition still applies IP drift",
			dbStatus:   host.StatusDeleting,
			dbIP:       "34.1.2.3",
			dbInternal: "10.0.0.1",
			inst:       newInstance("RUNNING", "34.9.9.9", "10.0.0.1"),
			wantChange: true,
			wantStatus: host.StatusDeleting,
			wantClass:  driftBlocked,
		},
		{
			// provisioning -> starting is the bring-up observation: the VM is
			// RUNNING but the ho_reachable latch is unset, so it is not yet
			// running (the device controller promotes it once HO answers).
			name:       "provisioning promoted by GCE running to starting",
			dbStatus:   host.StatusProvisioning,
			dbIP:       "",
			dbInternal: "",
			inst:       newInstance("RUNNING", "", ""),
			wantChange: true,
			wantStatus: host.StatusStarting,
			wantClass:  driftAdopted,
		},
		{
			name:       "pending never touched by GCE state",
			dbStatus:   host.StatusPending,
			dbVMStatus: "RUNNING", // already observed; nothing new to record
			dbIP:       "",
			dbInternal: "",
			inst:       newInstance("RUNNING", "", ""),
			wantChange: false,
			wantClass:  driftBlocked,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(Config{}, nil, newSyncHostRepo(), slog.New(slog.DiscardHandler))
			h := &host.Host{ID: "host-1", Status: tt.dbStatus, VMStatus: tt.dbVMStatus, IPAddress: tt.dbIP, InternalIP: tt.dbInternal}
			if tt.dbHOReady {
				ready := time.Now()
				h.HOReadyAt = &ready
			}
			update, class, changed := s.planGCEUpdate(h, tt.inst)
			if changed != tt.wantChange {
				t.Fatalf("changed = %v, want %v (update=%+v)", changed, tt.wantChange, update)
			}
			if class != tt.wantClass {
				t.Errorf("class = %d, want %d", class, tt.wantClass)
			}
			if changed && update.Status != tt.wantStatus {
				t.Errorf("status = %s, want %s", update.Status, tt.wantStatus)
			}
			if changed && update.PrevStatus != tt.dbStatus {
				t.Errorf("PrevStatus = %s, want the planned-from status %s", update.PrevStatus, tt.dbStatus)
			}
		})
	}
}

// vm_terminates_at follows the instance: the time GCE stops it under
// max_run_duration, which moves on every VM start. A change in it alone is an
// update to write, and a VM that is not running records none.
func TestPlanGCEUpdateFollowsVMTermination(t *testing.T) {
	t.Parallel()
	s := New(Config{}, nil, newSyncHostRepo(), slog.New(slog.DiscardHandler))
	const runSeconds int64 = 120 * 24 * 3600
	runningSince := func(start time.Time) *computepb.Instance {
		return &computepb.Instance{
			Status:             new("RUNNING"),
			LastStartTimestamp: new(start.Format(time.RFC3339Nano)),
			Scheduling:         &computepb.Scheduling{MaxRunDuration: &computepb.Duration{Seconds: new(runSeconds)}},
		}
	}
	started := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	want := started.Add(time.Duration(runSeconds) * time.Second)

	h := &host.Host{ID: "host-1", Status: host.StatusRunning, VMStatus: "RUNNING", HOReadyAt: &started}
	update, _, changed := s.planGCEUpdate(h, runningSince(started))
	if !changed || update.VMTerminatesAt == nil || !update.VMTerminatesAt.Equal(want) {
		t.Fatalf("first observation: changed=%v vm_terminates_at=%v, want %v", changed, update.VMTerminatesAt, want)
	}

	// Same instance, same start: nothing new to write.
	if _, _, changed := s.planGCEUpdate(h, runningSince(started)); changed {
		t.Error("an unchanged termination time produced an update")
	}

	// A restart moves the clock, and only the clock changed.
	restarted := started.Add(30 * 24 * time.Hour)
	update, _, changed = s.planGCEUpdate(h, runningSince(restarted))
	if wantAfter := restarted.Add(time.Duration(runSeconds) * time.Second); !changed || !update.VMTerminatesAt.Equal(wantAfter) {
		t.Errorf("after a restart: changed=%v vm_terminates_at=%v, want %v", changed, update.VMTerminatesAt, wantAfter)
	}

	// A stopped VM has no clock running.
	stopped := runningSince(restarted)
	stopped.Status = new("TERMINATED")
	update, _, changed = s.planGCEUpdate(h, stopped)
	if !changed || update.VMTerminatesAt != nil {
		t.Errorf("stopped VM: changed=%v vm_terminates_at=%v, want an update clearing it", changed, update.VMTerminatesAt)
	}
}
