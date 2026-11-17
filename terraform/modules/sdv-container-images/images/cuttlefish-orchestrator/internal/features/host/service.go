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
	"fmt"
	"log/slog"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"cloud.google.com/go/spanner"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/idempotency"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/gce"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// Queue task types for asynchronous host lifecycle work (canonical names in
// domain/task, where cross-feature machinery can reach them).
const (
	TaskTypeProvisionHost   = task.TypeHostProvision
	TaskTypeDeprovisionHost = task.TypeHostDeprovision
	TaskTypeRemediateHost   = task.TypeHostRemediate
	TaskTypeStopSettleHost  = task.TypeHostStopSettle
)

// Log/audit attribute keys repeated across host operations (goconst).
const (
	attrName        = "name"
	attrZone        = "zone"
	attrMachineType = "machine_type"
)

// The host-create entry's fleet facts. The fleet autoscaler's demand signal
// reads host-create AUDIT ENTRIES rather than host rows, because rows
// hard-delete and a row-based count goes blind exactly when reclamation is
// working. Nothing else durably records which path provisioned a host, what
// ceiling it took, or what a standby host would have offered instead, so an
// entry missing them is a hole in the signal that no later query can fill.
//
// The names are the JSON path the audit_logs generated columns read; renaming
// one here silently empties its column, which no test outside the emulator can
// see.
const (
	attrOrigin          = "origin"
	attrMaxDevices      = "max_devices"
	attrDensity         = "density"
	attrGPURequired     = "gpu_required"
	attrFitsDefaultHost = "fits_default_host"
	// attrNewInstance names the VM a re-homed host will come up as: the shape
	// or zone move renames the instance, so the entry has to carry the name or
	// nothing links it to what GCE actually created.
	attrNewInstance = "new_instance"
	// attrPreferredType is the shape this create wanted and could not have.
	// Absent on every create that got its preferred shape, which is what lets
	// a reader count fallbacks rather than infer them from a machine type that
	// looks perfectly ordinary on its own.
	attrPreferredType = "preferred_type"
	// attrPool is the pool this create was judged into, stamped because the row
	// hard-deletes and the demand signal has to attribute a miss to the pool
	// whose bounds could have absorbed it. Absent on the default pool, matching
	// the NULL the host row stores there: the entries that predate the stamp
	// read the same way, and the signal already takes an unnamed pool as the
	// default one.
	attrPool = "pool"
)

// IdempotencyStore records and resolves Idempotency-Key -> host id mappings so a
// retried create returns the original host. The Spanner IdempotencyRepository
// satisfies it.
type IdempotencyStore interface {
	idempotency.Store
	NewKeyMutation(key, resourceID, fingerprint string) *spanner.Mutation
}

// HostServiceConfig holds configuration for the host service.
type HostServiceConfig struct {
	ProjectID            string
	Network              string
	Subnetwork           string
	ServiceAccount       string
	NetworkTags          []string
	Labels               map[string]string
	UseExternalIP        bool
	HostOrchestratorPort int64

	// MaxConcurrentGCEOps caps concurrent GCE instance provisions per instance,
	// protecting the GCE operation quota during host-create bursts. 0 disables.
	MaxConcurrentGCEOps int
}

// Machine-shape defaults for a service with no machine catalog wired (tests
// and harnesses; production always wires one). There is no image default: a
// machine family with no enabled host image config is not provisionable.
const (
	fallbackMachineType = "n1-standard-8"
	fallbackGPUType     = "nvidia-tesla-t4"
)

// hostMaxRunDurationSeconds is the VM-side stop bound every host carries: 120
// days, the most GCE accepts. It exists for the case where nothing is left to
// reap; see the failsafe note in buildInstance. Host leases have no cap, and
// GCE changes max_run_duration only on a stopped VM, so the bound cannot follow
// a lease as it grows. It starts as far out as GCE allows instead, and the
// lease paths refuse to pass it (host.CheckVMHorizon against the observed
// vm_terminates_at). The clock restarts on every VM start.
const hostMaxRunDurationSeconds int64 = 120 * 24 * 3600

// HostServiceOption configures a HostService.
type HostServiceOption func(*HostService)

// WithHostAuditLogger configures an audit sink for the HostService.
func WithHostAuditLogger(al audit.Sink) HostServiceOption {
	return func(s *HostService) {
		s.auditLogger = al
	}
}

// WithTaskEnqueuer wires the queue used to enqueue host-provisioning tasks. It
// is required: without it Create and Retry cannot durably schedule provisioning.
func WithTaskEnqueuer(e TaskEnqueuer) HostServiceOption {
	return func(s *HostService) {
		s.taskEnqueuer = e
	}
}

// WithHostIdempotencyStore enables Idempotency-Key handling on Create. When
// unset, any Idempotency-Key is ignored.
func WithHostIdempotencyStore(store IdempotencyStore) HostServiceOption {
	return func(s *HostService) {
		s.idempotencyStore = store
	}
}

// AtomicHostRepository is the host repository plus the transactional operations
// the service composes: quota-atomic create/delete and the transactional
// read-modify-write mutator. The Spanner *HostRepository satisfies it.
type AtomicHostRepository interface {
	host.Repository
	CreateWithQuotaIncrement(ctx context.Context, h *host.Host, extra ...*spanner.Mutation) error
	// Mutate applies fn to the host inside a read-write transaction; extra
	// mutations (e.g. a task enqueue) commit only when fn succeeds.
	Mutate(ctx context.Context, id string, fn func(*host.Host) error, extra ...*spanner.Mutation) (*host.Host, error)
	DeleteWithQuotaDecrement(ctx context.Context, id string) error
	// SnapshotsLostMutations returns the updates that mark every live
	// snapshot row on the host lost, for folding into the commit that dooms
	// the host's disk (the re-provision incarnation bump).
	SnapshotsLostMutations(ctx context.Context, hostID string) ([]*spanner.Mutation, error)
	// ListRescuable returns the draining fleet hosts a lease renewal could
	// return to standby inventory, warmest first, at most perPool of them in
	// any one pool.
	ListRescuable(ctx context.Context, lead time.Duration, perPool int) ([]*host.Host, error)
	// ExtendLease raises one host's lease to target under a transaction that
	// re-checks the row, committing extra with it. Reports whether it wrote.
	ExtendLease(ctx context.Context, id string, target time.Time, extra ...*spanner.Mutation) (bool, error)
	// ListShortenable returns the standing fleet inventory a shorten could
	// reclaim, coldest first, at most perPool of them in any one pool.
	ListShortenable(ctx context.Context, lead time.Duration, perPool int) ([]*host.Host, error)
	// ListCollapsible returns the empty dedicated hosts a collapse could
	// reclaim, longest remaining lease first.
	ListCollapsible(ctx context.Context, lead time.Duration, limit int) ([]*host.Host, error)
	// ShortenLease lowers one host's lease to target under a transaction that
	// refuses while the host carries a live device, committing extra with it.
	// Reports whether it wrote.
	ShortenLease(ctx context.Context, id string, target time.Time, extra ...*spanner.Mutation) (bool, error)
	// BatchUpdateGCEState is the reconciler's CAS observation write. The stop
	// settle and Sync record a stop through it so every adopted stop writes
	// the same rows.
	BatchUpdateGCEState(ctx context.Context, updates []host.GCEStateUpdate) (int64, error)
}

// TaskEnqueuer builds the mutation that enqueues a task, so the host service can
// fold provisioning work into the host's create/update transaction.
//
// A folded enqueue never passes through the queue's own commit, so nothing
// tells this process's worker pool that work arrived; Wake does, and is called
// once the transaction carrying the mutation reports success. It only moves
// latency, because an unmade wake is found by the next poll.
type TaskEnqueuer interface {
	EnqueueMutation(ctx context.Context, t *task.Task) *spanner.Mutation
	Wake()
}

// StockoutReprober files a fresh capacity-advice probe for the one cell a
// stockout has just proved empty. The Spanner task queue satisfies it; nil
// disables the filing, which is what a deployment with the advice sync switched
// off wants, since a task nothing handles would postpone until it dead-letters.
//
// Its own port rather than TaskEnqueuer's third method, because this enqueue is
// NOT folded into the failure's transaction. The cell it refreshes
// is advisory, so a probe lost to a crash costs one sync interval of staleness,
// where making the failure write depend on it would put a second table in the
// path of recording the failure at all.
type StockoutReprober interface {
	Enqueue(ctx context.Context, t *task.Task) error
}

// WithStockoutReprober wires the stockout-triggered advice re-probe.
func WithStockoutReprober(r StockoutReprober) HostServiceOption {
	return func(s *HostService) {
		s.stockoutReprober = r
	}
}

// AuditMutator builds the mutation that writes one audit entry. The Spanner
// AuditRepository satisfies it.
//
// Only the create path uses it, for a reason: every other host verb leaves
// a row behind that says what happened, so a dropped entry there costs history.
// A create's entry is different, because the host row hard-deletes, so once the
// host is gone the entry is the only evidence the create ever happened, and it is
// what the fleet autoscaler's demand signal counts. The buffered sink drops
// entries under load, which is the load that signal exists to measure.
type AuditMutator interface {
	EntryMutation(entry audit.Entry) (*spanner.Mutation, error)
}

// WithHostAuditMutator makes the create's audit entry commit in the create's
// own transaction. Nil leaves the create unaudited rather than falling back to
// the buffered sink: a fallback would be indistinguishable from the wired path
// on every happy-path test while silently restoring the drop this exists to
// remove.
func WithHostAuditMutator(m AuditMutator) HostServiceOption {
	return func(s *HostService) {
		s.auditMutator = m
	}
}

// WithMachineTypeCatalog configures machine-type shape resolution for the
// HostService.
func WithMachineTypeCatalog(catalog machinetype.Catalog) HostServiceOption {
	return func(s *HostService) {
		s.machineTypes = catalog
	}
}

// WithCatalogValidator configures GCE catalog validation for the HostService.
func WithCatalogValidator(v gcecatalog.Validator) HostServiceOption {
	return func(s *HostService) {
		s.catalogValidator = v
	}
}

// WithZoneHealth wires the placement plane's reactive memory: classified
// provisioning failures cool their (zone, family) cell and successful
// provisions clear it.
func WithZoneHealth(store gcecatalog.ZoneHealthStore) HostServiceOption {
	return func(s *HostService) {
		s.zoneHealth = store
	}
}

// ImageResolver answers which concrete image boots a machine family here, or
// an *hostconfig.ImageUnavailableError saying why none does. The hostconfig
// slice's service satisfies it; internal/app wires it in.
type ImageResolver interface {
	ResolveImage(ctx context.Context, machineFamily string, arch machinetype.Architecture) (*hostconfig.Image, error)
}

// WithImageResolver wires the host image resolution the create preflight and
// the provision task read. Without it a create is not preflighted and a
// provision fails, since there is no image to boot.
func WithImageResolver(r ImageResolver) HostServiceOption {
	return func(s *HostService) {
		s.images = r
	}
}

// WithHostOrchestratorFactory supplies the Host Orchestrator client factory used
// by host-poison remediation to `cvd reset` a degraded host in place.
func WithHostOrchestratorFactory(f *hoclient.Factory) HostServiceOption {
	return func(s *HostService) {
		s.hoFactory = f
	}
}

// WithLiveDeviceCounter supplies the counter that gates the destructive reset in
// RunRemediateHost: a host is reset only when it carries no live-device tenants.
func WithLiveDeviceCounter(fn func(ctx context.Context, hostID string) (int64, error)) HostServiceOption {
	return func(s *HostService) {
		s.liveDeviceCounter = fn
	}
}

// WithLiveInstanceCounter supplies the counter that gates lowering max_devices:
// how many instance slots the host's live devices desire. Separate from
// WithLiveDeviceCounter because max_devices bounds instances, not device rows.
func WithLiveInstanceCounter(fn func(ctx context.Context, hostID string) (int64, error)) HostServiceOption {
	return func(s *HostService) {
		s.liveInstanceCounter = fn
	}
}

// HostService implements the business logic for the host vertical slice.
//
// It orchestrates the provisioning of GCE instances, manages quota consumption,
// and maintains the consistency between the Spanner registry and actual cloud resources.
type HostService struct {
	config          HostServiceConfig
	instancesClient *compute.InstancesClient
	// operationsClient waits on the zone operations the instances client
	// starts (insert, delete) via the server-side blocking wait RPC.
	operationsClient *compute.ZoneOperationsClient
	hostRepo         AtomicHostRepository
	machineTypes     machinetype.Catalog
	catalogValidator gcecatalog.Validator
	zoneHealth       gcecatalog.ZoneHealthStore
	stockoutReprober StockoutReprober
	taskEnqueuer     TaskEnqueuer
	idempotencyStore IdempotencyStore
	auditLogger      audit.Sink
	auditMutator     AuditMutator
	images           ImageResolver
	logger           *slog.Logger
	gceSem           chan struct{} // Caps concurrent GCE provisions; nil disables.

	// hoFactory builds Host Orchestrator clients for host-poison remediation
	// (`cvd reset`); nil disables the reset leg.
	hoFactory *hoclient.Factory

	// liveDeviceCounter reports how many devices on a host still back (or are
	// acquiring) a CVD, the tenants a host-wide reset would destroy. It gates
	// the destructive reset in RunRemediateHost; nil disables the reset leg
	// (a host is never reset without this check).
	liveDeviceCounter func(ctx context.Context, hostID string) (int64, error)

	// liveInstanceCounter reports how many instance slots those same live
	// devices desire. max_devices and current_device_count are both
	// instance-denominated, so a capacity gate reads this and never
	// liveDeviceCounter.
	liveInstanceCounter func(ctx context.Context, hostID string) (int64, error)

	// systemConfig resolves the operator policy this service acts on: the
	// default host lease a create takes, and the shape ladder a stocked-out
	// provision walks. Nil reads as the compiled defaults, which for the
	// ladder means none.
	systemConfig ConfigReader
}

// ConfigReader reads system_config keys. Satisfied by the platform
// SystemConfigRepository.
type ConfigReader interface {
	Get(ctx context.Context, key string) (*models.SystemConfig, error)
}

// WithSystemConfig wires the system-config reader this service resolves
// operator policy from. Nil reads as the compiled defaults.
func WithSystemConfig(policy ConfigReader) HostServiceOption {
	return func(s *HostService) {
		s.systemConfig = policy
	}
}

// policyHostTTLHours resolves the lease a host takes when the create names
// none. A nil reader and a missing, malformed or out-of-range key read as the
// compiled default, the same clamp the settings surface applies on read.
func (s *HostService) policyHostTTLHours(ctx context.Context) (int64, error) {
	fallback := host.DefaultHostTTLSeconds / 3600
	if s.systemConfig == nil {
		return fallback, nil
	}
	cfg, err := s.systemConfig.Get(ctx, models.ConfigKeyHostTTLHours)
	if err != nil {
		if errors.Is(err, spannerplatform.ErrKeyNotFound) {
			return fallback, nil
		}
		return 0, fmt.Errorf("reading lifetime policy %q: %w", models.ConfigKeyHostTTLHours, err)
	}
	hours, _ := hostconfig.PolicyInt64(cfg.AsString(), 1, host.MaxHostTTLHours, fallback)
	return hours, nil
}

// NewHostService creates a new host service.
func NewHostService(
	config HostServiceConfig,
	instancesClient *compute.InstancesClient,
	operationsClient *compute.ZoneOperationsClient,
	hostRepo AtomicHostRepository,
	logger *slog.Logger,
	opts ...HostServiceOption,
) *HostService {
	s := &HostService{
		config:           config,
		instancesClient:  instancesClient,
		operationsClient: operationsClient,
		hostRepo:         hostRepo,
		logger:           logger,
	}
	if config.MaxConcurrentGCEOps > 0 {
		s.gceSem = make(chan struct{}, config.MaxConcurrentGCEOps)
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Get retrieves a host by ID.
func (s *HostService) Get(ctx context.Context, id string) (*host.Host, error) {
	return s.hostRepo.Get(ctx, id)
}

// List retrieves hosts with filtering.
func (s *HostService) List(ctx context.Context, filter host.ListFilter) ([]*host.Host, error) {
	return s.hostRepo.List(ctx, filter)
}

// Count returns the number of hosts matching the filter, ignoring pagination.
func (s *HostService) Count(ctx context.Context, filter host.ListFilter) (int64, error) {
	count, err := s.hostRepo.Count(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("counting hosts: %w", err)
	}
	return count, nil
}

// Create creates a new host and provisions the GCE instance. Placement
// intent: an empty req.Zone means the service ranks the zone here (biased by
// req.LocationHint); a pinned zone is used verbatim and never failed over; a
// pre-ranked unpinned zone (batch templates) binds without re-ranking but
// stays failover-eligible.
//
// origin is a parameter and not a CreateRequest field on purpose. The
// declarative auto-provision and the fleet top-up both take their request from
// the same placement resolver, which cannot tell its two callers apart, so a
// field would have to be overwritten after resolution, a stamp that can be
// silently forgotten. As a parameter, a create site that does not choose one
// does not compile, which is the only check that reaches a site nobody has
// written yet.
func (s *HostService) Create(ctx context.Context, req host.CreateRequest, createdBy string, origin host.Origin) (*host.Host, error) {
	if err := validateCreateRequest(req); err != nil {
		return nil, err
	}

	req, err := s.resolvePlacement(ctx, req)
	if err != nil {
		return nil, err
	}

	if err := s.precheckCatalog(ctx, req); err != nil {
		return nil, err
	}

	if err := s.precheckImage(ctx, req); err != nil {
		return nil, err
	}

	h, err := s.buildHost(ctx, req, createdBy, origin)
	if err != nil {
		return nil, err
	}

	// Idempotency: a retried request with the same key returns the original host.
	// The key row is inserted in the same transaction as the host, so a concurrent
	// duplicate either reads the existing key here or loses the commit race below.
	extra, existing, err := s.idempotencyMutation(ctx, req, h)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	// Enqueue the provisioning task and create the host + quota increment in one
	// transaction: a created host always has durable provisioning work queued, and
	// quota usage can never drift from the host count. The task worker pool runs
	// provisionGCEInstance, so it survives instance restarts and retries on
	// transient GCE failures.
	if s.taskEnqueuer == nil {
		return nil, errors.New("host service has no task enqueuer configured")
	}
	extra = append(extra, s.taskEnqueuer.EnqueueMutation(ctx, task.New(TaskTypeProvisionHost, h.ID)))

	// The audit entry rides the same commit as the row it describes. A host row
	// hard-deletes, so this entry outlives the only other evidence the create
	// happened, and the fleet autoscaler counts these entries, so losing them
	// under load loses exactly the demand that mattered.
	if s.auditMutator != nil {
		auditMut, err := s.createAuditMutation(h, createdBy, req.Resolved)
		if err != nil {
			return nil, err
		}
		extra = append(extra, auditMut)
	}

	if err := s.hostRepo.CreateWithQuotaIncrement(ctx, h, extra...); err != nil {
		// A concurrent request with the same key may have won the commit race:
		// an equivalent request returns the winner's host (idempotent success);
		// a different request reusing the key is the same conflict the
		// sequential path reports, and never the wrong host.
		won, found, raceErr := s.lookupIdempotent(ctx, req)
		if raceErr != nil {
			return nil, raceErr
		}
		if found {
			return won, nil
		}
		// Returned unwrapped so *quota.ExceededError reaches the transport as a 429.
		return nil, err
	}
	s.taskEnqueuer.Wake()

	s.recordCreated(ctx, h)

	return h, nil
}

// validateCreateRequest rejects the request shapes placement must not spend a
// catalog round trip on. It runs before the zone is ranked.
func validateCreateRequest(req host.CreateRequest) error {
	// A name in the binding namespace is a caller bug that would otherwise
	// compose into "cf-cf-…".
	if err := host.ValidateDisplayName(req.Name); err != nil {
		return fmt.Errorf("validating host name: %w", err)
	}
	// The /v2 tags enforce the same bounds; this covers /v1, which decodes the
	// legacy wire shape without Huma validation.
	if err := host.ValidateBootDiskSizeGB(req.BootDiskSizeGB); err != nil {
		return fmt.Errorf("validating boot disk size: %w", err)
	}
	return nil
}

// precheckCatalog pre-validates the placed request against the GCE catalog
// when a validator is configured. Only a definite rejection stops the create;
// an unreachable catalog does not.
func (s *HostService) precheckCatalog(ctx context.Context, req host.CreateRequest) error {
	if s.catalogValidator == nil {
		return nil
	}
	validationReq := gcecatalog.ValidationRequest{
		Zone:              req.Zone,
		MachineType:       req.MachineType,
		GPUType:           req.GPUType,
		GPUCount:          req.GPUCount,
		ProvisioningModel: string(req.ProvisioningModel),
	}

	result, err := s.catalogValidator.Validate(ctx, validationReq)
	switch {
	case err != nil:
		// Log error but continue - fail_on_stale=false behavior.
		s.logger.Warn("catalog validation failed",
			slog.String(attrZone, req.Zone),
			slog.Any("error", err),
		)
	case !result.Valid:
		// Return validation error with structured details.
		return s.buildCatalogValidationError(result)
	default:
		// Log warnings but continue.
		for _, w := range result.Warnings {
			s.logger.Warn("catalog validation warning",
				slog.String("code", w.Code),
				slog.String("message", w.Message),
			)
		}
	}
	return nil
}

// precheckImage refuses a create whose machine family has no image it can
// boot, before any row or task is written. The refusal is the resolver's
// *hostconfig.ImageUnavailableError, returned unwrapped so it reaches the
// transport as a 422 naming the family, the project and the fix.
//
// Only a definite refusal stops the create. A machine shape the catalog cannot
// resolve, or an image lookup that failed without saying anything about the
// image, lets the create through: the provision task resolves both again and
// reports what it finds.
func (s *HostService) precheckImage(ctx context.Context, req host.CreateRequest) error {
	if s.images == nil {
		return nil
	}
	shape, err := s.resolveMachineType(ctx, req.MachineType, req.GPUType, req.GPUCount)
	if err != nil {
		s.logger.Warn("image precheck skipped: machine type unresolved",
			slog.String(attrMachineType, req.MachineType), slog.Any("error", err))
		return nil
	}
	_, err = s.images.ResolveImage(ctx, machinetype.FamilyOf(shape.machineType), shape.architecture)
	if _, refused := errors.AsType[*hostconfig.ImageUnavailableError](err); refused {
		return err //nolint:wrapcheck // The refusal names the family, project and fix, and is the 422 detail as it stands.
	}
	if err != nil {
		s.logger.Warn("image precheck skipped: lookup failed",
			slog.String(attrMachineType, shape.machineType), slog.Any("error", err))
	}
	return nil
}

// buildHost assembles the pending host row for a placed create request: the
// normalized provisioning model, the recorded placement intent, the lease the
// request or operator policy grants, and the accelerator an integrated machine
// type carries. req.Zone must already be resolved.
//
// Every create reaches its row through here (the /v2 and /v1 creates, the
// declarative miss, the keeper's top-up, a batch's host), so the pool is
// stamped once, here, and no path can write a host that belongs to none. A
// request naming none is the default pool, which is where a create nobody
// judged belongs and where every create landed before pools were selectable.
func (s *HostService) buildHost(ctx context.Context, req host.CreateRequest, createdBy string, origin host.Origin) (*host.Host, error) {
	hostID := uuid.New().String()
	gceInstanceName := host.InstanceName(req.Name, hostID, 0)
	// Validate and normalize provisioning model
	provisioningModel := req.ProvisioningModel
	if provisioningModel == "" {
		provisioningModel = host.ProvisioningModelStandard
	}
	if !provisioningModel.IsValid() {
		return nil, fmt.Errorf("invalid provisioning model: %s", provisioningModel)
	}

	// Persist the placement intent: a pinned zone is its own request token; a
	// ranked host records the hint (empty = auto). The provision task is
	// payload-less, so the row is the only carrier of this intent at failover
	// time, and /v1 listing partitions by it.
	requestedZone := req.LocationHint
	if req.ZonePinned {
		requestedZone = req.Zone
	}

	// The lease when the request names none is operator policy; every path
	// that leaves TTLHours zero, pool top-ups included, takes it.
	ttlSeconds := req.TTLHours * 3600
	if req.TTLHours <= 0 {
		hours, err := s.policyHostTTLHours(ctx)
		if err != nil {
			return nil, err
		}
		ttlSeconds = hours * 3600
	}

	h := &host.Host{
		ID:                   hostID,
		Name:                 req.Name,
		Zone:                 req.Zone,
		RequestedZone:        requestedZone,
		ZonePinned:           req.ZonePinned,
		GCEInstanceName:      gceInstanceName,
		GCEProject:           s.config.ProjectID,
		MachineType:          req.MachineType,
		GPUType:              req.GPUType,
		GPUCount:             req.GPUCount,
		MinCPUPlatform:       req.MinCPUPlatform,
		BootDiskSizeGB:       req.BootDiskSizeGB,
		ProvisioningModel:    provisioningModel,
		Status:               host.StatusPending,
		DesiredState:         host.DesiredRunning,
		HostOrchestratorPort: s.config.HostOrchestratorPort,
		MaxDevices:           req.MaxDevices,
		Exclusive:            req.Exclusive,
		ForeignCVDs:          req.ForeignCVDs,
		BatchID:              req.BatchID,
		Origin:               origin,
		Pool:                 host.NormalizePool(req.Pool),
		CreatedBy:            &createdBy,
		CreatedAt:            time.Now(),
		UpdatedAt:            time.Now(),
		Lifecycle:            host.NewEphemeralLifecycle(ttlSeconds),
	}
	if err := s.stampIntegratedGPU(ctx, h); err != nil {
		return nil, err
	}
	return h, nil
}

// stampIntegratedGPU records the accelerator an integrated-mode machine type
// carries onto the host row.
//
// Placement asks a host one question, gpu_count > 0, and a g2 row that leaves
// it zero answers "no GPU" about a machine whose GPU is welded on. Only
// ATTACHABLE shapes reach these fields any other way: the resolver fills them for
// an N1 taking a T4 and returns before them for everything integrated, and a user
// naming a g2 by hand never sets them at all.
//
// It runs at create rather than at provision because a host is placeable from the
// moment its row exists. Pending and provisioning are both placement-eligible,
// which is a multi-minute window a GPU create would otherwise not see it in.
//
// The catalog's own values are copied verbatim, and the exactness matters: the
// failover and alternative-zone paths re-validate a host from its row, and an
// integrated shape accepts a GPU request only when BOTH the type and the count
// equal the catalog's. A normalized name, or a count of 1 where the catalog says
// 2, invalidates the host on every later ranking.
//
// It cannot put guestAccelerators on the instance. The provisioning path
// re-derives attachment from the catalog's GPU mode rather than from the row, and
// that derivation attaches nothing to an integrated shape, which is what GCE
// requires, since it rejects the field on those machines.
//
// A failed catalog read fails the create: an unstamped integrated host is
// invisible to GPU placement for the rest of its life and no later pass repairs
// it. A create naming no machine type is left alone, since the row records no
// shape, so there is nothing to judge, and such a host is not GPU-placeable today
// either.
func (s *HostService) stampIntegratedGPU(ctx context.Context, h *host.Host) error {
	if s.machineTypes == nil || h.MachineType == "" {
		return nil
	}
	mt, err := s.machineTypes.Get(ctx, h.MachineType)
	if err != nil {
		return fmt.Errorf("resolving the accelerator of machine type %q: %w", h.MachineType, err)
	}
	if mt.GPUMode != machinetype.GPUModeIntegrated {
		return nil
	}
	h.GPUType = mt.IntegratedGPUType
	h.GPUCount = mt.IntegratedGPUCount
	return nil
}

// createAuditMutation builds the host-create entry's insert. resolved is what
// the placement resolution concluded, nil on the paths that answer to no
// judgement (a user's create, a batch's). The caller checks that a mutator is
// wired.
//
// The facts are stamped rather than compared here because the comparison they
// serve (could a standing buffer have absorbed this create at all?) is asked
// much later, by which time the packing policy, the catalog's default shape and
// the set of enabled GPU types may all have moved. Reading them live at that
// point would reclassify the whole horizon of history the moment an operator
// changed a number.
func (s *HostService) createAuditMutation(h *host.Host, createdBy string, resolved *host.ResolvedFacts) (*spanner.Mutation, error) {
	metadata := map[string]any{
		attrName:        h.Name,
		attrZone:        h.Zone,
		attrMachineType: h.MachineType,
		attrOrigin:      string(h.Origin),
		attrMaxDevices:  h.MaxDevices,
	}
	if h.Pool != host.PoolDefault {
		metadata[attrPool] = h.Pool
	}
	// Absent, not zero and not false: a create nobody judged has to stay
	// distinguishable from one judged and found wanting, and the generated
	// columns read an absent key as NULL, which the signal takes as a miss it
	// cannot classify rather than one it can rule out.
	if resolved != nil {
		metadata[attrDensity] = resolved.Density
		metadata[attrGPURequired] = resolved.GPURequired
		metadata[attrFitsDefaultHost] = resolved.FitsDefaultHost
		// The one fact here whose absence is the ordinary case rather than an
		// unjudged create: a resolution that got the shape it preferred has no
		// second shape to name.
		if resolved.PreferredType != "" {
			metadata[attrPreferredType] = resolved.PreferredType
		}
	}
	mut, err := s.auditMutator.EntryMutation(audit.New(audit.ActionHostCreate, audit.ResourceHost).
		WithActor(createdBy).
		WithResource(h.ID).
		WithMetadata(metadata).
		Build())
	if err != nil {
		return nil, fmt.Errorf("building the host-create audit entry: %w", err)
	}
	return mut, nil
}

// recordCreated publishes what a committed create owes the rest of the system
// but must not hold it up: the ranked-binding count and the log line. The audit
// entry is not here; it commits with the row, in Create.
func (s *HostService) recordCreated(ctx context.Context, h *host.Host) {
	if !h.ZonePinned {
		countRankedBinding(ctx, h.Zone)
	}
	s.logger.Info("host created",
		slog.String("host_id", h.ID),
		slog.String(attrName, h.Name),
	)
}

// idempotencyMutation handles the Idempotency-Key for a create. It returns the
// key-insert mutation to fold into the create transaction, or a non-nil existing
// host when the key was already used for an equivalent request (short-circuit).
// The fingerprint covers every JSON-visible CreateRequest field, so reusing a
// key with a different request returns idempotency.ErrConflict rather than the
// wrong host.
func (s *HostService) idempotencyMutation(ctx context.Context, req host.CreateRequest, h *host.Host) ([]*spanner.Mutation, *host.Host, error) {
	if req.IdempotencyKey == "" || s.idempotencyStore == nil {
		return nil, nil, nil
	}
	fingerprint, err := idempotency.Fingerprint(req)
	if err != nil {
		return nil, nil, err
	}
	prepared, err := idempotency.PrepareMutation[*host.Host, *spanner.Mutation](
		ctx, s.idempotencyStore, req.IdempotencyKey, fingerprint, h.ID, s.hostRepo.Get)
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

// lookupIdempotent resolves a lost commit race for req's Idempotency-Key: the
// winner's host when req matches the recorded fingerprint,
// idempotency.ErrConflict when a different request reused the key, or
// found=false when the key resolves to nothing, in which case the caller falls
// back to its original commit error.
func (s *HostService) lookupIdempotent(ctx context.Context, req host.CreateRequest) (*host.Host, bool, error) {
	if req.IdempotencyKey == "" || s.idempotencyStore == nil {
		return nil, false, nil
	}
	fingerprint, err := idempotency.Fingerprint(req)
	if err != nil {
		return nil, false, nil //nolint:nilerr // fall back to the caller's commit error
	}
	// Returned unwrapped so idempotency.ErrConflict reaches the transport as a 422.
	return idempotency.ResolveRace(ctx, s.idempotencyStore, req.IdempotencyKey, fingerprint, s.hostRepo.Get)
}

// Delete marks a host for deletion and enqueues a deprovisioning task in one
// commit. The worker pool tears down the GCE instance and removes the host row +
// quota durably (see RunDeprovision), so deletion survives instance restarts and
// retries transient GCE failures instead of blocking the request.
func (s *HostService) Delete(ctx context.Context, id string) error {
	return s.deleteIf(ctx, id, nil)
}

// DeleteExpired is the reaper's Delete. Its transaction re-checks that the
// host is still ephemeral, holds no protected devices and is past expires_at,
// because a host extend, device extend or pin can commit between the reaper's
// listing and this call, and the reaper runs on every instance. A host that is
// no longer expired is refused with *host.InvalidStateError.
func (s *HostService) DeleteExpired(ctx context.Context, id string) error {
	return s.deleteIf(ctx, id, func(h *host.Host) error {
		if h.Lifecycle.Mode != host.LifecycleModeEphemeral || !h.Lifecycle.IsExpired() {
			return &host.InvalidStateError{Message: fmt.Sprintf("host %s is no longer expired: its lease was raised or it is protected", id)}
		}
		return nil
	})
}

// deleteIf is Delete with guard, when set, checked against the row inside the
// transaction that marks it deleting.
func (s *HostService) deleteIf(ctx context.Context, id string, guard func(*host.Host) error) error {
	if s.taskEnqueuer == nil {
		return errors.New("host service has no task enqueuer configured")
	}
	deprovMut := s.taskEnqueuer.EnqueueMutation(ctx, task.New(TaskTypeDeprovisionHost, id))
	// Status check, transition, and enqueue commit atomically: two concurrent
	// deletes cannot both observe a non-deleting host and double-enqueue.
	h, err := s.hostRepo.Mutate(ctx, id, func(h *host.Host) error {
		if h.Status == host.StatusDeleting {
			return errHostAlreadyDeleting
		}
		if guard != nil {
			if err := guard(h); err != nil {
				return err
			}
		}
		if err := h.TransitionTo(host.StatusDeleting); err != nil {
			return err
		}
		h.DesiredState = host.DesiredAbsent
		return nil
	}, deprovMut)
	if errors.Is(err, errHostAlreadyDeleting) {
		return nil // already being deprovisioned (idempotent)
	}
	if err != nil {
		return fmt.Errorf("marking host for deletion: %w", err)
	}
	s.taskEnqueuer.Wake()

	if s.auditLogger != nil {
		entry := audit.New(audit.ActionHostDelete, audit.ResourceHost).
			WithResource(id).
			WithMetadata(map[string]any{attrName: h.Name}).
			Build()
		s.auditLogger.LogAsync(entry)
	}

	s.logger.Info("host deprovisioning enqueued", slog.String("host_id", id), slog.String(attrName, h.Name))
	return nil
}

// errHostAlreadyDeleting makes a repeated Delete an idempotent no-op without
// enqueueing a second deprovision task.
var errHostAlreadyDeleting = errors.New("host already deleting")

// Start starts a stopped host. The GCE call is asynchronous; the host is
// persisted as starting and GCE sync observes the transition to running.
func (s *HostService) Start(ctx context.Context, id string) error {
	h, err := s.hostRepo.Get(ctx, id)
	if err != nil {
		return err
	}
	if !h.Status.CanTransitionTo(host.StatusStarting) {
		return fmt.Errorf("host %s cannot start from status %s: %w", id, h.Status, host.ErrInvalidState)
	}
	if _, err := s.instancesClient.Start(ctx, &computepb.StartInstanceRequest{
		Project:  s.config.ProjectID,
		Zone:     h.Zone,
		Instance: h.GCEInstanceName,
	}); err != nil {
		return fmt.Errorf("starting GCE instance %s: %w", h.GCEInstanceName, err)
	}
	if _, err := s.hostRepo.Mutate(ctx, id, func(h *host.Host) error {
		h.DesiredState = host.DesiredRunning
		// A stopped VM boots a fresh Host Orchestrator: reset the latch so it
		// stays 'starting' until that HO answers, not promoted by a stale value.
		h.HOReadyAt = nil
		return h.TransitionTo(host.StatusStarting)
	}); err != nil {
		return fmt.Errorf("recording host start: %w", err)
	}
	audit.LogChange(ctx, s.auditLogger, audit.ActionHostStart, audit.ResourceHost, id, nil)
	return nil
}

// Stop stops a running host. The GCE stop is issued first, so a refused call
// leaves the host untouched; the stopping transition then commits with a
// host.stop_settle task, which waits on the stop operation and settles the
// host and its devices (RunStopSettle). The stop request carries a requestId
// derived from that task's ID, so the task's re-issue returns this same
// operation.
func (s *HostService) Stop(ctx context.Context, id string) error {
	if s.taskEnqueuer == nil {
		return errors.New("host service has no task enqueuer configured")
	}
	h, err := s.hostRepo.Get(ctx, id)
	if err != nil {
		return err
	}
	if !h.Status.CanTransitionTo(host.StatusStopping) {
		return fmt.Errorf("host %s cannot stop from status %s: %w", id, h.Status, host.ErrInvalidState)
	}
	settle := task.New(TaskTypeStopSettleHost, id)
	settle.ID = uuid.New().String()
	requestID := stopRequestID(settle.ID)
	if _, err := s.instancesClient.Stop(ctx, &computepb.StopInstanceRequest{
		Project:   s.config.ProjectID,
		Zone:      h.Zone,
		Instance:  h.GCEInstanceName,
		RequestId: &requestID,
	}); err != nil {
		return fmt.Errorf("stopping GCE instance %s: %w", h.GCEInstanceName, err)
	}
	if _, err := s.hostRepo.Mutate(ctx, id, func(h *host.Host) error {
		h.DesiredState = host.DesiredStopped
		return h.TransitionTo(host.StatusStopping)
	}, s.taskEnqueuer.EnqueueMutation(ctx, settle)); err != nil {
		return fmt.Errorf("recording host stop: %w", err)
	}
	s.taskEnqueuer.Wake()
	audit.LogChange(ctx, s.auditLogger, audit.ActionHostStop, audit.ResourceHost, id, nil)
	return nil
}

// UpdateMaxDevices reshapes how many instance slots a live host will accept.
//
// Lowering is gated on the instances the host's LIVE devices desire, never on
// CurrentDeviceCount: the counter includes error rows that back no CVD, so
// counting them would refuse to shrink a host that is in fact empty. It is
// equally not a count of device ROWS, because max_devices bounds instances, and
// one live device can want eight of them, so a row count would happily shrink a
// host below what it already carries. Raising is always allowed, and placement
// reads the new headroom on its next pass.
func (s *HostService) UpdateMaxDevices(ctx context.Context, id string, maxDevices int64) (*host.Host, error) {
	h, err := s.hostRepo.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("loading host %s: %w", id, err)
	}
	if h.Status.IsTerminal() {
		return nil, &host.InvalidStateError{Message: fmt.Sprintf("host %s is %s: its device capacity can no longer be changed", id, h.Status)}
	}
	if maxDevices == h.MaxDevices {
		return h, nil
	}
	if maxDevices < h.MaxDevices {
		if s.liveInstanceCounter == nil {
			return nil, errors.New("host service has no live-instance counter configured")
		}
		live, err := s.liveInstanceCounter(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("counting live instances on host %s: %w", id, err)
		}
		if live > maxDevices {
			return nil, &host.InvalidStateError{Message: fmt.Sprintf("host %s carries %d live instances: max_devices cannot be lowered to %d", id, live, maxDevices)}
		}
	}

	previous := h.MaxDevices
	updated, err := s.hostRepo.Mutate(ctx, id, func(h *host.Host) error {
		h.MaxDevices = maxDevices
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("recording host capacity change: %w", err)
	}
	audit.LogChange(ctx, s.auditLogger, audit.ActionHostUpdate, audit.ResourceHost, id,
		map[string]any{"max_devices": map[string]any{"from": previous, "to": maxDevices}})
	return updated, nil
}

// Retry recreates a preempted or failed host.
// If fallbackToStandard is true and the host is a Spot VM, it will be
// recreated as a standard VM if Spot capacity is unavailable.
func (s *HostService) Retry(ctx context.Context, id string, fallbackToStandard bool) (*host.Host, error) {
	return s.RetryDelayed(ctx, id, fallbackToStandard, 0)
}

// RetryDelayed is Retry with the re-provision task first claimable after
// delay. Preemption auto-recovery jitters its recoveries through this so a
// zone-wide Spot reclaim does not re-provision the whole fleet in one
// synchronized wave.
func (s *HostService) RetryDelayed(ctx context.Context, id string, fallbackToStandard bool, delay time.Duration) (*host.Host, error) {
	if s.taskEnqueuer == nil {
		return nil, errors.New("host service has no task enqueuer configured")
	}
	provisionTask := task.New(TaskTypeProvisionHost, id)
	if delay > 0 {
		provisionTask.AvailableAt = time.Now().Add(delay)
	}
	provisionMut := s.taskEnqueuer.EnqueueMutation(ctx, provisionTask)

	// The incarnation bump below dooms the current disk, and the host's
	// snapshots with it: mark them lost in the same commit. The artifact-cache
	// ledger dies with the same disk: a fresh incarnation is cold by
	// definition, and a surviving row would advertise warmth placement can
	// never collect on.
	extra, err := s.hostRepo.SnapshotsLostMutations(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("collecting snapshots to mark lost: %w", err)
	}
	extra = append(extra, spannerplatform.ArtifactCacheLostMutation(id), provisionMut)

	// The retryable check, error-state reset, transition, and provisioning
	// enqueue commit atomically: concurrent retries cannot double-enqueue.
	h, err := s.hostRepo.Mutate(ctx, id, func(h *host.Host) error {
		if !h.CanRetry() {
			return &host.NotRetryableError{
				HostID: id,
				Status: string(h.Status),
			}
		}

		if fallbackToStandard && h.IsSpotVM() {
			s.logger.Info("falling back to standard provisioning",
				slog.String("host_id", id),
			)
			h.ProvisioningModel = host.ProvisioningModelStandard
		}

		// Clear error state
		h.ErrorMessage = ""
		h.ErrorType = ""
		h.ErrorCode = ""
		h.ErrorSuggestion = ""
		h.ErrorRetryable = false
		h.ErrorOccurredAt = nil
		h.ErrorAlternativeZones = nil
		// A retry is a fresh bet on capacity: the refusal that fenced the old
		// incarnation from placement says nothing about this one until GCE
		// answers again.
		h.ClearProvisionBlocked()

		// Immutable-infrastructure recovery: every re-provision bumps
		// the incarnation, which the instance name embeds, so this attempt
		// targets a FRESH name and the previous incarnation's still-dying
		// husk (e.g. a preempted Spot VM under termination-action DELETE)
		// can never be adopted by mistake. The husk's name is now unclaimed:
		// Spot auto-delete removes it, the orphan GC is the fallback. The
		// provision adopt-guard remains as defense in depth for crashes
		// within one incarnation.
		h.Incarnation++
		h.GCEInstanceName = host.InstanceName(h.Name, h.ID, h.Incarnation)
		h.DesiredState = host.DesiredRunning
		// Fresh VM: the ho_reachable latch resets so the recovered host re-enters
		// 'starting' until its new Host Orchestrator answers.
		h.HOReadyAt = nil

		return h.TransitionTo(host.StatusProvisioning)
	}, extra...)
	if err != nil {
		// The transition-task unique index (at most one live transition task
		// per resource) rejects the enqueue when a provisioning task is still
		// pending or leased: an error host whose final attempt has not
		// finished dead-lettering, or a dead-lettered task an operator just
		// requeued. The journey the retry wants already exists and will drive
		// the freshly-reset row, so report the conflict instead of a raw
		// storage error.
		if spanner.ErrCode(err) == codes.AlreadyExists {
			return nil, &host.InvalidStateError{
				Message: fmt.Sprintf("host %s already has a provisioning task queued or running; retry after it settles", id),
			}
		}
		return nil, err
	}
	s.taskEnqueuer.Wake()

	if s.auditLogger != nil {
		entry := audit.New(audit.ActionHostRetry, audit.ResourceHost).
			WithResource(h.ID).
			WithMetadata(map[string]any{
				attrNewInstance:        h.GCEInstanceName,
				"fallback_to_standard": fallbackToStandard,
			}).
			Build()
		s.auditLogger.LogAsync(entry)
	}

	s.logger.Info("host retry initiated",
		slog.String("host_id", h.ID),
		slog.String(attrNewInstance, h.GCEInstanceName),
	)

	return h, nil
}

// Sync synchronizes host state with GCE. Like the background GCE sync, an
// observed status is applied only when the lifecycle allows it, so a host
// mid-deprovision is never resurrected by a still-running VM.
func (s *HostService) Sync(ctx context.Context, id string) error {
	h, err := s.hostRepo.Get(ctx, id)
	if err != nil {
		return err
	}
	inst, err := s.instancesClient.Get(ctx, &computepb.GetInstanceRequest{
		Project:  s.config.ProjectID,
		Zone:     h.Zone,
		Instance: h.GCEInstanceName,
	})
	if err != nil {
		return fmt.Errorf("getting GCE instance %s: %w", h.GCEInstanceName, err)
	}
	// An observed stop goes through the adoption write, which settles the
	// host's devices in the same commit. A CAS miss falls through to the
	// re-read below.
	observed := *h
	gce.UpdateHostFromGCEInstance(&observed, inst)
	if observed.ApplyVMObservation(inst.GetStatus()) && observed.Status == host.StatusStopped {
		applied, err := s.hostRepo.BatchUpdateGCEState(ctx, []host.GCEStateUpdate{stoppedAdoption(h.Status, &observed)})
		if err != nil {
			return fmt.Errorf("recording host sync: %w", err)
		}
		if applied == 1 {
			return nil
		}
	}
	if _, err := s.hostRepo.Mutate(ctx, id, func(h *host.Host) error {
		gce.UpdateHostFromGCEInstance(h, inst)
		h.ApplyVMObservation(inst.GetStatus())
		return nil
	}); err != nil {
		return fmt.Errorf("recording host sync: %w", err)
	}
	return nil
}

// FindAvailable finds hosts with capacity for devices.
func (s *HostService) FindAvailable(ctx context.Context, zone string, count int64) ([]*host.Host, error) {
	return s.hostRepo.FindAvailableHosts(ctx, zone, count)
}

// GetZoneCapacity returns available capacity per zone.
func (s *HostService) GetZoneCapacity(ctx context.Context) (map[string]int64, error) {
	status := host.StatusRunning
	hosts, err := s.hostRepo.List(ctx, host.ListFilter{Status: &status})
	if err != nil {
		return nil, err
	}
	capacity := make(map[string]int64)
	for _, h := range hosts {
		capacity[h.Zone] += h.AvailableSlots()
	}
	return capacity, nil
}
