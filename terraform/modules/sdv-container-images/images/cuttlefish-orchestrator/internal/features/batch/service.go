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
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"cloud-cuttlefish-orchestrator/internal/domain/batch"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/idempotency"
	"cloud-cuttlefish-orchestrator/internal/domain/quota"
)

// batchIDNamespace derives a stable batch id from a client Idempotency-Key so a
// retried admission resumes the same batch, with its per-index host and device
// idempotency keys, instead of provisioning a second fleet.
var batchIDNamespace = uuid.MustParse("6f1c8e2a-9b3d-4e6f-8a1b-2c3d4e5f6a7b")

// DeviceOrchestrator is the device-engine interface the batch drives. Satisfied
// by the device service and wired in main (features must not import features).
type DeviceOrchestrator interface {
	// ResolveBuildForBatch resolves the requirement to a build once for the
	// batch, returning the resolution (BuildID/BuildTarget + facts).
	ResolveBuildForBatch(ctx context.Context, br device.BuildRequirement, createdBy string) (*device.Resolution, error)
	// PlanBuildForBatch resolves the build read-only (validate_only): it
	// describes the build without registering anything.
	PlanBuildForBatch(ctx context.Context, br device.BuildRequirement) (*device.Resolution, error)
	// ResolveHostSpec builds the provisioning template for the batch's hosts:
	// shape from operator policy and a zone ranked once for the whole batch
	// (its hosts are one equivalence class), with shape warnings and the
	// ranking trace alongside.
	ResolveHostSpec(ctx context.Context, req device.Requirements) (*host.SpecResolution, error)
	// Create creates one member device (imperative: host + build resolved).
	Create(ctx context.Context, req device.CreateRequest, createdBy string) (*device.Device, error)
	// List returns devices matching the filter, enriched with host network data.
	List(ctx context.Context, filter device.ListFilter) ([]*device.Device, error)
	// ListPlaceableCapacity is the pack planner's fleet view of one pool:
	// placeable (shareable, undegraded, non-full) hosts with free slots, hosts
	// already holding buildID's artifacts first, then most-loaded first.
	// Capacity is re-checked at commit, so this view is advisory.
	ListPlaceableCapacity(ctx context.Context, pool string, gpuOnly bool, buildID string) ([]batch.HostSlot, error)
	// BatchDelete tears down the given devices (cascading CVD cleanup).
	BatchDelete(ctx context.Context, devices []*device.Device) (int, error)
}

// HostProvisioner provisions a host through the normal host-creation path.
// Satisfied by the host service.
type HostProvisioner interface {
	Create(ctx context.Context, req host.CreateRequest, createdBy string, origin host.Origin) (*host.Host, error)
}

// QuotaReader reads the platform host quota so admission fails the way the real
// provisioning commit would (AIP-163).
type QuotaReader interface {
	Get(ctx context.Context, scope quota.Scope, scopeID string) (*quota.Quota, error)
}

// Service admits, reports, and tears down batches.
type Service struct {
	devices DeviceOrchestrator
	hosts   HostProvisioner
	quota   QuotaReader
	store   batch.Repository
	logger  *slog.Logger
}

// NewService creates the batch service.
func NewService(devices DeviceOrchestrator, hosts HostProvisioner, q QuotaReader, store batch.Repository, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{devices: devices, hosts: hosts, quota: q, store: store, logger: logger}
}

// Result is the outcome of admitting a batch. Status/Rollup are derived from
// the returned members, so an idempotent replay of a settled batch reports its
// real state, not a fresh admission's. Status is empty on a validate_only plan
// (nothing exists to roll up).
type Result struct {
	BatchID    string
	Batch      *batch.Batch
	Devices    []*device.Device
	Resolution *device.Resolution
	Status     batch.Status
	Rollup     batch.Rollup
	Admitted   int
	NewHosts   int
	Warnings   []string
}

// View is a batch's derived status over its members. Members is populated by Get
// and nil in List.
type View struct {
	BatchID string
	Batch   *batch.Batch
	Status  batch.Status
	Rollup  batch.Rollup
	Members []*device.Device
}

// ErrBatchExists reports a batch-row insert losing to a concurrent same-key
// admission; the service resumes against the winner's row. Feature-internal:
// unlike batch.ErrNotFound/batch.ErrFailed it never reaches a handler.
var ErrBatchExists = errors.New("batch row already exists")

// specFingerprint hashes the full admission request. Stored on the batch row,
// it is what makes a key resumable: same key + same request resumes, same key
// + different request conflicts.
func specFingerprint(spec batch.Spec, req device.Requirements) (string, error) {
	fp, err := idempotency.Fingerprint(struct {
		Spec         batch.Spec          `json:"spec"`
		Requirements device.Requirements `json:"requirements"`
	}{spec, req})
	if err != nil {
		return "", fmt.Errorf("fingerprinting batch spec: %w", err)
	}
	return fp, nil
}

// resumableBatch returns the existing batch a keyed admission may resume, nil
// when the key is unused. A batch whose atomic policy tripped is not resumable
// (batch.ErrFailed), and a key presented with a different request fingerprint
// is a conflict. Resuming under a reshaped spec could grow membership past
// RequestedCount, leaving an atomic batch unsettleable or, worse, torn down by
// the sweep while fully healthy. Rows written before fingerprinting existed
// carry "" and skip the check.
func (s *Service) resumableBatch(ctx context.Context, batchID, fingerprint string) (*batch.Batch, error) {
	b, err := s.store.Get(ctx, batchID)
	switch {
	case errors.Is(err, batch.ErrNotFound):
		return nil, nil //nolint:nilnil // nil batch = key unused, a normal fresh admission.
	case err != nil:
		return nil, fmt.Errorf("checking for existing batch: %w", err)
	case b.FailedAt != nil:
		// The key's batch already tripped: its fleet is gone and its
		// per-resource idempotency state is dead.
		return nil, fmt.Errorf("%w: %s (%s); delete the batch or use a new idempotency key",
			batch.ErrFailed, batchID, b.FailureReason)
	case b.SpecFingerprint != "" && b.SpecFingerprint != fingerprint:
		return nil, fmt.Errorf("%w: batch %s", idempotency.ErrConflict, batchID)
	}
	return b, nil
}

// admission is the resolved, quota-gated placement shared by Create and Plan.
type admission struct {
	resolution *device.Resolution
	plan       batch.Plan
	hostSpec   *host.CreateRequest
}

// resolveAndPlan runs the read-only front half of admission: validate the spec,
// resolve the build (registering nothing when dryRun), resolve the host shape,
// gate on host quota, and compute the placement. It rejects when the plan cannot
// admit min_ready devices.
//
// A resuming admission (existing != nil: a keyed retry whose batch row already
// exists) skips the quota gate. The original request already passed it, and
// the batch's own hosts now count against Available(), so re-gating would
// reject the resume for the quota its own fleet consumed. Isolated placement
// replays the original assignment by planning from the stored RequestedCount;
// pack placement defers planning to resumePack, which fills only the missing
// members against current capacity. Per-index keys make provisioning replay
// existing hosts without consuming quota; a host that is actually new still
// passes host Create's atomic quota gate.
func (s *Service) resolveAndPlan(ctx context.Context, spec batch.Spec, req device.Requirements, createdBy string, dryRun bool, existing *batch.Batch) (*admission, error) {
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("validating batch spec: %w", err)
	}
	if err := req.Validate(); err != nil {
		// Returned unwrapped so the ValidationError's own message is the 400 body.
		return nil, err
	}

	var res *device.Resolution
	var err error
	if dryRun {
		res, err = s.devices.PlanBuildForBatch(ctx, req.Build)
	} else {
		res, err = s.devices.ResolveBuildForBatch(ctx, req.Build, createdBy)
	}
	if err != nil {
		return nil, fmt.Errorf("resolving batch build: %w", err)
	}

	hs, err := s.devices.ResolveHostSpec(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("resolving batch host shape: %w", err)
	}
	hostSpec := hs.Spec
	res.Warnings = append(res.Warnings, hs.Warnings...)
	res.MachineType, res.Zone = hostSpec.MachineType, hostSpec.Zone
	// The template's pool is the batch's: its hosts join that pool and its
	// members pack onto that pool's machines.
	res.Pool = hostSpec.Pool
	if hostSpec.ProvisioningModel != "" {
		res.CapacityModel = strings.ToLower(string(hostSpec.ProvisioningModel))
	}
	res.PlacementReasons = hs.PlacementReasons
	perHost := int(hostSpec.MaxDevices)

	if existing != nil {
		res.HostAction = device.HostActionProvision
		if existing.Placement == batch.PlacementIsolated {
			// Replay the original assignment. Plan from the STORED admitted
			// count, never the request's. A quota-clamped first attempt
			// admitted fewer than requested, and the retry must converge to
			// that fleet, not re-plan a bigger one it has no quota for.
			// Passing the count as headroom keeps the replay unclamped
			// (NewHosts <= count always holds).
			plan := batch.Compute(existing.RequestedCount, batch.PlacementIsolated, nil, perHost, existing.RequestedCount)
			return &admission{resolution: res, plan: plan, hostSpec: hostSpec}, nil
		}
		// A pack resume plans nothing here: the original slots are long taken,
		// so resumePack list-and-fills the missing members against current
		// capacity with its own remainder-sized plan. Gating the FULL count
		// here would spuriously reject a nearly-complete resume.
		return &admission{resolution: res, hostSpec: hostSpec}, nil
	}

	// Pack fills existing shareable capacity first, inside the pool the host
	// template was resolved into. A batch packs onto the machines its own
	// hosts would join, never another pool's. GPU-only when the GPU is a hard
	// constraint; a mere preference packs anywhere so it does not crowd the
	// GPU fleet. The resolved build orders warm hosts first.
	var capacity []batch.HostSlot
	if spec.Placement == batch.PlacementPack {
		if capacity, err = s.devices.ListPlaceableCapacity(ctx, hostSpec.Pool, req.GPURequired, res.BuildID); err != nil {
			return nil, fmt.Errorf("listing pack capacity: %w", err)
		}
	}

	q, err := s.quota.Get(ctx, quota.ScopePlatform, quota.GlobalScopeID)
	if err != nil {
		return nil, fmt.Errorf("reading host quota: %w", err)
	}
	plan := batch.Compute(spec.Count, spec.Placement, capacity, perHost, int(q.Available()))
	if plan.Admitted < spec.MinReady {
		return nil, &device.ValidationError{Message: fmt.Sprintf(
			"insufficient capacity: can admit %d of %d devices (min_ready %d); host quota allows %d more host(s)",
			plan.Admitted, spec.Count, spec.MinReady, q.Available())}
	}
	res.HostAction = device.HostActionProvision
	if len(plan.OnExisting) > 0 && plan.NewHosts == 0 {
		res.HostAction = device.HostActionPlace
	}
	return &admission{resolution: res, plan: plan, hostSpec: hostSpec}, nil
}

// Plan is the validate_only dry run: the same checks and resolution a real
// admission performs, with nothing provisioned or created (AIP-163).
func (s *Service) Plan(ctx context.Context, spec batch.Spec, req device.Requirements, createdBy string) (*Result, error) {
	a, err := s.resolveAndPlan(ctx, spec, req, createdBy, true, nil)
	if err != nil {
		return nil, err
	}
	if a.plan.NewHosts > 0 {
		// The resolution is this call's own value, so extending its trace in
		// place is safe. Same note a device plan carries.
		a.resolution.PlacementReasons = append(a.resolution.PlacementReasons,
			"initial placement; provisioning may fail over to another ranked zone")
	}
	return &Result{
		Resolution: a.resolution, Admitted: a.plan.Admitted,
		NewHosts: a.plan.NewHosts, Warnings: a.resolution.Warnings,
	}, nil
}

// Create admits a batch: resolve the build once, gate on host quota, plan the
// placement, persist the policy row, provision hosts, and create the member
// devices. When idempotencyKey is set the admission is retry-safe and
// resumable: a stable batch id plus per-index host and device keys make a
// retry converge to the same fleet instead of duplicating it. Resume requires
// the retry to be the SAME request: the batch row stores a spec fingerprint,
// and a key reused with a different spec is rejected as a conflict.
//
// Admission is sequential Spanner commits (the policy row + M hosts + N
// devices) inside one request, bounded by the server write deadline; at
// MaxCount that is normally a few seconds. If the response is lost anyway, a
// keyed retry converges: the policy row is written before any host exists, so
// every partial admission is detectable as a resume (a crash before the row
// means nothing happened). Committing the whole admission in one transaction
// would still remove the partial-fleet window for unkeyed requests.
//
// An admission error is NOT an atomic trip, because a keyed retry resumes and
// completes the fleet, which is what keyed admission is for. The atomic policy
// has exactly one enforcement point, the reaper's batch sweep, which fails a
// batch that errored a member, missed its ready timeout, or stopped short of
// full membership past the resume grace window.
func (s *Service) Create(ctx context.Context, spec batch.Spec, req device.Requirements, createdBy, idempotencyKey string) (*Result, error) {
	batchID := deriveBatchID(idempotencyKey)
	fingerprint, err := specFingerprint(spec, req)
	if err != nil {
		return nil, err
	}
	var existing *batch.Batch
	if idempotencyKey != "" {
		if existing, err = s.resumableBatch(ctx, batchID, fingerprint); err != nil {
			return nil, err
		}
	}

	a, err := s.resolveAndPlan(ctx, spec, req, createdBy, false, existing)
	if err != nil {
		return nil, err
	}

	b := existing
	if b == nil {
		b = &batch.Batch{
			ID:         batchID,
			NamePrefix: spec.NamePrefix,
			// The sweep's full-fill target is what was actually admitted (a
			// quota-clamped best_effort plan admits fewer than requested).
			RequestedCount:  a.plan.Admitted,
			FillPolicy:      spec.FillPolicy,
			Placement:       spec.Placement,
			ReadyTimeout:    spec.ReadyTimeout,
			SpecFingerprint: fingerprint,
			CreatedBy:       createdBy,
			CreatedAt:       time.Now(),
		}
		switch cerr := s.store.Create(ctx, b); {
		case errors.Is(cerr, ErrBatchExists):
			// Lost the insert to a concurrent same-key admission. Resume
			// against the winner's row (fingerprint-checked): its admitted
			// count is the batch's target, not this attempt's plan.
			if existing, err = s.resumableBatch(ctx, batchID, fingerprint); err != nil {
				return nil, err
			}
			if existing == nil {
				return nil, fmt.Errorf("batch %s: lost the insert race but the winner's row is gone", batchID)
			}
			b = existing
			if a, err = s.resolveAndPlan(ctx, spec, req, createdBy, false, existing); err != nil {
				return nil, err
			}
		case cerr != nil:
			return nil, fmt.Errorf("persisting batch: %w", cerr)
		}
	}
	resuming := existing != nil

	j := job{batch: b, spec: spec, a: a, createdBy: createdBy, idempotencyKey: idempotencyKey}
	if resuming && b.Placement == batch.PlacementPack {
		return s.resumePack(ctx, j)
	}

	devices, err := s.createPlannedMembers(ctx, j)
	if err != nil {
		return nil, err
	}

	s.logger.Info("admitted batch",
		slog.String("batch_id", batchID),
		slog.Int("devices", len(devices)),
		slog.Int("new_hosts", a.plan.NewHosts),
		slog.Bool("resumed", resuming))
	r := rollup(devices)
	return &Result{
		BatchID: batchID, Batch: b, Devices: devices, Resolution: a.resolution,
		Status: b.Status(r), Rollup: r,
		Admitted: a.plan.Admitted, NewHosts: a.plan.NewHosts, Warnings: a.resolution.Warnings,
	}, nil
}

// job is the request-scoped identity every host provision and member create
// in one admission shares.
type job struct {
	batch          *batch.Batch
	spec           batch.Spec
	a              *admission
	createdBy      string
	idempotencyKey string
}

// createPlannedMembers provisions the admission's new hosts and creates every
// member the plan placed. The fresh-admission counterpart to
// fillMissingMembers, which places a pack resume's remainder instead.
// Isolated placement makes the new hosts exclusive.
func (s *Service) createPlannedMembers(ctx context.Context, j job) ([]*device.Device, error) {
	newHostIDs, err := s.provisionHosts(ctx, j, j.a.plan.NewHosts, j.spec.Placement == batch.PlacementIsolated)
	if err != nil {
		return nil, err
	}
	assignment := j.a.plan.Assign(newHostIDs)
	toCreate := make(map[int]string, len(assignment))
	for i, hostID := range assignment {
		toCreate[i] = hostID
	}
	return s.createMembers(ctx, j, toCreate)
}

// createMembers creates the given members (index → assigned host), rescuing
// pack-placement capacity races: a member that loses its slot to a concurrent
// create is re-placed onto freshly provisioned hosts in one bounded fallback
// round. Isolated placement never races (its hosts are empty and exclusive),
// so any capacity error there is a real failure.
func (s *Service) createMembers(ctx context.Context, j job, toCreate map[int]string) ([]*device.Device, error) {
	devices := make([]*device.Device, 0, len(toCreate))
	var raced []int
	for _, i := range slices.Sorted(maps.Keys(toCreate)) {
		d, cerr := s.createMember(ctx, j, i, toCreate[i])
		switch {
		case cerr == nil:
			devices = append(devices, d)
		case j.batch.Placement == batch.PlacementPack && errors.Is(cerr, host.ErrCapacityExceeded):
			raced = append(raced, i)
		default:
			return nil, fmt.Errorf("creating batch device %d/%d: %w", i+1, j.batch.RequestedCount, cerr)
		}
	}
	if len(raced) == 0 {
		return devices, nil
	}

	// The raced members' planned slots were taken by concurrent creates.
	// Provision overflow hosts for exactly the raced remainder, keyed by the
	// member index so a retry converges, and land them there. A fresh host
	// can itself lose a race (pack hosts stay shareable), so a second loss is
	// surfaced as an error instead of looping.
	perHost := max(int(j.a.hostSpec.MaxDevices), 1)
	s.logger.Info("pack placement raced, provisioning overflow hosts",
		slog.String("batch_id", j.batch.ID), slog.Int("raced", len(raced)))
	var overflow []string
	for n := range (len(raced) + perHost - 1) / perHost {
		h, err := s.provisionHost(ctx, j, "host:overflow:"+strconv.Itoa(raced[n*perHost]), false)
		if err != nil {
			return nil, fmt.Errorf("provisioning overflow host for raced members: %w", err)
		}
		overflow = append(overflow, h)
	}
	for n, i := range raced {
		d, cerr := s.createMember(ctx, j, i, overflow[n/perHost])
		if cerr != nil {
			return nil, fmt.Errorf("creating raced batch device %d/%d: %w", i+1, j.batch.RequestedCount, cerr)
		}
		devices = append(devices, d)
	}
	return devices, nil
}

// createMember creates one member device. An idempotency-fingerprint conflict
// means a concurrent admission with the same key already created this member
// on a different host (pack plans are not deterministic). Re-read it and
// converge on the winner instead of failing.
func (s *Service) createMember(ctx context.Context, j job, i int, hostID string) (*device.Device, error) {
	d, err := s.devices.Create(ctx, device.CreateRequest{
		HostID:         hostID,
		Name:           fmt.Sprintf("%s-%03d", j.batch.NamePrefix, i),
		BuildID:        j.a.resolution.BuildID,
		BuildTarget:    j.a.resolution.BuildTarget,
		TTLHours:       j.spec.TTLHours,
		BatchID:        j.batch.ID,
		IdempotencyKey: idemKey(j.idempotencyKey, j.batch.ID, strconv.Itoa(i)),
	}, j.createdBy)
	if err != nil && errors.Is(err, idempotency.ErrConflict) {
		if won := s.findMember(ctx, j.batch.ID, j.batch.NamePrefix, i); won != nil {
			return won, nil
		}
	}
	//nolint:wrapcheck // callers wrap with member context; sentinel checks need the bare chain.
	return d, err
}

// findMember returns the batch member with index i, if it exists (strong read).
func (s *Service) findMember(ctx context.Context, batchID, prefix string, i int) *device.Device {
	members, err := s.devices.List(ctx, device.ListFilter{BatchID: &batchID, Strong: true})
	if err != nil {
		return nil
	}
	name := fmt.Sprintf("%s-%03d", prefix, i)
	for _, d := range members {
		if d.Name == name {
			return d
		}
	}
	return nil
}

// resumePack completes a keyed pack admission: existing members are kept
// as-is and only the missing indices are placed, against CURRENT capacity,
// because the original plan's slots have long been taken by other creates
// (this is exactly why the isolated path's replay-identical-plan trick cannot
// work for pack). The batch's own earlier pack hosts are shareable, so any
// free slots they still have are simply part of that capacity view.
func (s *Service) resumePack(ctx context.Context, j job) (*Result, error) {
	members, err := s.devices.List(ctx, device.ListFilter{BatchID: &j.batch.ID, Strong: true})
	if err != nil {
		return nil, fmt.Errorf("listing batch members for resume: %w", err)
	}
	have := make(map[string]bool, len(members))
	for _, d := range members {
		have[d.Name] = true
	}
	var missing []int
	for i := range j.batch.RequestedCount {
		if !have[fmt.Sprintf("%s-%03d", j.batch.NamePrefix, i)] {
			missing = append(missing, i)
		}
	}

	devices := slices.Clone(members)
	newHosts := 0
	if len(missing) > 0 {
		created, provisioned, err := s.fillMissingMembers(ctx, j, missing)
		if err != nil {
			return nil, err
		}
		devices = append(devices, created...)
		newHosts = provisioned
	}

	s.logger.Info("resumed pack batch",
		slog.String("batch_id", j.batch.ID),
		slog.Int("existing", len(members)),
		slog.Int("filled", len(missing)),
		slog.Int("new_hosts", newHosts))
	r := rollup(devices)
	return &Result{
		BatchID: j.batch.ID, Batch: j.batch, Devices: devices, Resolution: j.a.resolution,
		Status: j.batch.Status(r), Rollup: r,
		Admitted: j.batch.RequestedCount, NewHosts: newHosts, Warnings: j.a.resolution.Warnings,
	}, nil
}

// fillMissingMembers places and creates a pack resume's missing members
// against current capacity, returning the created devices and how many hosts
// were provisioned for them.
func (s *Service) fillMissingMembers(ctx context.Context, j job, missing []int) ([]*device.Device, int, error) {
	capacity, err := s.devices.ListPlaceableCapacity(ctx, j.a.hostSpec.Pool, false, j.a.resolution.BuildID)
	if err != nil {
		return nil, 0, fmt.Errorf("listing pack capacity for resume: %w", err)
	}
	q, err := s.quota.Get(ctx, quota.ScopePlatform, quota.GlobalScopeID)
	if err != nil {
		return nil, 0, fmt.Errorf("reading host quota for resume: %w", err)
	}
	perHost := max(int(j.a.hostSpec.MaxDevices), 1)
	plan := batch.Compute(len(missing), batch.PlacementPack, capacity, perHost, int(q.Available()))
	if plan.Admitted < len(missing) {
		return nil, 0, &device.ValidationError{Message: fmt.Sprintf(
			"cannot resume batch: capacity for %d of %d missing members", plan.Admitted, len(missing))}
	}
	// Resume-scoped host keys: the original per-plan indices cannot be
	// reconstructed, and reusing them could replay a now-full host.
	hostIDs := make([]string, 0, plan.NewHosts)
	for n := range plan.NewHosts {
		h, herr := s.provisionHost(ctx, j, "host:resume:"+strconv.Itoa(missing[0])+":"+strconv.Itoa(n), false)
		if herr != nil {
			return nil, 0, fmt.Errorf("provisioning resume host %d/%d: %w", n+1, plan.NewHosts, herr)
		}
		hostIDs = append(hostIDs, h)
	}
	assignment := plan.Assign(hostIDs)
	toCreate := make(map[int]string, len(missing))
	for n, i := range missing {
		toCreate[i] = assignment[n]
	}
	created, err := s.createMembers(ctx, j, toCreate)
	if err != nil {
		return nil, 0, err
	}
	return created, plan.NewHosts, nil
}

// batchHostName is the display name every batch-provisioned host carries (the
// GCE instance name is generated independently).
const batchHostName = "batch"

// provisionHost provisions one host for the batch from the template, keyed by
// keySuffix so a retry reuses it rather than provisioning another.
func (s *Service) provisionHost(ctx context.Context, j job, keySuffix string, exclusive bool) (string, error) {
	hs := *j.a.hostSpec
	hs.Name = batchHostName
	hs.BatchID = j.batch.ID
	hs.Exclusive = exclusive
	hs.IdempotencyKey = idemKey(j.idempotencyKey, j.batch.ID, keySuffix)
	// batch_id already decides this host's class; the origin is stamped anyway
	// so provenance never has to be inferred from which columns are set.
	h, err := s.hosts.Create(ctx, hs, j.createdBy, host.OriginBatch)
	if err != nil {
		//nolint:wrapcheck // callers wrap with host-index context.
		return "", err
	}
	return h.ID, nil
}

// provisionHosts provisions n hosts for the batch, keyed per index. Isolated
// placement makes them exclusive. Dedicated fences the host from automatic
// placement for its whole lifetime, so it stays empty after admission as well.
func (s *Service) provisionHosts(ctx context.Context, j job, n int, exclusive bool) ([]string, error) {
	ids := make([]string, 0, n)
	for i := range n {
		h, err := s.provisionHost(ctx, j, "host:"+strconv.Itoa(i), exclusive)
		if err != nil {
			return nil, fmt.Errorf("provisioning batch host %d/%d: %w", i+1, n, err)
		}
		ids = append(ids, h)
	}
	return ids, nil
}

// Get returns a batch's derived status. A batch is visible while it has live
// members or a tripped policy row (a failed batch stays readable for CI
// post-mortems until deleted); an emptied or not-yet-populated one 404s, same
// as it drops out of List. Strong reads: the canonical CI flow polls Get
// immediately after create, inside the bounded-stale window.
func (s *Service) Get(ctx context.Context, batchID string) (*View, error) {
	b, err := s.store.Get(ctx, batchID)
	if err != nil && !errors.Is(err, batch.ErrNotFound) {
		return nil, fmt.Errorf("reading batch: %w", err)
	}
	members, err := s.devices.List(ctx, device.ListFilter{BatchID: &batchID, Strong: true})
	if err != nil {
		return nil, fmt.Errorf("listing batch members: %w", err)
	}
	if len(members) == 0 && (b == nil || b.FailedAt == nil) {
		return nil, batch.ErrNotFound
	}
	return newView(batchID, b, members, true), nil
}

// List returns a view per visible batch (no members): live ones plus failed
// ones kept for post-mortem. createdBy ("" = all) scopes the listing to one
// creator's batches. Bounded-stale reads are fine here, because it is a
// dashboard-shaped listing and a batch created inside the staleness window
// simply appears one poll later.
func (s *Service) List(ctx context.Context, createdBy string) ([]*View, error) {
	var filter batch.ListFilter
	if createdBy != "" {
		filter.CreatedBy = &createdBy
	}
	batches, err := s.store.List(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("listing batches: %w", err)
	}
	views := make([]*View, 0, len(batches))
	for _, b := range batches {
		members, err := s.devices.List(ctx, device.ListFilter{BatchID: &b.ID})
		if err != nil {
			return nil, fmt.Errorf("listing batch %s members: %w", b.ID, err)
		}
		if len(members) == 0 && b.FailedAt == nil {
			continue
		}
		views = append(views, newView(b.ID, b, members, false))
	}
	return views, nil
}

// Delete tears down a batch's live members and removes its policy row.
// Idempotent: nothing left is a no-op. The batch's dedicated hosts drain to
// zero devices and are early-reaped by the host reaper via hosts.batch_id.
func (s *Service) Delete(ctx context.Context, batchID string) error {
	deleted, err := s.deleteMembers(ctx, batchID)
	if err != nil {
		return err
	}
	if err := s.store.Delete(ctx, batchID); err != nil {
		return fmt.Errorf("deleting batch row: %w", err)
	}
	if deleted > 0 {
		s.logger.Info("deleted batch", slog.String("batch_id", batchID), slog.Int("members", deleted))
	}
	return nil
}

// deleteMembers tears down the batch's live members. Strong read: a
// bounded-stale read could miss just-created members and silently no-op the
// teardown, leaking the fleet until TTL.
func (s *Service) deleteMembers(ctx context.Context, batchID string) (int, error) {
	members, err := s.devices.List(ctx, device.ListFilter{BatchID: &batchID, Strong: true})
	if err != nil {
		return 0, fmt.Errorf("listing batch members: %w", err)
	}
	if len(members) == 0 {
		return 0, nil
	}
	deleted, err := s.devices.BatchDelete(ctx, members)
	if err != nil {
		return deleted, fmt.Errorf("deleting batch members: %w", err)
	}
	return deleted, nil
}

// newView derives a batch view from its members. minReady for the derived
// status is the count of live (non-deleting) members: a batch is ready when all
// its live members are running. (The requested min_ready is an admission-time
// gate only; the raw counts are returned so callers can apply their own
// threshold.) b may be nil for pre-registry members; the rollup then speaks
// for itself.
func newView(batchID string, b *batch.Batch, members []*device.Device, withMembers bool) *View {
	r := rollup(members)
	status := batch.DerivePhase(r.Total-r.Deleting, r)
	if b != nil {
		status = b.Status(r)
	}
	v := &View{BatchID: batchID, Batch: b, Status: status, Rollup: r}
	if withMembers {
		v.Members = members
	}
	return v
}

// rollup counts the members' live state. Reconciling is computed via
// IsReconciling() instead of the stored flag, because Create's returned members
// are built in memory and never pass through the read path that stamps the
// flag. It also excludes members tearing down: a deleting member leaves
// minReady, so counting its (transitional) reconciling would flip a ready batch
// back to provisioning.
func rollup(members []*device.Device) batch.Rollup {
	var r batch.Rollup
	for _, d := range members {
		r.Total++
		switch d.Status {
		case device.StatusRunning:
			r.Running++
		case device.StatusError:
			r.Errored++
		case device.StatusDeleting, device.StatusDeleted:
			r.Deleting++
			continue
		case device.StatusPending, device.StatusCreating, device.StatusStarting,
			device.StatusStopping, device.StatusStopped:
			// In flight or idle: counted by Total, and by Reconciling below.
		}
		if d.IsReconciling() {
			r.Reconciling++
		}
	}
	return r
}

func deriveBatchID(clientKey string) string {
	if clientKey == "" {
		return uuid.New().String()
	}
	return uuid.NewSHA1(batchIDNamespace, []byte("cco:batch:"+clientKey)).String()
}

// idemKey returns a per-resource idempotency key derived from the batch, or ""
// when the client supplied no key (each request is then independent).
func idemKey(clientKey, batchID, suffix string) string {
	if clientKey == "" {
		return ""
	}
	return batchID + ":" + suffix
}
