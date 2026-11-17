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

package hostconfig

import (
	"slices"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// MaxStandbyHosts bounds the standby-host count an operator can set, and is the
// FLOOR of the bound on the composed target the autoscaler acts on. Shared by
// the settings write validation, the target composition, and the keeper's own
// read: a count past the bound, however it got stored, reads as off, because
// the fail-safe direction for a loop that spends money is always spend-nothing.
//
// It stays whole for what an operator STORES. Standing intent and schedule
// windows are numbers somebody typed against a fleet they can see, and a
// deployment that wants a wider buffer than sixteen on a fleet of ten has made
// a mistake rather than an observation. StandbyCap is what the third input,
// demand nobody typed, is allowed to reach.
const MaxStandbyHosts = 16

// MaxFleetFloor bounds the floor, the machines the fleet keeps running whether
// or not devices fill them.
//
// Its own name because it bounds a different contract, and defined as the
// standby bound rather than as a second 16 so the two can never be edited apart:
// both answer "how large may an operator ask this fleet to be", and a deployment
// that raised one alone would let the buffer overrun the floor's ceiling or the
// reverse.
//
// It is NOT StandbyCap, and the asymmetry follows from arithmetic. The buffer
// asks for a SUBSET of the fleet (idle hosts inside a total that demand drives)
// so a bound proportional to that total settles at a fraction of it. The floor
// asks for the total itself, so the same bound would clamp every stored floor
// to an eighth of what is already standing, which is always already satisfied.
// A proportional bound there would delete the contract.
const MaxFleetFloor = MaxStandbyHosts

// StandbyCapFleetShare and MinTopUpsPerCycle/TopUpFleetShare make the keeper's
// two blast-radius bounds proportional to the fleet they bound, so the property
// they were sized for survives at any fleet size instead of only at a fleet of
// ten.
//
// StandbyCapFleetShare bounds the LEVEL: the buffer may never exceed an eighth
// of the fleet standing. That converges rather than compounding, which is what
// makes a proportional cap safe here. A demand signal stuck at maximum drives
// the buffer to S where S = (occupied + S) / 8, so S settles at a seventh of the
// hosts real work occupies and the fleet can never exceed 8/7 of it.
//
// TopUpFleetShare bounds the RATE, and is tighter than the level on purpose: a
// per-cycle budget as wide as the level cap could never bind, which is exactly
// the case it exists for. A thirty-second per thirty-second cycle grows the
// fleet by ~3% a cycle, so a fleet of thousands is built in an hour or two where
// four hosts a minute took days, while one cycle still cannot insert more than
// a rounding error's worth of VMs.
const (
	StandbyCapFleetShare = 8
	MinTopUpsPerCycle    = 2
	TopUpFleetShare      = 32
)

// StandbyCap is the ceiling on the composed standby target: MaxStandbyHosts, or
// a share of the fleet already standing, whichever is larger.
//
// fleet is passed in rather than read, because this package sees no repository.
// The caller composing a status already holds the number, and the one caller
// that does not is not entitled to an opinion about it.
func StandbyCap(fleet int64) int64 {
	return max(MaxStandbyHosts, fleet/StandbyCapFleetShare)
}

// TopUpBudget is how many hosts one keeper cycle may provision: MinTopUpsPerCycle,
// or a share of the fleet standing, whichever is larger.
//
// Homed here beside the target it converges toward, rather than in the keeper,
// because two consumers act on it: the loop that provisions, and the page that
// explains why a large deficit is closing slowly. A number the page re-derived
// from a floor and a share handed over as data is two derivations of one rule,
// which is how a card ends up disagreeing with the loop it draws.
func TopUpBudget(fleet int64) int64 {
	return max(MinTopUpsPerCycle, fleet/TopUpFleetShare)
}

// The demand horizon: how far back the miss signal looks. 0 disables the
// signal entirely rather than meaning "no history", which is why the lower
// bound is 0 and not something positive.
const (
	MinDemandHorizonSec     = 0
	MaxDemandHorizonSec     = 86400
	DefaultDemandHorizonSec = 3600
)

// The drain grace: how much lease a host shortened as surplus keeps.
//
// The upper bound is structural rather than taste. A shortened host still
// counts as standby while its remaining lease exceeds host.ProvisionLead, so a
// grace at or above the lead would leave the host in the standby count after
// being shortened, so the surplus loop would see no change, shorten another,
// and walk the whole fleet. The bound is stated as a value here because domain
// packages do not import each other for constants; the agreement test in the
// settings slice pins it against host.ProvisionLead.
const (
	MinDrainGraceSec     = 120
	MaxDrainGraceSec     = 599
	DefaultDrainGraceSec = 480
)

// HostDensity is the device capacity of the machine the fleet builds by
// default: the composed fit of the catalog's default shape (its cores and
// memory, or the operator's per-shape override) and the shape's name beside
// it, because a capacity without its machine is a number nobody can check.
//
// Homed here rather than in the catalog slice because two features read it:
// the settings surface that names it, and the autoscaler page whose whole job
// is turning a target in HOSTS into capacity in DEVICE SLOTS.
type HostDensity struct {
	// Effective is what an auto-provisioned default-shape host is packed to.
	// Anything reporting standing capacity multiplies by THIS.
	Effective int64
	// MachineType names the default shape the capacity is about.
	MachineType string
}

// AutoscaleMode is how much of the fleet autoscaler is switched on. It is the
// reversibility switch: whatever goes wrong, setting off stops it.
type AutoscaleMode string

const (
	// ModeOff computes and publishes the target and provisions nothing. It is
	// what makes an observation week measure unbuffered demand, and it is what
	// off means permanently. This meaning never changes again.
	ModeOff AutoscaleMode = "off"
	// ModeProvisionOnly may raise a lease and provision toward the target, and
	// may never lower a lease.
	ModeProvisionOnly AutoscaleMode = "provision_only"
	// ModeOn may also lower a lease, which is how capacity is given back.
	ModeOn AutoscaleMode = "on"
)

// AutoscaleModes is the vocabulary, in increasing order of what it permits.
// The API enum tags are a second copy of it, bound by an agreement test.
var AutoscaleModes = []AutoscaleMode{ModeOff, ModeProvisionOnly, ModeOn}

// Valid reports whether m is one of the known modes.
func (m AutoscaleMode) Valid() bool {
	return m == ModeOff || m == ModeProvisionOnly || m == ModeOn
}

// Provisions reports whether this mode may add capacity toward the target.
//
// It gates both scale-out verbs, which is why it is not named for creation
// alone: buying a host and renewing the lease of one already draining are the
// same permission, because neither can ever leave the fleet with less than it
// had. provision_only and on are equivalent here and diverge only where a lease
// is LOWERED, which is Reclaims below.
func (m AutoscaleMode) Provisions() bool {
	return m == ModeProvisionOnly || m == ModeOn
}

// Reclaims reports whether this mode may lower a lease: shorten a surplus
// host, or collapse an empty dedicated one. Only on may.
func (m AutoscaleMode) Reclaims() bool { return m == ModeOn }

// DefaultTier is the pool every host belongs to until a create says otherwise,
// and the one key an unconfigured policy populates.
//
// The host aggregate's own constant rather than a second literal of it. A
// policy names pools that hold no machines and the fleet holds machines no
// policy configured, so the two vocabularies meet on every keeper cycle: spelled
// twice, a rename that moved one alone would judge the fleet's real machines
// against bounds nobody set while buying forever for a pool nothing joins.
const DefaultTier = host.PoolDefault

// FleetTier is one pool's standing-capacity intent: the two lower bounds the
// keeper holds that pool to, the windows that raise one of them, and the shapes
// the pool buys when it does.
//
// Every bound here is evaluated against the pool's OWN inventory. That is what
// makes a pool a partition rather than a label: a gpu pool short of its floor
// buys a gpu machine while the default pool sits at target, and neither pool's
// arithmetic can be satisfied by the other's machines.
type FleetTier struct {
	// Standby is the idle placement-eligible hosts this pool holds, before any
	// other signal raises the target. 0 is off for the pool.
	Standby int64 `json:"standby" doc:"Idle placement-eligible hosts this pool holds (0 = off)"`

	// Floor is the second contract, and the independent one: hosts that stand
	// whether or not devices fill them.
	//
	// Standby above sells "keep N hosts FREE": a create that takes one buys a
	// replacement, which bounds latency at the price of a pool always N larger
	// than usage. This one sells "keep N hosts RUNNING", which bounds cost: a
	// create landing on one triggers nothing while the count holds, and giving
	// capacity back stops here. Two free hosts satisfy both a floor of 2 and a
	// standby of 2 on their own, because they are lower bounds in different
	// dimensions, never addends.
	Floor int64 `json:"floor" doc:"Hosts this pool keeps standing whether or not devices fill them; scale-in stops here (0 = none)"`

	// Ladder is the pool's machine-type preference order for provisioning: rung
	// 0 is the shape the pool wants, and provisioning steps down the list when
	// GCE has no capacity for the one above. Empty is the catalog default and
	// zone failover alone.
	//
	// A property of the pool rather than a shared object pointed at, because
	// which rungs are admissible DEPENDS on the pool: bare metal belongs to a
	// pool whose machines are bare metal and nowhere else, and an
	// integrated-GPU shape would attach an accelerator to a CPU pool's host.
	// One list valid in one pool and refused in another cannot be one list.
	Ladder []string `json:"ladder,omitempty" required:"false" doc:"Machine types this pool's provisioning prefers, in order; empty means the catalog default"`

	// Schedules raise the pool's target across recurring wall-clock windows.
	// Empty is the ordinary state: standing intent and observed demand answer
	// the question until an operator knows something they do not.
	Schedules []FleetSchedule `json:"schedules,omitempty" required:"false" doc:"Recurring windows that raise this pool's target while they are open"`
}

// FleetPolicy is the operator's autoscaler intent. Homed here rather than in
// the settings slice because three consumers must agree on it: the endpoint that
// stores it, the control-plane card that reports it, and the keeper that acts
// on it.
type FleetPolicy struct {
	Mode  AutoscaleMode        `json:"mode" enum:"off,provision_only,on" doc:"How much of the autoscaler is switched on: off publishes a target and provisions nothing, provision_only may only add capacity, on may also give it back"`
	Tiers map[string]FleetTier `json:"tiers" doc:"Standing-capacity intent per pool, keyed by pool name; the default pool is always present"`

	// DemandHorizonSec is how far back the miss signal looks; 0 disables it.
	DemandHorizonSec int64 `json:"demand_horizon_sec" doc:"How far back the demand signal looks for placement misses, in seconds (0 disables it)"`

	// DrainGraceSec is the lease a host keeps when it is shortened as surplus.
	DrainGraceSec int64 `json:"drain_grace_sec" doc:"Lease (seconds) a host keeps when it is shortened as surplus"`

	// Floor and ShapeLadder are the DEFAULT pool's two bounds, published beside
	// the map for the surfaces that ask about a fleet rather than about a pool:
	// the machines they describe are the ones every create still lands on.
	//
	// Filled from Tiers[DefaultTier] by whoever reads the policy, and read by
	// nothing that acts. The keeper holds each pool to the bounds on that pool's
	// own tier, so a value written here alone changes no decision.
	Floor       int64    `json:"floor" doc:"The default pool's floor: hosts that stand whether or not devices fill them; scale-in stops here (0 = none)"`
	ShapeLadder []string `json:"shape_ladder,omitempty" required:"false" doc:"The default pool's machine-type preference order; empty means the catalog default"`

	// IsSet reports whether an operator stored the whole policy; false means at
	// least one field carries its compiled default, so a console can say
	// "compiled default" rather than implying a choice.
	IsSet bool `json:"is_set,omitempty" doc:"Whether an operator stored this policy; false means at least one field is the compiled default"`
}

// DefaultFleetPolicy is the compiled policy: what every consumer lands on when
// no operator stored one, and what a malformed one degrades to. Off, with no
// standing capacity, which is the answer that spends nothing.
func DefaultFleetPolicy() FleetPolicy {
	return FleetPolicy{
		Mode:             ModeOff,
		Tiers:            map[string]FleetTier{DefaultTier: {}},
		DemandHorizonSec: DefaultDemandHorizonSec,
		DrainGraceSec:    DefaultDrainGraceSec,
	}
}

// Pool is one pool's intent, and the zero intent for a pool the policy does not
// configure, which is standby 0, floor 0, no windows and the catalog default
// shape. A pool nobody configured asks for nothing, which is the answer that
// spends nothing.
func (p FleetPolicy) Pool(name string) FleetTier { return p.Tiers[name] }

// EnabledPools are the pools this deployment offers, in name order.
//
// A pool is enabled by being configured: an operator gives it bounds and it
// becomes a pool creates are routed into, and taking those bounds away is what
// turns it off again. Nothing else is stored, because a registry beside the
// bounds would be a second answer to the same question and the two would
// disagree the first time one was edited alone.
//
// The default pool is always among them, whatever is stored. It is where every
// machine naming no pool belongs, so it is not a pool an operator can withdraw.
func (p FleetPolicy) EnabledPools() []string {
	pools := make([]string, 0, len(p.Tiers)+1)
	for name := range p.Tiers {
		if name != DefaultTier {
			pools = append(pools, name)
		}
	}
	slices.Sort(pools)
	return append([]string{DefaultTier}, pools...)
}

// Signal names which input produced a buy, so an operator watching the card can
// tell a bound being held from demand being answered.
const (
	SignalStandby  = "standby"
	SignalSchedule = "schedule"
	SignalDemand   = "demand"
	// SignalFloor is the one signal that is not an input to the target: it says
	// the buffer was satisfied and the fleet is being grown anyway, because the
	// floor asks for more hosts than are standing. Attributed separately because
	// an operator who set both bounds cannot otherwise tell which one is spending.
	SignalFloor = "floor"
)

// Signals is the published vocabulary. The FleetStatus enum tag below is a
// second copy of it, bound by an agreement test.
var Signals = []string{SignalStandby, SignalSchedule, SignalDemand, SignalFloor}

// LimitStandbyCap is the only limit a target can report today: the composed
// value exceeded StandbyCap and was clamped. Reported rather than applied
// silently, because a fleet pinned at its cap while demand asks for three times
// it is a fact an operator needs and cannot otherwise see.
const LimitStandbyCap = "max_standby"

// TargetSignals are the inputs beyond the policy's own standing intent:
// whatever schedule window is open, and the peak-window miss count.
type TargetSignals struct {
	Schedule int64
	Demand   int64
}

// TargetResult is the composed answer: the number, and enough about how it was
// reached that the card does not have to re-derive it.
type TargetResult struct {
	Target int64
	// Signal is the input that won. Ties go to the cheaper explanation, in
	// standby → schedule → demand order, so a target equal to the floor reads
	// as the floor rather than as demand that happens to match.
	Signal string
	// LimitedBy is empty unless a bound clipped the composed value.
	LimitedBy string
}

// FleetStatus is the autoscaler's published picture: the number it reached,
// what the fleet actually holds, and enough of the derivation that an operator
// can tell a held floor from answered demand without re-deriving anything.
//
// It is composed from observations passed in, never from remembered state, so
// the operations card and the keeper produce identical numbers on any
// instance. The keeper runs on the leader alone, and a card served from the
// leader's memory would be blank on every other instance and stale on that one.
type FleetStatus struct {
	Mode     AutoscaleMode `json:"mode" enum:"off,provision_only,on" doc:"How much of the autoscaler is switched on"`
	Target   int64         `json:"target" doc:"Standby hosts the autoscaler is aiming for right now"`
	Standby  int64         `json:"standby" doc:"Fleet-class hosts currently counting as standby inventory"`
	Draining int64         `json:"draining" doc:"Fleet-class hosts whose remaining lease is too short to count as standby"`
	// Floor and Total are the second contract and what it is measured against.
	// Published beside the buffer's pair rather than folded into them: the two
	// bounds are independent, and a surface holding only the buffer's numbers
	// cannot explain a fleet that stopped shrinking or a host bought while the
	// buffer was full.
	Floor int64 `json:"floor" doc:"Fleet-class hosts that stand whether or not devices fill them; scale-in stops here"`
	Total int64 `json:"total" doc:"Fleet-class hosts standing right now, occupied or free; what the floor is held against"`
	// Misses counts only misses standing capacity could have absorbed. Demand
	// this fleet cannot serve (a required GPU, a shape the default machine
	// cannot hold) is real and is left out on purpose: buying hosts that
	// cannot serve it would spend money to change nothing.
	Misses int64 `json:"misses" doc:"Peak concurrent servable placement misses observed in the demand horizon"`
	// Demand attributes every miss the same window held, including the ones
	// Misses excludes. Published because the alternative is an
	// operator watching a horizon of real misses move nothing and having no way
	// to tell that from a signal that has stopped working.
	Demand DemandSplit `json:"demand" doc:"Every miss the demand window held, attributed: what standing capacity could have absorbed and what it could not"`
	// Schedule is what the open windows asked for at this instant, 0 when none
	// is open. Published because the target is a max over three inputs and only
	// the winner is otherwise visible: a surface that showed the winner alone
	// could not tell a schedule that lost from one that was never configured,
	// and re-deriving it client-side would put wall-clock arithmetic across
	// daylight-saving boundaries in a second place.
	Schedule  int64  `json:"schedule" doc:"Hosts the open schedule windows asked for at this instant (0 = none open)"`
	Signal    string `json:"signal" enum:"standby,schedule,demand,floor" doc:"Which input is spending: one of the three the target composes from, or the floor when it alone forces a host to be bought"`
	LimitedBy string `json:"limited_by,omitempty" doc:"Set when a compiled bound clipped the target"`
	// Partial reports that the demand read hit its sample bound, so Misses, and
	// any target derived from it, is a floor rather than the peak.
	Partial bool `json:"partial,omitempty" doc:"The demand window held more misses than one read returns, so misses is a lower bound"`
}

// ComposeStatus builds one pool's published picture from the policy and this
// cycle's observations. Pure, and the single composition both the keeper and the
// operations card call: two derivations of one number is how a card ends up
// disagreeing with the loop it describes.
//
// The inventory is that pool's own, and every bound is judged against it: the
// target's ceiling is a share of the machines standing IN THIS POOL, and the
// floor is the pool's. A pool whose machines another pool could not serve must
// not have its arithmetic met by them.
//
// now is passed rather than read so the whole composition stays a function of
// its arguments. The schedule signal turns on wall-clock arithmetic across
// daylight-saving boundaries, and a rule about which side of a transition a
// window falls on is not testable against whatever instant the suite happens to
// run at.
func ComposeStatus(policy FleetPolicy, pool string, now time.Time, inventory host.StandbyInventory, window DemandWindow) FleetStatus {
	tier := policy.Pool(pool)
	misses := int64(0)
	if policy.DemandHorizonSec > 0 {
		// ServableTimes, never every miss in the window. The target is a number
		// of standby hosts, so it may only be raised by demand a standby host
		// could have answered. Anything else buys capacity that would have
		// missed placement just the same.
		misses = PeakConcurrent(window.ServableTimes(), host.ProvisioningLeg)
	}
	signals := TargetSignals{
		Schedule: ScheduleHosts(tier.Schedules, now),
		Demand:   misses,
	}
	// The pool already standing is what the target's ceiling is a share of, and
	// it arrives with the inventory this composition was handed. Nothing here
	// reads it, which is what keeps the whole function pure.
	result := Target(tier, signals, inventory.Total)
	status := FleetStatus{
		Mode:     policy.Mode,
		Target:   result.Target,
		Standby:  inventory.Standby,
		Draining: inventory.Draining,
		Floor:    boundedFloor(tier.Floor),
		Total:    inventory.Total,
		Misses:   misses,
		Schedule: signals.Schedule,
		// The whole window, whatever the horizon setting did to the target above:
		// this describes what was read, and reporting nothing here when the signal
		// is off would be indistinguishable from a horizon that saw nothing.
		Demand:    window.Split(),
		Signal:    result.Signal,
		LimitedBy: result.LimitedBy,
		Partial:   window.Partial,
	}
	// The floor is a signal so it can take the attribution. With the buffer
	// met and the floor short, the target explains nothing about the host that
	// is about to be bought.
	//
	// Strictly greater, so a floor and a buffer asking for the same host leave
	// the attribution with the target. That is the same tie rule Target applies,
	// for the same reason: the cheaper explanation wins.
	if status.floorShortfall() > status.bufferShortfall() && status.floorShortfall() > 0 {
		status.Signal = SignalFloor
	}
	return status
}

// boundedFloor clamps a floor the evaluation was handed.
//
// Third copy of the bound by design (the settings write refuses one out of
// range and the policy read fail-safes it to 0) because a value that reached
// here some other way must still not be able to spend money.
func boundedFloor(floor int64) int64 {
	return min(max(0, floor), MaxFleetFloor)
}

// floorShortfall and bufferShortfall are the two contracts' unmet halves: hosts
// the floor is missing, and free hosts the buffer is missing. Either may be
// negative, which is slack rather than a shortfall.
func (s FleetStatus) floorShortfall() int64  { return s.Floor - s.Total }
func (s FleetStatus) bufferShortfall() int64 { return s.Target - s.Standby }

// Deficit is what this cycle must add: the larger of the two contracts'
// shortfalls, never their sum.
//
// Max and not a sum because they are bounds in different dimensions on one
// population (two free hosts satisfy a floor of 2 and a buffer of 2 at the same
// time) so adding them would buy the fleet twice.
func (s FleetStatus) Deficit() int64 {
	return max(0, s.floorShortfall(), s.bufferShortfall())
}

// Surplus is what this cycle may give back: capacity NEITHER contract needs.
//
// The min, and only while both terms are positive: a host may be released only
// when the fleet would still hold its floor without it AND the buffer would
// still be met. "Scale-in stops at the floor" is the floor's whole promise, and
// a surplus computed from the buffer alone breaks it in the one direction that
// costs a create the full provisioning wait.
func (s FleetStatus) Surplus() int64 {
	return max(0, min(-s.floorShortfall(), -s.bufferShortfall()))
}

// PoolStatus is one pool's picture, named: the unit the keeper acts on and the
// unit the page draws a region for.
//
// It carries no wire tags, and neither does the view below. FleetStatus inside
// it serializes wherever it is published, but these two are what a caller
// composes from: the page's per-pool region adds the gap and the reason to
// each entry, and a second serialized shape of the same pools would be a wire
// nobody serves.
type PoolStatus struct {
	Pool   string
	Status FleetStatus
}

// FleetView is this instant's whole answer: one picture per pool, the default
// pool first and the rest in name order, and the deployment's own summed picture
// beside them.
//
// Both, because they answer different questions. Every decision the keeper takes
// belongs to a pool (a machine spare in one does not meet another's deficit)
// while the host quota those decisions spend against, and the per-cycle budget
// that rations it, are one number for the whole deployment.
type FleetView struct {
	Pools []PoolStatus
	Fleet FleetStatus
}

// ComposeFleet builds the whole view: every pool the policy configures or the
// fleet holds machines in, each judged against its own inventory and its own
// share of the demand window.
//
// One pass over inputs already read. The grouping walks the rows the inventory
// statement returned and the misses the demand read returned, so a pool costs
// result size and never a round trip.
func ComposeFleet(policy FleetPolicy, now time.Time, inventory host.FleetInventory, window DemandWindow) FleetView {
	demand := window.ByPool()
	names := poolNames(policy, inventory)
	view := FleetView{Pools: make([]PoolStatus, 0, len(names))}
	for _, pool := range names {
		view.Pools = append(view.Pools, PoolStatus{
			Pool:   pool,
			Status: ComposeStatus(policy, pool, now, inventory[pool], demand[pool]),
		})
	}
	view.Fleet = foldStatuses(policy.Mode, view.Pools)
	return view
}

// poolNames are the pools this instant decides about, the default first and the
// rest in name order.
//
// The union of what an operator configured and what the fleet actually holds,
// because either side alone hides a real state: a configured pool standing empty
// is the one about to be bought for, and a pool holding machines nobody
// configured is the one whose bounds went to zero and whose machines are on
// their way out. The default is always among them, because it is where every
// machine naming no pool belongs.
func poolNames(policy FleetPolicy, inventory host.FleetInventory) []string {
	seen := map[string]bool{DefaultTier: true}
	rest := make([]string, 0, len(policy.Tiers)+len(inventory))
	for name := range policy.Tiers {
		if !seen[name] {
			seen[name] = true
			rest = append(rest, name)
		}
	}
	for name := range inventory {
		if !seen[name] {
			seen[name] = true
			rest = append(rest, name)
		}
	}
	slices.Sort(rest)
	return append([]string{DefaultTier}, rest...)
}

// foldStatuses folds the pools into the deployment's own picture.
//
// The counts add, because the pools partition the fleet: what the deployment
// holds is exactly what its pools hold. The EXPLANATIONS do not add, and they
// resolve to the pool that is spending: the largest deficit, ties to the pool
// drawn first. Averaging or blanking them would leave a fleet buying machines
// with nothing on the card saying why.
//
// With one pool the fold is that pool's own picture, field for field, which is
// what keeps every fleet-wide surface reading exactly as it did before there
// were pools.
func foldStatuses(mode AutoscaleMode, pools []PoolStatus) FleetStatus {
	fleet := FleetStatus{Mode: mode}
	if len(pools) == 0 {
		return fleet
	}
	lead := 0
	for i, pool := range pools {
		s := pool.Status
		fleet.Target += s.Target
		fleet.Standby += s.Standby
		fleet.Draining += s.Draining
		fleet.Floor += s.Floor
		fleet.Total += s.Total
		fleet.Misses += s.Misses
		fleet.Schedule += s.Schedule
		fleet.Demand.Servable += s.Demand.Servable
		fleet.Demand.Unservable += s.Demand.Unservable
		fleet.Demand.GPURequired += s.Demand.GPURequired
		fleet.Demand.NeedsLargerHost += s.Demand.NeedsLargerHost
		fleet.Demand.OversizedGroup += s.Demand.OversizedGroup
		fleet.Partial = fleet.Partial || s.Partial
		if s.Deficit() > pools[lead].Status.Deficit() {
			lead = i
		}
	}
	fleet.Signal = pools[lead].Status.Signal
	fleet.LimitedBy = pools[lead].Status.LimitedBy
	return fleet
}

// Deficits are the pools short of one of their bounds and by how much;
// Surpluses are the pools holding capacity neither of their bounds needs. A pool
// that is exactly met appears in neither, so a caller can size its work by the
// map it was handed.
func (v FleetView) Deficits() map[string]int64 { return v.gaps(FleetStatus.Deficit) }

// Surpluses is the reclamation half of Deficits.
func (v FleetView) Surpluses() map[string]int64 { return v.gaps(FleetStatus.Surplus) }

func (v FleetView) gaps(of func(FleetStatus) int64) map[string]int64 {
	gaps := make(map[string]int64, len(v.Pools))
	for _, pool := range v.Pools {
		if gap := of(pool.Status); gap > 0 {
			gaps[pool.Pool] = gap
		}
	}
	return gaps
}

// Deficit and Surplus are what the whole deployment is short of and what it may
// give back: the pools' own gaps added.
//
// Added rather than derived from the folded picture, and the difference is real
// the moment a second pool exists: one pool short of two machines beside one
// spare by two nets to nothing in the sums, while the keeper is about to buy two
// and release two. What the page states has to be what the loop will do.
func (v FleetView) Deficit() int64 { return total(v.Deficits()) }

// Surplus is the reclamation half of Deficit.
func (v FleetView) Surplus() int64 { return total(v.Surpluses()) }

// StandbyCeiling is the bound the deployment's composed target is actually held
// to: every pool's own ceiling, added.
//
// Added rather than taken over the summed fleet, for the reason Deficit is: the
// clamp is applied per pool against the machines standing IN that pool, so a
// fleet of two thousand beside a pool of two does not entitle the second to a
// share of the first's. A page stating StandbyCap of the fold would name a
// number no pool is judged against and no clamp it drew could be reconciled with.
func (v FleetView) StandbyCeiling() int64 {
	var ceiling int64
	for _, pool := range v.Pools {
		ceiling += StandbyCap(pool.Status.Total)
	}
	return ceiling
}

func total(gaps map[string]int64) int64 {
	var sum int64
	for _, gap := range gaps {
		sum += gap
	}
	return sum
}

// DemandSince is the start of the window the miss query reads. Zero time when
// the horizon disables the signal, which callers take as "do not query".
func (p FleetPolicy) DemandSince(now time.Time) time.Time {
	if p.DemandHorizonSec <= 0 {
		return time.Time{}
	}
	return now.Add(-time.Duration(p.DemandHorizonSec) * time.Second)
}

// Target composes one pool's standby target: max-wins across standing intent,
// schedules and demand, clamped to [0, StandbyCap(fleet)].
//
// Max-wins rather than a sum, because the three signals are three answers to
// the same question, how many idle hosts should be standing, and not three
// separate demands to satisfy. Summing them would triple-count a schedule that
// exists because of the demand it anticipates.
//
// fleet is the hosts standing in this pool right now, and only the third signal
// can ever reach past MaxStandbyHosts to need it: the other two are operator
// values the write path already bounds there.
//
// The mode is not read here. off still computes and publishes a
// target; what off changes is whether anything acts on it, which is the
// keeper's decision and testable on its own.
func Target(tier FleetTier, signals TargetSignals, fleet int64) TargetResult {
	result := TargetResult{Target: tier.Standby, Signal: SignalStandby}
	if signals.Schedule > result.Target {
		result = TargetResult{Target: signals.Schedule, Signal: SignalSchedule}
	}
	if signals.Demand > result.Target {
		result = TargetResult{Target: signals.Demand, Signal: SignalDemand}
	}
	if result.Target < 0 {
		result.Target = 0
	}
	if ceiling := StandbyCap(fleet); result.Target > ceiling {
		result.Target = ceiling
		result.LimitedBy = LimitStandbyCap
	}
	return result
}
