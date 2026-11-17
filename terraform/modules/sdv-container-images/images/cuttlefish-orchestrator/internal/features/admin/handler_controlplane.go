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
	"time"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

// LeaseAdmin reads the leader-election leases that gate the periodic jobs;
// the Spanner LeaseRepository satisfies it.
type LeaseAdmin interface {
	ListLeases(ctx context.Context) ([]spanner.LeaderLease, error)
}

// ZoneCatalog reads the operator's zone curation behind the zone panel; the
// gcecatalog feature's zone policy satisfies it.
type ZoneCatalog interface {
	ListEnabledZones(ctx context.Context) ([]string, error)
}

// ZoneUniverse reports what Compute Engine last said about each zone, out of
// the in-process snapshot. A zone the snapshot does not hold answers empty,
// which the panel renders as an unknown status rather than as down.
type ZoneUniverse interface {
	Zones() map[string]gcecatalog.ZoneStatus
}

// RegionQuotas reads the live Compute quota rows the panel's headroom bars
// derive from, out of the in-process GCE snapshot. A region the snapshot does
// not hold answers empty, which the panel renders as no bars rather than as
// exhausted capacity.
type RegionQuotas interface {
	RegionalQuotas(region string) []*gcecatalog.RegionalQuota
}

// ARMCapability answers whether this deployment can build an arm64 host.
// Satisfied by the host feature's probe and wired in main, because features
// must not import features.
type ARMCapability interface {
	Ready(ctx context.Context) bool
}

// ZoneHealthAdmin reads the placement plane's reactive memory and lets an
// operator drop a cell from it; the Spanner ZoneHealthRepository satisfies it.
//
// The capacity read is the narrow one the shape ladder asks: the stockout cells
// of several machine families in one statement, whatever the rung count.
type ZoneHealthAdmin interface {
	ListCells(ctx context.Context) ([]*gcecatalog.ZoneHealth, error)
	ListCapacityCells(ctx context.Context, machineFamilies []string) ([]*gcecatalog.ZoneHealth, error)
	ClearCell(ctx context.Context, zone, machineFamily string) error
}

// SweepPolicyReader answers what the background loops are currently allowed
// to do on their own. Declared here because this is where it is consumed; the
// settings service satisfies it and internal/app wires it in.
type SweepPolicyReader interface {
	GetSweepPolicy(ctx context.Context) (hostconfig.SweepPolicy, error)
}

// FleetPolicyReader answers the autoscaler policy and the packing density it
// multiplies into standing device capacity; the settings service satisfies
// it, same wiring as SweepPolicyReader.
//
// It answers the EFFECTIVE density. What a target stands for is what
// the fleet can hold, and an auto-provisioned host is packed to whichever is
// smaller of the operator's policy and what its machine's RAM allows. Reading
// the stored number here would overstate standing capacity by exactly the gap
// between them, on the page whose whole rule is that its numbers are
// server-derived and need no arithmetic to believe.
type FleetPolicyReader interface {
	GetFleetPolicy(ctx context.Context) (hostconfig.FleetPolicy, error)
	GetHostDensity(ctx context.Context) (hostconfig.HostDensity, error)
}

// StandbyCounter counts what each pool holds: inventory a create can count on,
// and inventory whose lease is running out. Both fleet-class, the keeper's own
// definition. The host feature repository satisfies it.
//
// One method returning every pool's pair: the card puts the two numbers beside
// each other so a standby count that just dropped is legible as leases running
// down rather than devices arriving, and that reading only holds of a single
// instant.
type StandbyCounter interface {
	StandbyInventory(ctx context.Context, lead time.Duration) (host.FleetInventory, error)
}

// DemandReader reads the misses the autoscaler's target is composed from, with
// the facts each was stamped with, and how often a create that wanted a GPU was
// placed without one; the Spanner audit repository satisfies it.
//
// The card reads them itself rather than asking the keeper for its last
// answer: the keeper cycles on the leader alone, so a remembered answer would
// be absent on every other serving instance. The composition is shared instead
// of the state (hostconfig.ComposeFleet, called from both) so the card and
// the loop cannot disagree about a number they both derive.
//
// The degradation figure is on THIS port and absent from the keeper's twin. It
// decides whether a GPU tier is ever worth building, which is a question an
// operator answers over a week; the keeper would read it every thirty seconds
// and act on none of it.
type DemandReader interface {
	Demand(ctx context.Context, since time.Time) (hostconfig.DemandWindow, error)
	GPUDegradation(ctx context.Context, since time.Time) (hostconfig.GPUDegradation, error)
}

// SpotFleetCounter reads the fleet's provisioning-model × status census; the
// host feature repository satisfies it.
type SpotFleetCounter interface {
	CountByModelStatus(ctx context.Context) ([]host.ModelStatusCount, error)
}

// SpotAdviceReader reads the synced Spot capacity-advice cells; the Spanner
// SpotAdviceRepository satisfies it.
type SpotAdviceReader interface {
	ListCells(ctx context.Context) ([]*gcecatalog.SpotAdvice, error)
}

// ControlPlaneInput is the input for the control-plane snapshot.
type ControlPlaneInput struct{}

// QueueDepth is the queue's by-state depth within the control-plane snapshot.
type QueueDepth struct {
	Pending                 int64 `json:"pending" doc:"Tasks awaiting a worker"`
	Leased                  int64 `json:"leased" doc:"Tasks currently held by a worker"`
	Failed                  int64 `json:"failed" doc:"Dead-lettered tasks"`
	OldestPendingAgeSeconds int64 `json:"oldest_pending_age_seconds" doc:"Age of the oldest pending task (0 if none)"`
}

// TypeStateCountItem is one cell of the task-type × state breakdown.
type TypeStateCountItem struct {
	Type  string `json:"type" doc:"Task type, e.g. host.provision"`
	State string `json:"state" doc:"Task state" enum:"pending,leased,failed"`
	Count int64  `json:"count"`
}

// LiveTaskItem is a pending or leased task in the control-plane snapshot.
type LiveTaskItem struct {
	ID          string    `json:"id"`
	Type        string    `json:"type" doc:"Task type, e.g. device.create_cvd"`
	State       string    `json:"state" doc:"Task state" enum:"pending,leased"`
	ResourceID  string    `json:"resource_id,omitempty" doc:"Device/host the task acts on"`
	Attempts    int       `json:"attempts"`
	MaxAttempts int       `json:"max_attempts"`
	Priority    int64     `json:"priority" doc:"Claim tier: 0 = user intent (edge), -100 = reconciler resync"`
	AvailableAt time.Time `json:"available_at" doc:"Earliest time the task may be claimed (delay/backoff)"`
	LeaseExpiry time.Time `json:"lease_expiry,omitzero" doc:"Visibility timeout while leased"`
	LeaseHolder string    `json:"lease_holder,omitempty" doc:"Instance holding the lease"`
	LastError   string    `json:"last_error,omitempty" doc:"Most recent failure cause (set while retrying)"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// LeaderLeaseItem is one leader lease. The periodic jobs run under a single
// one, background-plane; a row named anything else is a lease no running job
// gates on.
type LeaderLeaseItem struct {
	Name       string    `json:"name" doc:"Lease name; every periodic job runs under background-plane"`
	Holder     string    `json:"holder,omitempty" doc:"Instance currently leading (empty when unheld)"`
	AcquiredAt time.Time `json:"acquired_at,omitzero" doc:"When the current holder took leadership"`
	Expiry     time.Time `json:"lease_expiry,omitzero" doc:"When the lease lapses without renewal"`
	RenewedAt  time.Time `json:"renewed_at" doc:"Last acquire/renew/release write"`
}

// ConnectivityItem is the deployment's media path, read-only: how device
// media reaches clients. Terraform owns every value (they must agree with
// resources it creates), so the snapshot states them and offers no way to
// change them. This is the class of deploy-time fact WorkersPerInstance already
// established. Never carries the TURN shared secret or anything derived from
// it; Configured is all it says about credentials.
type ConnectivityItem struct {
	IngressMode     string `json:"ingress_mode" enum:"public,private,relay" doc:"How device media leaves the host: public = external IPs and direct flows, private = VPC-internal, relay = internal-only hosts with all media through TURN"`
	TransportPolicy string `json:"transport_policy" enum:"all,relay" doc:"Transport policy the client leg is held to; relay means direct candidate pairs cannot form and are not gathered"`

	StunServers []string `json:"stun_servers" doc:"STUN servers offered to any leg that can reach the public internet"`

	// The two TURN lists name the same relay machines at their two
	// addresses; presenting them as one list would hide exactly that.
	TurnClientServers []string `json:"turn_client_servers" doc:"TURN URIs for the client leg (browser or CLI), on external addresses"`
	TurnDeviceServers []string `json:"turn_device_servers" doc:"TURN URIs for the device leg, on internal addresses (the same relays reached from inside the VPC)"`
	TurnRealm         string   `json:"turn_realm,omitempty" doc:"Realm the TURN servers authenticate under"`

	CredentialTTLSeconds int64 `json:"credential_ttl_seconds" doc:"Lifetime of minted TURN credentials (a session ceiling, not a refresh interval)"`
	CredentialsMintable  bool  `json:"credentials_mintable" doc:"Whether the TURN shared secret is deployed (without it the relay plane is off)"`

	Problems []string `json:"problems" doc:"Combinations that cannot stream, stated as problems rather than left to be inferred from blank rows"`
}

// ControlPlaneOutput is a one-call snapshot of the background machinery for
// the operations view: queue depth, the live working set, the type × state
// breakdown, the background plane's leadership, the placement space, and the
// Spot capacity picture.
type ControlPlaneOutput struct {
	Body struct {
		Now                time.Time            `json:"now" doc:"Server time of the snapshot; compute ages/countdowns against this, not the client clock"`
		Queue              QueueDepth           `json:"queue"`
		TypeCounts         []TypeStateCountItem `json:"type_counts"`
		LiveTasks          []LiveTaskItem       `json:"live_tasks"`
		Leaders            []LeaderLeaseItem    `json:"leaders"`
		WorkersPerInstance int                  `json:"workers_per_instance" doc:"Worker goroutines one serving instance may run at most; each sizes itself down toward a floor when the queue is quiet (0 when the pool is disabled)"`
		// The same type the settings pair serves, so the page's toggles and the
		// endpoint an operator writes through cannot disagree.
		Sweeps hostconfig.SweepPolicy `json:"sweeps" doc:"What each background loop may do on its own"`
		Fleet  FleetPanelItem         `json:"fleet" doc:"The autoscaler picture: the composed target, what the fleet holds, and the policy behind both"`
		// Deploy-time, so served from memory: no store read can change it
		// within a revision.
		Connectivity ConnectivityItem `json:"connectivity" doc:"The media plane: ingress mode, ICE policy, and the STUN/TURN server sets"`
	}
}

// FleetPanelItem is the autoscaler block: the composed status beside the
// policy it came from, both the same types the settings pair serves, so the
// card's numbers and the keeper's arithmetic cannot disagree.
type FleetPanelItem struct {
	Status hostconfig.FleetStatus `json:"status"`
	Policy hostconfig.FleetPolicy `json:"policy"`
	// DeviceSlots is the target × the density an auto-provisioned host is
	// actually packed to, which is the operator's policy bounded by what the
	// default machine's RAM holds. Computed server-side
	// so the console never multiplies numbers from two endpoints, and so the bound
	// cannot be left out by whichever surface does the multiplying.
	DeviceSlots int64 `json:"device_slots" doc:"Device capacity the target stands for (target × devices per auto-provisioned host)"`
	// GPUDegradation rides the card and stays off the status because the
	// keeper never reads it. It answers whether standing GPU stock would pay, a
	// decision taken over an observation week that no target uses.
	GPUDegradation hostconfig.GPUDegradation `json:"gpu_degradation" doc:"Creates that wanted a GPU and were placed without one, over the demand window, beside how many wanted one at all"`
}

// ControlPlane returns the control-plane snapshot. Task reads are bounded
// staleness (seconds of lag are fine for observability); the lease read is
// strong because renewal recency is the jobs' liveness signal.
func (h *AdminHandler) ControlPlane(ctx context.Context, _ *ControlPlaneInput) (*ControlPlaneOutput, error) {
	if err := h.requireTaskAdmin(); err != nil {
		return nil, err
	}
	stats, err := h.taskAdmin.Stats(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to read queue statistics", err)
	}
	counts, err := h.taskAdmin.CountByTypeState(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to read queue type counts", err)
	}
	live, err := h.taskAdmin.ListLive(ctx, 100)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to list live tasks", err)
	}

	out := &ControlPlaneOutput{}
	out.Body.Now = time.Now().UTC()
	out.Body.WorkersPerInstance = h.workersPerInstance
	out.Body.Connectivity = h.connectivity
	out.Body.Queue = QueueDepth{
		Pending:                 stats.Pending,
		Leased:                  stats.Leased,
		Failed:                  stats.Failed,
		OldestPendingAgeSeconds: int64(stats.OldestPendingAge.Seconds()),
	}
	out.Body.TypeCounts = make([]TypeStateCountItem, 0, len(counts))
	for _, c := range counts {
		out.Body.TypeCounts = append(out.Body.TypeCounts, TypeStateCountItem{
			Type:  c.Type,
			State: string(c.State),
			Count: c.Count,
		})
	}
	out.Body.LiveTasks = make([]LiveTaskItem, 0, len(live))
	for _, t := range live {
		out.Body.LiveTasks = append(out.Body.LiveTasks, LiveTaskItem{
			ID:          t.ID,
			Type:        t.Type,
			State:       string(t.State),
			ResourceID:  t.ResourceID,
			Attempts:    t.Attempts,
			MaxAttempts: t.MaxAttempts,
			Priority:    t.Priority,
			AvailableAt: t.AvailableAt,
			LeaseExpiry: t.LeaseExpiry,
			LeaseHolder: t.LeaseHolder,
			LastError:   t.LastError,
			UpdatedAt:   t.UpdatedAt,
		})
	}
	out.Body.Leaders = make([]LeaderLeaseItem, 0)
	if h.leaseAdmin != nil {
		leases, err := h.leaseAdmin.ListLeases(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to list leader leases", err)
		}
		for _, l := range leases {
			out.Body.Leaders = append(out.Body.Leaders, LeaderLeaseItem{
				Name:       l.Name,
				Holder:     l.Holder,
				AcquiredAt: l.AcquiredAt,
				Expiry:     l.Expiry,
				RenewedAt:  l.RenewedAt,
			})
		}
	}
	if err := h.fillPolicyPanels(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// fillPolicyPanels attaches the operator-policy blocks (the sweep switches
// and the autoscaler picture) to the snapshot.
func (h *AdminHandler) fillPolicyPanels(ctx context.Context, out *ControlPlaneOutput) error {
	if h.sweepPolicy != nil {
		policy, err := h.sweepPolicy.GetSweepPolicy(ctx)
		if err != nil {
			return huma.Error500InternalServerError("failed to read the sweep policy", err)
		}
		out.Body.Sweeps = policy
	}
	if h.fleetPolicy == nil || h.standby == nil {
		return nil
	}
	fleet, _, err := h.fleetPanel(ctx, out.Body.Now)
	if err != nil {
		return err
	}
	out.Body.Fleet = fleet
	return nil
}

// fleetPanel composes the autoscaler block from the same policy, inventory and
// demand the keeper reads, through the same composition.
//
// It returns the whole per-pool view beside the block, because the two pages
// this serves want different halves of one composition: the operations card
// draws the deployment's summed picture, and the autoscaler page draws a region
// per pool. Composing twice would be two derivations of one number.
//
// now is the snapshot's own instant, the one every other panel here is built
// against, rather than a fresh read: the demand window and the schedule windows
// are two views of the same moment, and reading the clock twice lets a request
// land either side of a window's edge and report a target for neither.
func (h *AdminHandler) fleetPanel(ctx context.Context, now time.Time) (FleetPanelItem, hostconfig.FleetView, error) {
	policy, err := h.fleetPolicy.GetFleetPolicy(ctx)
	if err != nil {
		return FleetPanelItem{}, hostconfig.FleetView{},
			huma.Error500InternalServerError("failed to read the fleet policy", err)
	}
	density, err := h.fleetPolicy.GetHostDensity(ctx)
	if err != nil {
		return FleetPanelItem{}, hostconfig.FleetView{},
			huma.Error500InternalServerError("failed to read the auto-host density", err)
	}
	inventory, err := h.standby.StandbyInventory(ctx, host.ProvisionLead)
	if err != nil {
		return FleetPanelItem{}, hostconfig.FleetView{},
			huma.Error500InternalServerError("failed to count standing inventory", err)
	}
	window, degradation, err := h.demandPicture(ctx, policy, now)
	if err != nil {
		return FleetPanelItem{}, hostconfig.FleetView{}, err
	}
	view := hostconfig.ComposeFleet(policy, now, inventory, window)
	return FleetPanelItem{
		Status:         view.Fleet,
		Policy:         policy,
		DeviceSlots:    view.Fleet.Target * density.Effective,
		GPUDegradation: degradation,
	}, view, nil
}

// demandPicture reads what the horizon saw: the misses, and how often a create
// that wanted a GPU was placed without one. Both zero when the signal is
// disabled or the reader is unwired.
//
// One `since` for both reads, so the split the card shows and the ratio beside
// it describe the same window. Two horizons would put two populations side by
// side and invite an operator to compare them.
//
// A failed degradation read fails the snapshot, exactly as a failed window read
// does. They are the same table and the same class of failure, so tolerating one
// would leave the page blank anyway. A zero ratio is not "unknown", it is
// the claim that nothing was degraded.
func (h *AdminHandler) demandPicture(ctx context.Context, policy hostconfig.FleetPolicy, now time.Time) (
	hostconfig.DemandWindow, hostconfig.GPUDegradation, error,
) {
	since := policy.DemandSince(now)
	if h.demand == nil || since.IsZero() {
		return hostconfig.DemandWindow{}, hostconfig.GPUDegradation{}, nil
	}
	window, err := h.demand.Demand(ctx, since)
	if err != nil {
		return hostconfig.DemandWindow{}, hostconfig.GPUDegradation{},
			huma.Error500InternalServerError("failed to read the demand window", err)
	}
	degradation, err := h.demand.GPUDegradation(ctx, since)
	if err != nil {
		return hostconfig.DemandWindow{}, hostconfig.GPUDegradation{},
			huma.Error500InternalServerError("failed to read the GPU degradation", err)
	}
	return window, degradation, nil
}
