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

	"cloud.google.com/go/spanner"
	"github.com/google/uuid"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	"cloud-cuttlefish-orchestrator/internal/domain/batch"
	"cloud-cuttlefish-orchestrator/internal/domain/build"
	deviceDomain "cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/idempotency"
	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/features/device/placement"
	"cloud-cuttlefish-orchestrator/internal/features/device/validation"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

// VerbAdmitter records an operation handle and enqueues its task in one
// transaction, refusing a verb whose resource already has a live exclusive
// operation. The Spanner OperationsRepository satisfies it; declared here, where
// it is consumed, so this slice depends on one method.
type VerbAdmitter interface {
	Admit(ctx context.Context, req spannerplatform.AdmitRequest) (*operation.Record, error)
}

// TaskTypeCreateCVD is the queue task type for asynchronous CVD creation
// (canonical name in domain/task).
const TaskTypeCreateCVD = task.TypeDeviceCreateCVD

// TaskEnqueuer enqueues background work: a mutation to fold into the
// producer's own transaction (create's quad-write, delete's cleanup). The
// Spanner TaskQueue satisfies it.
//
// A folded enqueue never passes through the queue's own commit, so nothing
// tells this process's worker pool that work arrived; Wake does, and is called
// once the transaction carrying the mutation reports success. It only moves
// latency, because an unmade wake is found by the next poll.
type TaskEnqueuer interface {
	EnqueueMutation(ctx context.Context, t *task.Task) *spanner.Mutation
	Wake()
}

// TaskTypeCleanupCVD is the queue task type for asynchronous CVD teardown on the
// host after a device is deleted (canonical name in domain/task).
const TaskTypeCleanupCVD = task.TypeDeviceCleanupCVD

// TaskTypeCaptureBugreport is the queue task type for policy-gated
// capture-on-error bugreports (canonical name in domain/task).
const TaskTypeCaptureBugreport = task.TypeDeviceCaptureBugreport

// IdempotencyStore records and resolves Idempotency-Key -> device id mappings so
// a retried create returns the original device. The Spanner IdempotencyRepository
// satisfies it.
type IdempotencyStore interface {
	idempotency.Store
	NewKeyMutation(key, resourceID, fingerprint string) *spanner.Mutation
}

// DeviceServiceOption configures a DeviceService.
type DeviceServiceOption func(*DeviceService)

// WithDeviceAuditLogger configures an audit sink for the DeviceService.
func WithDeviceAuditLogger(al audit.Sink) DeviceServiceOption {
	return func(s *DeviceService) {
		s.auditLogger = al
	}
}

// WithTaskEnqueuer wires the queue used to enqueue CVD-creation tasks. It is
// required for Create to function; without it Create returns an error.
func WithTaskEnqueuer(e TaskEnqueuer) DeviceServiceOption {
	return func(s *DeviceService) {
		s.taskEnqueuer = e
	}
}

// WithOperations enables the async verbs (restart, powerwash) by wiring the
// admission path. Unset, those verbs refuse. Reverting to a synchronous call
// whose outcome nothing records is worse than a plain failure.
func WithOperations(a VerbAdmitter) DeviceServiceOption {
	return func(s *DeviceService) { s.operations = a }
}

// WithIdempotencyStore enables Idempotency-Key handling on Create. When unset,
// any Idempotency-Key is ignored.
func WithIdempotencyStore(store IdempotencyStore) DeviceServiceOption {
	return func(s *DeviceService) {
		s.idempotencyStore = store
	}
}

// DeviceService implements the business logic for the device vertical slice.
//
// It orchestrates interactions between the domain (Device entity), storage (Spanner),
// and infrastructure (Host Orchestrator, GCE) to manage the full device lifecycle.
type DeviceService struct {
	deviceRepo       AtomicDeviceRepository
	hostRepo         host.Repository
	builds           build.Reader
	hoClientFactory  *hoclient.Factory
	taskEnqueuer     TaskEnqueuer
	idempotencyStore IdempotencyStore
	auditLogger      audit.Sink
	logger           *slog.Logger
	artifacts        *artifactDeps

	// operations admits the async verbs (restart, powerwash). Nil disables them.
	// The verbs then refuse rather than silently reverting to a synchronous call
	// whose outcome nothing records.
	operations VerbAdmitter

	// lifetimePolicy resolves the operator lifetime defaults (session TTL,
	// extension grant and allowance). Nil reads as the compiled defaults.
	lifetimePolicy PolicyReader

	// Declarative-create wiring (nil until WithDeclarativeCreate): placement
	// resolves intent, the provisioner and quota reader serve the commits and
	// plans this service still owns.
	placement   *placement.Resolver
	provisioner HostProvisioner
	hostQuota   QuotaReader

	// wireErr collects option-validation failures (a partial optional
	// dependency set); the constructor returns it so a wiring bug fails boot
	// loudly instead of panicking at first use.
	wireErr error
}

// NewDeviceService creates a new device service.
// The deviceRepo must implement AtomicDeviceRepository for atomic device creation.
func NewDeviceService(
	deviceRepo AtomicDeviceRepository,
	hostRepo host.Repository,
	builds build.Reader,
	hoClientFactory *hoclient.Factory,
	logger *slog.Logger,
	opts ...DeviceServiceOption,
) (*DeviceService, error) {
	if deviceRepo == nil {
		return nil, errors.New("device repository is required")
	}
	if hostRepo == nil {
		return nil, errors.New("host repository is required")
	}
	if builds == nil {
		return nil, errors.New("build reader is required")
	}
	if hoClientFactory == nil {
		return nil, errors.New("hoClientFactory is required")
	}
	if logger == nil {
		logger = slog.Default()
	}

	s := &DeviceService{
		deviceRepo:      deviceRepo,
		hostRepo:        hostRepo,
		builds:          builds,
		hoClientFactory: hoClientFactory,
		logger:          logger,
	}

	for _, opt := range opts {
		opt(s)
	}
	if s.wireErr != nil {
		return nil, s.wireErr
	}

	return s, nil
}

// Get retrieves a device by ID and enriches it with host information.
func (s *DeviceService) Get(ctx context.Context, id string) (*deviceDomain.Device, error) {
	d, err := s.deviceRepo.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	// Enrich with host information for direct signaling support
	s.enrichDeviceWithHost(ctx, d)

	return d, nil
}

// enrichDeviceWithHost hydrates the device entity with transient host network data.
//
// This is critical for the "Hybrid" signaling mode, allowing the frontend to
// establish direct WebRTC connections to the GCE host instance.
func (s *DeviceService) enrichDeviceWithHost(ctx context.Context, d *deviceDomain.Device) {
	if d.HostID == "" {
		return
	}

	h, err := s.hostRepo.Get(ctx, d.HostID)
	if err != nil {
		s.logger.Debug("failed to enrich device with host info",
			slog.String("device_id", d.ID),
			slog.String(colHostID, d.HostID),
			slog.Any("error", err),
		)
		return
	}

	d.HostName = h.Name
	d.HostZone = h.Zone
	d.HostExternalIP = h.IPAddress
	d.HostInternalIP = h.InternalIP
}

// List retrieves devices with optional filtering and enriches them with host
// information via one batched host lookup (the console polls this endpoint, so
// a per-device read would be an N+1 on the hot path).
func (s *DeviceService) List(ctx context.Context, filter deviceDomain.ListFilter) ([]*deviceDomain.Device, error) {
	devices, err := s.deviceRepo.List(ctx, filter)
	if err != nil {
		return nil, err
	}

	var hostIDs []string
	for _, d := range devices {
		if d.HostID != "" && !slices.Contains(hostIDs, d.HostID) {
			hostIDs = append(hostIDs, d.HostID)
		}
	}
	if len(hostIDs) == 0 {
		return devices, nil
	}

	hosts, err := s.hostRepo.GetByIDs(ctx, hostIDs)
	if err != nil {
		// Enrichment is best effort; the device list itself is still valid.
		s.logger.Debug("failed to enrich devices with host info", slog.Any("error", err))
		return devices, nil
	}
	for _, d := range devices {
		if h, ok := hosts[d.HostID]; ok {
			d.HostName = h.Name
			d.HostZone = h.Zone
			d.HostExternalIP = h.IPAddress
			d.HostInternalIP = h.InternalIP
		}
	}

	return devices, nil
}

// Count returns the number of devices matching the filter, ignoring pagination.
func (s *DeviceService) Count(ctx context.Context, filter deviceDomain.ListFilter) (int64, error) {
	count, err := s.deviceRepo.Count(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("counting devices: %w", err)
	}
	return count, nil
}

// ListPlaceableCapacity is the batch pack planner's view of placeable hosts in
// one pool. buildID (the batch's resolved registry build) orders hosts already
// holding that build's artifacts first; empty or non-pinned degrades to plain
// bin-packing.
//
// The pool comes from the caller because a batch resolved it once, when its host
// template was built, and packs against that same partition every time it plans.
// A resume that re-derived it from what it happened to know could spread the
// second half of a batch across a different set of machines from the first.
func (s *DeviceService) ListPlaceableCapacity(ctx context.Context, pool string, gpuOnly bool, buildID string) ([]batch.HostSlot, error) {
	if s.placement == nil {
		return nil, errDeclarativeNotConfigured
	}
	prefs := placement.Prefs{Pool: pool, AffineFetchKey: s.placement.AffineFetchKey(ctx, buildID)}
	return s.placement.PlaceableCapacity(ctx, gpuOnly, prefs)
}

// Create initializes a new device record and triggers asynchronous provisioning.
//
// This method enforces atomic reservation of host capacity and ensures the
// device is persisted in a 'Pending' state before the long-running CVD creation starts.
func (s *DeviceService) Create(ctx context.Context, req deviceDomain.CreateRequest, createdBy string) (*deviceDomain.Device, error) {
	// Declarative create: resolve the requirements into a build and a host,
	// then commit through createOnHost with the resolved host.
	if req.Requirements != nil {
		d, _, err := s.CreateDeclarative(ctx, req, createdBy)
		return d, err
	}
	if err := validateDeviceName(req.Name); err != nil {
		return nil, err
	}
	if req.HostID == "" {
		return nil, &deviceDomain.ValidationError{Message: "host_id is required unless requirements is set"}
	}
	if err := s.checkInstanceBuilds(ctx, req.EnvConfig); err != nil {
		return nil, err
	}
	return s.createOnHost(ctx, req, createdBy)
}

// validateDeviceName refuses a name that parses as a UUID, which the transport
// pattern admits. Clients take a name or an ID in the same argument.
func validateDeviceName(name string) error {
	if host.IDShaped(name) {
		return &deviceDomain.ValidationError{Message: fmt.Sprintf(
			"device name %q has the shape of a UUID: clients accept a name or an ID in the same argument, so choose a name that is not an ID", name)}
	}
	return nil
}

// createOnHost is Create for a request whose host and builds are settled. The
// declarative path commits through it after its own checks have run.
func (s *DeviceService) createOnHost(ctx context.Context, req deviceDomain.CreateRequest, createdBy string) (*deviceDomain.Device, error) {
	// Fail fast if the host is missing; existence, capacity, and running state are
	// re-checked atomically in CreateWithHostIncrement. The CVD-creation handler
	// reloads the host when it runs.
	if _, err := s.hostRepo.Get(ctx, req.HostID); err != nil {
		return nil, fmt.Errorf("host not found: %w", err)
	}

	d, err := s.newPendingDevice(ctx, req, createdBy)
	if err != nil {
		return nil, err
	}

	// Idempotency: a retried request with the same key returns the original
	// device. The key row is inserted in the same transaction as the device, so a
	// concurrent duplicate either reads the existing key here or loses the
	// primary-key race on commit (handled below).
	extra, existing, err := s.idempotencyMutation(ctx, req, d)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	// Enqueue the CVD-creation task so the work is durably queued iff the device
	// is created (no dual-write, no lost work).
	taskMut, err := s.enqueueCreateMutation(ctx, d)
	if err != nil {
		return nil, err
	}
	extra = append(extra, taskMut)

	// Atomically create the device, increment the host counter, enqueue the task,
	// and record the idempotency key. All succeed or all fail.
	if err := s.deviceRepo.CreateWithHostIncrement(ctx, d, extra...); err != nil {
		// A concurrent request with the same key may have won the commit race:
		// an equivalent request returns the winner's device (idempotent
		// success); a different request reusing the key is the same conflict
		// the sequential path reports. It is never the wrong device.
		won, found, raceErr := s.lookupIdempotent(ctx, req)
		if raceErr != nil {
			return nil, raceErr
		}
		if found {
			return won, nil
		}
		return nil, fmt.Errorf("failed to create device: %w", err)
	}
	s.taskEnqueuer.Wake()

	s.auditDeviceCreated(d, createdBy, req.GPUDegraded)

	s.logger.Info("created device record",
		slog.String("id", d.ID),
		slog.String(colName, d.Name),
		slog.String(colHostID, d.HostID),
		slog.String("gpu_mode", string(d.GPUMode)),
	)

	// CVD creation now runs from the durable task queue (see CreateCVDHandler),
	// so it survives instance restarts and is retried on transient failure.
	return d, nil
}

// checkInstanceBuilds refuses an env_config whose per-instance @build names a
// build the registry does not hold. Without it the create would commit and
// the payload would find no artifacts for that instance on the host. Any other
// lookup failure is infrastructure and passes through unchanged.
func (s *DeviceService) checkInstanceBuilds(ctx context.Context, envConfig map[string]any) error {
	for i, id := range validation.InstanceBuildRefs(envConfig) {
		if id == "" {
			continue
		}
		_, err := s.builds.Get(ctx, id)
		if errors.Is(err, build.ErrNotFound) {
			return &deviceDomain.ValidationError{Message: fmt.Sprintf(
				"env_config.instances[%d].%s: build %q is not registered", i, validation.BuildKey, id)}
		}
		if err != nil {
			return fmt.Errorf("looking up build %s for instances[%d]: %w", id, i, err)
		}
	}
	return nil
}

// newPendingDevice assembles the validated pending row for a create whose host
// is already resolved: the create-time defaults, the ephemeral lifetime and the
// born-pinned exemption.
func (s *DeviceService) newPendingDevice(ctx context.Context, req deviceDomain.CreateRequest, createdBy string) (*deviceDomain.Device, error) {
	// Auto lets cvd pick the renderer by host hardware. Hosts are CPU-only
	// unless a GPU was explicitly attached, so a fixed renderer default would
	// be wrong for one host shape or the other.
	gpuMode := req.GPUMode
	if gpuMode == "" {
		gpuMode = deviceDomain.GPUModeAuto
	}

	// Determine mode and instance count
	mode := req.Mode
	if mode == "" {
		mode = "simple"
	}
	// The SAME derivation the create is charged against. Two independent counts
	// here is the defect the instance-denominated accounting exists to prevent.
	// The row would carry one number while the host counter was debited another,
	// and the refund at delete reads the row.
	instanceCount := int(requestedShape(req).Instances)

	// Ephemeral with the requested TTL (operator policy when zero); pinned
	// creation takes precedence but keeps TTLSeconds so a later unpin has a
	// base TTL.
	ttlSeconds := int64(req.TTLHours) * 3600
	if req.TTLHours <= 0 {
		policy, err := s.policyLifetimes(ctx)
		if err != nil {
			return nil, err
		}
		ttlSeconds = policy.DeviceTTLHours * 3600
	}
	lifecycle := deviceDomain.NewEphemeralLifecycle(ttlSeconds)
	if req.Pinned {
		lifecycle.Pin(createdBy, req.PinnedReason, req.PinnedReasonDetail, req.PinnedUntil)
	}

	d := &deviceDomain.Device{
		ID:            uuid.New().String(),
		HostID:        req.HostID,
		Name:          req.Name,
		Status:        deviceDomain.StatusPending,
		BuildID:       req.BuildID,
		BuildTarget:   req.BuildTarget,
		GPUMode:       gpuMode,
		CreatedBy:     &createdBy,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		Lifecycle:     lifecycle,
		Mode:          mode,
		InstanceCount: instanceCount,
		EnvConfig:     req.EnvConfig,
		Tags:          req.Tags,
		BatchID:       req.BatchID,
	}

	if err := d.Validate(); err != nil {
		return nil, err
	}
	return d, nil
}

// auditDeviceCreated records a committed create.
//
// This entry rides the buffered sink, which drops under pressure, while the
// host-create entry commits in its own transaction. The asymmetry is intended
// and survivable HERE for one reason. The degradation figure below is a RATIO,
// and its numerator and denominator are the same rows dropped in the same
// batches. A bare count would deflate silently under exactly the load that
// produces degradations. If that ratio ever gates spending, this entry moves
// into the create transaction the way host.create did.
func (s *DeviceService) auditDeviceCreated(d *deviceDomain.Device, createdBy string, gpuDegraded *bool) {
	if s.auditLogger == nil {
		return
	}
	metadata := map[string]any{
		colHostID:  d.HostID,
		colName:    d.Name,
		"gpu_mode": string(d.GPUMode),
	}
	if d.BatchID != "" {
		metadata["batch_id"] = d.BatchID
	}
	// Both arms, or neither. Stamping only the degraded case would leave the
	// index small and the denominator missing, and forty degradations mean
	// nothing without "out of how many creates wanted a GPU". Absent stays
	// absent: a create that named its own host made no placement decision to
	// judge, and NULL keeps it out of the index the ratio scans.
	if gpuDegraded != nil {
		metadata["gpu_degraded"] = *gpuDegraded
	}
	entry := audit.New(audit.ActionDeviceCreate, audit.ResourceDevice).
		WithActor(createdBy).
		WithResource(d.ID).
		WithMetadata(metadata).
		Build()
	s.auditLogger.LogAsync(entry)
}

// enqueueCreateMutation builds the mutation that enqueues this device's
// CVD-creation task. The task carries only the device ID. The create-time
// spec (env_config/mode/instance_count) is persisted on the device row
// (desired_spec) and the actuator re-derives it from there.
func (s *DeviceService) enqueueCreateMutation(ctx context.Context, d *deviceDomain.Device) (*spanner.Mutation, error) {
	if s.taskEnqueuer == nil {
		return nil, errors.New("device service has no task enqueuer configured")
	}
	return s.taskEnqueuer.EnqueueMutation(ctx, task.New(TaskTypeCreateCVD, d.ID)), nil
}

// idempotencyMutation handles the Idempotency-Key for a create. It returns the
// key-insert mutation to fold into the create transaction, or a non-nil existing
// device when the key was already used for an equivalent request (short-circuit).
// The fingerprint covers every JSON-visible CreateRequest field, so reusing a
// key with a different request returns idempotency.ErrConflict rather than the
// wrong device.
func (s *DeviceService) idempotencyMutation(ctx context.Context, req deviceDomain.CreateRequest, d *deviceDomain.Device) ([]*spanner.Mutation, *deviceDomain.Device, error) {
	if req.IdempotencyKey == "" || s.idempotencyStore == nil {
		return nil, nil, nil
	}
	fingerprint, err := requestFingerprint(req)
	if err != nil {
		return nil, nil, err
	}
	prepared, err := idempotency.PrepareMutation[*deviceDomain.Device, *spanner.Mutation](
		ctx, s.idempotencyStore, req.IdempotencyKey, fingerprint, d.ID, s.deviceRepo.Get)
	if err != nil {
		// Passed through unwrapped so idempotency.ErrConflict maps to 422 with
		// its own message.
		return nil, nil, err
	}
	if prepared.Found {
		return nil, prepared.Existing, nil
	}
	return prepared.Mutations, nil, nil
}

// lookupIdempotent resolves a lost commit race for req's Idempotency-Key. It
// returns the winner's device when req matches the recorded fingerprint,
// idempotency.ErrConflict when a different request reused the key, or
// found=false when the key resolves to nothing, in which case the caller falls
// back to its original commit error.
func (s *DeviceService) lookupIdempotent(ctx context.Context, req deviceDomain.CreateRequest) (*deviceDomain.Device, bool, error) {
	if req.IdempotencyKey == "" || s.idempotencyStore == nil {
		return nil, false, nil
	}
	fingerprint, err := requestFingerprint(req)
	if err != nil {
		return nil, false, nil //nolint:nilerr // fall back to the caller's commit error
	}
	// Returned unwrapped so idempotency.ErrConflict reaches the transport as a 422.
	return idempotency.ResolveRace(ctx, s.idempotencyStore, req.IdempotencyKey, fingerprint, s.deviceRepo.Get)
}

// requestFingerprint returns the Idempotency-Key fingerprint for req. That is
// the declarative entry's original-request override when set (the request was
// rewritten by placement resolution), else the hash of req itself.
func requestFingerprint(req deviceDomain.CreateRequest) (string, error) {
	if req.FingerprintOverride != "" {
		return req.FingerprintOverride, nil
	}
	return idempotency.Fingerprint(req)
}

// Delete stops and removes a device
func (s *DeviceService) Delete(ctx context.Context, id string) error {
	d, err := s.deviceRepo.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("device not found: %w", err)
	}

	// There is NO `deleting` intent write before the commit below, on purpose.
	// An informational pre-write used to live here, and it was the only way a
	// device could strand. The intent landed, the transactional mark timed
	// out, and the row sat `deleting` with no task until the stale-teardown
	// suspector healed it. With a single commit, a failed delete leaves the
	// row exactly as it was and the client retries an idempotent verb, so the
	// strandable state is unreachable instead of compensated for. Nothing
	// observed the phase anyway. The mark follows within milliseconds, and every
	// consumer of "this device is being torn down" (the create's row-poll,
	// the reaper's exclusions, Mutate's tombstone guard) accepts `deleted`.

	// Mark deleted, decrement the host counter, and enqueue durable CVD teardown
	// in one commit. Idempotent: a repeat delete transitions nothing, so it can
	// neither double-decrement the counter nor enqueue a second cleanup.
	var extra []*spanner.Mutation
	if mut := s.cvdCleanupMutation(ctx, d); mut != nil {
		extra = append(extra, mut)
	}
	// The transaction also releases the host's protected count when the device
	// was pinned, so no separate best-effort adjustment is needed.
	transitioned, err := s.deviceRepo.MarkDeletedWithHostDecrement(ctx, d.HostID, d.ID, extra...)
	if err != nil {
		return fmt.Errorf("failed to mark device as deleted: %w", err)
	}
	if transitioned && s.taskEnqueuer != nil {
		s.taskEnqueuer.Wake()
	}

	// A repeat delete is an idempotent no-op; only the transition is auditable.
	if transitioned {
		audit.LogChange(ctx, s.auditLogger, audit.ActionDeviceDelete, audit.ResourceDevice, d.ID, map[string]any{
			colHostID: d.HostID,
			colName:   d.Name,
		})
	}

	return nil
}

// BatchDelete deletes multiple devices efficiently with batched database operations.
// The status+counter updates are batched into one transaction; CVD teardown on
// the hosts is queued per device (durable, retried) rather than called inline.
func (s *DeviceService) BatchDelete(ctx context.Context, devices []*deviceDomain.Device) (int, error) {
	if len(devices) == 0 {
		return 0, nil
	}

	// Atomically mark deleted, decrement host counters, and enqueue durable CVD
	// teardown per device in one transaction. The step no-ops on already-deleted
	// or cascade-removed devices, dropping their cleanup enqueues with it.
	cleanups := make(map[string]*spanner.Mutation, len(devices))
	for _, d := range devices {
		if mut := s.cvdCleanupMutation(ctx, d); mut != nil {
			cleanups[d.ID] = mut
		}
	}
	// The transaction also releases each host's protected count for pinned
	// devices, so no separate best-effort adjustment is needed.
	transitioned, err := s.deviceRepo.BatchMarkDeletedWithHostDecrement(ctx, devices, cleanups)
	if err != nil {
		s.logger.Error("failed to batch mark devices as deleted",
			slog.Int("count", len(devices)), slog.Any("error", err))
		return 0, fmt.Errorf("batch deleting devices: %w", err)
	}
	if len(transitioned) > 0 && s.taskEnqueuer != nil {
		s.taskEnqueuer.Wake()
	}

	for _, d := range transitioned {
		audit.LogChange(ctx, s.auditLogger, audit.ActionDeviceDelete, audit.ResourceDevice, d.ID, map[string]any{
			colHostID: d.HostID,
			colName:   d.Name,
			"batch":   true,
		})
	}

	return len(transitioned), nil
}

// Start starts a stopped device
func (s *DeviceService) Start(ctx context.Context, id string) error {
	d, err := liveDevice(ctx, s.deviceRepo, id)
	if err != nil {
		return err
	}

	if d.Status != deviceDomain.StatusStopped {
		return fmt.Errorf("can only start stopped devices, current status %s: %w", d.Status, deviceDomain.ErrInvalidState)
	}

	h, err := s.hostRepo.Get(ctx, d.HostID)
	if err != nil {
		return fmt.Errorf("failed to get host: %w", err)
	}

	hoURL := h.HostOrchestratorURL()
	if hoURL == "" {
		return fmt.Errorf("host %s has no IP address", h.ID)
	}

	group, name := deviceDomain.ParseCVDID(d.CVDID)
	if group == "" || name == "" {
		return fmt.Errorf("device %s has malformed CVD ID %q", d.ID, d.CVDID)
	}

	client := s.hoClientFactory.ClientForHost(hoURL)
	// The group verb, for the same reason Stop below uses one. Starting a device
	// starts all of it. The per-instance route would start the first member and
	// leave its siblings stopped behind a row that says running.
	if err := client.StartCVDGroup(ctx, group); err != nil {
		// The row is not written here. After a failed start the device is still
		// stopped and the row already says so, so marking it `error` would
		// replace accurate state with inaccurate state, and a wait that
		// expired on its budget leaves the host booting regardless. A truly
		// broken CVD is still marked `error` by the device controller one cycle
		// later, from an observed CVD state rather than from a failed call.
		//
		// audit.LogChange fires only on the success path below, so this warn is
		// the sole durable trace of a failed start.
		s.logger.Warn("start CVD failed; leaving device status unchanged",
			slog.String("device_id", d.ID),
			slog.String("cvd_id", d.CVDID),
			slog.String(colHostID, d.HostID),
			slog.Any("error", err))
		return fmt.Errorf("failed to start CVD: %w", err)
	}

	// The CVD is up; commit the transition against the current row so a
	// concurrent delete cannot be overwritten (deleted -> running is rejected
	// by the state machine).
	if _, err := s.deviceRepo.Mutate(ctx, id, func(d *deviceDomain.Device) error {
		if err := d.TransitionTo(deviceDomain.StatusRunning); err != nil {
			return err
		}
		now := time.Now()
		d.StartedAt = &now
		d.StoppedAt = nil
		return nil
	}); err != nil {
		return fmt.Errorf("recording device start: %w", err)
	}
	audit.LogChange(ctx, s.auditLogger, audit.ActionDeviceStart, audit.ResourceDevice, d.ID, nil)
	return nil
}

// Stop stops a running device
func (s *DeviceService) Stop(ctx context.Context, id string) error {
	d, err := liveDevice(ctx, s.deviceRepo, id)
	if err != nil {
		return err
	}

	if d.Status != deviceDomain.StatusRunning {
		return fmt.Errorf("can only stop running devices, current status %s: %w", d.Status, deviceDomain.ErrInvalidState)
	}

	h, err := s.hostRepo.Get(ctx, d.HostID)
	if err != nil {
		return fmt.Errorf("failed to get host: %w", err)
	}

	hoURL := h.HostOrchestratorURL()
	if hoURL == "" {
		return fmt.Errorf("host %s has no IP address", h.ID)
	}

	group, name := deviceDomain.ParseCVDID(d.CVDID)
	if group == "" || name == "" {
		return fmt.Errorf("device %s has malformed CVD ID %q", d.ID, d.CVDID)
	}

	client := s.hoClientFactory.ClientForHost(hoURL)
	// The group verb. Stopping a device stops all of it. The per-instance route
	// would stop the first member and leave the rest running behind a row that
	// says stopped, still billed and still holding their taps.
	if err := client.StopCVDGroup(ctx, group); err != nil {
		// Not written as `error`, and here that matters more than for Start.
		// error -> stopped is not a legal transition, so applyDerivedStatus
		// could never correct it and would swallow the attempt silently. The
		// device would be wrong forever. Leaving the row at `running` keeps the
		// device controller able to observe the CVD and settle it either way.
		s.logger.Warn("stop CVD failed; leaving device status unchanged",
			slog.String("device_id", d.ID),
			slog.String("cvd_id", d.CVDID),
			slog.String(colHostID, d.HostID),
			slog.Any("error", err))
		return fmt.Errorf("failed to stop CVD: %w", err)
	}

	if _, err := s.deviceRepo.Mutate(ctx, id, func(d *deviceDomain.Device) error {
		if err := d.TransitionTo(deviceDomain.StatusStopped); err != nil {
			return err
		}
		now := time.Now()
		d.StoppedAt = &now
		return nil
	}); err != nil {
		return fmt.Errorf("recording device stop: %w", err)
	}
	audit.LogChange(ctx, s.auditLogger, audit.ActionDeviceStop, audit.ResourceDevice, d.ID, nil)
	return nil
}

// Restart relaunches a running device's CVD in place, so a hung launcher
// revives without re-fetching artifacts. The device stays running; the device
// controller's CVD-state derivation absorbs the transient reboot.
//
// It is ASYNCHRONOUS and returns an operation handle rather than waiting. The
// device row is identical before and after a restart, so it cannot carry either
// progress or outcome. That is what the handle is for, and why this verb had no
// completion or failure signal at all before.
// It addresses ONE instance, because upstream serves no group-level :restart,
// so a relaunch is inherently per-Android-VM.
func (s *DeviceService) Restart(ctx context.Context, id, instance string) (*operation.Record, error) {
	return s.admitVerb(ctx, id, instance, operation.VerbDeviceRestart, "restart")
}

// Powerwash resets a running device to first-boot state. Userdata is wiped,
// the build stays, and nothing is re-fetched. It is the clean-state-between-
// tests primitive.
//
// It is ASYNCHRONOUS for the same reason as Restart, and with more at stake. It
// destroys user data, so "did it happen?" needs a durable answer.
func (s *DeviceService) Powerwash(ctx context.Context, id, instance string) (*operation.Record, error) {
	return s.admitVerb(ctx, id, instance, operation.VerbDevicePowerwash, "powerwash")
}

// admitVerb is the request-path half of an async verb: validate, then admit.
//
// The guards here exist to produce good 4xx messages, never to establish
// invariants. A device can be deleted between this check and the claim, so the
// handler re-derives and re-checks everything. Admission itself is one
// transaction in the repository: the exclusivity read, the operation insert and
// the task enqueue commit together, so a handle always names real queued work.
// The instance is recorded on the operation row, which is what lets the device
// controller exempt exactly the Android VM this verb is passing through and keep
// judging its siblings. Exclusivity stays per DEVICE, so a second per-instance
// verb is refused while this one is live even on a different instance.
func (s *DeviceService) admitVerb(ctx context.Context, id, instance string, verb operation.Verb, label string) (*operation.Record, error) {
	// Validate the request BEFORE checking that the feature is wired. A 4xx about
	// the request is true either way and is what the caller can act on, so
	// reporting a misconfiguration in its place would hide the real problem. The
	// availability error then only reaches an otherwise-valid request, which is
	// exactly when it is the reason.
	target, err := s.resolveRunningInstance(ctx, id, instance, label)
	if err != nil {
		return nil, err
	}
	if s.operations == nil {
		return nil, fmt.Errorf("%s is unavailable: operations are not configured", label)
	}

	actor := ""
	if p, ok := auth.PrincipalFromContext(ctx); ok {
		actor = p.Email
	}
	rec, err := s.operations.Admit(ctx, spannerplatform.AdmitRequest{
		Verb:         verb,
		ResourceID:   id,
		InstanceName: target.name,
		CreatedBy:    actor,
	})
	if err != nil {
		return nil, fmt.Errorf("admitting %s on device %s: %w", label, id, err)
	}
	s.logger.InfoContext(ctx, "admitted device verb",
		slog.String("device_id", id),
		slog.String("instance", target.name),
		slog.String("verb", string(verb)),
		slog.String("operation_id", rec.ID))
	return rec, nil
}

// Powerbtn presses one instance's power button.
func (s *DeviceService) Powerbtn(ctx context.Context, id, instance string) error {
	return s.runningInstanceOp(ctx, id, instance, "press power button on", audit.ActionDevicePowerbtn, hoclient.Client.PowerbtnCVD)
}

// StartRecording starts a screen recording on one running instance.
func (s *DeviceService) StartRecording(ctx context.Context, id, instance string) error {
	return s.runningInstanceOp(ctx, id, instance, "start recording on", audit.ActionDeviceRecordStart, hoclient.Client.StartScreenRecording)
}

// StopRecording stops the screen recording on one running instance.
func (s *DeviceService) StopRecording(ctx context.Context, id, instance string) error {
	return s.runningInstanceOp(ctx, id, instance, "stop recording on", audit.ActionDeviceRecordStop, hoclient.Client.StopScreenRecording)
}

// runningInstanceOp runs a Host Orchestrator instance verb against one instance
// of a running device. Unlike Start/Stop it changes no row state. The device
// remains running and the device controller observes any transient CVD states
// the verb causes. verb names the action for errors; the audit entry records
// who asked, and which instance they asked about.
func (s *DeviceService) runningInstanceOp(ctx context.Context, id, instance, verb string, action audit.Action, call func(hoclient.Client, context.Context, string, string) error) error {
	target, err := s.resolveRunningInstance(ctx, id, instance, verb)
	if err != nil {
		return err
	}
	if err := call(s.hoClientFactory.ClientForHost(target.hoURL), ctx, target.group, target.name); err != nil {
		return fmt.Errorf("failed to %s CVD: %w", verb, err)
	}
	audit.LogChange(ctx, s.auditLogger, action, audit.ResourceDevice, id, map[string]any{"instance": target.name})
	return nil
}

// CVD creation no longer runs in service goroutines, so the service has no
// background work to drain at shutdown; the task worker pool owns that lifecycle.

// Pin marks a device as pinned, or re-pins a pinned one: a re-pin replaces
// reason, detail and pinned_until and keeps pinned_at. The lifecycle change
// and the host's protected-count adjustment commit in one transaction (via
// Mutate), and the count moves only when the pin state flips, so neither
// concurrent pins nor a re-pin can double-protect a host.
func (s *DeviceService) Pin(ctx context.Context, deviceID string, req deviceDomain.PinRequest, userID string) (*deviceDomain.Device, error) {
	d, err := s.deviceRepo.Mutate(ctx, deviceID, func(d *deviceDomain.Device) error {
		d.Lifecycle.Pin(userID, req.Reason, req.ReasonDetail, req.PinnedUntil)
		return nil
	})
	if err != nil {
		return nil, err
	}

	if s.auditLogger != nil {
		// userID carries the principal email (single-tenant identity model),
		// so it belongs in actor_email like every other entry.
		entry := audit.New(audit.ActionDevicePin, audit.ResourceDevice).
			WithActor(userID).
			WithResource(d.ID).
			WithMetadata(map[string]any{"reason": string(req.Reason)}).
			Build()
		s.auditLogger.LogAsync(entry)
	}

	return d, nil
}

// Unpin removes the pinned status from a device; see Pin for the atomicity
// contract.
func (s *DeviceService) Unpin(ctx context.Context, deviceID, userID string) (*deviceDomain.Device, error) {
	return s.unpinIf(ctx, deviceID, userID, nil)
}

// UnpinLapsed is the reaper's Unpin. Its transaction refuses, with
// deviceDomain.ErrInvalidState, unless pinned_until is set and already past:
// a re-pin that commits between the reaper's listing and this call must not be
// undone, and the reaper runs on every instance.
func (s *DeviceService) UnpinLapsed(ctx context.Context, deviceID, userID string) (*deviceDomain.Device, error) {
	return s.unpinIf(ctx, deviceID, userID, func(d *deviceDomain.Device) error {
		if until := d.Lifecycle.PinnedUntil; until == nil || !until.Before(time.Now()) {
			return fmt.Errorf("device pin has not lapsed: %w", deviceDomain.ErrInvalidState)
		}
		return nil
	})
}

// unpinIf is Unpin with guard, when set, checked against the row inside the
// unpin's transaction.
func (s *DeviceService) unpinIf(ctx context.Context, deviceID, userID string, guard func(*deviceDomain.Device) error) (*deviceDomain.Device, error) {
	d, err := s.deviceRepo.Mutate(ctx, deviceID, func(d *deviceDomain.Device) error {
		if !d.Lifecycle.IsPinned() {
			return fmt.Errorf("device not pinned: %w", deviceDomain.ErrInvalidState)
		}
		if guard != nil {
			if err := guard(d); err != nil {
				return err
			}
		}
		d.Lifecycle.Unpin()
		return nil
	})
	if err != nil {
		return nil, err
	}

	if s.auditLogger != nil {
		entry := audit.New(audit.ActionDeviceUnpin, audit.ResourceDevice).
			WithActor(userID).
			WithResource(d.ID).
			Build()
		s.auditLogger.LogAsync(entry)
	}

	return d, nil
}

// Extend extends the TTL of an ephemeral device. The grant when the request
// names no amount and the allowance are operator policy, resolved before the
// transactional mutate.
func (s *DeviceService) Extend(ctx context.Context, deviceID string, req deviceDomain.ExtendRequest) (*deviceDomain.Device, error) {
	policy, err := s.policyLifetimes(ctx)
	if err != nil {
		return nil, err
	}
	seconds := req.Seconds
	if seconds <= 0 {
		seconds = policy.ExtendHours * 3600
	}
	d, err := s.deviceRepo.Mutate(ctx, deviceID, func(d *deviceDomain.Device) error {
		if err := d.Lifecycle.Extend(seconds, int(policy.ExtendCount)); err != nil {
			return fmt.Errorf("%w: %w", deviceDomain.ErrInvalidState, err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("extending device TTL: %w", err)
	}
	audit.LogChange(ctx, s.auditLogger, audit.ActionDeviceExtend, audit.ResourceDevice, d.ID, map[string]any{"seconds": seconds})
	return d, nil
}

// GetLifecycle returns the lifecycle state of a device
func (s *DeviceService) GetLifecycle(ctx context.Context, deviceID string) (*deviceDomain.DeviceLifecycle, error) {
	d, err := s.deviceRepo.Get(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	return &d.Lifecycle, nil
}

// cvdCleanupMutation builds the enqueue for a device's durable CVD teardown,
// folded into the delete transaction so a deleted device can never leak its
// CVD on the host. It does NOT gate on CVDID: that is recorded only on full
// creation success, so an interrupted create can leave a group (and its
// multi-GB artifacts) on the host with no CVDID to show for it, and the cleanup
// task resolves the group by its deterministic name instead. Gating on the
// pre-delete status would race the create task's pending→creating transition,
// so a device that never reached the host just pays one no-op probe.
func (s *DeviceService) cvdCleanupMutation(ctx context.Context, d *deviceDomain.Device) *spanner.Mutation {
	if s.taskEnqueuer == nil {
		return nil
	}
	return s.taskEnqueuer.EnqueueMutation(ctx, task.New(TaskTypeCleanupCVD, d.ID))
}

// SnapshotStore is the snapshot inventory the service reads and settles. It is
// the platform's record of what exists on which host disk, since the Host
// Orchestrator keeps none. Named as a set so the test fake implements one
// contract rather than six stray methods.
type SnapshotStore interface {
	// SnapshotInsert returns the insert mutation for folding into the take
	// verb's admission transaction.
	SnapshotInsert(s *deviceDomain.Snapshot) (*spanner.Mutation, error)
	GetSnapshot(ctx context.Context, deviceID, snapshotID string) (*deviceDomain.Snapshot, error)
	ListSnapshotsByDevice(ctx context.Context, deviceID string) ([]*deviceDomain.Snapshot, error)
	// CountLiveSnapshotsByDevice counts creating+ready rows, the occupancy
	// the per-device cap gates on.
	CountLiveSnapshotsByDevice(ctx context.Context, deviceID string) (int64, error)
	MarkSnapshotReady(ctx context.Context, hostID, snapshotID string) error
	DeleteSnapshotRow(ctx context.Context, hostID, snapshotID string) error
}

// AtomicDeviceRepository extends deviceDomain.Repository with atomic operations.
type AtomicDeviceRepository interface {
	deviceDomain.Repository
	SnapshotStore
	// Mutate applies fn to the device inside a read-write transaction and
	// writes the result back atomically; a pinned-state change adjusts the
	// host's protected count in the same commit.
	Mutate(ctx context.Context, id string, fn func(*deviceDomain.Device) error) (*deviceDomain.Device, error)
	// CreateWithHostIncrement atomically creates a device, increments the host
	// counter, and commits any extra mutations (e.g. the CVD-creation enqueue).
	CreateWithHostIncrement(ctx context.Context, d *deviceDomain.Device, extra ...*spanner.Mutation) error
	// MarkDeletedWithHostDecrement atomically marks a device deleted and decrements
	// its host counter, idempotently (a repeat delete does not double-decrement).
	// Extra mutations commit only with the transition. It reports whether this
	// call performed the transition.
	MarkDeletedWithHostDecrement(ctx context.Context, hostID, deviceID string, extra ...*spanner.Mutation) (bool, error)
	// BatchMarkDeletedWithHostDecrement does the same for many devices in one
	// transaction, returning the devices it actually transitioned. Per-device
	// extra mutations (keyed by device ID) commit only with their transition.
	BatchMarkDeletedWithHostDecrement(ctx context.Context, devices []*deviceDomain.Device, extraFor map[string]*spanner.Mutation) ([]*deviceDomain.Device, error)
	// ListRecentBuildIDs returns the distinct registry build ids among device
	// creates since the cutoff, the warm plane's prefetch-plan input.
	ListRecentBuildIDs(ctx context.Context, since time.Time) ([]string, error)
}
