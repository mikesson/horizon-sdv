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
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	"go.opentelemetry.io/otel/attribute"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/quota"
	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// Config holds configuration for the sync service
type Config struct {
	// ProjectID is the GCP project ID
	ProjectID string

	// Zones is the list of zones to sync
	Zones []string

	// SyncInterval is how often to sync with GCE
	SyncInterval time.Duration

	// InstanceLabels are the labels an unclaimed instance must carry before
	// the orphan sweep counts it as this install's. They never gate drift: a
	// host row finds its VM by instance name alone.
	InstanceLabels map[string]string

	// MaxPreemptionRecoveries bounds automatic re-provisioning of a repeatedly
	// preempted host: once its preemption count (within
	// host.PreemptionCountWindow) exceeds this, auto-recovery stops and the
	// host is left preempted for manual handling, so chronic preemption in a
	// constrained zone cannot recreate the VM forever. 0 uses a default of 5.
	MaxPreemptionRecoveries int

	// MaxLostMarksPerCycle bounds hosts marked lost in one sweep. A mass
	// disappearance is far more likely a GCE listing/API anomaly than a real
	// mass deletion. Beyond the bound the sweep degrades to alerting and
	// re-observes next cycle. 0 uses a default of 5.
	MaxLostMarksPerCycle int

	// DeleteOrphanVMs is the compiled fallback for whether orphan instances
	// (managed VMs no live host row claims) are actually deleted, used only
	// when no runtime policy is wired. Production reads the sweeps setting
	// each cycle. Default false: orphans are detected, logged, and counted
	// (cco.hostreconciler.orphan_vms) but never touched. Dry-run is the safe
	// default because an orphan is always an anomaly worth a human look
	// before the mechanism that produced it is trusted.
	DeleteOrphanVMs bool
}

// defaultMaxPreemptionRecoveries caps automatic preemption recovery; see Config.
const defaultMaxPreemptionRecoveries = 5

// defaultMaxLostMarksPerCycle caps lost-host marks per sweep; see Config.
const defaultMaxLostMarksPerCycle = 5

// Repository extends host.Repository with the batch operations the reconciler needs.
type Repository interface {
	host.Repository
	// BatchUpdateGCEState updates GCE-owned columns for multiple hosts in a
	// single batch. Each row write is preconditioned on the update's
	// PrevStatus; it returns the number of host rows actually written, so the
	// shortfall from len(updates) is the stale-miss count. An update into
	// stopped settles the host's running devices to stopped in the same
	// commit; those device rows are not counted.
	BatchUpdateGCEState(ctx context.Context, updates []host.GCEStateUpdate) (int64, error)
	// MarkHostLost marks a host whose VM vanished outside CCO as deleted and
	// fails over its live devices to error in the same commit.
	MarkHostLost(ctx context.Context, id, reason string) error
	// CountLostMarksSince counts hosts tombstoned with the given reason after
	// since. This is the durable form of the lost-mark blast budget. Counting
	// the marks themselves (not a process-local counter) makes the budget
	// shared: leadership is best-effort, and during a handoff two instances
	// can sweep concurrently, so one budget each would double the cap exactly
	// when a mass anomaly is in flight.
	CountLostMarksSince(ctx context.Context, since time.Time, reason string) (int64, error)
	// MarkPreempted records a Spot-VM preemption on a host, re-reading the row
	// transactionally so it cannot clobber a concurrent device-count write and
	// validating the transition under the same transaction. A host that
	// concurrently entered deleting/error/deleted returns
	// *host.InvalidStateError. It returns the updated host for the recovery
	// decision.
	MarkPreempted(ctx context.Context, id, opID string) (*host.Host, error)
	// ListActiveZones returns the distinct zones of non-deleted hosts, so the
	// sync follows wherever hosts actually live instead of a static zone list.
	ListActiveZones(ctx context.Context) ([]string, error)
}

// ResyncEnqueuer files reconciler-priority work into the durable queue; the
// Spanner TaskQueue satisfies it.
type ResyncEnqueuer interface {
	EnqueueResync(ctx context.Context, taskType, resourceID string, delay time.Duration) error
}

// RecoveryQueue is the preemption-recovery boundary: filing plus the liveness
// probe the level check needs to re-file a stranded preempted host without
// churning hosts whose recovery is already queued or running. The Spanner
// TaskQueue satisfies it.
type RecoveryQueue interface {
	ResyncEnqueuer
	HasActive(ctx context.Context, taskType, resourceID string) (bool, error)
}

// Service performs one-way state synchronization from GCE to the Orchestrator.
//
// It periodically polls the Google Compute Engine API to detect and reconcile
// state drifts (e.g., VMs stopped/preempted externally), ensuring the Spanner
// inventory reflects reality.
type Service struct {
	config           Config
	client           *compute.InstancesClient
	operationsClient *compute.ZoneOperationsClient
	hostRepo         Repository
	quotaService     quota.Service
	zoneHealth       gcecatalog.ZoneHealthStore
	auditLogger      audit.Sink
	deletePolicy     func(context.Context) (bool, error)
	recovery         RecoveryQueue
	reconcile        ResyncEnqueuer
	logger           *slog.Logger
	runner           telemetry.Runner

	driftUpdates         atomic.Int64
	casMisses            atomic.Int64
	hostsLost            atomic.Int64
	preemptionsDetected  atomic.Int64
	preemptionsRecovered atomic.Int64
	driftBenign          atomic.Int64 // IP/metadata-only divergence
	driftAdopted         atomic.Int64 // out-of-band status change absorbed
	driftBlocked         atomic.Int64 // GCE status conflicting with app-owned lifecycle
	remediationDeferred  atomic.Int64 // corrective actions withheld by a blast-radius gate
	orphanVMs            atomic.Int64 // orphans seen in the last sweep (gauge)
	orphanVMsDeleted     atomic.Int64 // orphans actually deleted (counter)

	// zoneEvidence is the current zone's out-of-band operation evidence
	// (preemptions, deletes), gathered by checkPreemptions BEFORE the zone
	// sync so absence classification never races its own detector. Cycle
	// goroutine only.
	zoneEvidence map[string]instanceEvidence

	// missingSpotSince tracks when a Spot host's VM was first observed
	// missing with NO operation evidence. Preempted Spot VMs are deleted by
	// GCE itself and the preempted operation is not guaranteed to appear
	// (termination action DELETE), so absence waits out missingSpotGrace for
	// evidence before it may be read as out-of-band deletion. Cycle
	// goroutine only.
	missingSpotSince map[string]time.Time
}

// Option configures a Service.
type Option func(*Service)

// WithDeletePolicy supplies the runtime answer to whether this loop may
// delete the orphan VMs it finds, read once per cycle so an operator's change
// takes effect without a redeploy. Unset, the compiled Config value stands.
func WithDeletePolicy(fn func(context.Context) (bool, error)) Option {
	return func(s *Service) {
		s.deletePolicy = fn
	}
}

// WithQuotaService configures quota reconciliation for the sync service.
// When set, the sync service will reconcile quota counters after each sync cycle.
func WithQuotaService(qs quota.Service) Option {
	return func(s *Service) {
		s.quotaService = qs
	}
}

// WithPreemptionRecovery wires automatic re-provisioning of preempted hosts.
// When set, a detected preemption is recorded and the host is durably
// re-provisioned (same provisioning model) via a jittered keyed reconcile
// task, which the reconcile handler backs with the provision queue. Without
// it, preempted hosts are only marked and await a manual retry.
func WithPreemptionRecovery(q RecoveryQueue) Option {
	return func(s *Service) {
		s.recovery = q
	}
}

// WithOperationsClient configures the zone operations client for preemption detection.
// When set, the sync service will check for preemption events on Spot VMs.
func WithOperationsClient(oc *compute.ZoneOperationsClient) Option {
	return func(s *Service) {
		s.operationsClient = oc
	}
}

// WithZoneHealth wires the placement plane's zone_health memory: a tripped
// zone recovery breaker also writes a preemption-storm cooldown cell, so new
// Spot capacity stops landing in a zone that is actively reclaiming, and does
// not merely stop auto-recovering there. Nil disables the write.
func WithZoneHealth(store gcecatalog.ZoneHealthStore) Option {
	return func(s *Service) {
		s.zoneHealth = store
	}
}

// WithAuditLogger wires audit logging of detected preemptions. Host rows are
// torn down with the fleet, so the audit trail is the only durable record.
// The operations view's preemption KPIs count these entries. Nil disables it.
func WithAuditLogger(l audit.Sink) Option {
	return func(s *Service) {
		s.auditLogger = l
	}
}

// WithReconcileEnqueuer wires the keyed reconcile queue: the sweep
// flags transitional hosts that look ownerless (no status write for
// staleTransitionalAfter) and enqueues host.reconcile suspicion, which the
// reconcile handler verifies and heals, deduped by the queue while pending.
// The detector detects; it does not actuate.
func WithReconcileEnqueuer(q ResyncEnqueuer) Option {
	return func(s *Service) {
		s.reconcile = q
	}
}

// WithLeaderCheck installs a leadership predicate; sync cycles run only while
// it returns true (or when unset), so just the leader instance touches GCE.
func WithLeaderCheck(fn func() bool) Option {
	return func(s *Service) {
		s.runner.LeaderCheck = fn
	}
}

// New creates the host reconciler.
func New(
	config Config,
	client *compute.InstancesClient,
	hostRepo Repository,
	logger *slog.Logger,
	opts ...Option,
) *Service {
	s := &Service{
		config:           config,
		client:           client,
		hostRepo:         hostRepo,
		logger:           logger,
		missingSpotSince: make(map[string]time.Time),
	}
	s.runner = telemetry.Runner{
		Interval:   config.SyncInterval,
		Cycle:      s.runCycle,
		Metrics:    telemetry.NewLoopMetrics("hostreconciler"),
		RunAtStart: true,
	}
	for _, opt := range opts {
		opt(s)
	}
	s.initMetrics()
	return s
}

// initMetrics registers observable counters for the sync loop's corrective
// actions. Instruments are no-ops without a meter provider, so this is always
// safe (e.g. in tests).
func (s *Service) initMetrics() {
	classAttr := func(class string) []attribute.KeyValue {
		return []attribute.KeyValue{attribute.String("class", class)}
	}
	const driftDetected = "cco.hostreconciler.drift_detected"
	const driftByClass = "Divergences between GCE and the inventory, by class: benign (IP/metadata), adopted (out-of-band status change absorbed), blocked (GCE status conflicting with app-owned lifecycle)."
	telemetry.RegisterCounters("hostreconciler", []telemetry.ObservedCounter{
		{Name: "cco.hostreconciler.drift_updates", Description: "Host rows corrected from observed GCE state.", Value: &s.driftUpdates},
		{Name: "cco.hostreconciler.cas_misses", Description: "Drift writes skipped because the row changed after the sync snapshot; each one is a prevented clobber.", Value: &s.casMisses},
		{Name: "cco.hostreconciler.hosts_lost", Description: "Hosts marked deleted because their VM vanished outside CCO.", Value: &s.hostsLost},
		{Name: "cco.hostreconciler.preemptions_detected", Description: "Spot VM preemptions observed.", Value: &s.preemptionsDetected},
		{Name: "cco.hostreconciler.preemptions_recovered", Description: "Preempted hosts handed to durable re-provisioning.", Value: &s.preemptionsRecovered},
		{Name: driftDetected, Description: driftByClass, Value: &s.driftBenign, Attrs: classAttr("benign")},
		{Name: driftDetected, Description: driftByClass, Value: &s.driftAdopted, Attrs: classAttr("adopted")},
		{Name: driftDetected, Description: driftByClass, Value: &s.driftBlocked, Attrs: classAttr("blocked")},
		{Name: "cco.hostreconciler.remediation_deferred", Description: "Corrective actions withheld by a blast-radius gate (lost-mark budget, zone recovery breaker, orphan-delete cap).", Value: &s.remediationDeferred},
		{Name: "cco.hostreconciler.orphan_vms", Description: "Managed instances no live host row claims, as of the last sweep. Any nonzero value is an anomaly.", Value: &s.orphanVMs, Gauge: true},
		{Name: "cco.hostreconciler.orphan_vms_deleted", Description: "Orphan instances deleted by the sweep (only with orphan deletion enabled).", Value: &s.orphanVMsDeleted},
	})
}

// Start begins the sync loop. The initial sync runs inside the loop goroutine
// so a slow GCE API cannot block server startup.
func (s *Service) Start(ctx context.Context) {
	s.logger.Info("starting host reconciler",
		slog.Duration("interval", s.config.SyncInterval),
		slog.Any("zones", s.config.Zones),
	)
	s.runner.Start(ctx)
}

// Stop stops the sync loop and waits for an in-flight cycle to finish.
func (s *Service) Stop() {
	s.runner.Stop()
	s.logger.Info("stopped host reconciler")
}

// runCycle syncs all configured zones (or, by default, the zones where hosts
// currently exist); a non-nil error marks the cycle unsuccessful for the
// loop's staleness metric (every failure is also logged in place).
func (s *Service) runCycle(ctx context.Context) error {
	zones := s.config.Zones
	if len(zones) == 0 {
		var err error
		zones, err = s.hostRepo.ListActiveZones(ctx)
		if err != nil {
			s.logger.Error("failed to list active zones", slog.Any("error", err))
			return fmt.Errorf("listing active zones: %w", err)
		}
	}
	// Age out unexplained-absence entries for hosts that stopped being
	// observed at all (deleted rows); resolved entries are pruned inline.
	for id, first := range s.missingSpotSince {
		if time.Since(first) > 2*missingSpotGrace {
			delete(s.missingSpotSince, id)
		}
	}

	ok := true
	for _, zone := range zones {
		// Preemption evidence FIRST: the ops scan must record (and mark)
		// preemptions before the zone sync consumes the observation.
		// Otherwise the drift writer adopts the dying VM's STOPPING/
		// TERMINATED, or the missing-VM path marks the host lost, and the
		// recovery that should have happened is foreclosed.
		if err := s.checkPreemptions(ctx, zone); err != nil {
			ok = false
			s.logger.Error("failed to check preemptions",
				slog.String("zone", zone),
				slog.Any("error", err),
			)
		}

		if err := s.SyncZone(ctx, zone); err != nil {
			ok = false
			s.logger.Error("failed to sync zone",
				slog.String("zone", zone),
				slog.Any("error", err),
			)
		}
	}

	// Orphan direction: managed VMs no host row claims. Runs across ALL
	// zones via aggregatedList, so an orphan in a zone with no host rows,
	// invisible to the per-zone sync above, is still found.
	if err := s.sweepOrphanVMs(ctx); err != nil {
		ok = false
		s.logger.Error("orphan VM sweep failed", slog.Any("error", err))
	}

	// Reconcile quota counters after all zones have been synced
	if err := s.reconcileQuotas(ctx); err != nil {
		ok = false
	}
	if !ok {
		return errors.New("sync cycle completed with errors")
	}
	return nil
}

// The ours-predicates, side by side because they differ on purpose: instance
// listings are filtered server-side by name prefix (managedNameFilter). The
// orphan sweep then also requires the full label set, because an unclaimed VM
// has nothing else to say whose it is. The zone sync matches rows to VMs by
// name, which the row already owns. The zone-operations scan sees only
// instance NAMES in target links; labels are unavailable there, so the prefix
// is the only possible test.

// managedNameFilter is the server-side listing filter for managed instances.
func (s *Service) managedNameFilter() string {
	return fmt.Sprintf("name:%s*", host.InstancePrefix)
}

// hasRequiredLabels checks if instance has required labels
func (s *Service) hasRequiredLabels(labels map[string]string) bool {
	for k, v := range s.config.InstanceLabels {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// isManagedInstance checks if an instance is managed by CCO.
func (s *Service) isManagedInstance(instanceName string) bool {
	return strings.HasPrefix(instanceName, host.InstancePrefix)
}
