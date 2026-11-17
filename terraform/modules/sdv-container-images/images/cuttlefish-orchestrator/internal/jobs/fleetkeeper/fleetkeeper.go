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

// Package fleetkeeper holds the fleet's standing capacity: a number of idle,
// placement-eligible hosts, so a device create finds capacity standing instead
// of paying the multi-minute host-provisioning leg. Standing capacity needs
// no mechanism of its own. Placement already prefers any existing eligible host,
// an idle host survives to its lease's absolute expiry, and "claiming" a
// standby host is just normal placement finding it.
//
// The keeper meets a deficit two ways, and prefers the one that spends nothing:
// renewing the lease of a host already draining out of the buffer, and only then
// provisioning a new one. Both are scale-out and answer to the same mode gate.
//
// Capacity moves the other way by shortening leases so the reaper collects the
// hosts sooner, and two unrelated things are given back that way: a surplus the
// target no longer wants, and dedicated hosts fenced for groups that have ended,
// which are not inventory at all and which nothing else reclaims. Both answer to
// a stricter gate than the two above, and both still delete nothing: the reaper
// stays the sole executioner, so every guard it applies still holds.
//
// The target is composed rather than configured: the operator's standing intent
// and the demand the last horizon actually saw, max-wins. What the keeper is
// permitted to do about the resulting number is the autoscaler MODE, and in off
// the answer is nothing at all. The target is still computed and published,
// because a number nobody acts on is exactly what an observation week needs.
//
// It also drives the warm plane one step per cycle. That plane is a sibling
// package the composition root builds and hands in, so no job imports a job and
// warming stays independent of what the keeper decides about capacity.
package fleetkeeper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync/atomic"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/quota"
	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// The provisioning cap per cycle is hostconfig.TopUpBudget, a share of the
// fleet standing floored at two. It lives there rather than here because the
// autoscaler page states it beside the target it converges toward, and a page
// re-deriving it from parts handed over as data would be a second derivation of
// one rule. The property is unchanged: a floor raised by ten converges over
// cycles instead of inserting ten VMs in one.
//
// Lease renewal carries no such cap, and the asymmetry is intended: it
// creates no VM and lowers no lease, and the deficit already bounds it.

// MaxShortensPerCycle caps how many surplus leases one cycle may lower. It is
// the reclamation twin of the provisioning budget, and exported because the
// robustness harness bounds reclamation by it.
//
// It is absolute where the provisioning budget is proportional, because this
// is the direction that gives capacity back, so a bound that grew with the
// fleet would hand back more the more there was to hand back.
//
// A per-cycle cap matters more in this direction than in the other. A target
// that drops from 16 to 0 converges over cycles rather than shortening sixteen
// hosts at once, so a target set wrong and corrected a minute later costs two
// hosts instead of the whole fleet.
const MaxShortensPerCycle = 2

// MaxCollapsesPerCycle caps how many empty dedicated hosts one cycle may
// reclaim.
//
// Its own budget rather than a share of MaxShortensPerCycle, which the two would
// otherwise starve each other out of. They answer to unrelated conditions, one
// to a target the operator lowered and the other to groups that ended, and a
// fleet busy giving back surplus would stop collapsing idle exclusive VMs for
// exactly as long as the surplus lasted, which is when they cost most.
const MaxCollapsesPerCycle = 2

// QuotaReserveHosts is the host-quota headroom the buffer never spends.
//
// Quota is a hard ceiling: the create transaction returns a
// *quota.ExceededError that the API maps to 429, so a buffer that fills the
// last slot turns somebody's next create into a client-visible failure. Demand
// outranks buffer. A surplus host costs drain minutes, and a refused create
// cannot be un-refused.
const QuotaReserveHosts = 1

// DefaultInterval is the time between keeper cycles when the config names none.
//
// Exported because the autoscaler page states it: a target of ten against a
// standby of two converges over cycles rather than at once, which is only
// legible beside how long a cycle is.
const DefaultInterval = 30 * time.Second

// Config configures the fleet keeper.
type Config struct {
	// Enabled determines whether the keeper runs.
	Enabled bool

	// Interval is the time between keeper cycles. Defaults to DefaultInterval.
	Interval time.Duration

	// ProvisionLead is how much lease an idle host must have left to count
	// toward the floor: one ending sooner is reaped before a replacement
	// could land. Defaults to host.ProvisionLead.
	ProvisionLead time.Duration
}

// PolicyReader answers the operator's autoscaler intent, already defaulted and
// bounds-checked: a missing, malformed or out-of-range value has become the
// compiled default before it reaches here, which for the mode is off. The
// settings service satisfies it; the composition root wires it in, so this job
// still imports no feature.
type PolicyReader interface {
	GetFleetPolicy(ctx context.Context) (hostconfig.FleetPolicy, error)
}

// StandbyCounter counts what every pool holds: inventory a create can count on,
// and inventory whose lease is running out from under it. Both are fleet-class
// only. A host somebody created by hand is neither the keeper's to count nor
// its to replace. The host feature's repository satisfies it.
//
// One method returning every pool's pair, because they are complements about a
// single lease cutoff. Two calls were two cutoffs, and a host expiring between
// them counted twice or not at all, in the two numbers this cycle publishes
// side by side. A call per pool would be the same defect once per pool, plus a
// statement count that grew with the fleet's shape.
type StandbyCounter interface {
	StandbyInventory(ctx context.Context, lead time.Duration) (host.FleetInventory, error)
}

// DemandReader reads the misses a horizon holds, each with the facts the
// placement resolver froze about it. The Spanner audit repository satisfies it.
//
// Facts rather than bare moments, because which of them standing capacity could
// have absorbed is a domain rule and not a query: it decides the target here and
// the split an operator reads on the card, and one read at one timestamp is what
// stops those two disagreeing.
type DemandReader interface {
	Demand(ctx context.Context, since time.Time) (hostconfig.DemandWindow, error)
}

// WithDemandReader wires the placement-miss signal. Without it the target
// composes from standing intent alone, which is what a deployment that has not
// enabled the signal wants and is also the safe reading: fewer inputs can only
// lower the target.
func WithDemandReader(r DemandReader) Option {
	return func(s *Service) { s.demand = r }
}

// Provisioner provisions one standby host for a pool through the platform's
// declarative path (shape from that pool's policy, ranked zone, Spot per the
// headless default). The composition root adapts the device and host services
// into it, so the keeper never imports the features packages.
//
// The pool is an argument because the machine has to JOIN the pool whose gap
// bought it: a host that landed elsewhere leaves the gap open next cycle and
// reads as surplus where it went, which is a fleet buying and reclaiming real
// VMs forever with nothing reporting it.
type Provisioner interface {
	ProvisionFleetHost(ctx context.Context, pool string) (hostID string, err error)
}

// Rescuer renews the leases of draining fleet hosts, warmest first, and reports
// how many it raised in each pool. The host service satisfies it.
//
// Per pool in both directions, and the RETURN is what makes it so: a deficit
// belongs to a pool, so a renewal that answered it has to say which pool it
// answered. One count for the fleet would be subtracted from a pool that never
// gained anything.
//
// A separate port from Provisioner because they answer differently: one buys
// capacity and one keeps capacity the fleet already paid for, and a deployment
// can reasonably run the second without the first ever being reached.
type Rescuer interface {
	RescueFleetHosts(ctx context.Context, lead time.Duration, deficits map[string]int64) (map[string]int64, error)
}

// WithRescuer wires lease renewal into the deficit path. Without it a deficit is
// met by provisioning alone, which is the behavior that predates this and is
// also the safe reading. A fleet that cannot renew simply buys what it needs.
func WithRescuer(r Rescuer) Option {
	return func(s *Service) { s.rescuer = r }
}

// Shortener lowers the leases of surplus fleet hosts, coldest first within each
// pool, and reports how many it lowered in total. The host service satisfies it.
//
// The surplus arrives per pool and the count comes back for the fleet, and the
// asymmetry is what each answer is for: which machines to take is a per-pool
// decision, while what came back is a counter and a log line. Nothing subtracts
// from it.
//
// The only port here that can shorten a host's life, which is why it is the only
// one gated on Mode.Reclaims rather than Mode.Provisions.
type Shortener interface {
	ShortenSurplusHosts(ctx context.Context, lead time.Duration, surplus map[string]int64, grace time.Duration, baking map[string]int64) (int64, error)
}

// WithShortener wires reclamation. Without it a surplus is simply left to run
// out its own leases, which is what the fleet did before this existed and is the
// safe reading of an unwired dependency.
func WithShortener(sh Shortener) Option {
	return func(s *Service) { s.shortener = sh }
}

// Collapser reclaims dedicated hosts whose group is gone, and reports how many
// leases it lowered. The host service satisfies it.
//
// A third port rather than a second method on Shortener, because the two answer
// to different worlds: the shortener is handed a surplus this cycle computed and
// gives inventory back against a target, while this one is handed a budget and
// finds waste that no target describes. A deployment can reasonably have one
// wired and not the other.
type Collapser interface {
	CollapseEmptyDedicated(ctx context.Context, lead time.Duration, budget int64, grace time.Duration) (int64, error)
}

// WithCollapser wires the dedicated-empty collapse. Without it an empty
// dedicated host runs out its own lease, which is what happened before this
// existed and is the safe reading of an unwired dependency.
func WithCollapser(c Collapser) Option {
	return func(s *Service) { s.collapser = c }
}

// BakeReader reports which disks the warm plane is mid-bake on, host id to the
// incarnation being baked. The warm plane satisfies it.
//
// A second, narrower port onto the same object the keeper already drives as a
// WarmPlane: driving the plane and asking it what it is doing are different
// questions, and a shorten that could not ask would keep choosing the host
// closest to becoming warm.
type BakeReader interface {
	BakeInFlight() map[string]int64
}

// WithBakeReader wires the mid-bake exemption. Without it reclamation still
// works and simply cannot spare a host mid-bake, the same degradation a
// restart causes, since the plane's bookkeeping is in memory.
func WithBakeReader(b BakeReader) Option {
	return func(s *Service) { s.bakes = b }
}

// QuotaReader reads the platform host quota, CCO's own ceiling and the one
// that returns the 429. The quota service satisfies it.
//
// It reads this ceiling and not the GCE-side regional quota the catalog
// models (CPUs, GPU types, addresses): a reserve built against that one would
// guard a limit unrelated to the failure it exists to prevent.
type QuotaReader interface {
	Get(ctx context.Context, scope quota.Scope, scopeID string) (*quota.Quota, error)
}

// WithQuotaReader wires the host-quota reserve. Without it the keeper spends to
// the ceiling, which is what it did before the reserve existed.
func WithQuotaReader(q QuotaReader) Option {
	return func(s *Service) { s.quota = q }
}

// WarmPlane advances the prefetch state machine one step. The warmplane package
// satisfies it; nil leaves the keeper holding the floor and nothing else.
type WarmPlane interface {
	Advance(ctx context.Context)
}

// Option configures a Service.
type Option func(*Service)

// WithLeaderCheck installs a leadership predicate; keeper cycles run only
// while it returns true (or when unset), so just the leader provisions.
func WithLeaderCheck(fn func() bool) Option {
	return func(s *Service) { s.runner.LeaderCheck = fn }
}

// WithARMCapability wires the deployment fact the arm pool's spending gate
// reads: whether an arm64 host can be built here at all. Absent reads as no,
// which keeps the keeper from buying machines that boot nothing.
func WithARMCapability(ready func(context.Context) bool) Option {
	return func(s *Service) { s.armReady = ready }
}

// WithWarmPlane installs the prefetch plane the keeper drives each cycle.
func WithWarmPlane(warm WarmPlane) Option {
	return func(s *Service) { s.warm = warm }
}

// Service is the fleet keeper background job.
type Service struct {
	config      Config
	policy      PolicyReader
	standby     StandbyCounter
	demand      DemandReader
	provisioner Provisioner
	rescuer     Rescuer
	shortener   Shortener
	collapser   Collapser
	quota       QuotaReader
	warm        WarmPlane
	bakes       BakeReader
	armReady    func(context.Context) bool
	logger      *slog.Logger
	runner      telemetry.Runner

	totalTopUps       atomic.Int64
	totalRescues      atomic.Int64
	totalShortens     atomic.Int64
	totalCollapses    atomic.Int64
	totalQuotaBlocked atomic.Int64
	totalFailures     atomic.Int64
}

// New creates the fleet keeper.
func New(config Config, policy PolicyReader, standby StandbyCounter, provisioner Provisioner, logger *slog.Logger, opts ...Option) *Service {
	if config.Interval <= 0 {
		config.Interval = DefaultInterval
	}
	if config.ProvisionLead <= 0 {
		config.ProvisionLead = host.ProvisionLead
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		config:      config,
		policy:      policy,
		standby:     standby,
		provisioner: provisioner,
		logger:      logger,
	}
	s.runner = telemetry.Runner{
		Interval:   config.Interval,
		Cycle:      s.keep,
		Metrics:    telemetry.NewLoopMetrics("fleetkeeper"),
		RunAtStart: true,
	}
	for _, opt := range opts {
		opt(s)
	}
	telemetry.RegisterCounters("fleetkeeper", []telemetry.ObservedCounter{
		{Name: "cco.fleetkeeper.topups", Description: "Standby hosts provisioned to hold the floor.", Value: &s.totalTopUps},
		{Name: "cco.fleetkeeper.rescues", Description: "Draining fleet hosts whose lease was renewed instead of provisioning a replacement.", Value: &s.totalRescues},
		{Name: "cco.fleetkeeper.shortens", Description: "Surplus fleet hosts whose lease was lowered to give capacity back.", Value: &s.totalShortens},
		{Name: "cco.fleetkeeper.collapses", Description: "Empty dedicated hosts whose lease was lowered because the group they were fenced for is gone.", Value: &s.totalCollapses},
		{Name: "cco.fleetkeeper.quota_blocked", Description: "Cycles where the host-quota reserve held back a standby top-up.", Value: &s.totalQuotaBlocked},
		{Name: "cco.fleetkeeper.failures", Description: "Standby top-up provisions that failed.", Value: &s.totalFailures},
	})
	return s
}

// Start begins the keeper loop. It returns immediately; disabled never starts.
func (s *Service) Start(ctx context.Context) {
	if !s.config.Enabled {
		return
	}
	s.runner.Start(ctx)
}

// Stop drains an in-flight cycle and stops the loop.
func (s *Service) Stop() {
	s.runner.Stop()
}

// keep runs one cycle: compose every pool's target from policy and observation,
// advance the warm plane, collapse dedicated hosts whose groups have ended, then
// close each pool's gap either way. A deficit is met first from hosts that
// pool already has and then by provisioning under the fleet's per-cycle cap. A
// surplus is given back by shortening leases under a cap of its own.
//
// That order is the whole shape, and nothing but a failed read of the world
// cuts it short: every actuation this cycle can make is attempted, and what
// failed is reported together at the end.
//
// The pool loop issues NO statements. Every pool's arithmetic is done over rows
// the fixed statement set already returned, and the two actuating ports are
// called once each with a map, so a fleet partitioned into ten pools opens
// exactly the reads a fleet with one does.
//
// Every failure to READ the world provisions nothing: the fail-safe direction is
// always "spend nothing", never "act on a guess". That cuts both ways here, and
// it matters most in the reclamation direction. A cycle that could not
// read the target must not conclude the fleet is spare.
func (s *Service) keep(ctx context.Context) error {
	view, policy, err := s.observe(ctx)
	if err != nil {
		s.logger.Warn("fleet status read failed; provisioning nothing", slog.Any("error", err))
		return err
	}
	status := view.Fleet
	// Warming walks the inventory whether or not the target needs a top-up: a
	// full complement is exactly when there is inventory worth warming. Above the
	// deficit branch for that reason. Below it, a fleet sitting at target would
	// stop warming, which is the steady state. Above the mode gate too: warming
	// spends no money and creates nothing, so it is not what the mode protects.
	//
	// The target gate belongs to WARMING alone, and only reads like a cycle-wide
	// guard. A target of zero is a fleet nobody asked for, and warming one would
	// mean prefetching onto whatever idle hosts happen to exist, including hosts
	// a user created. Reclamation below is the opposite case: a target of zero
	// beside standing inventory is exactly the operator saying "give it all
	// back", which is the loudest reclamation there is.
	walked := false
	if s.warm != nil && status.Target > 0 {
		s.warm.Advance(ctx)
		walked = true
	}
	// Above the deficit branch, and for a reason of its own: a dedicated host is
	// exclusive, so it is not inventory, never enters the arithmetic below, and a
	// fleet BELOW target is exactly when an idle exclusive VM is least
	// affordable: the cycle is about to buy capacity while holding some nobody
	// can reach. Inside the deficit branch it would be skipped in that case,
	// every time.
	s.collapse(ctx, status, policy)
	// Both directions, in the same cycle, because with pools they are no longer
	// alternatives: one pool short beside another with capacity to spare is an
	// ordinary state, and a cycle that only ever did one of the two would leave
	// whichever pool lost the coin toss waiting a cycle for no reason. With one
	// pool they still cannot both happen, because the two are computed from the
	// same status and a pool cannot be short and spare at once, so this is the
	// shape the cycle always had, unrolled.
	//
	// Deficit and surplus come from the domain rather than from one subtraction
	// here, so the operations gate reports the arithmetic this acts on.
	//
	// A failed buy is reported and does not stop the reclamation, for the reason
	// a failed renewal does not stop the buying: what failed is one pool's way of
	// closing one gap, while the reads that produced every other pool's gap
	// succeeded. Abandoning here would leave a fleet whose gpu pool has no
	// capacity to buy reclaiming nothing anywhere, every cycle, for as long as
	// that lasted.
	var topUpErr error
	if deficits := view.Deficits(); len(deficits) > 0 {
		topUpErr = s.topUp(ctx, view, deficits)
	}
	return errors.Join(topUpErr, s.shorten(ctx, view, policy, view.Surpluses(), walked))
}

// topUp meets each pool's deficit: first from hosts that pool already has, then
// by provisioning under the fleet's per-cycle cap and the quota reserve.
//
// Both of those bounds stay GLOBAL. They ration the host quota, which is one
// number for the deployment, so splitting them per pool would strand headroom in
// a pool that had nothing to buy. What the pools do get is the allocation: the
// budget goes to the deepest deficit first, so a pool short by one cannot take
// the cycle from a pool short by ten.
func (s *Service) topUp(ctx context.Context, view hostconfig.FleetView, deficits map[string]int64) error {
	status := view.Fleet
	// This is the one thing off uniquely gates. Every other guard here is
	// arithmetic that happens to reach zero; this one is the operator saying so.
	if !status.Mode.Provisions() {
		return nil
	}
	deficits = s.buyable(ctx, s.rescue(ctx, deficits))
	remaining := int64(0)
	for _, deficit := range deficits {
		remaining += deficit
	}
	if remaining <= 0 {
		return nil
	}
	budget, err := s.provisionBudget(ctx, status, remaining)
	if err != nil {
		return err
	}
	// The numbers logged against a buy are the POOL's, never the fleet's: a line
	// naming one pool beside the deployment's totals reads as an explanation and
	// is not one. It would say a host was bought against a target another pool's
	// machines already met. The pool named is the deficit the buy answers; which
	// pool the machine joins is the stamp its create carries.
	spending := make(map[string]hostconfig.FleetStatus, len(view.Pools))
	for _, pool := range view.Pools {
		spending[pool.Pool] = pool.Status
	}
	// A pool whose buy failed is done for this cycle, since the next attempt
	// would fail the same way and the next cycle re-reads the world before
	// trying again, while the pools the failure is not about still get theirs.
	// A pool with nothing to buy for the shape it needs is allocated FIRST for
	// being the deepest deficit, so abandoning on its failure held every other
	// pool at whatever it was standing at.
	var failures []error
	failed := make(map[string]bool, len(deficits))
	for _, pool := range allocate(deficits, budget) {
		if failed[pool] {
			continue
		}
		id, err := s.provisioner.ProvisionFleetHost(ctx, pool)
		if err != nil {
			s.totalFailures.Add(1)
			failed[pool] = true
			failures = append(failures, fmt.Errorf("provisioning a standby host for the %s pool: %w", pool, err))
			s.logger.Warn("standby top-up provision failed",
				slog.String("pool", pool), slog.Any("error", err))
			continue
		}
		s.totalTopUps.Add(1)
		short := spending[pool]
		s.logger.Info("fleet host provisioned",
			slog.String("host_id", id),
			slog.String("pool", pool),
			slog.Int64("target", short.Target),
			slog.Int64("standby_before", short.Standby),
			slog.Int64("floor", short.Floor),
			slog.Int64("total_before", short.Total),
			slog.String("signal", short.Signal))
	}
	return errors.Join(failures...)
}

// buyable drops the deficits provisioning cannot answer.
//
// A pool whose machines cannot be built is the case: the buy would fail every
// cycle, or worse land the host somewhere else, and then the gap that bought it
// stands unchanged next cycle while the machine reads as surplus where it went.
// That is a fleet creating and reclaiming real VMs forever with nothing
// reporting it, since a deficit that never closes is not an error condition
// anywhere.
//
// Renewal is not filtered this way. It raises the lease of a host
// already standing in the pool that is short, so it answers that pool's own gap
// whichever pool it is.
func (s *Service) buyable(ctx context.Context, deficits map[string]int64) map[string]int64 {
	// Read once per cycle, and only when an arm gap could spend it: the fact
	// changes on operator timescales and the probe behind it may read storage.
	armReady := false
	if _, short := deficits[host.PoolARM]; short && s.armReady != nil {
		armReady = s.armReady(ctx)
	}
	answerable := make(map[string]int64, len(deficits))
	for pool, deficit := range deficits {
		// A gap renewal has already closed is not a gap: the entry survives the
		// subtraction at zero, and saying nothing can buy for it would report a
		// pool that has just been answered.
		if deficit <= 0 {
			continue
		}
		if !host.PoolProvisionable(pool, armReady) {
			s.logger.Warn("a pool is short and no machine can be built for it",
				slog.String("pool", pool), slog.Int64("deficit", deficit))
			continue
		}
		answerable[pool] = deficit
	}
	return answerable
}

// allocate hands the cycle's provisioning budget out one host at a time, always
// to the pool with the deepest remaining deficit, and returns the pools in the
// order they are bought for.
//
// Largest-first rather than a share each, because the budget is often smaller
// than one pool's gap: an even split would buy a machine for a pool short by one
// while the pool short by ten converged a host per cycle. Pools level with each
// other split the rest, with a tie broken by who has been served least this
// cycle, so two equally short pools converge together instead of one absorbing
// the whole tail. A cycle with nothing left to choose between spends in name
// order, so it still spends the same way twice.
func allocate(deficits map[string]int64, budget int64) []string {
	remaining := maps.Clone(deficits)
	pools := slices.Sorted(maps.Keys(remaining))
	served := make(map[string]int64, len(remaining))
	bought := make([]string, 0, budget)
	for range budget {
		next := ""
		for _, pool := range pools {
			switch {
			case remaining[pool] <= 0:
			case next == "" || remaining[pool] > remaining[next]:
				next = pool
			case remaining[pool] == remaining[next] && served[pool] < served[next]:
				next = pool
			}
		}
		if next == "" {
			break
		}
		remaining[next]--
		served[next]++
		bought = append(bought, next)
	}
	return bought
}

// shorten gives a surplus back by lowering leases, capped per cycle.
//
// Gated on Reclaims and not Provisions: this is the only thing the keeper does
// that can shorten a host's life, and it is what separates `on` from
// `provision_only`. Nothing is deleted here. A shortened lease reaches the
// reaper sooner, and the reaper remains the only thing that removes a host.
//
// walked reports whether this cycle drove the warm plane, and the mid-bake
// exemption is read ONLY when it did. The in-flight set is evidence that a host
// is about to become warm; it stays evidence only while the walk that produces
// it is running. A target of zero stops the walk, and a target of zero beside
// standing inventory is the loudest reclamation there is, so the set frozen at
// that moment would veto exactly the hosts the operator asked for, and being
// in-memory it would go on doing so until the process restarted.
func (s *Service) shorten(ctx context.Context, view hostconfig.FleetView, policy hostconfig.FleetPolicy, surpluses map[string]int64, walked bool) error {
	status := view.Fleet
	if s.shortener == nil || len(surpluses) == 0 || !status.Mode.Reclaims() {
		return nil
	}
	// The cap is per pool, where the provisioning budget is global, and the
	// asymmetry follows what each bound protects. The provisioning budget rations
	// a quota the whole deployment shares; this one bounds how fast capacity is
	// GIVEN BACK, which is a promise made to each pool separately. A fleet
	// draining one pool must not stop reclaiming another's surplus for as long as
	// that lasts.
	capped := make(map[string]int64, len(surpluses))
	for pool, surplus := range surpluses {
		capped[pool] = min(surplus, MaxShortensPerCycle)
	}
	var baking map[string]int64
	if s.bakes != nil && walked {
		baking = s.bakes.BakeInFlight()
	}
	shortened, err := s.shortener.ShortenSurplusHosts(ctx, s.config.ProvisionLead,
		capped, drainGrace(policy), baking)
	s.totalShortens.Add(shortened)
	if err != nil {
		s.logger.Warn("shortening surplus fleet hosts failed",
			slog.Any("error", err), slog.Int64("shortened", shortened))
		return fmt.Errorf("shortening surplus fleet hosts: %w", err)
	}
	if shortened > 0 {
		s.logger.Info("surplus fleet hosts shortened; both bounds still hold",
			slog.Int64("shortened", shortened),
			slog.Int64("surplus", view.Surplus()),
			slog.Int64("target", status.Target),
			slog.Int64("standby_before", status.Standby),
			slog.Int64("floor", status.Floor),
			slog.Int64("total_before", status.Total))
	}
	return nil
}

// collapse reclaims empty dedicated hosts, under a budget of its own.
//
// Gated on Reclaims for the same reason the surplus is: it lowers a lease, which
// is the whole of what separates `on` from `provision_only`. That gate is easy
// to leave off and impossible to notice: the mode assertions on the surplus arm
// count calls to the SHORTENER, so a collapse arriving through a different port
// satisfies every one of them while giving hosts back under a mode that may only
// add them.
//
// A failure does not abandon the cycle, matching the renewal: the reads that
// produced the target succeeded, so the deficit below is still true, and a fleet
// under target must not stop buying capacity because an unrelated reclamation
// could not run. Nothing here is a target the rest of the cycle depends on.
func (s *Service) collapse(ctx context.Context, status hostconfig.FleetStatus, policy hostconfig.FleetPolicy) {
	if s.collapser == nil || !status.Mode.Reclaims() {
		return
	}
	collapsed, err := s.collapser.CollapseEmptyDedicated(ctx, s.config.ProvisionLead,
		MaxCollapsesPerCycle, drainGrace(policy))
	s.totalCollapses.Add(collapsed)
	if err != nil {
		s.logger.Warn("collapsing empty dedicated hosts failed; the rest of the cycle continues",
			slog.Any("error", err), slog.Int64("collapsed", collapsed))
	}
	if collapsed > 0 {
		s.logger.Info("empty dedicated hosts collapsed",
			slog.Int64("collapsed", collapsed))
	}
}

// drainGrace is the lease a shortened host keeps. The policy reader has already
// bounds-checked it, so this only catches a zero reaching here some other way.
// It is worth catching, because a zero grace would write an expiry of now
// and hand the whole surplus to the reaper immediately.
func drainGrace(policy hostconfig.FleetPolicy) time.Duration {
	sec := policy.DrainGraceSec
	if sec <= 0 {
		sec = hostconfig.DefaultDrainGraceSec
	}
	return time.Duration(sec) * time.Second
}

// rescue meets what it can of the deficit from the fleet's own draining hosts
// and returns what is left.
//
// A failure here does NOT abandon the cycle, which is the one place this job
// departs from "any failure provisions nothing", and the difference is what
// failed. The reads that produced the deficit succeeded, so the deficit is
// still true; what failed is one way of meeting it, and the other way is still
// capped and still gated by the mode. Abandoning would leave a fleet below
// target for as long as renewal kept failing. The count that comes back with
// the error is what actually committed, so a partial renewal is subtracted
// rather than provisioned over.
func (s *Service) rescue(ctx context.Context, deficits map[string]int64) map[string]int64 {
	if s.rescuer == nil {
		return deficits
	}
	rescued, err := s.rescuer.RescueFleetHosts(ctx, s.config.ProvisionLead, deficits)
	if err != nil {
		s.logger.Warn("renewing draining fleet hosts failed; provisioning the remainder",
			slog.Any("error", err), slog.Any("rescued", rescued))
	}
	remaining := maps.Clone(deficits)
	for pool, count := range rescued {
		s.totalRescues.Add(count)
		if count <= 0 {
			continue
		}
		remaining[pool] = max(0, remaining[pool]-count)
		s.logger.Info("draining fleet hosts renewed toward the target",
			slog.String("pool", pool),
			slog.Int64("rescued", count), slog.Int64("deficit", deficits[pool]))
	}
	return remaining
}

// provisionBudget is how many hosts this cycle may create: the remaining
// deficit, capped per cycle, and capped again by the quota the buffer must not
// spend to the last slot.
//
// The per-cycle cap is a share of the fleet this cycle already counted, which is
// why it takes the status rather than reading anything: the one inventory
// statement the cycle opens is the source for the target, the deficit and this
// bound alike.
//
// The quota read is not transactional and does not need to be. The atomic check
// inside the create commit is still the real ceiling; this is a priority policy
// about who gets the last slots, and a policy one host stale for one cycle is
// still the policy. What it must not do is read the wrong ceiling; see
// QuotaReader.
func (s *Service) provisionBudget(ctx context.Context, status hostconfig.FleetStatus, deficit int64) (int64, error) {
	budget := min(deficit, hostconfig.TopUpBudget(status.Total))
	if s.quota == nil {
		return budget, nil
	}
	q, err := s.quota.Get(ctx, quota.ScopePlatform, quota.GlobalScopeID)
	if err != nil {
		return 0, fmt.Errorf("reading the host quota: %w", err)
	}
	spendable := q.Available() - QuotaReserveHosts
	if spendable <= 0 {
		s.totalQuotaBlocked.Add(1)
		s.logger.Warn("standby top-up held back: the host quota's last slots are reserved for user creates",
			slog.Int64("available", q.Available()),
			slog.Int64("reserve", QuotaReserveHosts))
		return 0, nil
	}
	return min(budget, spendable), nil
}

// observe composes this instant's fleet picture (the policy, what the fleet
// holds, and the demand the horizon can see) and returns the policy beside it.
//
// Both, because the cycle needs one field the published status does not
// carry: the drain grace is a settings-page value the card does not show, and
// reading the policy a second time to get it would let one cycle act on two
// different policies.
//
// It reads fresh state on every call. The keeper cycles on the leader
// alone, so anything served from remembered state would be empty on every other
// instance and up to a cycle stale on that one. That is also why the
// operations card composes its own picture through the same pure
// hostconfig.ComposeFleet rather than asking the keeper for this one.
func (s *Service) observe(ctx context.Context) (hostconfig.FleetView, hostconfig.FleetPolicy, error) {
	// One instant for the whole cycle. The demand window and the schedule
	// windows are two views of the same moment, and a second clock read between
	// them lets a cycle straddle a window edge, reading demand from before it
	// opened and schedules from after it closed.
	now := time.Now()
	policy, err := s.policy.GetFleetPolicy(ctx)
	if err != nil {
		return hostconfig.FleetView{}, policy, fmt.Errorf("reading the fleet policy: %w", err)
	}
	inventory, err := s.standby.StandbyInventory(ctx, s.config.ProvisionLead)
	if err != nil {
		return hostconfig.FleetView{}, policy, fmt.Errorf("counting standing inventory: %w", err)
	}
	window, err := s.demandWindow(ctx, policy, now)
	if err != nil {
		return hostconfig.FleetView{}, policy, err
	}
	if window.Partial {
		s.logger.Warn("demand window held more misses than one read returns; the target is a lower bound",
			slog.Int("sample", hostconfig.MaxDemandSample))
	}
	return hostconfig.ComposeFleet(policy, now, inventory, window), policy, nil
}

// demandWindow reads the demand horizon, or nothing when the signal is disabled
// or unwired.
func (s *Service) demandWindow(ctx context.Context, policy hostconfig.FleetPolicy, now time.Time) (hostconfig.DemandWindow, error) {
	since := policy.DemandSince(now)
	if s.demand == nil || since.IsZero() {
		return hostconfig.DemandWindow{}, nil
	}
	window, err := s.demand.Demand(ctx, since)
	if err != nil {
		return hostconfig.DemandWindow{}, fmt.Errorf("reading the demand window: %w", err)
	}
	return window, nil
}
