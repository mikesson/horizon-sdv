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

package batchfeature

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/batch"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/idempotency"
	"cloud-cuttlefish-orchestrator/internal/domain/quota"
)

// fakeDeviceOrch is an in-memory DeviceOrchestrator that honors idempotency keys
// (like the real device/host services) so resumability can be tested. Replays
// enforce the real services' request fingerprint: the same key with a different
// request is a conflict, not a silent success.
type fakeDeviceOrch struct {
	devices  map[string]*device.Device
	byKey    map[string]*device.Device
	created  []device.CreateRequest
	buildID  string
	hostSpec *host.CreateRequest
	warnings []string
	// capacity is the pack planner's fleet view; capacityDenials[hostID]
	// injects that many ErrCapacityExceeded losses on creates targeting it
	// (the commit-time race a pack admission must rescue).
	capacity []batch.HostSlot
	// capacityPools records the pool every pack listing asked about, so a batch
	// packing onto another pool's machines is visible rather than plausible.
	capacityPools   []string
	capacityDenials map[string]int
	createErr       error
	failAfter       int // inject a failure once this many devices exist (0 = never)
	nextID          int
	hostSpecCalls   int
	// hostSpecReq is the intent the host template was resolved from, which is
	// where a requirement that never left the transport stops being visible.
	hostSpecReq device.Requirements
}

func newFakeDeviceOrch() *fakeDeviceOrch {
	return &fakeDeviceOrch{
		devices:  map[string]*device.Device{},
		byKey:    map[string]*device.Device{},
		buildID:  "build-123",
		hostSpec: &host.CreateRequest{MachineType: "n2-standard-8", Zone: "z1", MaxDevices: 4},
	}
}

func (f *fakeDeviceOrch) ResolveBuildForBatch(_ context.Context, _ device.BuildRequirement, _ string) (*device.Resolution, error) {
	return &device.Resolution{BuildID: f.buildID, BuildTarget: "target-x", BuildAction: device.BuildActionUse}, nil
}

func (f *fakeDeviceOrch) PlanBuildForBatch(_ context.Context, _ device.BuildRequirement) (*device.Resolution, error) {
	return &device.Resolution{BuildID: f.buildID, BuildTarget: "target-x", BuildAction: device.BuildActionUse}, nil
}

func (f *fakeDeviceOrch) ResolveHostSpec(_ context.Context, req device.Requirements) (*host.SpecResolution, error) {
	f.hostSpecCalls++
	f.hostSpecReq = req
	return &host.SpecResolution{Spec: f.hostSpec, Warnings: f.warnings}, nil
}

func (f *fakeDeviceOrch) ListPlaceableCapacity(_ context.Context, pool string, _ bool, _ string) ([]batch.HostSlot, error) {
	f.capacityPools = append(f.capacityPools, pool)
	return f.capacity, nil
}

func (f *fakeDeviceOrch) Create(_ context.Context, req device.CreateRequest, _ string) (*device.Device, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	if n := f.capacityDenials[req.HostID]; n > 0 {
		f.capacityDenials[req.HostID] = n - 1
		return nil, host.ErrCapacityExceeded
	}
	if req.IdempotencyKey != "" {
		if d, ok := f.byKey[req.IdempotencyKey]; ok {
			// The real fingerprint covers every JSON-visible request field; the
			// ones a divergent re-plan would change are host, name, and build.
			if d.HostID != req.HostID || d.Name != req.Name || d.BuildID != req.BuildID {
				return nil, fmt.Errorf("idempotency conflict: key %s replayed with a different request", req.IdempotencyKey)
			}
			return d, nil // idempotent replay
		}
	}
	if f.failAfter > 0 && len(f.created) >= f.failAfter {
		return nil, fmt.Errorf("injected create failure after %d devices", f.failAfter)
	}
	f.nextID++
	d := &device.Device{
		ID: fmt.Sprintf("dev-%d", f.nextID), HostID: req.HostID, Name: req.Name,
		BuildID: req.BuildID, BatchID: req.BatchID, Status: device.StatusPending,
	}
	f.devices[d.ID] = d
	f.created = append(f.created, req)
	if req.IdempotencyKey != "" {
		f.byKey[req.IdempotencyKey] = d
	}
	return d, nil
}

func (f *fakeDeviceOrch) List(_ context.Context, filter device.ListFilter) ([]*device.Device, error) {
	var out []*device.Device
	for _, d := range f.devices {
		if filter.BatchID != nil && d.BatchID != *filter.BatchID {
			continue
		}
		if !filter.IncludeDeleted && d.Status == device.StatusDeleted {
			continue
		}
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b *device.Device) int {
		if a.Name != b.Name {
			return cmpString(a.Name, b.Name)
		}
		return cmpString(a.ID, b.ID)
	})
	return out, nil
}

func (f *fakeDeviceOrch) BatchDelete(_ context.Context, devices []*device.Device) (int, error) {
	for _, d := range devices {
		if stored, ok := f.devices[d.ID]; ok {
			stored.Status = device.StatusDeleted
		}
	}
	return len(devices), nil
}

func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

type fakeHostProvisioner struct {
	created []host.CreateRequest
	origins []host.Origin
	byKey   map[string]*host.Host
	nextID  int
}

func newFakeHostProvisioner() *fakeHostProvisioner {
	return &fakeHostProvisioner{byKey: map[string]*host.Host{}}
}

func (f *fakeHostProvisioner) Create(_ context.Context, req host.CreateRequest, _ string, origin host.Origin) (*host.Host, error) {
	if req.IdempotencyKey != "" {
		if h, ok := f.byKey[req.IdempotencyKey]; ok {
			return h, nil
		}
	}
	f.nextID++
	h := &host.Host{ID: fmt.Sprintf("host-%d", f.nextID), Origin: origin}
	f.created = append(f.created, req)
	f.origins = append(f.origins, origin)
	if req.IdempotencyKey != "" {
		f.byKey[req.IdempotencyKey] = h
	}
	return h, nil
}

type fakeQuota struct{ q *quota.Quota }

func (f *fakeQuota) Get(_ context.Context, _ quota.Scope, _ string) (*quota.Quota, error) {
	return f.q, nil
}

// fakePolicyStore mirrors the real repository's contract: Create reports an
// insert-race loss as ErrBatchExists, reads return copies. getMisses makes the
// next N Gets miss even when the row exists, simulating the window where a
// concurrent same-key admission wins the insert between our Get and Create.
type fakePolicyStore struct {
	batches   map[string]*batch.Batch
	getMisses int
}

func newFakePolicyStore() *fakePolicyStore {
	return &fakePolicyStore{batches: map[string]*batch.Batch{}}
}

func (f *fakePolicyStore) Create(_ context.Context, b *batch.Batch) error {
	if _, ok := f.batches[b.ID]; ok {
		return ErrBatchExists
	}
	cp := *b
	f.batches[b.ID] = &cp
	return nil
}

func (f *fakePolicyStore) Get(_ context.Context, id string) (*batch.Batch, error) {
	if f.getMisses > 0 {
		f.getMisses--
		return nil, batch.ErrNotFound
	}
	b, ok := f.batches[id]
	if !ok {
		return nil, batch.ErrNotFound
	}
	cp := *b
	return &cp, nil
}

func (f *fakePolicyStore) List(_ context.Context, filter batch.ListFilter) ([]*batch.Batch, error) {
	out := make([]*batch.Batch, 0, len(f.batches))
	for _, id := range slices.Sorted(maps.Keys(f.batches)) {
		if filter.CreatedBy != nil && f.batches[id].CreatedBy != *filter.CreatedBy {
			continue
		}
		cp := *f.batches[id]
		out = append(out, &cp)
	}
	return out, nil
}

func (f *fakePolicyStore) Delete(_ context.Context, id string) error {
	delete(f.batches, id)
	return nil
}

func (f *fakePolicyStore) markFailed(id, reason string) {
	now := time.Now()
	f.batches[id].FailedAt = &now
	f.batches[id].FailureReason = reason
}

// testRig bundles the service with its fakes.
type testRig struct {
	svc   *Service
	dev   *fakeDeviceOrch
	hosts *fakeHostProvisioner
	store *fakePolicyStore
}

func newTestRig(headroom int64) testRig {
	d := newFakeDeviceOrch()
	h := newFakeHostProvisioner()
	st := newFakePolicyStore()
	q := &fakeQuota{q: &quota.Quota{Scope: quota.ScopePlatform, ScopeID: quota.GlobalScopeID, MaxHosts: headroom, UsedHosts: 0}}
	return testRig{svc: NewService(d, h, q, st, slog.Default()), dev: d, hosts: h, store: st}
}

func newTestService(headroom int64) (*Service, *fakeDeviceOrch, *fakeHostProvisioner) {
	r := newTestRig(headroom)
	return r.svc, r.dev, r.hosts
}

func headlessSpec(count int) (batch.Spec, device.Requirements) {
	return batch.Spec{
			NamePrefix: "ci", Count: count, MinReady: count,
			FillPolicy: batch.FillBestEffort, Placement: batch.PlacementIsolated, TTLHours: 1,
		}, device.Requirements{
			Interaction: device.InteractionHeadless,
		}
}

func TestCreate_IsolatedProvisionsAndCreates(t *testing.T) {
	t.Parallel()
	svc, dev, hosts := newTestService(100)
	spec, req := headlessSpec(10)

	res, err := svc.Create(t.Context(), spec, req, "u", "")
	require.NoError(t, err)

	assert.Equal(t, 10, res.Admitted)
	assert.Equal(t, 3, res.NewHosts, "ceil(10/4) dedicated hosts")
	assert.Equal(t, batch.StatusProvisioning, res.Status, "fresh members are still converging")
	assert.Len(t, hosts.created, 3)
	for _, h := range hosts.created {
		// Isolated means fenced from automatic placement for the host's
		// lifetime, not merely empty at admission.
		assert.True(t, h.Exclusive, "isolated batch hosts must be exclusive")
		assert.Equal(t, res.BatchID, h.BatchID, "batch hosts carry batch_id for early reap")
	}
	// These hosts are exclusive AND system-created, which is the shape that
	// derives 'dedicated', the class the fleet reclaims when it empties.
	// Stamping the origin keeps batch_id the thing that decides, so batch
	// settlement stays their only owner.
	assert.Equal(t, []host.Origin{host.OriginBatch, host.OriginBatch, host.OriginBatch}, hosts.origins)
	require.Len(t, res.Devices, 10)

	// Deterministic names and batch tag.
	for i, d := range res.Devices {
		assert.Equal(t, fmt.Sprintf("ci-%03d", i), d.Name)
		assert.Equal(t, res.BatchID, d.BatchID)
		assert.Equal(t, "build-123", d.BuildID)
	}
	// Pack 4+4+2 across the three provisioned hosts.
	perHost := map[string]int{}
	for _, req := range dev.created {
		perHost[req.HostID]++
	}
	assert.Equal(t, map[string]int{"host-1": 4, "host-2": 4, "host-3": 2}, perHost)
}

// A batch is one equivalence class: however many hosts the plan replicates,
// admission resolves the host spec, and with it the zone ranking, exactly
// once, and every host binds the once-ranked template zone.
func TestCreate_ResolvesHostSpecOnce(t *testing.T) {
	t.Parallel()
	svc, dev, hosts := newTestService(100)
	spec, req := headlessSpec(10)

	_, err := svc.Create(t.Context(), spec, req, "u", "")
	require.NoError(t, err)
	require.Len(t, hosts.created, 3)
	assert.Equal(t, 1, dev.hostSpecCalls, "one admission = one spec resolution")
	for _, h := range hosts.created {
		assert.Equal(t, "z1", h.Zone, "every host binds the once-ranked template zone")
	}
}

func TestPlan_DryRunHasNoSideEffects(t *testing.T) {
	t.Parallel()
	svc, dev, hosts := newTestService(100)
	spec, req := headlessSpec(10)

	res, err := svc.Plan(t.Context(), spec, req, "u")
	require.NoError(t, err)
	assert.Equal(t, 10, res.Admitted)
	assert.Equal(t, 3, res.NewHosts)
	assert.Empty(t, res.BatchID, "a plan creates no batch")
	assert.Empty(t, res.Devices)
	assert.Empty(t, dev.created, "a plan creates no devices")
	assert.Empty(t, hosts.created, "a plan provisions no hosts")
	assert.Contains(t, res.Resolution.PlacementReasons,
		"initial placement; provisioning may fail over to another ranked zone",
		"a provisioning plan notes the zone is subject to failover")
}

// The plan states the pool the batch would land in, taken from the host
// template it resolved: the members are placed on that pool's machines and its
// own hosts join it. The requirements' field is empty here, so a resolution
// echoing the request instead would say nothing.
func TestPlan_StatesTheResolvedPool(t *testing.T) {
	t.Parallel()
	svc, dev, _ := newTestService(100)
	dev.hostSpec.Pool = host.PoolGPU
	spec, req := headlessSpec(4)

	res, err := svc.Plan(t.Context(), spec, req, "u")
	require.NoError(t, err)
	assert.Equal(t, host.PoolGPU, res.Resolution.Pool)
}

func TestPlan_RejectsBelowMinReady(t *testing.T) {
	t.Parallel()
	svc, _, _ := newTestService(2) // headroom 2 hosts => 8 < min_ready 10
	spec, req := headlessSpec(10)

	_, err := svc.Plan(t.Context(), spec, req, "u")
	var verr *device.ValidationError
	require.ErrorAs(t, err, &verr)
}

func TestCreate_QuotaGateRejectsBelowMinReady(t *testing.T) {
	t.Parallel()
	svc, _, hosts := newTestService(2) // headroom 2 hosts => at most 8 devices
	spec, req := headlessSpec(10)      // min_ready 10 > 8

	_, err := svc.Create(t.Context(), spec, req, "u", "")
	require.Error(t, err)
	var verr *device.ValidationError
	assert.ErrorAs(t, err, &verr)
	assert.Empty(t, hosts.created, "nothing is provisioned when admission is rejected")
}

func TestCreate_AtomicPersistsPolicy(t *testing.T) {
	t.Parallel()
	r := newTestRig(100)
	svc, st := r.svc, r.store
	spec, req := headlessSpec(4)
	spec.FillPolicy = batch.FillAtomic
	spec.ReadyTimeout = 10 * time.Minute

	res, err := svc.Create(t.Context(), spec, req, "u", "ci-atomic")
	require.NoError(t, err)
	b, err := st.Get(t.Context(), res.BatchID)
	require.NoError(t, err)
	assert.Equal(t, batch.FillAtomic, b.FillPolicy)
	assert.Equal(t, 4, b.RequestedCount)
	assert.Equal(t, 10*time.Minute, b.ReadyTimeout)
	assert.False(t, b.Settled())
}

func TestCreate_FailedBatchRejectsKeyReplay(t *testing.T) {
	t.Parallel()
	r := newTestRig(100)
	svc, st := r.svc, r.store
	spec, req := headlessSpec(4)
	spec.FillPolicy = batch.FillAtomic

	res, err := svc.Create(t.Context(), spec, req, "u", "ci-doomed")
	require.NoError(t, err)
	st.markFailed(res.BatchID, "member dev-2 permanently failed")

	_, err = svc.Create(t.Context(), spec, req, "u", "ci-doomed")
	require.ErrorIs(t, err, batch.ErrFailed)
}

func TestGetAndList_FailedBatchStaysVisible(t *testing.T) {
	t.Parallel()
	r := newTestRig(100)
	svc, st := r.svc, r.store
	spec, req := headlessSpec(4)
	spec.FillPolicy = batch.FillAtomic

	res, err := svc.Create(t.Context(), spec, req, "u", "ci-postmortem")
	require.NoError(t, err)
	st.markFailed(res.BatchID, "ready_timeout elapsed")
	_, err = svc.deleteMembers(t.Context(), res.BatchID)
	require.NoError(t, err)

	// Torn down to zero members, the failed batch still reads as failed.
	v, err := svc.Get(t.Context(), res.BatchID)
	require.NoError(t, err)
	assert.Equal(t, batch.StatusFailed, v.Status)
	assert.Equal(t, 0, v.Rollup.Total)

	views, err := svc.List(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.Equal(t, batch.StatusFailed, views[0].Status)

	// User delete removes the post-mortem row: gone for good.
	require.NoError(t, svc.Delete(t.Context(), res.BatchID))
	_, err = svc.Get(t.Context(), res.BatchID)
	require.ErrorIs(t, err, batch.ErrNotFound)
}

func TestGet_RowWithoutMembersIs404(t *testing.T) {
	t.Parallel()
	r := newTestRig(100)
	svc, dev := r.svc, r.dev
	spec, req := headlessSpec(2)

	res, err := svc.Create(t.Context(), spec, req, "u", "ci-emptied")
	require.NoError(t, err)
	// All members expire (TTL teardown), the non-failed row alone is not a
	// visible batch. It is an internal resume anchor awaiting GC.
	for _, d := range res.Devices {
		dev.devices[d.ID].Status = device.StatusDeleted
	}
	_, err = svc.Get(t.Context(), res.BatchID)
	require.ErrorIs(t, err, batch.ErrNotFound)

	views, err := svc.List(t.Context(), "")
	require.NoError(t, err)
	assert.Empty(t, views)
}

func TestCreate_IdempotentByKey(t *testing.T) {
	t.Parallel()
	svc, dev, hosts := newTestService(100)
	spec, req := headlessSpec(10)

	r1, err := svc.Create(t.Context(), spec, req, "u", "ci-run-1")
	require.NoError(t, err)
	r2, err := svc.Create(t.Context(), spec, req, "u", "ci-run-1")
	require.NoError(t, err)

	assert.Equal(t, r1.BatchID, r2.BatchID, "same key -> same batch id")
	assert.Equal(t, deriveBatchID("ci-run-1"), r1.BatchID, "batch id derived from the key")
	assert.Len(t, dev.devices, 10, "retry resumes, does not duplicate devices")
	assert.Len(t, hosts.created, 3, "retry reuses the same hosts")
}

// TestCreate_ResumeAfterCrashUnderTightQuota pins the resume contract. A keyed
// retry of an admission that died mid-way must converge to the same fleet even
// when the batch's own hosts have since consumed all remaining quota.
// Re-planning against current headroom would clamp the plan, either assigning
// members to different hosts (an idempotency-fingerprint conflict) or rejecting
// the resume outright.
func TestCreate_ResumeAfterCrashUnderTightQuota(t *testing.T) {
	t.Parallel()
	// Built inline (not via newTestService) so the test can move the quota
	// under the batch between attempts.
	dev := newFakeDeviceOrch()
	hosts := newFakeHostProvisioner()
	q := &fakeQuota{q: &quota.Quota{Scope: quota.ScopePlatform, ScopeID: quota.GlobalScopeID, MaxHosts: 3}} // exactly the 3 hosts the plan needs
	svc := NewService(dev, hosts, q, newFakePolicyStore(), slog.Default())
	spec, req := headlessSpec(10)

	// First attempt: hosts provisioned, then the device loop dies at member 4.
	dev.failAfter = 4
	_, err := svc.Create(t.Context(), spec, req, "u", "ci-crash")
	require.Error(t, err)
	require.Len(t, hosts.created, 3)
	require.Len(t, dev.devices, 4, "crashed after four members")

	// The batch's own hosts now occupy the whole quota.
	q.q.UsedHosts = 3
	dev.failAfter = 0

	res, err := svc.Create(t.Context(), spec, req, "u", "ci-crash")
	require.NoError(t, err, "resume must not re-gate against quota its own hosts consumed")
	assert.Len(t, dev.devices, 10, "resume completes the fleet")
	assert.Len(t, hosts.created, 3, "resume replays the existing hosts")
	require.Len(t, res.Devices, 10)
	for i, d := range res.Devices {
		assert.Equal(t, fmt.Sprintf("ci-%03d", i), d.Name, "assignment identical to the first attempt's")
	}
}

// TestCreate_ResumeOfClampedPlanConvergesToAdmitted: when the first attempt was
// quota-clamped (admitted 8 of 10), a keyed retry must converge to the STORED
// admitted count. Re-planning at the request's full count would provision
// hosts the batch has no quota for and the retry could never converge.
func TestCreate_ResumeOfClampedPlanConvergesToAdmitted(t *testing.T) {
	t.Parallel()
	dev := newFakeDeviceOrch()
	hosts := newFakeHostProvisioner()
	q := &fakeQuota{q: &quota.Quota{Scope: quota.ScopePlatform, ScopeID: quota.GlobalScopeID, MaxHosts: 2}}
	svc := NewService(dev, hosts, q, newFakePolicyStore(), slog.Default())
	spec := batch.Spec{
		NamePrefix: "ci", Count: 10, MinReady: 4,
		FillPolicy: batch.FillBestEffort, Placement: batch.PlacementIsolated,
	}
	req := device.Requirements{Interaction: device.InteractionHeadless}

	// First attempt: 2 hosts of 4 fit the quota (admitted 8), then the device
	// loop dies at member 6.
	dev.failAfter = 5
	_, err := svc.Create(t.Context(), spec, req, "u", "ci-clamped")
	require.Error(t, err)
	require.Len(t, hosts.created, 2)
	require.Len(t, dev.devices, 5, "crashed after five members")

	// The batch's own hosts now occupy the whole quota.
	q.q.UsedHosts = 2
	dev.failAfter = 0

	res, err := svc.Create(t.Context(), spec, req, "u", "ci-clamped")
	require.NoError(t, err, "clamped resume must plan from the stored admitted count")
	assert.Len(t, dev.devices, 8, "resume completes the CLAMPED fleet, not the requested one")
	assert.Len(t, hosts.created, 2, "resume replays the existing hosts, provisions none")
	assert.Equal(t, 8, res.Admitted)
}

// TestCreate_SameKeyDifferentSpecConflicts: an Idempotency-Key reused with a
// different request is a conflict, never a silent fleet reshape. Resuming
// under a bigger count would grow membership past RequestedCount and leave an
// atomic batch unsettleable (or torn down while healthy).
func TestCreate_SameKeyDifferentSpecConflicts(t *testing.T) {
	t.Parallel()
	svc, dev, hosts := newTestService(100)
	spec, req := headlessSpec(4)

	_, err := svc.Create(t.Context(), spec, req, "u", "ci-reuse")
	require.NoError(t, err)
	require.Len(t, dev.devices, 4)
	hostsBefore := len(hosts.created)

	bigger, _ := headlessSpec(6)
	_, err = svc.Create(t.Context(), bigger, req, "u", "ci-reuse")
	require.ErrorIs(t, err, idempotency.ErrConflict)
	assert.Len(t, dev.devices, 4, "conflicting retry must not touch the fleet")
	assert.Len(t, hosts.created, hostsBefore, "conflicting retry must not provision")
}

// TestCreate_InsertRaceLossResumesWinner: a concurrent same-key admission that
// wins the row insert between our existence check and our insert is a resume,
// not an error, and the loser converges to the winner's fleet.
func TestCreate_InsertRaceLossResumesWinner(t *testing.T) {
	t.Parallel()
	r := newTestRig(100)
	spec, req := headlessSpec(4)

	r1, err := r.svc.Create(t.Context(), spec, req, "u", "ci-race")
	require.NoError(t, err)

	// The winner's row exists but our existence check misses it: the insert
	// collides and the loser must resume.
	r.store.getMisses = 1
	r2, err := r.svc.Create(t.Context(), spec, req, "u", "ci-race")
	require.NoError(t, err)
	assert.Equal(t, r1.BatchID, r2.BatchID)
	assert.Len(t, r.dev.devices, 4, "race loser replays the winner's fleet")
}

// TestCreate_InsertRaceLossWithDifferentSpecConflicts: losing the insert race
// to a same-key admission with a DIFFERENT request must surface the conflict,
// not proceed with this attempt's plan against the winner's row.
func TestCreate_InsertRaceLossWithDifferentSpecConflicts(t *testing.T) {
	t.Parallel()
	r := newTestRig(100)
	spec, req := headlessSpec(4)

	_, err := r.svc.Create(t.Context(), spec, req, "u", "ci-race-spec")
	require.NoError(t, err)

	r.store.getMisses = 1
	bigger, _ := headlessSpec(6)
	_, err = r.svc.Create(t.Context(), bigger, req, "u", "ci-race-spec")
	require.ErrorIs(t, err, idempotency.ErrConflict)
	assert.Len(t, r.dev.devices, 4, "conflicting race loser must not touch the fleet")
}

// TestCreate_ReplayOfSettledBatchReportsRealStatus: an idempotent replay of a
// batch that has since settled reports the members' real state, not a fresh
// admission's provisioning.
func TestCreate_ReplayOfSettledBatchReportsRealStatus(t *testing.T) {
	t.Parallel()
	svc, dev, _ := newTestService(100)
	spec, req := headlessSpec(4)

	r1, err := svc.Create(t.Context(), spec, req, "u", "ci-done")
	require.NoError(t, err)
	assert.Equal(t, batch.StatusProvisioning, r1.Status)
	for _, d := range r1.Devices {
		dev.devices[d.ID].Status = device.StatusRunning
	}

	r2, err := svc.Create(t.Context(), spec, req, "u", "ci-done")
	require.NoError(t, err)
	assert.Equal(t, batch.StatusReady, r2.Status)
	assert.Equal(t, 4, r2.Rollup.Running)
}

func TestGet_RollupAndStatus(t *testing.T) {
	t.Parallel()
	svc, dev, _ := newTestService(100)
	spec, req := headlessSpec(4)
	res, err := svc.Create(t.Context(), spec, req, "u", "")
	require.NoError(t, err)

	// 3 running, 1 still creating (reconciling by status).
	for i, d := range res.Devices {
		if i < 3 {
			dev.devices[d.ID].Status = device.StatusRunning
		} else {
			dev.devices[d.ID].Status = device.StatusCreating
		}
	}

	v, err := svc.Get(t.Context(), res.BatchID)
	require.NoError(t, err)
	assert.Equal(t, 4, v.Rollup.Total)
	assert.Equal(t, 3, v.Rollup.Running)
	assert.Equal(t, 1, v.Rollup.Reconciling)
	assert.Equal(t, batch.StatusProvisioning, v.Status, "not all live members running yet")

	// All running -> ready.
	for _, d := range res.Devices {
		dev.devices[d.ID].Status = device.StatusRunning
	}
	v, err = svc.Get(t.Context(), res.BatchID)
	require.NoError(t, err)
	assert.Equal(t, batch.StatusReady, v.Status)

	// A running-but-zombie member is being recovered, so the batch un-settles
	// back to provisioning.
	dev.devices[res.Devices[0].ID].HealthStatus = device.HealthStatusZombie
	v, err = svc.Get(t.Context(), res.BatchID)
	require.NoError(t, err)
	assert.Equal(t, 1, v.Rollup.Reconciling)
	assert.Equal(t, batch.StatusProvisioning, v.Status, "zombie recovery in flight")
	dev.devices[res.Devices[0].ID].HealthStatus = device.HealthStatusHealthy

	// A member tearing down leaves the ready target; its transitional status
	// must not count as reconciling and drag the batch back to provisioning.
	dev.devices[res.Devices[1].ID].Status = device.StatusDeleting
	v, err = svc.Get(t.Context(), res.BatchID)
	require.NoError(t, err)
	assert.Equal(t, 1, v.Rollup.Deleting)
	assert.Equal(t, 0, v.Rollup.Reconciling)
	assert.Equal(t, batch.StatusReady, v.Status, "remaining live members are all running")
}

func TestGet_NotFound(t *testing.T) {
	t.Parallel()
	svc, _, _ := newTestService(100)
	_, err := svc.Get(t.Context(), "no-such-batch")
	assert.ErrorIs(t, err, batch.ErrNotFound)
}

func TestDelete_TearsDownLiveMembers(t *testing.T) {
	t.Parallel()
	svc, dev, _ := newTestService(100)
	spec, req := headlessSpec(4)
	res, err := svc.Create(t.Context(), spec, req, "u", "")
	require.NoError(t, err)

	require.NoError(t, svc.Delete(t.Context(), res.BatchID))
	for _, d := range res.Devices {
		assert.Equal(t, device.StatusDeleted, dev.devices[d.ID].Status)
	}
	// Idempotent: deleting again is a no-op (no live members).
	require.NoError(t, svc.Delete(t.Context(), res.BatchID))

	// A fully-torn-down batch is gone from BOTH read surfaces: Get 404s and
	// List omits it (a batch exists exactly while it has non-deleted members).
	_, err = svc.Get(t.Context(), res.BatchID)
	assert.ErrorIs(t, err, batch.ErrNotFound)
	views, err := svc.List(t.Context(), "")
	require.NoError(t, err)
	assert.Empty(t, views)
}

func packSpec(count int) (batch.Spec, device.Requirements) {
	spec, req := headlessSpec(count)
	spec.Placement = batch.PlacementPack
	return spec, req
}

func TestCreate_PackFillsExistingCapacityFirst(t *testing.T) {
	t.Parallel()
	svc, dev, hosts := newTestService(100)
	dev.capacity = []batch.HostSlot{{HostID: "warm-1", Free: 3}, {HostID: "warm-2", Free: 2}}
	spec, req := packSpec(6)

	res, err := svc.Create(t.Context(), spec, req, "u", "")
	require.NoError(t, err)
	assert.Equal(t, 6, res.Admitted)
	// 3 + 2 slots packed, 1 overflow member on one new host.
	require.Len(t, hosts.created, 1)
	assert.Equal(t, 1, res.NewHosts)

	byHost := map[string]int{}
	for _, d := range res.Devices {
		byHost[d.HostID]++
	}
	assert.Equal(t, 3, byHost["warm-1"])
	assert.Equal(t, 2, byHost["warm-2"])
	assert.Equal(t, 1, byHost["host-1"])
	// Pack overflow hosts stay shareable (the batch opted into the shared
	// fleet) but carry the batch link for early reaping.
	assert.False(t, hosts.created[0].Exclusive)
	assert.NotEmpty(t, hosts.created[0].BatchID)
}

func TestCreate_PackRescuesCapacityRace(t *testing.T) {
	t.Parallel()
	svc, dev, hosts := newTestService(100)
	dev.capacity = []batch.HostSlot{{HostID: "warm-1", Free: 4}}
	// Two of the four planned slots on warm-1 are stolen by concurrent
	// creates between planning and commit.
	dev.capacityDenials = map[string]int{"warm-1": 2}
	spec, req := packSpec(4)

	res, err := svc.Create(t.Context(), spec, req, "u", "ci-race")
	require.NoError(t, err)
	require.Len(t, res.Devices, 4, "every member lands despite the race")
	require.Len(t, hosts.created, 1, "one overflow host rescues the raced members")

	byHost := map[string]int{}
	for _, d := range res.Devices {
		byHost[d.HostID]++
	}
	assert.Equal(t, 2, byHost["warm-1"])
	assert.Equal(t, 2, byHost["host-1"])
}

func TestCreate_PackResumeFillsOnlyMissing(t *testing.T) {
	t.Parallel()
	svc, dev, hosts := newTestService(100)
	dev.capacity = []batch.HostSlot{{HostID: "warm-1", Free: 2}}
	spec, req := packSpec(4)

	// First attempt dies after two members (both packed onto warm-1).
	dev.failAfter = 2
	_, err := svc.Create(t.Context(), spec, req, "u", "ci-pack-crash")
	require.Error(t, err)
	require.Len(t, dev.devices, 2)

	// The retry sees different capacity (warm-1 is now full; warm-2 opened
	// up) and fills ONLY the missing members against it. Existing members
	// are untouched and no duplicate fleet appears.
	dev.failAfter = 0
	dev.capacity = []batch.HostSlot{{HostID: "warm-2", Free: 1}}
	res, err := svc.Create(t.Context(), spec, req, "u", "ci-pack-crash")
	require.NoError(t, err)
	require.Len(t, res.Devices, 4)
	assert.Len(t, dev.devices, 4, "resume created exactly the missing members")

	byHost := map[string]int{}
	names := map[string]bool{}
	for _, d := range res.Devices {
		byHost[d.HostID]++
		names[d.Name] = true
	}
	assert.Equal(t, 2, byHost["warm-1"], "original members kept in place")
	assert.Equal(t, 1, byHost["warm-2"], "one missing member packed onto new capacity")
	// Two provisioned hosts total: attempt 1's planned host (never populated
	// before the crash) plus the resume host for the last missing member.
	require.Len(t, hosts.created, 2)
	assert.Equal(t, 1, byHost["host-2"], "remainder landed on the resume host")
	for i := range 4 {
		assert.True(t, names[fmt.Sprintf("ci-%03d", i)], "member %d present", i)
	}
}

// A batch packs onto the machines its own hosts would join, never another
// pool's. The listing is advisory, so a wrong pool here fails no commit. It
// hands the planner slots on hosts the members can never be placed on, and the
// batch converges by provisioning the whole fleet it was told it did not need.
func TestCreate_PackListsCapacityInTheHostTemplatesPool(t *testing.T) {
	t.Parallel()
	svc, dev, _ := newTestService(100)
	dev.hostSpec.Pool = host.PoolGPU
	dev.capacity = []batch.HostSlot{{HostID: "warm-1", Free: 4}}
	spec, req := packSpec(4)

	_, err := svc.Create(t.Context(), spec, req, "u", "")
	require.NoError(t, err)
	require.NotEmpty(t, dev.capacityPools)
	for _, pool := range dev.capacityPools {
		assert.Equal(t, host.PoolGPU, pool,
			"the pack listing asks about the pool the batch's own hosts join")
	}
}
