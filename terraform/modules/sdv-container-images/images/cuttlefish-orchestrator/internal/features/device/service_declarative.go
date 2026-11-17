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
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/build"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/idempotency"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/domain/quota"
	"cloud-cuttlefish-orchestrator/internal/features/device/placement"
)

// HostProvisioner provisions a host through the normal host-creation path
// (catalog validation, quota, provision task, audit). Satisfied structurally
// by the host feature's service and wired in main, because features must not
// import features.
type HostProvisioner interface {
	Create(ctx context.Context, req host.CreateRequest, createdBy string, origin host.Origin) (*host.Host, error)
}

// QuotaReader reads the platform host quota, so a validate_only plan can
// fail the way the real provisioning commit would (a dry run must fail if
// the actual request would).
type QuotaReader interface {
	Get(ctx context.Context, scope quota.Scope, scopeID string) (*quota.Quota, error)
}

// DeclarativeDeps is the dependency set requirements-based creation needs:
// the host provisioner and quota for the commits and plans the service owns,
// and the resolution inputs the placement resolver consumes.
type DeclarativeDeps struct {
	Provisioner  HostProvisioner
	Policy       placement.PolicyReader
	MachineTypes machinetype.Catalog
	Ranker       placement.ZoneRanker
	Quota        QuotaReader
	Registrar    placement.TrackingBuildRegistrar
	Capacity     placement.CapacityStore
	ZoneHealth   placement.StockoutMemory
	// ARM is the deployment's arm64 buildability fact. It sits outside the
	// all-or-nothing check below because nil is a real state, a deployment
	// that cannot build an arm64 host, and the resolver refuses arm creates
	// under it rather than failing to wire.
	ARM placement.ARMCapability
}

// WithDeclarativeCreate enables requirements-based creation (placement and
// auto-provisioning). With the zero DeclarativeDeps (the disabled Deps state)
// it is a no-op and requests carrying Requirements fail as unconfigured (400);
// a partial set is a wiring bug the constructor rejects.
func WithDeclarativeCreate(deps DeclarativeDeps) DeviceServiceOption {
	return func(s *DeviceService) {
		if deps == (DeclarativeDeps{ARM: deps.ARM}) {
			return
		}
		if deps.Provisioner == nil || deps.Policy == nil || deps.MachineTypes == nil ||
			deps.Ranker == nil || deps.Quota == nil || deps.Registrar == nil ||
			deps.Capacity == nil || deps.ZoneHealth == nil {
			s.wireErr = errors.Join(s.wireErr,
				errors.New("declarative create requires every DeclarativeDeps field"))
			return
		}
		s.provisioner = deps.Provisioner
		s.hostQuota = deps.Quota
		s.placement = placement.NewResolver(placement.ResolverDeps{
			Capacity:     deps.Capacity,
			Builds:       s.builds,
			Policy:       deps.Policy,
			MachineTypes: deps.MachineTypes,
			Ranker:       deps.Ranker,
			Registrar:    deps.Registrar,
			ZoneHealth:   deps.ZoneHealth,
			ARM:          deps.ARM,
			Logger:       s.logger,
		})
	}
}

// errDeclarativeNotConfigured rejects the declarative entry points on a
// deployment wired without the capability. It is a caller-visible 400 (the
// request asks for something this deployment does not offer), never a 500.
var errDeclarativeNotConfigured = &device.ValidationError{
	Message: "declarative create is not enabled on this deployment",
}

// Resolution is the domain type; aliased so the feature's call sites read
// unqualified.
type Resolution = device.Resolution

// placementAttempts bounds the place→commit→capacity-race loop. The commit's
// atomic capacity re-check is what guarantees correctness; re-placing after a
// race is an optimization.
const placementAttempts = 3

// capacityIntent is what one derivation settled about a create: which subset of
// the fleet its host belongs to, and how hard its GPU want is.
//
// The three travel together because they are one answer. The pool decides the
// other two (a pool whose machines all carry an accelerator is asking for one
// whether or not the create said so), so a caller that recomputed either half
// could fence the create one way and provision it the other.
type capacityIntent struct {
	pool        string
	gpuWanted   bool
	gpuRequired bool
}

// resolveIntent settles that answer once per create. The pool read is the one
// policy round trip it costs, whatever the fleet is partitioned into.
func (s *DeviceService) resolveIntent(ctx context.Context, req device.CreateRequest) (capacityIntent, error) {
	pool, err := s.placement.ResolvePool(ctx, *req.Requirements, req.GPUMode)
	if err != nil {
		return capacityIntent{}, err
	}
	return capacityIntent{
		pool:        pool,
		gpuWanted:   placement.WantsGPU(*req.Requirements, req.GPUMode, pool),
		gpuRequired: placement.RequiresGPU(*req.Requirements, pool),
	}, nil
}

// CreateDeclarative resolves a requirements-based create and commits it. The
// build resolves to a registered (possibly newly auto-registered) build, and
// the device is placed onto an existing host with capacity or a host is
// provisioned from operator policy defaults. Resolution belongs to the
// placement sub-package; this method owns every commit a resolution leads to.
func (s *DeviceService) CreateDeclarative(ctx context.Context, req device.CreateRequest, createdBy string) (*device.Device, *Resolution, error) {
	r := req.Requirements
	if err := validateDeviceName(req.Name); err != nil {
		return nil, nil, err
	}
	if err := s.validateRequirements(req); err != nil {
		return nil, nil, err
	}

	// Resolve the Idempotency-Key against the user's ORIGINAL request before
	// anything commits. A replay must return the recorded device rather than
	// re-run placement (which may resolve a different host and misread as a
	// key conflict), and on the no-capacity path it must never auto-provision
	// a second host. The fingerprint then rides req into the final commit so
	// the stored key records what the user sent, not the resolved form.
	if req.IdempotencyKey != "" && s.idempotencyStore != nil {
		fingerprint, err := idempotency.Fingerprint(req)
		if err != nil {
			return nil, nil, err
		}
		d, found, err := idempotency.Resolve(ctx, s.idempotencyStore, req.IdempotencyKey, fingerprint, s.deviceRepo.Get)
		if err != nil {
			// Returned unwrapped so idempotency.ErrConflict reaches the transport as a 422.
			return nil, nil, err
		}
		if found {
			return d, resolutionForReplay(d), nil
		}
		req.FingerprintOverride = fingerprint
	}

	// Before anything resolves or commits: build resolution may register a
	// tracking build and the no-capacity path provisions a host, and neither
	// should happen for a request that is refused.
	if err := s.checkInstanceBuilds(ctx, req.EnvConfig); err != nil {
		return nil, nil, err
	}

	// Before the build resolves, because that step can register a tracking
	// build. A create naming a pool this deployment does not offer must be
	// refused without leaving a row behind.
	intent, err := s.resolveIntent(ctx, req)
	if err != nil {
		return nil, nil, err
	}

	res := &Resolution{Pool: intent.pool}
	if err := s.placement.ResolveBuild(ctx, r.Build, createdBy, &req, res); err != nil {
		return nil, nil, err
	}

	if !needsOwnHost(req) {
		d, placed, err := s.placeAndCommit(ctx, req, createdBy, intent, res)
		if err != nil {
			return nil, res, err
		}
		if placed {
			return d, res, nil
		}
	}

	// No candidate host, or a group: provision one from operator policy defaults.
	return s.provisionAndCommit(ctx, req, createdBy, intent, res)
}

// provisionAndCommit provisions a host from operator policy defaults for a
// create that is not landing on existing capacity, then commits the device to
// it.
func (s *DeviceService) provisionAndCommit(ctx context.Context, req device.CreateRequest, createdBy string, intent capacityIntent, res *Resolution) (*device.Device, *Resolution, error) {
	hs, err := s.placement.ProvisionSpec(ctx, *req.Requirements, req.GPUMode, placement.WorkloadSingle, requestedShape(req), intent.pool)
	if err != nil {
		return nil, nil, err
	}
	res.Warnings = append(res.Warnings, hs.Warnings...)
	// The provision path's own degradation. No enabled machine type carries a
	// GPU, so a create that wanted one is getting software rendering. Recorded
	// for every create that wanted a GPU, degraded or not, because the figure
	// this feeds is a ratio and this is where its denominator comes from.
	if intent.gpuWanted {
		req.GPUDegraded = &hs.GPUDegraded
	}
	// The spec came from the shared resolver, which the fleet keeper also
	// calls; only the call site knows whether this host is a miss the fleet
	// should learn from or standing inventory it provisioned itself.
	h, err := s.provisioner.Create(ctx, *hs.Spec, createdBy, host.OriginAuto)
	if err != nil {
		return nil, res, fmt.Errorf("auto-provisioning host: %w", err)
	}
	res.HostID, res.HostName, res.HostAction = h.ID, h.Name, device.HostActionProvision
	res.MachineType, res.Zone = hs.Spec.MachineType, h.Zone
	res.CapacityModel = placement.CapacityModelName(hs.Spec.ProvisioningModel)
	res.PlacementReasons = hs.PlacementReasons
	s.logger.Info("auto-provisioned host for declarative create",
		slog.String("host_id", h.ID),
		slog.String("machine_type", hs.Spec.MachineType),
		slog.String("zone", h.Zone),
		slog.String("capacity_model", res.CapacityModel))

	d, err := s.commitResolved(ctx, req, createdBy, h.ID)
	if err != nil {
		return nil, res, err
	}
	return d, res, nil
}

// ResolveBuildForBatch resolves a build requirement once for a whole batch (the
// batch feature's build entry point), returning the resolution
// (BuildID/BuildTarget + facts). It has the same side effects as a declarative
// create's build resolution, including auto-registering a tracking build, so a
// batch of N devices registers at most one build.
func (s *DeviceService) ResolveBuildForBatch(ctx context.Context, br device.BuildRequirement, createdBy string) (*device.Resolution, error) {
	if s.placement == nil {
		return nil, errDeclarativeNotConfigured
	}
	res := &device.Resolution{}
	var req device.CreateRequest
	if err := s.placement.ResolveBuild(ctx, br, createdBy, &req, res); err != nil {
		return nil, err
	}
	return res, nil
}

// PlanBuildForBatch resolves a build requirement read-only for a batch's
// validate_only plan. It describes the build that would be used (or registered)
// without registering anything, and is the dry-run twin of ResolveBuildForBatch.
func (s *DeviceService) PlanBuildForBatch(ctx context.Context, br device.BuildRequirement) (*device.Resolution, error) {
	if s.placement == nil {
		return nil, errDeclarativeNotConfigured
	}
	res := &device.Resolution{}
	var req device.CreateRequest
	if err := s.placement.PlanBuild(ctx, br, &req, res); err != nil {
		return nil, err
	}
	return res, nil
}

// ResolveHostSpec builds the host provisioning template a batch's dedicated
// hosts are created from (machine type, ranked zone, GPU shape from operator
// policy). It is the batch feature's host-shape entry point. The zone is ranked
// once here and replicated by the whole batch, because its hosts are one
// equivalence class (identical shape), so per-member re-ranking would repeat
// identical catalog and quota reads for identical answers. Failover still
// re-ranks per host, since by then its inputs have changed. gpuRequired with no
// GPU-capable type fails, matching a declarative create.
func (s *DeviceService) ResolveHostSpec(ctx context.Context, r device.Requirements) (*host.SpecResolution, error) {
	if s.placement == nil {
		return nil, errDeclarativeNotConfigured
	}
	// Batch members are always single-instance. This slice has no
	// env_config path at all, so every member costs exactly one slot. It has no
	// gpu_mode either, so no member is ever fenced off GPU capacity.
	pool, err := s.placement.ResolvePool(ctx, r, device.GPUModeAuto)
	if err != nil {
		return nil, err
	}
	return s.placement.ProvisionSpec(ctx, r, device.GPUModeAuto, placement.WorkloadBatch, placement.SingleInstanceShape(), pool)
}

// ResolveFleetHostSpec is the fleet keeper's provisioning entry point. It
// returns the declarative host template for one policy-default headless host in
// the named pool, which is the same resolution a no-capacity declarative create
// routed there runs, so keeper-provisioned hosts are exactly the hosts such a
// create would have provisioned (Spot per the headless default).
//
// The pool is taken as given and not derived. The keeper is answering the gap in
// a particular pool, and a host bought for one pool that landed in another would
// leave that gap open next cycle while reading as surplus where it went.
func (s *DeviceService) ResolveFleetHostSpec(ctx context.Context, pool string) (*host.SpecResolution, error) {
	if s.placement == nil {
		return nil, errDeclarativeNotConfigured
	}
	// A standby host is a spare for an ordinary create, which is one instance
	// and no pinned rendering mode. What the pool needs the machine to carry is
	// the pool's own property, which is what ProvisionSpec reads it for.
	return s.placement.ProvisionSpec(ctx, device.Requirements{Interaction: device.InteractionHeadless},
		device.GPUModeAuto, placement.WorkloadSingle, placement.SingleInstanceShape(), pool)
}

// warmRecentWindow is how far back "hot" reaches for the warm plane's prefetch
// plan.
const warmRecentWindow = 24 * time.Hour

// WarmFetchKeys is the warm plane's prefetch-plan entry point. It returns the
// default build's fetch key plus the distinct pinned keys among the last day's
// device creates.
func (s *DeviceService) WarmFetchKeys(ctx context.Context) ([]build.WarmKey, error) {
	if s.placement == nil {
		return nil, errDeclarativeNotConfigured
	}
	recents, err := s.deviceRepo.ListRecentBuildIDs(ctx, time.Now().Add(-warmRecentWindow))
	if err != nil {
		return nil, fmt.Errorf("listing recent builds for the warm plan: %w", err)
	}
	return s.placement.WarmKeys(ctx, recents)
}

// validateRequirements enforces the declarative create's semantic rules: the
// shared requirement rules (device.Requirements.Validate) plus the rules only
// this entry point has (host_id exclusivity, feature wiring).
func (s *DeviceService) validateRequirements(req device.CreateRequest) error {
	r := req.Requirements
	if r == nil {
		return &device.ValidationError{Message: "requirements is required for a declarative create"}
	}
	if s.placement == nil {
		return errDeclarativeNotConfigured
	}
	if req.HostID != "" {
		return &device.ValidationError{Message: "requirements and host_id are mutually exclusive"}
	}
	// A mode that requires a CPU host cannot also demand one with a GPU. The
	// two are not ranked against each other. An explicit flag the platform
	// cannot honor is refused, never silently resolved.
	if req.GPUMode.RequiresCPUHost() && r.GPURequired {
		return &device.ValidationError{Message: fmt.Sprintf(
			"requirements.gpu_required conflicts with gpu_mode %q (software rendering): drop one", req.GPUMode)}
	}
	// Returned unwrapped so the ValidationError's own message is the 400 body.
	return r.Validate()
}

// needsOwnHost reports whether this create skips existing capacity and always
// provisions. A multi-instance group does. ProvisionSpec gives a group the
// host to itself (Exclusive), because its instances share the host's netsim
// and rootcanal medium and its network bridges. The capacity pick admits any
// non-exclusive host with free slots, so placing a group there would put it
// beside devices that are already running.
func needsOwnHost(req device.CreateRequest) bool {
	return requestedShape(req).Instances > 1
}

// placeAndCommit tries to land the device on existing capacity. A capacity
// race at commit excludes the loser and re-places. Placement considers hosts
// that are still provisioning on purpose, so concurrent declarative creates
// pack onto the host the first one started instead of each provisioning
// another. placed=false means no candidate host exists and the caller should
// provision.
func (s *DeviceService) placeAndCommit(ctx context.Context, req device.CreateRequest, createdBy string, intent capacityIntent, res *Resolution) (*device.Device, bool, error) {
	prefs := s.placementPrefs(ctx, req, intent.pool)
	var excluded []string
	for range placementAttempts {
		pick, err := s.placement.PlaceHost(ctx, intent.gpuWanted, intent.gpuRequired, excluded, prefs)
		if err != nil {
			return nil, false, err
		}
		if pick.HostID == "" {
			return nil, false, nil
		}
		res.HostID, res.HostAction = pick.HostID, device.HostActionPlace
		if pick.Warning != "" && !slices.Contains(res.Warnings, pick.Warning) {
			res.Warnings = append(res.Warnings, pick.Warning)
		}
		// Placement's own degradation, recorded both ways for every create that
		// wanted a GPU. Taken from the typed field rather than from the warning
		// beside it. The warning is prose that may be reworded, and the count
		// this feeds is what decides whether GPU capacity is worth standing up.
		if intent.gpuWanted {
			req.GPUDegraded = &pick.Degraded
		}
		d, err := s.commitResolved(ctx, req, createdBy, pick.HostID)
		if err == nil {
			// Only the committed pick's warmth is a fact about this create.
			// A capacity-raced warm pick never held the device.
			if pick.Warm {
				res.PlacementReasons = append(res.PlacementReasons, affinityReason(res))
			}
			if w := s.spotFallbackWarning(ctx, pick.HostID, req); w != "" {
				res.Warnings = append(res.Warnings, w)
			}
			return d, true, nil
		}
		// A host whose VM stops before this device's lease would end cannot
		// serve it either, so placement moves on as it does for a full host.
		if _, horizon := errors.AsType[*host.VMHorizonError](err); !horizon && !errors.Is(err, host.ErrCapacityExceeded) {
			return nil, false, err
		}
		excluded = append(excluded, pick.HostID)
	}
	return nil, false, nil
}

// placementPrefs derives the placement terms for a resolved request: the pool
// this create was judged into, the CPU fence the pinned rendering mode demands,
// build affinity from the resolved (pinned) build, and STANDARD-first for
// interactive intent. A session outlives a one-time fetch, so that term
// outranks affinity in the query. The ordering terms are best-effort by
// construction, since every failure degrades to plain bin-packing, while the
// pool and the fence are hard terms.
func (s *DeviceService) placementPrefs(ctx context.Context, req device.CreateRequest, pool string) placement.Prefs {
	return placement.Prefs{
		Pool:           pool,
		Instances:      requestedShape(req).Instances,
		CPUOnly:        req.GPUMode.RequiresCPUHost(),
		AffineFetchKey: s.placement.AffineFetchKey(ctx, req.BuildID),
		PreferStandard: req.Requirements != nil && req.Requirements.Interaction == device.InteractionInteractive,
	}
}

// requestedShape is what this create asks a host for: the slot count the
// capacity paths compare against, and the declared resources the auto-provision
// branch sizes a machine type from. One derivation, because the count must
// agree with what the committed device carries (that is what the atomic create
// is actually charged) and the shape must agree with the count.
func requestedShape(req device.CreateRequest) placement.GroupShape {
	return placement.ShapeFromEnvConfig(req.EnvConfig)
}

// spotFallbackWarning names the preemption risk when an interactive create
// lands on Spot capacity. The STANDARD-first preference makes this the
// fallback outcome, so it fires only when no standard host was free, and a
// silent Spot placement would hand a user a session that can vanish
// mid-interaction.
func (s *DeviceService) spotFallbackWarning(ctx context.Context, hostID string, req device.CreateRequest) string {
	if req.Requirements == nil || req.Requirements.Interaction != device.InteractionInteractive {
		return ""
	}
	h, err := s.hostRepo.Get(ctx, hostID)
	if err != nil || !h.IsSpotVM() {
		return ""
	}
	return "interactive device placed on Spot capacity (no standard host was free); the session may be preempted"
}

// affinityReason renders the placement-reasons line for a warm pick.
func affinityReason(res *Resolution) string {
	name := res.BuildName
	if name == "" {
		name = res.BuildID
	}
	return fmt.Sprintf("affine to build %s: host already holds its artifacts", name)
}

// commitResolved runs the ordinary imperative create with the resolved host.
// req still carries FingerprintOverride, so the committed key records the
// user's original request rather than this rewritten form.
func (s *DeviceService) commitResolved(ctx context.Context, req device.CreateRequest, createdBy, hostID string) (*device.Device, error) {
	req.HostID = hostID
	req.Requirements = nil
	return s.createOnHost(ctx, req, createdBy)
}

// resolutionForReplay reconstructs the resolution view of an Idempotency-Key
// replay from the recorded device. The original call resolved and committed
// already, so the replay reports the winner's binding instead of re-running
// placement.
func resolutionForReplay(d *device.Device) *Resolution {
	return &Resolution{
		BuildID:     d.BuildID,
		BuildTarget: d.BuildTarget,
		BuildAction: device.BuildActionUse,
		HostID:      d.HostID,
		HostAction:  device.HostActionPlace,
	}
}

// PlanCreate is the validate_only dry run. It applies the same checks and
// resolution a real create performs, committing nothing (AIP-163). The returned
// device is the would-be device without generated fields (no ID, no
// timestamps); the resolution's IDs are empty where the real call would
// generate them (a build that would be registered, a host that would be
// provisioned).
func (s *DeviceService) PlanCreate(ctx context.Context, req device.CreateRequest) (*device.Device, *Resolution, error) {
	if err := validateDeviceName(req.Name); err != nil {
		return nil, nil, err
	}
	if req.Requirements == nil {
		return s.planImperative(ctx, req)
	}
	return s.planDeclarative(ctx, req)
}

// planImperative validates a host_id create without committing. The host must
// exist, accept devices, and have capacity, which are the checks whose failure
// would fail the real commit.
func (s *DeviceService) planImperative(ctx context.Context, req device.CreateRequest) (*device.Device, *Resolution, error) {
	if req.HostID == "" {
		return nil, nil, &device.ValidationError{Message: "host_id is required unless requirements is set"}
	}
	if err := s.checkInstanceBuilds(ctx, req.EnvConfig); err != nil {
		return nil, nil, err
	}
	h, err := s.hostRepo.Get(ctx, req.HostID)
	if err != nil {
		return nil, nil, fmt.Errorf("host not found: %w", err)
	}
	switch h.Status {
	case host.StatusPending, host.StatusProvisioning, host.StatusStarting, host.StatusRunning:
	default:
		return nil, nil, &device.ValidationError{
			Message: fmt.Sprintf("host %s cannot accept devices (status: %s)", h.ID, h.Status),
		}
	}
	if h.CurrentDeviceCount+requestedShape(req).Instances > h.MaxDevices {
		return nil, nil, host.ErrCapacityExceeded
	}
	instances := int(requestedShape(req).Instances)
	occ := hostOccupancy{HostID: h.ID, Exclusive: h.Exclusive, Slots: h.CurrentDeviceCount}
	if occ.needsGroupRead(instances) {
		onHost, err := s.deviceRepo.ListByHost(ctx, h.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("listing devices on host %s: %w", h.ID, err)
		}
		occ.CarriesGroup = carriesGroup(onHost)
	}
	if err := checkHostExclusivity(occ, instances); err != nil {
		return nil, nil, err
	}
	return wouldBeDevice(req), nil, nil
}

// planDeclarative resolves the requirements read-only. No build is registered
// and no host is provisioned, but quota and shape failures surface exactly as
// the real create's would.
func (s *DeviceService) planDeclarative(ctx context.Context, req device.CreateRequest) (*device.Device, *Resolution, error) {
	r := req.Requirements
	if err := s.validateRequirements(req); err != nil {
		return nil, nil, err
	}
	if err := s.checkInstanceBuilds(ctx, req.EnvConfig); err != nil {
		return nil, nil, err
	}

	intent, err := s.resolveIntent(ctx, req)
	if err != nil {
		return nil, nil, err
	}

	res := &Resolution{Pool: intent.pool}
	if err := s.placement.PlanBuild(ctx, r.Build, &req, res); err != nil {
		return nil, nil, err
	}

	var pick placement.Pick
	if !needsOwnHost(req) {
		pick, err = s.placement.PlaceHost(ctx, intent.gpuWanted, intent.gpuRequired, nil,
			s.placementPrefs(ctx, req, intent.pool))
		if err != nil {
			return nil, nil, err
		}
	}
	if pick.HostID != "" {
		res.HostID, res.HostAction = pick.HostID, device.HostActionPlace
		if pick.Warm {
			res.PlacementReasons = append(res.PlacementReasons, affinityReason(res))
		}
		if pick.Warning != "" {
			res.Warnings = append(res.Warnings, pick.Warning)
		}
		if w := s.spotFallbackWarning(ctx, pick.HostID, req); w != "" {
			res.Warnings = append(res.Warnings, w)
		}
		req.HostID = pick.HostID
		return wouldBeDevice(req), res, nil
	}

	hs, err := s.placement.ProvisionSpec(ctx, *r, req.GPUMode, placement.WorkloadSingle, requestedShape(req), intent.pool)
	if err != nil {
		return nil, nil, err
	}
	res.Warnings = append(res.Warnings, hs.Warnings...)
	res.HostAction = device.HostActionProvision
	res.MachineType, res.Zone = hs.Spec.MachineType, hs.Spec.Zone
	res.CapacityModel = placement.CapacityModelName(hs.Spec.ProvisioningModel)
	// hs is this call's own value, so extending its trace in place is safe.
	hs.PlacementReasons = append(hs.PlacementReasons,
		"initial placement; provisioning may fail over to another ranked zone")
	res.PlacementReasons = hs.PlacementReasons

	// The real provisioning commit consumes host quota; the plan must fail
	// the same way when none remains.
	q, err := s.hostQuota.Get(ctx, quota.ScopePlatform, quota.GlobalScopeID)
	if err != nil {
		return nil, nil, fmt.Errorf("reading host quota: %w", err)
	}
	if q.UsedHosts >= q.MaxHosts {
		return nil, nil, &quota.ExceededError{
			Scope: quota.ScopePlatform, ScopeID: quota.GlobalScopeID,
			Requested: 1, Available: 0, MaxHosts: q.MaxHosts,
		}
	}
	return wouldBeDevice(req), res, nil
}

// wouldBeDevice is the response body for a validate_only request. It is the
// device the create would produce, without generated fields (AIP-163 says to
// omit output that cannot be known, e.g. server-assigned IDs).
func wouldBeDevice(req device.CreateRequest) *device.Device {
	gpuMode := req.GPUMode
	if gpuMode == "" {
		gpuMode = device.GPUModeAuto
	}
	mode := req.Mode
	if mode == "" {
		mode = "simple"
	}
	return &device.Device{
		Name:        req.Name,
		HostID:      req.HostID,
		BuildID:     req.BuildID,
		BuildTarget: req.BuildTarget,
		GPUMode:     gpuMode,
		Mode:        mode,
		Status:      device.StatusPending,
		Tags:        req.Tags,
	}
}
