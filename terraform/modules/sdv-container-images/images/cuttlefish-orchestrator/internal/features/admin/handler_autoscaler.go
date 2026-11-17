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

package adminfeature

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/domain/quota"
)

// The autoscaler page's own snapshot: the fleet the keeper steers, drawn host by
// host, beside the arithmetic that produced its target and the reason it is or
// is not acting on it.
//
// Separate from the control-plane snapshot rather than folded into it, because
// the two answer to different pages and the cost is not symmetric: this one
// walks every host and its devices, which the operations page has no use for and
// would pay for on every poll.

// FleetViewer draws the fleet: every host with its class, its occupancy and the
// standby predicate evaluated term by term, the shape census beneath it, and the
// order a reduction would reclaim in. The host feature repository satisfies it.
//
// The shorten order comes from the reclaimer's OWN listing rather than a sort
// this package performs. Which hosts a reduction takes is the one opaque
// consequence on this page (coldest by artifact-cache warmth, skipping hosts
// under the settle age, sparing anything mid-bake), and a second ordering
// derived here would be free to disagree with the one that actually runs.
type FleetViewer interface {
	ListFleetView(ctx context.Context, lead time.Duration) ([]host.FleetHost, error)
	ListShortenable(ctx context.Context, lead time.Duration, perPool int) ([]*host.Host, error)
	CountByShape(ctx context.Context) ([]host.ShapeCount, error)
}

// KeeperActivityReader counts what the autoscaler actually did, from the audit
// log; the Spanner audit repository satisfies it.
type KeeperActivityReader interface {
	KeeperActivity(ctx context.Context, since time.Time) (hostconfig.KeeperActivity, error)
}

// KeeperLimits are the keeper's compiled facts, handed in by the composition
// root because features may not import internal/jobs.
//
// Data rather than an import, following WorkersPerInstance. These are numbers a
// page states, and it states them so nobody adds environment variables for
// them. LeaseName is the background plane's single lease, passed in so it is
// spelled once. The keeper leads or not exactly as every other periodic loop
// does, and this page must not be able to disagree with the operations page
// about which row says so.
// The per-cycle provisioning budget is absent. It is a share of the standing
// fleet, so it comes from hostconfig.TopUpBudget over the same status this page
// composes. Handing the floor and the share over as two numbers for the page to
// multiply would derive one rule twice.
type KeeperLimits struct {
	CycleSeconds         int64
	MaxShortensPerCycle  int64
	MaxCollapsesPerCycle int64
	QuotaReserveHosts    int64
	LeaseName            string
}

// AutoscalerInput is the input for the autoscaler snapshot.
type AutoscalerInput struct{}

// AutoscalerLimits is the compiled half of the autoscaler: bounds and budgets
// no setting can move.
//
// Published as facts rather than left implicit because they explain an
// apparently stuck fleet (a target of 10 against a standby of 2 converges over
// several cycles), and because a page that states them is a page nobody adds
// environment variables to.
//
// Two of them are computed from the fleet standing at this instant, so they are
// published as the EFFECTIVE numbers. A console handed a floor and a share to
// multiply would derive them a second time, against a fleet size it would have
// to read separately.
type AutoscalerLimits struct {
	MaxStandbyHosts      int64 `json:"max_standby_hosts" doc:"Ceiling on the composed target right now: every pool's own ceiling added, each of them a compiled floor or a share of the machines standing in that pool, whichever is larger"`
	MaxTopUpsPerCycle    int64 `json:"max_topups_per_cycle" doc:"Hosts one cycle may provision right now: a compiled floor, or a share of the fleet standing, whichever is larger"`
	MaxShortensPerCycle  int64 `json:"max_shortens_per_cycle" doc:"Surplus leases one cycle may lower"`
	MaxCollapsesPerCycle int64 `json:"max_collapses_per_cycle" doc:"Empty dedicated hosts one cycle may reclaim"`
	QuotaReserveHosts    int64 `json:"quota_reserve_hosts" doc:"Host-quota headroom the buffer never spends, so a top-up cannot turn somebody's create into a 429"`
	CycleSeconds         int64 `json:"cycle_seconds" doc:"How often the keeper runs"`
	ProvisionLeadSeconds int64 `json:"provision_lead_seconds" doc:"Lease a host must retain to count as standby; below it the reaper collects it before a replacement could land"`
	HostImageBackedSlots int64 `json:"host_image_backed_slots" doc:"Instance slots the host image preallocates a TAP set for; a slot past this is promised but cannot be launched against"`
	MinShortenAgeSeconds int64 `json:"min_shorten_age_seconds" doc:"How old a fleet host must be before its emptiness reads as drained rather than still filling"`
}

// FleetOccupantItem is one device on a host, sized by the instance slots it
// costs. Never a unit: max_devices and current_device_count are both denominated
// in instances, and one device can want eight of them.
type FleetOccupantItem struct {
	Name      string `json:"name"`
	Instances int64  `json:"instances" doc:"Instance slots this device holds"`
	Errored   bool   `json:"errored,omitempty" doc:"The row is in error: its slots are still charged, so the host is full while running nothing"`
}

// FleetHostItem is one host as the autoscaler's lens sees it.
type FleetHostItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Zone        string `json:"zone"`
	MachineType string `json:"machine_type,omitempty"`
	GPUType     string `json:"gpu_type,omitempty"`
	GPUCount    int64  `json:"gpu_count"`
	Status      string `json:"status"`
	Class       string `json:"class" enum:"reserved,fleet,dedicated,batch" doc:"Whose lifecycle rules apply; only the fleet class is the autoscaler's"`
	Origin      string `json:"origin,omitempty" enum:"user,auto,pool,batch" doc:"Which path created it; auto and pool are both fleet class"`
	Pool        string `json:"pool" doc:"Which subset of the fleet it belongs to; a host naming none belongs to the default pool"`

	MaxDevices     int64               `json:"max_devices" doc:"Instance slots the host promises"`
	BackedSlots    int64               `json:"backed_slots" doc:"Slots it can actually run: max_devices bounded by what the image backs. Every total sums THIS"`
	LiveInstances  int64               `json:"live_instances" doc:"Slots held by devices that are running"`
	ErrorInstances int64               `json:"error_instances" doc:"Slots held by rows in error. They are real slots with a dead row in them, never unbacked slots"`
	Occupants      []FleetOccupantItem `json:"occupants"`

	Standby     bool   `json:"standby" doc:"Whether the keeper counts this host as standing inventory"`
	FailingTerm string `json:"failing_term,omitempty" enum:"class,placeable,not_degraded,shareable,idle,lease" doc:"Which term of the standby predicate excluded it; absent when it is counted"`
	// ShortenOrder is 1-based and set only on the hosts a reduction would take
	// next, and only while there is a surplus to take. Zero everywhere else.
	ShortenOrder int64 `json:"shorten_order,omitzero" doc:"Position in the order a reduction would reclaim, for the hosts this cycle could reach"`

	ExpiresAt time.Time `json:"expires_at,omitzero"`
	CreatedAt time.Time `json:"created_at"`
}

// LadderRungItem is one rung of the fleet's shape ladder as the provisioning
// walk sees it right now: what a host built there would hold, and what the
// placement plane's memory says about building one.
//
// The two counts are zone counts (a zone can hold several observations),
// because that is the question the walk asks. A rung is stepped over only
// when every zone it could go to is already known to be out of capacity, and
// one zone left is a zone worth probing.
type LadderRungItem struct {
	MachineType   string `json:"machine_type"`
	MachineFamily string `json:"machine_family" doc:"Stockout observations are recorded per family, so rungs sharing one share their stockout state"`
	Slots         int64  `json:"slots" doc:"Device slots a host on this rung is packed to: the stored density bounded by this machine's RAM"`

	Zones            int64     `json:"zones" doc:"Zones this rung can be placed in: the enabled zone catalog narrowed by the machine type's own catalog availability"`
	StockedOutZones  int64     `json:"stocked_out_zones" doc:"How many of those a stockout observation still stands in"`
	NewestStockoutAt time.Time `json:"newest_stockout_at,omitzero" doc:"When the newest of those stockouts was observed; absent when none stands"`

	// The accelerator a machine built here carries, present only where one
	// does: the gpu pool's rungs. An attachable rung shows the curated default
	// with the same floor-of-one the attach applies.
	GPUType  string `json:"gpu_type,omitempty" doc:"GPU a machine built on this rung carries; absent when none does"`
	GPUCount int64  `json:"gpu_count,omitempty" doc:"How many of them"`

	// Landing is set on at most one rung, and on none at all when every rung is
	// stocked out everywhere it could go. That is the state where provisioning
	// has nothing left to try and fails outright without building anything.
	Landing bool `json:"landing,omitempty" doc:"Whether provisioning would build this rung right now"`

	// Refused names the rule that has taken this rung out of the walk since the
	// operator stored it: a shape disabled, a family withdrawn, a machine the
	// catalog no longer offers. The rung stays in the list because a step the
	// fleet silently stopped taking would be a preference lost with nothing
	// anywhere saying so. Every other field is zero on such a rung.
	Refused string `json:"refused,omitempty" doc:"Why this stored rung is out of the walk; absent on a rung provisioning still uses"`
}

// ShapeCensusItem is one cell of the fleet's shape census.
type ShapeCensusItem struct {
	MachineType string `json:"machine_type"`
	GPUType     string `json:"gpu_type,omitempty"`
	GPUCount    int64  `json:"gpu_count"`
	Class       string `json:"class" enum:"reserved,fleet,dedicated,batch"`
	Status      string `json:"status"`
	Count       int64  `json:"count"`
}

// The gate reasons: what is limiting this cycle. Exactly one is reported, most
// fundamental first, because a still diagram has several possible causes and
// listing them all would only repeat the raw state.
const (
	// GateActing: nothing is limiting the keeper.
	GateActing = "acting"
	// GateNoLeader: no instance holds the background plane's lease, so the loop
	// is not running anywhere. It outranks every other reason, because none of
	// them matter while nothing is cycling.
	GateNoLeader = "no_leader"
	// GateModeOff: the mode permits nothing. The target is still computed and
	// published; that is what off means, and it is not a fault.
	GateModeOff = "mode_off"
	// GateProvisionOnly: there is a surplus and the mode may only add capacity,
	// so the fleet runs its leases out instead of giving them back.
	GateProvisionOnly = "provision_only"
	// GateQuotaReserved: the host quota's last slots are reserved for user
	// creates, so a top-up is held back. Silent and permanent near the ceiling,
	// which is why it is named.
	GateQuotaReserved = "quota_reserved"
	// GateCapped: the gap exceeds what one cycle may move, so the fleet
	// converges over several cycles.
	GateCapped = "capped"
	// GateTargetMet: the fleet holds what the target asks for.
	GateTargetMet = "target_met"
)

// FleetPoolItem is one pool's region of the page: its own two contracts, the
// gap either of them leaves, and what is limiting it.
//
// The server computes the gap because two independent bounds on one population
// compose into a deficit through the same domain arithmetic the keeper spends
// against. A page that re-derived it would be free to disagree with the loop it
// draws.
//
// The reason is the pool's own, drawn from the same vocabulary the engine's is:
// a pool can be exactly met while another is capped, and the causes that belong
// to the deployment (no leader, a mode that permits nothing, a quota reserve
// held back) stay on the engine's gate where there is one of each.
type FleetPoolItem struct {
	Pool    string                 `json:"pool" doc:"Which subset of the fleet this region is about"`
	Status  hostconfig.FleetStatus `json:"status" doc:"This pool's composed picture: its target, what it holds, and the derivation behind both"`
	Deficit int64                  `json:"deficit" doc:"Hosts this pool is short of, 0 when both its bounds are met"`
	Surplus int64                  `json:"surplus" doc:"Standing hosts this pool holds beyond both bounds, 0 when there are none"`
	Reason  string                 `json:"reason" enum:"acting,no_leader,mode_off,provision_only,quota_reserved,capped,target_met" doc:"What is limiting this pool's half of the cycle; acting means nothing is"`

	// Ladder is this pool's own, because which rungs are admissible depends on
	// the pool that owns them. A list valid in one pool and refused in another
	// cannot be one list. Absent without a stored ladder, which is the catalog
	// default.
	Ladder []LadderRungItem `json:"ladder,omitempty" doc:"This pool's shape ladder in the operator's order, judged against the stockout memory provisioning consults"`
}

// GateStatus is why the keeper is or is not moving, and the arithmetic behind
// it.
//
// It exists because a still diagram is indistinguishable from a broken one. All
// of it is derivable from the snapshot's own parts. Deriving it here, outside
// the console, keeps the page and the loop from disagreeing about a decision
// neither of them stores.
//
// Its counts are the pools' own gaps ADDED, never the gap of the summed picture:
// one pool short by two beside one spare by two nets to nothing in the sums
// while the keeper is about to buy two and release two.
type GateStatus struct {
	Reason  string `json:"reason" enum:"acting,no_leader,mode_off,provision_only,quota_reserved,capped,target_met" doc:"What is limiting this cycle; acting means nothing is"`
	Deficit int64  `json:"deficit" doc:"Hosts the target is short of, 0 when it is met"`
	Surplus int64  `json:"surplus" doc:"Standing hosts beyond the target, 0 when there are none"`

	QuotaAvailable int64 `json:"quota_available" doc:"Host quota left before the reserve"`
	QuotaSpendable int64 `json:"quota_spendable" doc:"Host quota the buffer may actually spend: available minus the reserve, floored at 0"`

	LeaderHeld   bool   `json:"leader_held" doc:"Whether an instance currently holds the keeper's lease"`
	LeaderHolder string `json:"leader_holder,omitempty" doc:"The instance leading, when one is"`
}

// AutoscalerOutput is the autoscaler page's whole snapshot.
type AutoscalerOutput struct {
	Body struct {
		Now time.Time `json:"now" doc:"Server time of the snapshot; compute ages and countdowns against this, not the client clock"`

		// Fleet is the same block the control-plane snapshot serves, from the
		// same composition, so the two pages cannot publish different targets.
		Fleet  FleetPanelItem   `json:"fleet"`
		Limits AutoscalerLimits `json:"limits"`
		Gate   GateStatus       `json:"gate"`
		// Pools is the same composition split the way the keeper acts on it: one
		// entry per pool, the default first. With one pool its single entry
		// restates the fleet block above, so the page's regions and its
		// masthead agree by construction.
		Pools []FleetPoolItem `json:"pools" doc:"One entry per pool: the two contracts it is held to, its gap, and what is limiting it"`

		Hosts  []FleetHostItem   `json:"hosts" doc:"Every live host, all classes: the other three render muted so the totals reconcile against the hosts page"`
		Shapes []ShapeCensusItem `json:"shapes" doc:"What the fleet is made of, by machine shape, class and status"`
		// The DEFAULT pool's ladder, published beside the per-pool regions for
		// the surfaces that still draw one section for the fleet. It is the same
		// value its entry in Pools carries, filled from there so the two cannot
		// differ.
		// Absent without a stored ladder, which is the catalog default: the shape
		// the fleet builds then is density_machine_type below, packed to
		// effective_density.
		Ladder []LadderRungItem `json:"ladder,omitempty" doc:"The default pool's shape ladder in the operator's order, judged against the stockout memory provisioning consults"`

		// ArmImageReady is what the arm pool's card states: whether an
		// arm64 machine family here carries an enabled host image, without which
		// nothing can be built for that pool however it is configured.
		ArmImageReady bool `json:"arm_image_ready" doc:"Whether this deployment can build an arm64 host: an arm machine family with an enabled host image"`

		Activity            hostconfig.KeeperActivity `json:"activity" doc:"What the keeper actually did over the activity window, from the audit log"`
		ActivityWindowSec   int64                     `json:"activity_window_sec" doc:"How far back the activity counts reach"`
		EffectiveDensity    int64                     `json:"effective_density" doc:"Device slots the default shape carries (its cores-and-memory fit, or the operator's per-shape override), and the multiplier from a target in hosts to capacity in slots"`
		DensityMachineType  string                    `json:"density_machine_type,omitempty" doc:"The default shape the capacity is about; empty when the catalog has no default"`
		DemandHorizonWindow int64                     `json:"demand_horizon_sec" doc:"The horizon the demand figures above were read over"`
	}
}

// KeeperActivityWindow is how far back the flow diagram's edge weights reach.
//
// A day, fixed. The edges answer "has this lane moved lately", which wants a
// window long enough that a quiet hour does not read as a dead lane and short
// enough that last week's reclamation does not. The demand horizon is minutes
// and answers a different question, and a selectable window would serve
// neither.
const KeeperActivityWindow = 24 * time.Hour

// Autoscaler returns the autoscaler page's snapshot.
func (h *AdminHandler) Autoscaler(ctx context.Context, _ *AutoscalerInput) (*AutoscalerOutput, error) {
	if h.fleetPolicy == nil || h.standby == nil || h.fleetView == nil {
		return nil, huma.Error500InternalServerError("the autoscaler view is not configured")
	}
	out := &AutoscalerOutput{}
	out.Body.Now = time.Now().UTC()

	fleet, view, err := h.fleetPanel(ctx, out.Body.Now)
	if err != nil {
		return nil, err
	}
	out.Body.Fleet = fleet
	out.Body.DemandHorizonWindow = fleet.Policy.DemandHorizonSec

	density, err := h.fleetPolicy.GetHostDensity(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to read the packing density", err)
	}
	out.Body.EffectiveDensity = density.Effective
	out.Body.DensityMachineType = density.MachineType

	ladders, err := h.ladderViews(ctx, fleet.Policy, out.Body.Now)
	if err != nil {
		return nil, err
	}
	out.Body.Ladder = ladders[hostconfig.DefaultTier]
	out.Body.ArmImageReady = h.armCapability != nil && h.armCapability.Ready(ctx)

	out.Body.Limits = h.autoscalerLimits(view)

	hosts, err := h.fleetView.ListFleetView(ctx, host.ProvisionLead)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to read the fleet view", err)
	}
	out.Body.Hosts = fleetHostItems(hosts)

	shapes, err := h.fleetView.CountByShape(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to read the shape census", err)
	}
	out.Body.Shapes = shapeCensusItems(shapes)

	gate, err := h.gateStatus(ctx, view, out.Body.Now)
	if err != nil {
		return nil, err
	}
	out.Body.Gate = gate
	out.Body.Pools = h.fleetPoolItems(view, gate, ladders)
	if err := h.stampShortenOrder(ctx, out.Body.Hosts, view.Surpluses()); err != nil {
		return nil, err
	}

	out.Body.ActivityWindowSec = int64(KeeperActivityWindow.Seconds())
	if h.keeperActivity != nil {
		activity, err := h.keeperActivity.KeeperActivity(ctx, out.Body.Now.Add(-KeeperActivityWindow))
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to read the keeper's activity", err)
		}
		out.Body.Activity = activity
	}
	return out, nil
}

// ladderViews resolves every pool's shape ladder the way a create in that pool
// resolves it, and judges every rung against the memory the provisioning walk
// consults.
//
// Three reads whatever the ladders hold and however many pools there are: the
// catalog the rungs are resolved against, the enabled zones ranking places
// inside, and ONE stockout read covering every rung's family at once. No read
// sits inside the pool loop, because the page polls every few seconds and a
// statement per pool would be paid forever. None of the three
// happens with no ladder stored anywhere, so a deployment on the catalog default
// pays nothing for a section it would draw empty.
//
// A stored shape the catalog no longer offers costs one catalog lookup beyond
// those, once per shape however many pools stored it, because only the catalog
// can say which of its three refusals applies.
//
// The stored lists are the ladders as an operator wrote them. Which of those
// rungs the walk still takes is the catalog's answer, judged per OWNING pool,
// and it is the same answer provisioning gets. A rung the pool no longer admits
// is still drawn, with the rule it breaks. The walk really does skip it, and
// a page that showed a shorter ladder than the stored one would hide a
// preference the operator set and never got.
func (h *AdminHandler) ladderViews(ctx context.Context, policy hostconfig.FleetPolicy, now time.Time) (map[string][]LadderRungItem, error) {
	none := map[string][]LadderRungItem{}
	if h.machineTypes == nil || h.zoneCatalog == nil || h.zoneHealth == nil || !policyHasLadder(policy) {
		return none, nil
	}
	catalog, err := h.machineTypes.List(ctx, machinetype.ListFilter{})
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to read the catalog behind the shape ladder", err)
	}
	why := h.whyUnoffered(ctx)
	judged := make(map[string][]hostconfig.LadderJudgement, len(policy.Tiers))
	var families []string
	for _, pool := range policy.EnabledPools() {
		rungs := hostconfig.JudgeLadder(pool, policy.Pool(pool).Ladder, catalog, why)
		if len(rungs) == 0 {
			continue
		}
		judged[pool] = rungs
		for _, family := range hostconfig.LadderFamilies(admittedRungs(rungs)) {
			if !slices.Contains(families, family) {
				families = append(families, family)
			}
		}
	}
	if len(judged) == 0 {
		return none, nil
	}
	zones, err := h.zoneCatalog.ListEnabledZones(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to read the enabled zones", err)
	}
	cells, err := h.zoneHealth.ListCapacityCells(ctx, families)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to read the stockout memory", err)
	}
	fresh := gcecatalog.FreshCapacityCells(cells, now)
	stocked := gcecatalog.StockedOutZones(cells, now)
	views := make(map[string][]LadderRungItem, len(judged))
	for pool, rungs := range judged {
		views[pool] = ladderRungItems(rungs, zones, fresh, stocked)
	}
	return views, nil
}

// whyUnoffered lets a drawn refusal state the catalog's own reason for a rung
// the listing does not hold. It is the same lookup, and so the same sentence,
// the write that stored it would have been refused with.
//
// Answered from the memo after the first time, because a shape the catalog
// stopped offering is refused in every pool that stored it and the pools are
// judged one after another. An error that is not the catalog's verdict on the
// shape (a cold catalog, a failed read) answers nothing, so the refusal stays
// the generic one and does not blame the operator's rung for an infrastructure
// fault.
func (h *AdminHandler) whyUnoffered(ctx context.Context) hostconfig.WhyUnoffered {
	asked := map[string]error{}
	return func(machineType string) error {
		if reason, seen := asked[machineType]; seen {
			return reason
		}
		var reason error
		if _, err := h.machineTypes.Get(ctx, machineType); err != nil {
			if unoffered, ok := errors.AsType[*machinetype.UnofferedError](err); ok {
				reason = unoffered
			}
		}
		asked[machineType] = reason
		return reason
	}
}

// admittedRungs is the subset the walk actually probes, which is the set every
// stockout question is asked about.
func admittedRungs(judged []hostconfig.LadderJudgement) []hostconfig.LadderRung {
	rungs := make([]hostconfig.LadderRung, 0, len(judged))
	for _, judgement := range judged {
		if judgement.Refusal == "" {
			rungs = append(rungs, judgement.Rung)
		}
	}
	return rungs
}

// policyHasLadder reports whether any pool has a ladder stored, which is what
// decides whether the three reads above are worth making at all.
func policyHasLadder(policy hostconfig.FleetPolicy) bool {
	for _, tier := range policy.Tiers {
		if len(tier.Ladder) > 0 {
			return true
		}
	}
	return false
}

// ladderRungItems judges one pool's stored rungs, in memory, against inputs
// every pool shares.
//
// The walk's own decision over the walk's own inputs: the slot ceiling a host of
// the preferred rung promises, and the first rung worth an insert while holding
// it. Both are asked of the ADMITTED rungs alone, because those are the ones the
// walk has; a refused rung carries its rule and nothing else, since every number
// beside it would describe a machine the fleet will not build.
func ladderRungItems(
	judged []hostconfig.LadderJudgement, zonePool []string,
	fresh []*gcecatalog.ZoneHealth, stocked map[string]map[string]bool,
) []LadderRungItem {
	admitted := admittedRungs(judged)
	landingType := ""
	if len(admitted) > 0 {
		if i, lands := hostconfig.FirstProbableRung(admitted, 0, admitted[0].Slots(), zonePool, stocked); lands {
			landingType = admitted[i].MachineType
		}
	}
	items := make([]LadderRungItem, 0, len(judged))
	for _, judgement := range judged {
		if judgement.Refusal != "" {
			items = append(items, LadderRungItem{
				MachineType: judgement.MachineType,
				Refused:     judgement.Refusal,
			})
			continue
		}
		rung := judgement.Rung
		zones := rung.PlaceableZones(zonePool)
		stockedOut, newest := standingStockouts(rung.Family, zones, fresh)
		items = append(items, LadderRungItem{
			MachineType:      rung.MachineType,
			MachineFamily:    rung.Family,
			Slots:            rung.Slots(),
			GPUType:          rung.GPUType,
			GPUCount:         rung.GPUCount,
			Zones:            int64(len(zones)),
			StockedOutZones:  stockedOut,
			NewestStockoutAt: newest,
			Landing:          rung.MachineType == landingType,
		})
	}
	return items
}

// standingStockouts counts the rung's zones a stockout observation still stands
// in, and dates the newest of them. The cells are the fresh ones the walk's own
// predicate kept, so nothing here decides what still standing means a second
// time; zone_health holds one cell per (zone, family), so counting cells counts
// zones.
func standingStockouts(family string, zones []string, fresh []*gcecatalog.ZoneHealth) (int64, time.Time) {
	var standing int64
	var newest time.Time
	for _, cell := range fresh {
		if cell.MachineFamily != family || !slices.Contains(zones, cell.Zone) {
			continue
		}
		standing++
		if cell.LastFailureAt != nil && cell.LastFailureAt.After(newest) {
			newest = *cell.LastFailureAt
		}
	}
	return standing, newest
}

// autoscalerLimits assembles the compiled facts: the keeper's own budgets, which
// arrive as data, beside the lifecycle constants this package may import.
//
// The two fleet-proportional bounds are evaluated against the SAME composition
// the panel above published. A page whose stated
// ceiling and stated fleet size came from different instants would explain a
// clamp that the numbers beside it contradict.
//
// They take the standing count differently, and each takes the one it is really
// bound by. The per-cycle budget rations a quota the deployment shares, so it is
// a share of the whole fleet. The target's ceiling is applied inside each pool
// against that pool's own machines, so it is the pools' ceilings added. The
// fold's ceiling would be a number no pool is ever held to.
func (h *AdminHandler) autoscalerLimits(view hostconfig.FleetView) AutoscalerLimits {
	return AutoscalerLimits{
		MaxStandbyHosts:      view.StandbyCeiling(),
		MaxTopUpsPerCycle:    hostconfig.TopUpBudget(view.Fleet.Total),
		MaxShortensPerCycle:  h.keeperLimits.MaxShortensPerCycle,
		MaxCollapsesPerCycle: h.keeperLimits.MaxCollapsesPerCycle,
		QuotaReserveHosts:    h.keeperLimits.QuotaReserveHosts,
		CycleSeconds:         h.keeperLimits.CycleSeconds,
		ProvisionLeadSeconds: int64(host.ProvisionLead.Seconds()),
		HostImageBackedSlots: host.HostImageCVDAccounts,
		MinShortenAgeSeconds: int64(host.MinShortenAge.Seconds()),
	}
}

// gateStatus decides what is limiting this cycle.
//
// The order is the order the keeper itself would hit them, most fundamental
// first: a loop nobody is running cannot be limited by anything else, and a mode
// that permits nothing makes the arithmetic beneath it moot. Reporting the first
// that applies is what turns six possible causes into one sentence.
func (h *AdminHandler) gateStatus(ctx context.Context, view hostconfig.FleetView, now time.Time) (GateStatus, error) {
	// These are the functions the keeper's own branch calls. The page must not
	// disagree with the loop it draws, and with two independent bounds per pool
	// the arithmetic is no longer a subtraction a reader can check by eye.
	gate := GateStatus{
		Deficit: view.Deficit(),
		Surplus: view.Surplus(),
	}
	gate.LeaderHeld, gate.LeaderHolder = h.keeperLeader(ctx, now)

	if h.quotaService != nil {
		q, err := h.quotaService.Get(ctx, quota.ScopePlatform, quota.GlobalScopeID)
		if err != nil {
			return GateStatus{}, huma.Error500InternalServerError("failed to read the host quota", err)
		}
		gate.QuotaAvailable = q.Available()
		gate.QuotaSpendable = max(0, q.Available()-h.keeperLimits.QuotaReserveHosts)
	}
	gate.Reason = h.gateReason(gate, view.Fleet.Mode, gapFacts{
		draining:    view.Fleet.Draining,
		deficit:     gate.Deficit,
		surplus:     gate.Surplus,
		perCycle:    hostconfig.TopUpBudget(view.Fleet.Total),
		reclaimable: h.reclaimable(view.Surpluses()),
	})
	return gate, nil
}

// reclaimable is how much of a surplus one cycle may really lower: each pool's
// own gap under the per-pool cap, added. That is the arithmetic the keeper does
// before it hands the reclamation over.
//
// Added rather than measured against the cap once, because the cap binds each
// pool separately: three pools spare by one each are all reclaimed in the cycle
// that reports them, and a deployment gate comparing their total against one
// pool's cap would say the fleet is converging over cycles after it had
// finished.
func (h *AdminHandler) reclaimable(surpluses map[string]int64) int64 {
	var reclaimable int64
	for _, surplus := range surpluses {
		reclaimable += min(surplus, h.keeperLimits.MaxShortensPerCycle)
	}
	return reclaimable
}

// gapFacts are the numbers a gate reason turns on: what a cycle is short of or
// spare by, what it could renew instead of buying, and how much of each gap one
// cycle may move. Each is a pool's when the reason is a pool's.
type gapFacts struct {
	draining, deficit, surplus int64
	// The two bounds on how far one cycle gets, and they are not symmetric.
	// perCycle rations the host quota, which is one number for the deployment,
	// so it is a share of the whole fleet standing however few pools want it.
	// reclaimable is the reclamation cap, which binds each pool on its own, so
	// the deployment's figure is its pools' capped gaps added.
	perCycle, reclaimable int64
}

// gateReason names what is limiting one gap, whether that gap is a pool's or the
// deployment's.
//
// The deployment-wide causes stay first because they outrank anything a pool can
// say about itself: no pool is acting while nothing holds the lease, and the
// mode gates every pool at once.
func (h *AdminHandler) gateReason(gate GateStatus, mode hostconfig.AutoscaleMode, facts gapFacts) string {
	switch {
	case !gate.LeaderHeld:
		return GateNoLeader
	case !mode.Provisions():
		return GateModeOff
	case facts.deficit > 0:
		return deficitGate(gate, facts)
	case facts.surplus > 0 && !mode.Reclaims():
		return GateProvisionOnly
	case facts.surplus > facts.reclaimable:
		return GateCapped
	case facts.surplus > 0:
		return GateActing
	default:
		return GateTargetMet
	}
}

// deficitGate names what is limiting a cycle that is short of its target.
//
// The quota reserve is checked before the per-cycle cap because it is the one
// that can stop the fleet permanently: a cap delays convergence by a cycle, and
// the reserve holds near the ceiling forever with no error anywhere. Rescue is
// exempt from both, since it renews a lease the fleet already paid for, so a
// deficit with draining hosts to renew is a cycle that acts whatever the quota
// says.
//
// The cap is reported on this side exactly as it is on the surplus side. Both
// mean the same thing: the gap is wider than one cycle may move, so the fleet
// converges over several cycles. Reporting it in one direction
// only would make an operator watching a large deficit close slowly wonder which
// of the two pages was lying.
func deficitGate(gate GateStatus, facts gapFacts) string {
	if facts.draining > 0 {
		return GateActing
	}
	if gate.QuotaSpendable <= 0 {
		return GateQuotaReserved
	}
	if facts.deficit > facts.perCycle {
		return GateCapped
	}
	return GateActing
}

// fleetPoolItems maps the per-pool half of the composition onto the wire, each
// pool judged by the same gate the engine's is.
//
// The gate it is handed is the deployment's, because the facts that decide a
// pool's reason beyond its own gap (leadership, the mode, the quota headroom,
// the share of the fleet one cycle may buy) are one per deployment. What
// differs per pool is the gap, and the draining hosts that gap could be met from
// without buying anything: a pool with nothing draining is not acting merely
// because some other pool has leases to renew.
func (h *AdminHandler) fleetPoolItems(view hostconfig.FleetView, gate GateStatus, ladders map[string][]LadderRungItem) []FleetPoolItem {
	perCycle := hostconfig.TopUpBudget(view.Fleet.Total)
	items := make([]FleetPoolItem, 0, len(view.Pools))
	for _, pool := range view.Pools {
		item := FleetPoolItem{
			Pool:    pool.Pool,
			Status:  pool.Status,
			Deficit: pool.Status.Deficit(),
			Surplus: pool.Status.Surplus(),
			Ladder:  ladders[pool.Pool],
		}
		item.Reason = h.gateReason(gate, pool.Status.Mode, gapFacts{
			draining:    pool.Status.Draining,
			deficit:     item.Deficit,
			surplus:     item.Surplus,
			perCycle:    perCycle,
			reclaimable: min(item.Surplus, h.keeperLimits.MaxShortensPerCycle),
		})
		items = append(items, item)
	}
	return items
}

// keeperLeader reports whether the background plane's lease is currently held,
// and by whom. The keeper cycles on whichever instance holds it. An expired
// lease means the background plane stopped. The holder renews every few
// seconds while alive, and a graceful stop releases the row outright.
func (h *AdminHandler) keeperLeader(ctx context.Context, now time.Time) (bool, string) {
	if h.leaseAdmin == nil || h.keeperLimits.LeaseName == "" {
		// Nothing to ask. Reported as HELD, because an unwired lease reader is
		// a deployment fact and says nothing about the loop. Claiming no
		// leader would put a fault on the page that nobody could fix.
		return true, ""
	}
	leases, err := h.leaseAdmin.ListLeases(ctx)
	if err != nil {
		return true, ""
	}
	for _, l := range leases {
		if l.Name != h.keeperLimits.LeaseName {
			continue
		}
		return l.Holder != "" && l.Expiry.After(now), l.Holder
	}
	return false, ""
}

// stampShortenOrder marks the hosts a reduction would reclaim next, in the order
// the reclaimer itself would take them.
//
// Only in pools that have a surplus, because that is the only place the order
// exists: a pool at or below target has nothing spare, and an ordinal on a host
// that will not be touched is a threat the system is not making. Bounded by what
// one cycle can reach in a pool, so a chip always means the next cycle.
//
// The ordinal is the host's position among the candidates of ITS OWN pool, which
// is the order the reclamation takes them in: numbering a single list across
// pools would tell the second pool's first victim it was third.
func (h *AdminHandler) stampShortenOrder(ctx context.Context, hosts []FleetHostItem, surpluses map[string]int64) error {
	if len(surpluses) == 0 || h.keeperLimits.MaxShortensPerCycle <= 0 {
		return nil
	}
	perPool := int64(0)
	for _, surplus := range surpluses {
		perPool = max(perPool, min(surplus, h.keeperLimits.MaxShortensPerCycle))
	}
	candidates, err := h.fleetView.ListShortenable(ctx, host.ProvisionLead, int(perPool))
	if err != nil {
		return huma.Error500InternalServerError("failed to read the reclamation order", err)
	}
	order := make(map[string]int64, len(candidates))
	taken := make(map[string]int64, len(surpluses))
	for _, c := range candidates {
		pool := host.NormalizePool(c.Pool)
		if taken[pool] >= min(surpluses[pool], h.keeperLimits.MaxShortensPerCycle) {
			continue
		}
		taken[pool]++
		order[c.ID] = taken[pool]
	}
	for i := range hosts {
		hosts[i].ShortenOrder = order[hosts[i].ID]
	}
	return nil
}

// fleetHostItems maps the fleet view onto the wire.
func fleetHostItems(hosts []host.FleetHost) []FleetHostItem {
	items := make([]FleetHostItem, 0, len(hosts))
	for _, h := range hosts {
		item := FleetHostItem{
			ID:             h.ID,
			Name:           h.Name,
			Zone:           h.Zone,
			MachineType:    h.MachineType,
			GPUType:        h.GPUType,
			GPUCount:       h.GPUCount,
			Status:         string(h.Status),
			Class:          string(h.Class),
			Origin:         string(h.Origin),
			Pool:           h.Pool,
			MaxDevices:     h.MaxDevices,
			BackedSlots:    h.BackedSlots(),
			LiveInstances:  h.LiveInstances,
			ErrorInstances: h.ErrorInstances,
			Occupants:      make([]FleetOccupantItem, 0, len(h.Occupants)),
			Standby:        h.IsStandby(),
			FailingTerm:    string(h.FailingTerm()),
			CreatedAt:      h.CreatedAt,
		}
		if h.ExpiresAt != nil {
			item.ExpiresAt = *h.ExpiresAt
		}
		for _, o := range h.Occupants {
			item.Occupants = append(item.Occupants, FleetOccupantItem{
				Name: o.Name, Instances: o.Instances, Errored: o.Errored,
			})
		}
		items = append(items, item)
	}
	return items
}

// shapeCensusItems maps the shape census onto the wire.
func shapeCensusItems(shapes []host.ShapeCount) []ShapeCensusItem {
	items := make([]ShapeCensusItem, 0, len(shapes))
	for _, s := range shapes {
		items = append(items, ShapeCensusItem{
			MachineType: s.MachineType,
			GPUType:     s.GPUType,
			GPUCount:    s.GPUCount,
			Class:       string(s.Class),
			Status:      string(s.Status),
			Count:       s.Count,
		})
	}
	return items
}

// StandbyRecommendationInput is the input for the standby recommendation.
type StandbyRecommendationInput struct{}

// StandbyRecommendationOutput carries the observation window's one decision.
type StandbyRecommendationOutput struct {
	Body hostconfig.StandbyRecommendation
}

// StandbyRecommendation answers the question the observation week exists to
// answer: what to set the standing capacity to.
//
// Its own operation rather than a field on the snapshot above, because it is a
// weekly statistic over a multi-day scan of the audit log and the snapshot is
// polled every few seconds. Folding it in would put a week's scan on a
// five-second cadence to publish a number that changes by the day.
//
// It is the same statistic the target is composed from (the peak of concurrent
// SERVABLE misses, one provisioning leg wide) read over a longer window, so the
// number it suggests is one the autoscaler would actually pursue.
func (h *AdminHandler) StandbyRecommendation(ctx context.Context, _ *StandbyRecommendationInput) (*StandbyRecommendationOutput, error) {
	if h.demand == nil {
		return nil, huma.Error500InternalServerError("the demand signal is not configured")
	}
	now := time.Now().UTC()
	window, err := h.demand.Demand(ctx, now.Add(-hostconfig.RecommendationWindow))
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to read the observation window", err)
	}
	return &StandbyRecommendationOutput{Body: hostconfig.Recommend(window, now)}, nil
}
