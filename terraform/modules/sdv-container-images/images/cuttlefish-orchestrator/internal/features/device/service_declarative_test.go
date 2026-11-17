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

package devicefeature

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/batch"
	"cloud-cuttlefish-orchestrator/internal/domain/build"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/domain/idempotency"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/domain/quota"
	"cloud-cuttlefish-orchestrator/internal/features/device/placement"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/idempotencytest"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// --- declarative dependency fakes ---

// fakeProvisioner creates hosts directly in the host fake (status
// provisioning, like a real async create) and records the specs it saw.
type fakeProvisioner struct {
	hostRepo *hosttest.FakeRepo
	specs    []host.CreateRequest
	origins  []host.Origin
	err      error
}

func (p *fakeProvisioner) Create(_ context.Context, req host.CreateRequest, _ string, origin host.Origin) (*host.Host, error) {
	if p.err != nil {
		return nil, p.err
	}
	p.specs = append(p.specs, req)
	p.origins = append(p.origins, origin)
	h := &host.Host{
		ID:          uuid.New().String(),
		Name:        req.Name,
		Zone:        req.Zone,
		MachineType: req.MachineType,
		GPUType:     req.GPUType,
		GPUCount:    req.GPUCount,
		MaxDevices:  req.MaxDevices,
		Status:      host.StatusProvisioning,
		Origin:      origin,
		Exclusive:   req.Exclusive,
		BatchID:     req.BatchID,
	}
	p.hostRepo.Hosts[h.ID] = h
	return h, nil
}

type fakePolicy struct{ values map[string]string }

func (p *fakePolicy) Get(_ context.Context, key string) (*models.SystemConfig, error) {
	v, ok := p.values[key]
	if !ok {
		return nil, cspanner.ErrKeyNotFound
	}
	return &models.SystemConfig{Key: key, Value: v}, nil
}

type fakeMachineTypes struct {
	types     map[string]*machinetype.MachineType
	defaultID string
}

func (m *fakeMachineTypes) Get(_ context.Context, id string) (*machinetype.MachineType, error) {
	mt, ok := m.types[id]
	if !ok {
		return nil, machinetype.ErrNotFound
	}
	return mt, nil
}

func (m *fakeMachineTypes) GetDefault(_ context.Context) (*machinetype.MachineType, error) {
	if mt, ok := m.types[m.defaultID]; ok {
		return mt, nil
	}
	return nil, machinetype.ErrNotFound
}

// List honors every filter the production code sets. The Family filter matters
// because host sizing grows a shape within its own family, and a fake that
// ignored it would let a sizing bug that crosses families pass here.
func (m *fakeMachineTypes) List(_ context.Context, f machinetype.ListFilter) ([]*machinetype.MachineType, error) {
	var out []*machinetype.MachineType
	for _, mt := range m.types {
		if f.IsEnabled != nil && mt.IsEnabled != *f.IsEnabled {
			continue
		}
		if f.Family != nil && mt.Family != *f.Family {
			continue
		}
		if f.GPUSupport != nil && *f.GPUSupport && mt.GPUMode == machinetype.GPUModeNone {
			continue
		}
		out = append(out, mt)
	}
	return out, nil
}

// fakeRanker returns scripted zone candidates and records the ranking inputs.
// byModel, when non-nil, scripts candidates per provisioning model (keyed by
// ValidationRequest.ProvisioningModel) so tests can starve Spot while leaving
// standard capacity viable.
type fakeRanker struct {
	candidates []gcecatalog.ZoneCandidate
	byModel    map[string][]gcecatalog.ZoneCandidate
	err        error
	calls      int
	lastReq    gcecatalog.ValidationRequest
	lastOpts   gcecatalog.RankOptions
}

func (r *fakeRanker) RankZones(_ context.Context, req gcecatalog.ValidationRequest, opts gcecatalog.RankOptions) ([]gcecatalog.ZoneCandidate, error) {
	r.calls++
	r.lastReq, r.lastOpts = req, opts
	if r.byModel != nil {
		return r.byModel[req.ProvisioningModel], r.err
	}
	return r.candidates, r.err
}

type fakeQuota struct{ maxHosts, usedHosts int64 }

func (q *fakeQuota) Get(_ context.Context, scope quota.Scope, scopeID string) (*quota.Quota, error) {
	return &quota.Quota{Scope: scope, ScopeID: scopeID, MaxHosts: q.maxHosts, UsedHosts: q.usedHosts}, nil
}

// fakeRegistrar registers tracking builds straight into the mock repo, the
// way the build service's Register does on the real path.
type fakeRegistrar struct{ repo *mockBuildRepo }

func (f *fakeRegistrar) Register(ctx context.Context, req build.CreateRequest) (*build.Build, error) {
	b := &build.Build{
		ID: uuid.New().String(), Name: req.Name, Source: req.Source,
		Branch: req.Branch, Target: req.Target,
		IsDefault: req.IsDefault,
	}
	if req.CreatedBy != "" {
		b.CreatedBy = &req.CreatedBy
	}
	if err := f.repo.Create(ctx, b); err != nil {
		return nil, err
	}
	return b, nil
}

// declarativeFixture wires a DeviceService with all declarative fakes.
type declarativeFixture struct {
	svc         *DeviceService
	hostRepo    *hosttest.FakeRepo
	deviceRepo  *atomicDeviceRepo
	buildRepo   *mockBuildRepo
	provisioner *fakeProvisioner
	policy      *fakePolicy
	ranker      *fakeRanker
}

func newDeclarativeFixture(t *testing.T) *declarativeFixture {
	t.Helper()
	return newDeclarativeFixtureCapacity(t, nil)
}

// newDeclarativeFixtureCapacity is newDeclarativeFixture with the placement
// capacity store overridden (nil = the device repo fake, whose picks mirror
// the real query) and any extra service options appended.
func newDeclarativeFixtureCapacity(t *testing.T, capacity placement.CapacityStore, opts ...DeviceServiceOption) *declarativeFixture {
	t.Helper()
	hostRepo := hosttest.NewFakeRepo()
	deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
	buildRepo := newMockBuildRepo()
	provisioner := &fakeProvisioner{hostRepo: hostRepo}
	policy := &fakePolicy{values: map[string]string{}}
	// vCPU and memory mirror the seeded catalog, because host sizing compares
	// against them: a catalog of zero-sized shapes states nothing to size
	// against and would exercise the degrade path instead of the rule.
	machineTypes := &fakeMachineTypes{
		defaultID: "n1-standard-8",
		types: map[string]*machinetype.MachineType{
			"n1-standard-8": {
				ID: "n1-standard-8", Family: "n1", VCPUs: 8, MemoryGB: 30, Fit: 3,
				IsEnabled: true, IsDefault: true,
				GPUMode: machinetype.GPUModeAttachable, DefaultGPUType: "nvidia-tesla-t4",
			},
			"n1-standard-16": {
				ID: "n1-standard-16", Family: "n1", VCPUs: 16, MemoryGB: 60, Fit: 7,
				IsEnabled: true,
				GPUMode:   machinetype.GPUModeAttachable, DefaultGPUType: "nvidia-tesla-t4",
			},
			"n1-standard-32": {
				ID: "n1-standard-32", Family: "n1", VCPUs: 32, MemoryGB: 120, Fit: 14,
				IsEnabled: true,
				GPUMode:   machinetype.GPUModeAttachable, DefaultGPUType: "nvidia-tesla-t4",
			},
			// Not in the seeded catalog, but the catalog is operator-editable
			// and synced from GCE: a family whose vCPU and memory do NOT rise
			// together is what tells a growth rule from a rule that only looks
			// like one.
			"n1-highcpu-16": {
				ID: "n1-highcpu-16", Family: "n1", VCPUs: 16, MemoryGB: 16, Fit: 1,
				IsEnabled: true,
				GPUMode:   machinetype.GPUModeAttachable, DefaultGPUType: "nvidia-tesla-t4",
			},
			"g2-standard-8": {
				ID: "g2-standard-8", Family: "g2", VCPUs: 8, MemoryGB: 32, Fit: 3,
				IsEnabled: true,
				GPUMode:   machinetype.GPUModeIntegrated, SortOrder: 5,
			},
		},
	}
	ranker := &fakeRanker{candidates: []gcecatalog.ZoneCandidate{
		{Zone: "europe-west4-a", Region: "europe-west4", Score: 80, Reasons: []string{"headroom 80/100"}},
		{Zone: "europe-west4-b", Region: "europe-west4", Score: 60, Reasons: []string{"headroom 60/100"}},
	}}

	if capacity == nil {
		capacity = deviceRepo
	}
	allOpts := append([]DeviceServiceOption{
		WithTaskEnqueuer(&fakeEnqueuer{}),
		WithLifetimePolicy(policy),
		WithDeclarativeCreate(DeclarativeDeps{
			Provisioner: provisioner, Policy: policy, MachineTypes: machineTypes,
			Ranker: ranker, Quota: &fakeQuota{maxHosts: 100},
			Registrar: &fakeRegistrar{repo: buildRepo}, Capacity: capacity,
			ZoneHealth: fakeStockoutMemory{},
		}),
	}, opts...)
	svc, err := NewDeviceService(deviceRepo, hostRepo, buildRepo,
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(), allOpts...)
	require.NoError(t, err)
	return &declarativeFixture{
		svc: svc, hostRepo: hostRepo, deviceRepo: deviceRepo,
		buildRepo: buildRepo, provisioner: provisioner, policy: policy, ranker: ranker,
	}
}

func declarativeReq(interaction device.Interaction) device.CreateRequest {
	return device.CreateRequest{
		Name: "dev-1",
		Requirements: &device.Requirements{
			Interaction: interaction,
		},
	}
}

func (f *declarativeFixture) addBuild(t *testing.T, isDefault bool) *build.Build {
	t.Helper()
	b := &build.Build{
		ID: uuid.New().String(), Name: "seeded",
		Source: build.SourceAB, Branch: "seeded-branch", Target: "seeded-target", IsDefault: isDefault,
	}
	require.NoError(t, f.buildRepo.Create(t.Context(), b))
	return b
}

func (f *declarativeFixture) addHost(id string, status host.Status, devices, maxDevices, gpuCount int64) {
	f.hostRepo.Hosts[id] = &host.Host{
		ID: id, Name: id, Status: status,
		CurrentDeviceCount: devices, MaxDevices: maxDevices, GPUCount: gpuCount,
	}
}

// A declarative replay with the same key returns the recorded device. The
// fingerprint covers the user's ORIGINAL request (not the resolved form
// commitResolved rewrites), and the key resolves at entry, before placement
// re-runs and before the no-capacity path can auto-provision an orphan host.
// The setup makes the replay resolve DIFFERENTLY on purpose: the first create
// fills the only host, so a re-run of placement would provision.
func TestDeclarativeCreateIdempotentReplay(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixtureCapacity(t, nil, WithIdempotencyStore(idempotencytest.NewFakeStore()))
	f.addBuild(t, true)
	f.addHost("h1", host.StatusRunning, 1, 2, 0) // one slot left
	ctx := t.Context()

	req := declarativeReq(device.InteractionHeadless)
	req.IdempotencyKey = "k-decl"
	d1, res1, err := f.svc.CreateDeclarative(ctx, req, "u@example.com")
	require.NoError(t, err)
	require.Equal(t, "h1", res1.HostID, "first create takes h1's last slot")
	require.Empty(t, f.provisioner.specs)

	// On the replay h1 is now full, so re-running placement would
	// auto-provision. The recorded device must come back instead, with no
	// new host.
	d2, res2, err := f.svc.CreateDeclarative(ctx, req, "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, d1.ID, d2.ID, "replay returns the recorded device")
	assert.Equal(t, d1.HostID, res2.HostID)
	assert.Empty(t, f.provisioner.specs, "replay must not auto-provision a host")
	assert.Len(t, f.deviceRepo.Devices, 1, "replay must not create a second device")

	// Same key, different requirements -> the sequential conflict.
	other := declarativeReq(device.InteractionInteractive)
	other.IdempotencyKey = "k-decl"
	_, _, err = f.svc.CreateDeclarative(ctx, other, "u@example.com")
	require.ErrorIs(t, err, idempotency.ErrConflict)
}

// --- validation ---

func TestCreateDeclarativeValidation(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	ctx := t.Context()

	req := declarativeReq(device.InteractionHeadless)
	req.HostID = "also-set"
	_, _, err := f.svc.CreateDeclarative(ctx, req, "u@example.com")
	var verr *device.ValidationError
	require.ErrorAs(t, err, &verr, "host_id + requirements must be rejected")

	req = declarativeReq(device.InteractionHeadless)
	req.Requirements.Build = device.BuildRequirement{
		Registered: "some-id", TrackBranch: "b", TrackTarget: "t",
	}
	_, _, err = f.svc.CreateDeclarative(ctx, req, "u@example.com")
	require.ErrorAs(t, err, &verr, "registered + track must be rejected")

	req = declarativeReq("driving")
	_, _, err = f.svc.CreateDeclarative(ctx, req, "u@example.com")
	require.ErrorAs(t, err, &verr, "unknown interaction must be rejected")
}

func TestCreateRequiresHostIDWithoutRequirements(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)

	_, err := f.svc.Create(t.Context(), device.CreateRequest{Name: "dev-1"}, "u@example.com")
	var verr *device.ValidationError
	require.ErrorAs(t, err, &verr, "imperative create without host_id must 400, not 'host not found'")
}

// --- build resolution ---

func TestDeclarativeBuildDefaultAutoRegisters(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addHost("h1", host.StatusRunning, 0, 4, 0)
	ctx := t.Context()

	// Empty registry: the upstream default is registered as a tracking build
	// and flagged default.
	d, res, err := f.svc.CreateDeclarative(ctx, declarativeReq(device.InteractionHeadless), "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, "register", res.BuildAction)
	registered, gerr := f.buildRepo.Get(ctx, res.BuildID)
	require.NoError(t, gerr)
	assert.Equal(t, "aosp-android-latest-release", registered.Branch)
	assert.Equal(t, "aosp_cf_x86_64_only_phone-userdebug", registered.Target)
	assert.Empty(t, registered.BuildID, "tracking build pins no build ID (resolves latest)")
	assert.True(t, registered.IsDefault, "first build in an empty registry becomes the default")
	assert.Equal(t, registered.ID, d.BuildID)

	// Second create resolves to the same build instead of registering another.
	req2 := declarativeReq(device.InteractionHeadless)
	req2.Name = "dev-2"
	_, res2, err := f.svc.CreateDeclarative(ctx, req2, "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, "use", res2.BuildAction)
	assert.Equal(t, registered.ID, res2.BuildID)
}

func TestDeclarativeBuildPolicyOverridesUpstream(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addHost("h1", host.StatusRunning, 0, 4, 0)
	f.policy.values[models.ConfigKeyDefaultUpstreamBranch] = "corp-branch"
	f.policy.values[models.ConfigKeyDefaultUpstreamTarget] = "corp-target"

	_, res, err := f.svc.CreateDeclarative(t.Context(), declarativeReq(device.InteractionHeadless), "u@example.com")
	require.NoError(t, err)
	registered, gerr := f.buildRepo.Get(t.Context(), res.BuildID)
	require.NoError(t, gerr)
	assert.Equal(t, "corp-branch", registered.Branch)
	assert.Equal(t, "corp-target", registered.Target)
}

func TestDeclarativeBuildRegisteredAndTrack(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addHost("h1", host.StatusRunning, 0, 4, 0)
	seeded := f.addBuild(t, true)
	ctx := t.Context()

	// Registered ID.
	req := declarativeReq(device.InteractionHeadless)
	req.Requirements.Build.Registered = seeded.ID
	d, res, err := f.svc.CreateDeclarative(ctx, req, "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, seeded.ID, d.BuildID)
	assert.Equal(t, "use", res.BuildAction)

	// Track a new branch/target: registered once, reused after.
	req = declarativeReq(device.InteractionHeadless)
	req.Name = "dev-2"
	req.Requirements.Build.TrackBranch = "release-x"
	req.Requirements.Build.TrackTarget = "target-x"
	_, res, err = f.svc.CreateDeclarative(ctx, req, "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, "register", res.BuildAction)
	tracked, gerr := f.buildRepo.Get(ctx, res.BuildID)
	require.NoError(t, gerr)
	assert.False(t, tracked.IsDefault, "explicitly tracked builds never steal the default")
}

// --- placement ---

func TestDeclarativePlacesMostLoadedFirst(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)
	f.addHost("h-empty", host.StatusRunning, 0, 4, 0)
	f.addHost("h-busy", host.StatusRunning, 3, 4, 0)

	d, res, err := f.svc.CreateDeclarative(t.Context(), declarativeReq(device.InteractionHeadless), "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, "h-busy", d.HostID, "bin-pack: most-loaded host with capacity wins")
	assert.Equal(t, "place", res.HostAction)
	assert.Empty(t, f.provisioner.specs, "no provisioning when capacity exists")
}

func TestDeclarativeInteractivePrefersGPUHost(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)
	f.addHost("h-cpu", host.StatusRunning, 3, 4, 0)
	f.addHost("h-gpu", host.StatusRunning, 0, 4, 1)

	d, res, err := f.svc.CreateDeclarative(t.Context(), declarativeReq(device.InteractionInteractive), "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, "h-gpu", d.HostID, "interactive prefers the GPU host even when a CPU host is fuller")
	assert.Empty(t, res.Warnings)
}

func TestDeclarativeInteractiveDegradesWithWarning(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)
	f.addHost("h-cpu", host.StatusRunning, 0, 4, 0)

	d, res, err := f.svc.CreateDeclarative(t.Context(), declarativeReq(device.InteractionInteractive), "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, "h-cpu", d.HostID)
	// Both GPU shortfalls end in "software rendering", so the full string is
	// what distinguishes placing on a CPU host from provisioning one; and the
	// fallback must place on existing capacity rather than provision.
	assert.Equal(t, []string{"no GPU capacity available; placed on a CPU host (software rendering)"},
		res.Warnings)
	assert.Empty(t, f.provisioner.specs)
}

// The inverse of TestDeclarativeInteractivePrefersGPUHost: the same interactive
// intent, with the rendering mode pinned to swiftshader, must land on the CPU
// host. A relaunch on a GPU host runs the auto walk into an accelerated mode,
// and snapshotting one takes crosvm down.
//
// The GPU host is the FULLER one here on purpose, so the bin-pack would pick
// it and only the fence moves the choice. With the loads the GPU-preference
// test uses, this would pass with no fence at all.
//
// The empty warnings assert as much as the host does. The fence CANCELS the GPU
// want rather than degrading it, so this create is not a GPU shortfall and must
// not be reported or counted as one.
func TestDeclarativeSwiftshaderNeverPlacesOnGPUHost(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)
	f.addHost("h-gpu", host.StatusRunning, 3, 4, 1)
	f.addHost("h-cpu", host.StatusRunning, 0, 4, 0)

	req := declarativeReq(device.InteractionInteractive)
	req.GPUMode = device.GPUModeSwift

	d, res, err := f.svc.CreateDeclarative(t.Context(), req, "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, "h-cpu", d.HostID, "swiftshader is fenced off GPU capacity even for interactive intent")
	assert.Empty(t, res.Warnings, "a fenced create got the host it asked for; that is no degradation")
	assert.Empty(t, f.provisioner.specs, "the fence must place on the CPU host, not provision")
}

func TestDeclarativePacksOntoProvisioningHost(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)
	f.addHost("h-coming-up", host.StatusProvisioning, 1, 4, 0)

	d, res, err := f.svc.CreateDeclarative(t.Context(), declarativeReq(device.InteractionHeadless), "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, "h-coming-up", d.HostID, "provisioning hosts are placement candidates")
	assert.Equal(t, "place", res.HostAction)
	assert.Empty(t, f.provisioner.specs)
}

func TestDeclarativeCarriesAdvancedEnvConfig(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)
	f.addHost("h1", host.StatusRunning, 0, 4, 0)

	req := declarativeReq(device.InteractionHeadless)
	req.Mode = modeAdvanced
	req.EnvConfig = map[string]any{"instances": []any{map[string]any{"name": "dev-1"}}}

	d, res, err := f.svc.CreateDeclarative(t.Context(), req, "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, modeAdvanced, d.Mode, "mode survives the declarative commit")
	assert.NotNil(t, d.EnvConfig, "env_config survives the declarative commit")
	assert.Equal(t, "place", res.HostAction)
}

// --- auto-provision ---

func TestDeclarativeAutoProvisionsWhenNoCapacity(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)
	f.addHost("h-full", host.StatusRunning, 4, 4, 0)

	d, res, err := f.svc.CreateDeclarative(t.Context(), declarativeReq(device.InteractionHeadless), "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, "provision", res.HostAction)
	require.Len(t, f.provisioner.specs, 1)
	spec := f.provisioner.specs[0]
	assert.Equal(t, "n1-standard-8", spec.MachineType, "headless uses the catalog default shape")
	assert.Equal(t, "europe-west4-a", spec.Zone, "best-ranked zone binds the auto host")
	assert.Equal(t, int64(3), spec.MaxDevices,
		"30 GB holds three 8 GB guests beside the host reserve, not the density policy's four")
	assert.Empty(t, spec.GPUType, "headless provisions no GPU")
	assert.Equal(t, res.HostID, d.HostID)
	assert.Equal(t, d.HostID, f.hostRepo.Hosts[d.HostID].ID, "device landed on the provisioned host")
	// A miss, stamped as one. The spec here comes from the same resolver the
	// fleet keeper calls, so nothing on the request distinguishes the two and
	// only the call site can: stamped 'pool' instead, this host would read as
	// standing inventory the fleet had already provided.
	assert.Equal(t, []host.Origin{host.OriginAuto}, f.provisioner.origins)
}

// The auto-provisioned host takes the resolved shape's own fit as its slot
// ceiling (the cores-and-memory arithmetic, or a per-shape override) with no
// fleet-wide scalar in the way.
func TestDeclarativeAutoProvisionTakesTheShapesFit(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)
	f.addHost("h-full", host.StatusRunning, 4, 4, 0)

	_, res, err := f.svc.CreateDeclarative(t.Context(), declarativeReq(device.InteractionHeadless), "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, "provision", res.HostAction)
	require.Len(t, f.provisioner.specs, 1)
	assert.Equal(t, int64(3), f.provisioner.specs[0].MaxDevices,
		"an n1-standard-8 holds three 8 GB guests beside the host reserve")
}

// --- group host sizing ---

// groupReq is a headless advanced create of n instances. memoryMB and cpus of
// zero declare nothing, so the instance takes the same defaults the create
// itself would send the host. Numbers go in as float64 because that is what a
// decoded JSON body carries, which is the only form production ever sees.
func groupReq(n, memoryMB, cpus int) device.CreateRequest {
	req := declarativeReq(device.InteractionHeadless)
	req.Mode = modeAdvanced
	instances := make([]any, n)
	for i := range instances {
		vm := map[string]any{}
		if memoryMB > 0 {
			vm["memory_mb"] = float64(memoryMB)
		}
		if cpus > 0 {
			vm["cpus"] = float64(cpus)
		}
		inst := map[string]any{"name": fmt.Sprintf("inst-%d", i)}
		if len(vm) > 0 {
			inst["vm"] = vm
		}
		instances[i] = inst
	}
	req.EnvConfig = map[string]any{"instances": instances}
	return req
}

// provisionedSpec runs a create with no host available and returns the host
// spec it provisioned.
func provisionedSpec(t *testing.T, req device.CreateRequest) (host.CreateRequest, *Resolution) {
	t.Helper()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)
	_, res, err := f.svc.CreateDeclarative(t.Context(), req, "u@example.com")
	require.NoError(t, err)
	require.Len(t, f.provisioner.specs, 1)
	return f.provisioner.specs[0], res
}

// The phone-beside-a-watch case, which was measured running on the catalog
// default: two default instances fit, so the shape must not move. A sizing rule
// that grows hosts nobody needs is as wrong as one that never grows them.
func TestDeclarativeGroupKeepsDefaultShapeWhenItFits(t *testing.T) {
	t.Parallel()
	spec, res := provisionedSpec(t, groupReq(2, 0, 0))
	assert.Equal(t, "n1-standard-8", spec.MachineType)
	assert.True(t, spec.Exclusive, "a group gets its host to itself")
	assert.Equal(t, int64(3), spec.MaxDevices, "the machine holds three, and two of them are this group's")
	assert.Empty(t, res.Warnings)
}

// Four default instances declare more guest RAM than the default shape carries,
// so the host grows within its own family, which is what keeps the host image,
// the GPU mode and the boot-disk type the same facts they were.
func TestDeclarativeGroupOutgrowsTheDefaultShape(t *testing.T) {
	t.Parallel()
	spec, res := provisionedSpec(t, groupReq(4, 0, 0))
	assert.Equal(t, "n1-standard-16", spec.MachineType,
		"4 x 8 GB plus the host reserve does not fit 30 GB; the next n1 rung does")
	assert.Empty(t, res.Warnings, "growing to a shape that fits is not a degradation")
}

// The pair the fleet's demand signal reads: what this host holds, and what a
// standby host would have held instead. They are allowed to differ, and the
// four-instance default group is the create that sits exactly on the boundary.
// It grows the MACHINE without growing the slot count the packing policy
// allows, so a signal comparing it against the policy alone would read it as
// demand a buffer could have absorbed.
//
// Both halves are stamped rather than the verdict they imply, so an operator
// moving the packing policy cannot reclassify a horizon of history.
func TestDeclarativeStampsWhatAStandbyHostWouldHaveOffered(t *testing.T) {
	t.Parallel()

	ordinary, _ := provisionedSpec(t, declarativeReq(device.InteractionHeadless))
	require.NotNil(t, ordinary.Resolved, "an auto-provision is judged by the resolver")
	assert.Equal(t, int64(3), ordinary.MaxDevices)
	assert.Equal(t, int64(3), ordinary.Resolved.Density,
		"an ordinary create is what the buffer exists for; a gap here would read as unservable demand")
	assert.True(t, ordinary.Resolved.FitsDefaultHost)
	assert.False(t, ordinary.Resolved.GPURequired)

	grown, _ := provisionedSpec(t, groupReq(4, 0, 0))
	require.NotNil(t, grown.Resolved)
	assert.Equal(t, "n1-standard-16", grown.MachineType)
	assert.Equal(t, int64(7), grown.MaxDevices, "the grown machine packs to its own fit")
	assert.Equal(t, int64(3), grown.Resolved.Density, "a standby host would still have offered three")
	assert.False(t, grown.Resolved.FitsDefaultHost,
		"four default guests outgrew the default machine's RAM, which is why it was grown")
}

// The GPU axis, and the distinction the whole discriminator rests on. Both of
// these creates provision a g2, since interactive WANTS a GPU, but only one of
// them is beyond a CPU standby host, because placement degrades an unmet GPU
// PREFERENCE onto a CPU host and refuses only an unmet REQUIREMENT.
//
// Stamping the provisioned shape instead would mark both unservable and delete
// the interactive population from the demand signal entirely.
func TestDeclarativeStampsRequiredGPUNotThePreferredOne(t *testing.T) {
	t.Parallel()

	preferred, _ := provisionedSpec(t, declarativeReq(device.InteractionInteractive))
	require.NotNil(t, preferred.Resolved)
	assert.Equal(t, "g2-standard-8", preferred.MachineType, "interactive provisions a GPU shape")
	assert.False(t, preferred.Resolved.GPURequired,
		"a preference a CPU standby host can still serve, degraded")
	assert.True(t, preferred.Resolved.FitsDefaultHost,
		"one default guest fits the default template; the GPU is the only thing it lacks")

	req := declarativeReq(device.InteractionInteractive)
	req.Requirements.GPURequired = true
	required, _ := provisionedSpec(t, req)
	require.NotNil(t, required.Resolved)
	assert.True(t, required.Resolved.GPURequired, "no CPU standby host can answer this")
}

// The declared per-instance shape drives the size, not the instance count: two
// big instances outgrow four small ones. Without this the rule would just be a
// count threshold wearing a memory calculation.
func TestDeclarativeGroupSizesFromDeclaredMemory(t *testing.T) {
	t.Parallel()
	spec, _ := provisionedSpec(t, groupReq(2, 32768, 0))
	assert.Equal(t, "n1-standard-32", spec.MachineType,
		"2 x 32 GB clears the 60 GB rung and lands on the 120 GB one")
}

// vCPUs are a floor, never a sum. Two instances declaring eight vCPUs each ask
// for sixteen between them, and the host stays at eight, which is the same
// oversubscription the operator's density policy already grants four single
// devices on one host.
func TestDeclarativeGroupDoesNotSumVCPUs(t *testing.T) {
	t.Parallel()
	spec, _ := provisionedSpec(t, groupReq(2, 2048, 8))
	assert.Equal(t, "n1-standard-8", spec.MachineType,
		"summing vCPUs would grow this host to 16 for no reason")
}

// Growth stays inside the machine family. A headless group that outgrows the
// default must not land on the GPU family just because a G2 shape happened to
// be the cheapest fit. The host image is chosen per family, so that would be
// a different kind of host.
func TestDeclarativeGroupGrowsWithinItsFamily(t *testing.T) {
	t.Parallel()
	// Just past 30 GB, where the g2 shape (32 GB) would undercut the next n1
	// rung (60 GB) if the family filter were dropped.
	spec, _ := provisionedSpec(t, groupReq(3, 9000, 0))
	assert.Equal(t, "n1-standard-16", spec.MachineType)
}

// Growing for one resource must not shrink the other. A group that needs vCPU
// width and little memory still keeps the default shape's memory: a host that
// silently dropped from 30 GB to 16 GB while "growing" would be a downgrade
// wearing an upgrade's name.
func TestDeclarativeGroupGrowthNeverShrinksTheOtherDimension(t *testing.T) {
	t.Parallel()
	spec, _ := provisionedSpec(t, groupReq(2, 2048, 16))
	assert.Equal(t, "n1-standard-16", spec.MachineType,
		"n1-highcpu-16 has the vCPUs and less memory than the shape being grown from")
}

// A group no enabled shape in the family can hold still provisions, on the
// resolved shape, saying so. Refusing would strand a create on a catalog the
// operator can widen; silence would hand back a host that thrashes.
func TestDeclarativeGroupTooLargeForTheFamilyWarns(t *testing.T) {
	t.Parallel()
	spec, res := provisionedSpec(t, groupReq(8, 32768, 0))
	assert.Equal(t, "n1-standard-8", spec.MachineType)
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], "oversubscribed")
	assert.Contains(t, res.Warnings[0], "8 instances")
}

func TestDeclarativeInteractiveProvisionsGPUShape(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)

	_, res, err := f.svc.CreateDeclarative(t.Context(), declarativeReq(device.InteractionInteractive), "u@example.com")
	require.NoError(t, err)
	require.Len(t, f.provisioner.specs, 1)
	assert.Equal(t, "g2-standard-8", f.provisioner.specs[0].MachineType,
		"integrated GPU shape preferred for interactive provisioning")
	assert.Equal(t, "provision", res.HostAction)
	assert.Empty(t, res.Warnings)
}

// The other half of the fence, on the branch that has no host to pass over:
// the provisioned shape must not be a GPU one either. Without this the
// preceding test would still pass and a fenced create would simply provision
// the g2 host it was not allowed to be placed on.
func TestDeclarativeSwiftshaderNeverProvisionsGPUShape(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)

	req := declarativeReq(device.InteractionInteractive)
	req.GPUMode = device.GPUModeSwift

	_, res, err := f.svc.CreateDeclarative(t.Context(), req, "u@example.com")
	require.NoError(t, err)
	require.Len(t, f.provisioner.specs, 1)
	spec := f.provisioner.specs[0]
	assert.Equal(t, "n1-standard-8", spec.MachineType, "the fenced create takes the catalog default, not the GPU shape")
	assert.Empty(t, spec.GPUType, "no accelerator may be attached to a fenced create's host")
	assert.Zero(t, spec.GPUCount)
	assert.Empty(t, res.Warnings)
}

// gpu_required and a software-rendering mode cannot both be honored, so the
// platform refuses the create. Resolving to CPU capacity would silently drop
// an explicit flag the caller set.
func TestDeclarativeSwiftshaderWithGPURequiredIsRefused(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)
	f.addHost("h-gpu", host.StatusRunning, 0, 4, 1)

	req := declarativeReq(device.InteractionHeadless)
	req.GPUMode = device.GPUModeSwift
	req.Requirements.GPURequired = true

	_, _, err := f.svc.CreateDeclarative(t.Context(), req, "u@example.com")
	var verr *device.ValidationError
	require.ErrorAs(t, err, &verr, "the contradiction is a 400, not a resolution")
	assert.Contains(t, verr.Message, "gpu_required")
	assert.Contains(t, verr.Message, "swiftshader")
	assert.Empty(t, f.provisioner.specs, "a refused create commits nothing")
}

func TestDeclarativeGPUPolicyOverride(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)
	f.policy.values[models.ConfigKeyDefaultGPUMachineType] = "n1-standard-8"

	_, _, err := f.svc.CreateDeclarative(t.Context(), declarativeReq(device.InteractionInteractive), "u@example.com")
	require.NoError(t, err)
	require.Len(t, f.provisioner.specs, 1)
	spec := f.provisioner.specs[0]
	assert.Equal(t, "n1-standard-8", spec.MachineType)
	assert.Equal(t, "nvidia-tesla-t4", spec.GPUType, "attachable override carries its default GPU type")
	assert.Equal(t, int64(1), spec.GPUCount)
	assert.Equal(t, "n1-standard-8", f.ranker.lastReq.MachineType,
		"ranking judges capacity for the resolved GPU shape")
	assert.Equal(t, "nvidia-tesla-t4", f.ranker.lastReq.GPUType)
}

func TestDeclarativeLocationPreferenceReachesRanking(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)

	req := declarativeReq(device.InteractionInteractive)
	req.Requirements.Location = &device.LocationRequirement{PreferredRegion: "us-central1"}
	_, res, err := f.svc.CreateDeclarative(t.Context(), req, "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, "us-central1", f.ranker.lastOpts.PreferredRegion)
	assert.True(t, f.ranker.lastOpts.GeoDominant, "interactive intent makes proximity dominant")
	require.Len(t, f.provisioner.specs, 1)
	assert.Equal(t, "us-central1", f.provisioner.specs[0].LocationHint,
		"the hint is persisted as the host's placement intent")
	assert.False(t, f.provisioner.specs[0].ZonePinned, "a ranked zone is a binding, never a pin")
	assert.NotEmpty(t, res.PlacementReasons, "the resolution explains the chosen zone")

	// Headless keeps the preference only as a tie-break. Fresh
	// fixture: the first create's provisioning host would otherwise absorb
	// this one via placement, never reaching the ranker.
	f2 := newDeclarativeFixture(t)
	f2.addBuild(t, true)
	req = declarativeReq(device.InteractionHeadless)
	req.Requirements.Location = &device.LocationRequirement{PreferredRegion: "us-central1"}
	_, _, err = f2.svc.CreateDeclarative(t.Context(), req, "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, "us-central1", f2.ranker.lastOpts.PreferredRegion)
	assert.False(t, f2.ranker.lastOpts.GeoDominant)
}

// ResolveHostSpec is the batch path's equivalence-class entry point. One call
// ranks exactly once and returns the pre-ranked, unpinned template the whole
// batch replicates, never one ranking per member.
func TestResolveHostSpecRanksOnceIntoTemplate(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)

	hs, err := f.svc.ResolveHostSpec(t.Context(), device.Requirements{Interaction: device.InteractionHeadless})
	require.NoError(t, err)
	assert.Equal(t, 1, f.ranker.calls, "one spec resolution costs one ranking")
	assert.Equal(t, "europe-west4-a", hs.Spec.Zone, "the template binds the best-ranked zone")
	assert.False(t, hs.Spec.ZonePinned, "a pre-ranked template is a binding, never a pin")
	assert.NotEmpty(t, hs.PlacementReasons, "the ranking trace rides along for the resolution")
}

// --- capacity intent ---

func TestDeclarativeCapacityResolution(t *testing.T) {
	t.Parallel()
	standard := string(host.ProvisioningModelStandard)
	spot := string(host.ProvisioningModelSpot)
	cases := []struct {
		name        string
		interaction device.Interaction
		capacity    device.CapacityModel
		spotDefault string // spot.auto_default policy value; "" = unseeded
		wantModel   string
	}{
		{"auto headless default off", device.InteractionHeadless, "", "", standard},
		{"auto headless policy on", device.InteractionHeadless, "", "true", spot},
		{"explicit auto headless policy on", device.InteractionHeadless, device.CapacityAuto, "true", spot},
		{"auto interactive policy on stays standard", device.InteractionInteractive, "", "true", standard},
		{"explicit spot", device.InteractionHeadless, device.CapacitySpot, "", spot},
		{"explicit standard beats policy", device.InteractionHeadless, device.CapacityStandard, "true", standard},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newDeclarativeFixture(t)
			f.addBuild(t, true)
			if tc.spotDefault != "" {
				f.policy.values[models.ConfigKeySpotAutoDefault] = tc.spotDefault
			}

			req := declarativeReq(tc.interaction)
			req.Requirements.Capacity = tc.capacity
			_, res, err := f.svc.CreateDeclarative(t.Context(), req, "u@example.com")
			require.NoError(t, err)
			require.Len(t, f.provisioner.specs, 1)
			assert.Equal(t, tc.wantModel, string(f.provisioner.specs[0].ProvisioningModel),
				"provisioned host carries the resolved model")
			assert.Equal(t, tc.wantModel, f.ranker.lastReq.ProvisioningModel,
				"ranking judges the quota pools of the resolved model")
			assert.Equal(t, strings.ToLower(tc.wantModel), res.CapacityModel,
				"the resolution reports the chosen model")
		})
	}
}

func TestDeclarativeCapacityRejectsUnknownValue(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)

	req := declarativeReq(device.InteractionHeadless)
	req.Requirements.Capacity = "preemptible"
	_, _, err := f.svc.CreateDeclarative(t.Context(), req, "u@example.com")
	var verr *device.ValidationError
	require.ErrorAs(t, err, &verr, "unknown capacity value must be rejected")
}

// An auto-resolved Spot intent with no viable Spot zone degrades to standard
// capacity with a warning; an explicit spot request fails outright instead.
func TestDeclarativeSpotFallsBackToStandard(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)
	f.policy.values[models.ConfigKeySpotAutoDefault] = "true"
	f.ranker.byModel = map[string][]gcecatalog.ZoneCandidate{
		string(host.ProvisioningModelSpot): {},
		string(host.ProvisioningModelStandard): {
			{Zone: "europe-west4-b", Region: "europe-west4", Score: 70, Reasons: []string{"headroom 70/100"}},
		},
	}

	_, res, err := f.svc.CreateDeclarative(t.Context(), declarativeReq(device.InteractionHeadless), "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, 2, f.ranker.calls, "a failed Spot ranking re-ranks standard capacity")
	require.Len(t, f.provisioner.specs, 1)
	assert.Equal(t, host.ProvisioningModelStandard, f.provisioner.specs[0].ProvisioningModel)
	assert.Equal(t, "europe-west4-b", f.provisioner.specs[0].Zone)
	assert.Equal(t, "standard", res.CapacityModel)
	assert.Contains(t, res.Warnings, "no viable zone for Spot capacity; provisioning standard (on-demand) capacity")
}

// Spot rankings carry the workload class's min-uptime policy: singles default
// to the 10-minute tier (operator-tunable), batch host specs to the hour
// tier, and standard rankings carry no uptime constraint.
func TestDeclarativeSpotMinUptimeReachesRanking(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)

	req := declarativeReq(device.InteractionHeadless)
	req.Requirements.Capacity = device.CapacitySpot
	_, _, err := f.svc.CreateDeclarative(t.Context(), req, "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, int64(600), f.ranker.lastOpts.SpotMinUptimeSeconds,
		"a single device demands the 10-minute tier by default")

	f2 := newDeclarativeFixture(t)
	f2.addBuild(t, true)
	f2.policy.values[models.ConfigKeySpotMinUptimeSingle] = "60"
	req = declarativeReq(device.InteractionHeadless)
	req.Requirements.Capacity = device.CapacitySpot
	_, _, err = f2.svc.CreateDeclarative(t.Context(), req, "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, int64(60), f2.ranker.lastOpts.SpotMinUptimeSeconds,
		"the operator policy overrides the compiled default")

	f3 := newDeclarativeFixture(t)
	f3.policy.values[models.ConfigKeySpotAutoDefault] = "true"
	hs, err := f3.svc.ResolveHostSpec(t.Context(), device.Requirements{Interaction: device.InteractionHeadless})
	require.NoError(t, err)
	assert.Equal(t, host.ProvisioningModelSpot, hs.Spec.ProvisioningModel)
	assert.Equal(t, int64(3600), f3.ranker.lastOpts.SpotMinUptimeSeconds,
		"a batch host spec demands the hour tier by default")

	f4 := newDeclarativeFixture(t)
	f4.addBuild(t, true)
	_, _, err = f4.svc.CreateDeclarative(t.Context(), declarativeReq(device.InteractionHeadless), "u@example.com")
	require.NoError(t, err)
	assert.Zero(t, f4.ranker.lastOpts.SpotMinUptimeSeconds,
		"a standard ranking carries no uptime constraint")
}

func TestDeclarativeExplicitSpotFails(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)
	f.ranker.byModel = map[string][]gcecatalog.ZoneCandidate{
		string(host.ProvisioningModelSpot): {},
		string(host.ProvisioningModelStandard): {
			{Zone: "europe-west4-b", Region: "europe-west4", Score: 70},
		},
	}

	req := declarativeReq(device.InteractionHeadless)
	req.Requirements.Capacity = device.CapacitySpot
	_, _, err := f.svc.CreateDeclarative(t.Context(), req, "u@example.com")
	require.ErrorIs(t, err, host.ErrCapacityExceeded, "zone exhaustion is the capacity family (503), not a bare error")
	assert.Contains(t, err.Error(), "Spot capacity", "the error names the Spot constraint")
	assert.Equal(t, 1, f.ranker.calls, "explicit spot never falls back to a standard ranking")
	assert.Empty(t, f.provisioner.specs, "nothing may be provisioned")
}

// Standard-capacity exhaustion (no viable zone anywhere) is the same capacity
// family, so web.MapError renders it 503, never a bare 500.
func TestDeclarativeNoViableZoneIsCapacityExhaustion(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)
	f.ranker.candidates = nil

	_, _, err := f.svc.CreateDeclarative(t.Context(), declarativeReq(device.InteractionHeadless), "u@example.com")
	require.ErrorIs(t, err, host.ErrCapacityExceeded)
	assert.Contains(t, err.Error(), "no viable zone for the requested shape")
	assert.Empty(t, f.provisioner.specs, "nothing may be provisioned")
}

// newNoGPUCatalogService builds a declarative service over a catalog with NO
// GPU-capable machine type, seeded with a default build. That shortfall is
// what separates the hard constraint from the interaction-derived preference,
// so both sides of it are exercised from here.
func newNoGPUCatalogService(t *testing.T) (*DeviceService, *fakeProvisioner) {
	t.Helper()
	hostRepo := hosttest.NewFakeRepo()
	deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
	buildRepo := newMockBuildRepo()
	provisioner := &fakeProvisioner{hostRepo: hostRepo}
	machineTypes := &fakeMachineTypes{
		defaultID: "e2-standard-8",
		types: map[string]*machinetype.MachineType{
			"e2-standard-8": {ID: "e2-standard-8", IsEnabled: true, IsDefault: true, GPUMode: machinetype.GPUModeNone},
		},
	}
	svc, err := NewDeviceService(deviceRepo, hostRepo, buildRepo,
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
		WithTaskEnqueuer(&fakeEnqueuer{}),
		WithDeclarativeCreate(DeclarativeDeps{
			Provisioner: provisioner, Policy: &fakePolicy{values: map[string]string{}},
			MachineTypes: machineTypes, Ranker: &fakeRanker{candidates: []gcecatalog.ZoneCandidate{{Zone: "z-a"}}},
			Quota: &fakeQuota{maxHosts: 100}, Registrar: &fakeRegistrar{repo: buildRepo}, Capacity: deviceRepo,
			ZoneHealth: fakeStockoutMemory{},
		}))
	require.NoError(t, err)

	require.NoError(t, buildRepo.Create(t.Context(), &build.Build{
		ID: "b1", Name: "b", Source: build.SourceAB,
		Branch: "br", Target: "tg", IsDefault: true,
	}))
	return svc, provisioner
}

func TestDeclarativeGPURequiredFailsWithoutGPUCatalog(t *testing.T) {
	t.Parallel()
	svc, provisioner := newNoGPUCatalogService(t)

	req := declarativeReq(device.InteractionInteractive)
	req.Requirements.GPURequired = true
	_, _, err := svc.CreateDeclarative(t.Context(), req, "u@example.com")
	var verr *device.ValidationError
	require.ErrorAs(t, err, &verr, "gpu required with no GPU capacity or shapes must fail, not degrade")
	assert.Empty(t, provisioner.specs, "a refused create provisions nothing")
}

// The counterfactual that gives the test above its meaning: the same
// GPU-less catalog under the interaction-derived preference provisions a CPU
// shape and says so, rather than failing. The warning is the only signal a
// caller gets, and nothing asserted it.
func TestDeclarativeInteractiveProvisionDegradesWithoutGPUCatalog(t *testing.T) {
	t.Parallel()
	svc, provisioner := newNoGPUCatalogService(t)

	_, res, err := svc.CreateDeclarative(t.Context(), declarativeReq(device.InteractionInteractive), "u@example.com")
	require.NoError(t, err, "a preference must degrade, not fail")
	require.Len(t, provisioner.specs, 1)
	assert.Equal(t, "e2-standard-8", provisioner.specs[0].MachineType)
	assert.Empty(t, provisioner.specs[0].GPUType)
	assert.Contains(t, res.Warnings,
		"no GPU-capable machine type enabled; provisioning a CPU host (software rendering)")
}

// A capacity race at commit excludes the loser and re-places instead of
// failing the create.
func TestDeclarativeRetriesOnCapacityRace(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixtureCapacity(t, &racingCapacity{picks: []string{"h-race", "h-ok"}})
	f.addBuild(t, true)
	// h-race reports capacity to placement, but its committed count is full,
	// which is the shape of losing a race to a concurrent create.
	f.addHost("h-race", host.StatusRunning, 3, 4, 0)
	f.addHost("h-ok", host.StatusRunning, 1, 4, 0)
	f.hostRepo.Hosts["h-race"].CurrentDeviceCount = 4 // full at commit time

	d, res, err := f.svc.CreateDeclarative(t.Context(), declarativeReq(device.InteractionHeadless), "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, "h-ok", d.HostID, "capacity race must re-place, not fail")
	assert.Equal(t, "place", res.HostAction)
}

// racingCapacity scripts placement picks to simulate a stale placement read.
type racingCapacity struct {
	picks []string
	i     int
}

func (r *racingCapacity) PickHostForPlacement(_ context.Context, _ bool, _ []string, _ placement.Prefs) (string, bool, error) {
	if r.i >= len(r.picks) {
		return "", false, nil
	}
	id := r.picks[r.i]
	r.i++
	return id, false, nil
}

func (r *racingCapacity) ListPlaceableCapacity(_ context.Context, _ bool, _ placement.Prefs) ([]batch.HostSlot, error) {
	return nil, nil
}

// --- validate_only plans ---

// A plan must resolve exactly like a create while writing NOTHING: no build
// registration, no provisioner call, no device row.
func TestPlanDeclarativeMakesNoChanges(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	ctx := t.Context()

	d, res, err := f.svc.PlanCreate(ctx, declarativeReq(device.InteractionHeadless))
	require.NoError(t, err)

	assert.Equal(t, "register", res.BuildAction, "empty registry: the build would be registered")
	assert.Empty(t, res.BuildID, "nothing was created, so the plan has no build ID")
	assert.Contains(t, res.BuildName, "aosp-android-latest-release")
	assert.Equal(t, "provision", res.HostAction, "no hosts: one would be provisioned")
	assert.Empty(t, res.HostID)
	assert.Equal(t, "n1-standard-8", res.MachineType)
	assert.Equal(t, "europe-west4-a", res.Zone)

	assert.Empty(t, d.ID, "would-be device carries no generated ID")
	assert.Equal(t, "dev-1", d.Name)

	assert.Empty(t, f.buildRepo.builds, "plan must not register builds")
	assert.Empty(t, f.provisioner.specs, "plan must not provision hosts")
	assert.Empty(t, f.deviceRepo.Devices, "plan must not create devices")
}

func TestPlanDeclarativeUsesExistingResources(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	seeded := f.addBuild(t, true)
	f.addHost("h1", host.StatusRunning, 2, 4, 0)

	d, res, err := f.svc.PlanCreate(t.Context(), declarativeReq(device.InteractionHeadless))
	require.NoError(t, err)
	assert.Equal(t, "use", res.BuildAction)
	assert.Equal(t, seeded.ID, res.BuildID)
	assert.Equal(t, "place", res.HostAction)
	assert.Equal(t, "h1", res.HostID)
	assert.Equal(t, "h1", d.HostID)
	assert.Empty(t, f.deviceRepo.Devices)
}

// The resolution says where the create landed, taking the derivation's answer:
// a create that named no pool still resolves to one, and a hard GPU requirement
// resolves to the gpu pool without naming it. Echoing the request's own field
// would leave both cases blank.
func TestDeclarativeResolutionStatesTheResolvedPool(t *testing.T) {
	t.Parallel()

	t.Run("a plan with nothing to say lands in the default pool", func(t *testing.T) {
		t.Parallel()
		f := newDeclarativeFixture(t)

		_, res, err := f.svc.PlanCreate(t.Context(), declarativeReq(device.InteractionHeadless))
		require.NoError(t, err)
		assert.Equal(t, host.PoolDefault, res.Pool)
	})

	t.Run("a hard GPU requirement lands in the gpu pool", func(t *testing.T) {
		t.Parallel()
		f := newDeclarativeFixture(t)
		f.policy.values[models.ConfigKeyFleetPoolBounds] = `{"default":{"standby":0,"floor":0},"gpu":{"standby":0,"floor":0}}`
		req := declarativeReq(device.InteractionHeadless)
		req.Requirements.GPURequired = true

		_, res, err := f.svc.PlanCreate(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, host.PoolGPU, res.Pool)
	})

	t.Run("a committed create answers the same as its plan", func(t *testing.T) {
		t.Parallel()
		f := newDeclarativeFixture(t)
		f.addBuild(t, true)
		f.addHost("h1", host.StatusRunning, 0, 4, 0)

		_, res, err := f.svc.CreateDeclarative(t.Context(),
			declarativeReq(device.InteractionHeadless), "u@example.com")
		require.NoError(t, err)
		assert.Equal(t, host.PoolDefault, res.Pool)
	})
}

// AIP-163 requires the plan to fail the way the real request would.
// With host quota exhausted, a plan that would provision fails with a 429.
func TestPlanDeclarativeQuotaExhausted(t *testing.T) {
	t.Parallel()
	hostRepo := hosttest.NewFakeRepo()
	deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
	buildRepo := newMockBuildRepo()
	machineTypes := &fakeMachineTypes{
		defaultID: "n1-standard-8",
		types: map[string]*machinetype.MachineType{
			"n1-standard-8": {ID: "n1-standard-8", IsEnabled: true, IsDefault: true, GPUMode: machinetype.GPUModeNone},
		},
	}
	svc, err := NewDeviceService(deviceRepo, hostRepo, buildRepo,
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
		WithTaskEnqueuer(&fakeEnqueuer{}),
		WithDeclarativeCreate(DeclarativeDeps{
			Provisioner: &fakeProvisioner{hostRepo: hostRepo}, Policy: &fakePolicy{values: map[string]string{}},
			MachineTypes: machineTypes, Ranker: &fakeRanker{candidates: []gcecatalog.ZoneCandidate{{Zone: "z-a"}}},
			Quota: &fakeQuota{maxHosts: 5, usedHosts: 5}, Registrar: &fakeRegistrar{repo: buildRepo}, Capacity: deviceRepo,
			ZoneHealth: fakeStockoutMemory{},
		}))
	require.NoError(t, err)

	_, _, err = svc.PlanCreate(t.Context(), declarativeReq(device.InteractionHeadless))
	var qerr *quota.ExceededError
	require.ErrorAs(t, err, &qerr, "plan must 429 when provisioning would exceed quota")
}

func TestPlanImperative(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)
	f.addHost("h-full", host.StatusRunning, 4, 4, 0)
	f.addHost("h-ok", host.StatusRunning, 1, 4, 0)
	ctx := t.Context()

	// Capacity failure surfaces exactly as the real commit's would.
	_, _, err := f.svc.PlanCreate(ctx, device.CreateRequest{HostID: "h-full", Name: "dev-1"})
	require.ErrorIs(t, err, host.ErrCapacityExceeded)

	d, res, err := f.svc.PlanCreate(ctx, device.CreateRequest{HostID: "h-ok", Name: "dev-1"})
	require.NoError(t, err)
	assert.Nil(t, res, "imperative plans carry no resolution")
	assert.Equal(t, "h-ok", d.HostID)
	assert.Empty(t, d.ID)
	assert.Empty(t, f.deviceRepo.Devices)
}

// groupDeclarativeReq is a declarative create for a group: n instances in the
// env_config, which is the only place an instance count is ever stated.
func groupDeclarativeReq(n int) device.CreateRequest {
	instances := make([]any, n)
	for i := range instances {
		instances[i] = map[string]any{"name": fmt.Sprintf("i%d", i)}
	}
	req := declarativeReq(device.InteractionHeadless)
	req.Mode = "advanced"
	req.EnvConfig = map[string]any{"instances": instances}
	return req
}

// A group's host must have room for the whole group. max_devices counts
// instances, so provisioning at the operator's packing density (a number of
// ordinary single-instance devices) hands the group a host that refuses it,
// and the create fails on the very host provisioned for it.
func TestDeclarativeGroupProvisionsAHostThatFitsIt(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)
	_, res, err := f.svc.CreateDeclarative(t.Context(), groupDeclarativeReq(8), "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, "provision", res.HostAction)
	require.Len(t, f.provisioner.specs, 1)

	spec := f.provisioner.specs[0]
	assert.GreaterOrEqual(t, spec.MaxDevices, int64(8),
		"an 8-instance group needs 8 slots; the density policy is a device count, not an instance count")
	assert.True(t, spec.Exclusive,
		"a group owns its host: its instances share the host's radio medium, so a stranger's device must not land here")
	assert.False(t, spec.ForeignCVDs,
		"these CVDs have device rows; exempting the host would blind the orphan census to them")
}

// One ordinary device shares a host packed to the shape's own fit. The
// group sizing above must not leak into the single-device path.
func TestDeclarativeSingleDeviceTakesTheDefaultShapesFit(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)

	_, _, err := f.svc.CreateDeclarative(t.Context(), declarativeReq(device.InteractionHeadless), "u@example.com")
	require.NoError(t, err)
	require.Len(t, f.provisioner.specs, 1)
	assert.Equal(t, int64(3), f.provisioner.specs[0].MaxDevices,
		"the default shape's own fit decides for one device")
}

// fakeStockoutMemory is a stockout memory that remembers nothing, so every
// shape is worth probing. The ladder's own tests live beside the resolver,
// where the memory is seeded.
type fakeStockoutMemory struct{}

func (fakeStockoutMemory) ListCapacityCells(context.Context, []string) ([]*gcecatalog.ZoneHealth, error) {
	return nil, nil
}
