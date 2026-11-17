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

package placement

import (
	"context"
	"fmt"
	"math/rand/v2"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/batch"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

// Prefs carries the per-create terms a placement query applies on top of the
// GPU predicate and the retry exclusions: how much room the device needs,
// which capacity it is fenced off, then the soft ordering applied ahead of the
// bin-pack default.
//
// Pool, Instances and CPUOnly filter. The ordering terms below them never do:
// an empty result means "no capacity", not "no preferred capacity".
type Prefs struct {
	// Pool is the subset of the fleet this device may land in, as ResolvePool
	// judged it. A hard term and the first one: a pool partitions the machines,
	// so a host in another pool is not capacity for this create however much
	// room it has. Its shape, its lease and the bounds it is held to all belong
	// to a population this create was not counted in.
	//
	// Empty is the default pool, the same rule a host row naming no pool
	// follows.
	Pool string

	// Instances is how many of a host's slots the device will occupy, one per
	// CVD instance it runs. A host has to have that many free to be a
	// candidate at all. The counter it is compared against is denominated in
	// instances, so a 4-slot host already holding one 2-instance device has
	// two. Zero reads as one: every device runs at least one instance.
	Instances int64
	// CPUOnly fences the device off every host carrying an accelerator, for
	// modes whose guarantee depends on the host having none
	// (device.GPUMode.RequiresCPUHost). It filters candidates and never only
	// ranks them. A miss provisions a CPU host, which is what the create
	// wanted anyway.
	CPUOnly bool
	// AffineFetchKey orders hosts whose artifact-cache ledger holds this
	// pinned fetch key first, skipping the artifact fetch on a hit. Empty
	// disables the term (tracking builds, unregistered references).
	AffineFetchKey string
	// PreferStandard orders STANDARD-capacity hosts ahead of Spot. An
	// interactive session outlives a one-time fetch, so this term outranks
	// affinity.
	PreferStandard bool
}

// CapacityStore is the fleet-capacity view placement consumes. The Spanner
// CapacityRepository satisfies it.
type CapacityStore interface {
	// PickHostForPlacement returns the host a declarative create should land
	// on: belongs to prefs.Pool, has room for prefs.Instances, is running or
	// still coming up with its provision not yet refused for capacity, ordered
	// by prefs then most-loaded first (bin-pack) then VM-exists first, and,
	// when gpuOnly, carries a GPU, or, when prefs.CPUOnly, carries none. Among
	// the leading hosts the ordering ranks alike it answers at random, so
	// concurrent creates spread rather than all commit against one host. warm
	// reports the affinity preference matched on the picked host.
	// Empty when no candidate matches. Advisory only; the create's atomic
	// capacity re-check is what enforces capacity.
	PickHostForPlacement(ctx context.Context, gpuOnly bool, excluded []string, prefs Prefs) (hostID string, warm bool, err error)

	// ListPlaceableCapacity is the batch pack planner's view of the fleet:
	// every host automatic placement may use (same predicate as
	// PickHostForPlacement) with its free slot count and the same ordering, so
	// warm hosts fill first. Advisory in the same way: capacity is re-checked
	// at commit.
	//
	// A free slot is a free INSTANCE, so the planner's "one slot, one member"
	// arithmetic holds only because batch members are always single-instance:
	// the batch slice has no env_config path. Give batches one and this
	// boundary oversubscribes silently.
	ListPlaceableCapacity(ctx context.Context, gpuOnly bool, prefs Prefs) ([]batch.HostSlot, error)
}

// CapacityRepository queries the fleet's placeable capacity in Cloud Spanner.
type CapacityRepository struct {
	client *spannerplatform.Client
}

// NewCapacityRepository creates the placeable-capacity repository.
func NewCapacityRepository(client *spannerplatform.Client) *CapacityRepository {
	return &CapacityRepository{client: client}
}

// placementQuery parameterizes the two intended differences between the
// placement consumers. The predicate and the ordering come from
// placementStatement, the single copy both ride.
type placementQuery struct {
	// selectList is the consumer's projection.
	selectList string
	// excluded is the pick path's retry filter; nil binds an empty (no-op)
	// list so the SQL itself never varies.
	excluded []string
	// limit truncates to that many leading candidates; zero leaves the fleet
	// view whole.
	limit int64
}

// affinityWarmExpr is the affinity term: does the host's ledger hold the
// wanted pinned fetch key? Shared between ORDER BY and the pick's warm
// projection so the reported fact is the ordered-by fact. An empty key
// matches no ledger row, turning the term constant, so preferences degrade to
// plain bin-packing instead of branching the SQL.
const affinityWarmExpr = `EXISTS(SELECT 1 FROM host_artifact_cache c
	             WHERE c.host_id = hosts.host_id AND c.fetch_key = @affineKey)`

// preferStandardExpr is the STANDARD-first term, shared the same way and for
// the same reason: the class the pick spreads within has to be the class the
// ordering ranked, and one copy is what guarantees that.
const preferStandardExpr = `IF(@preferStandard, provisioning_model = 'STANDARD', FALSE)`

// provisionedExpr is the ready term: does the host's VM already exist? Shared
// between the predicate (where it exempts a host from the provision-blocked
// fence), the ORDER BY (where it separates equally loaded hosts) and the pick's
// projection (where it is a class bit), so all three read one fact.
const provisionedExpr = `status IN UNNEST(@provisioned)`

// pickProjection is the pick path's row: the host, the affinity fact the
// caller reports, and the three further bits deciding which candidates the
// spread may treat as interchangeable. Occupancy is the sign of the very
// column the bin-pack sorts on, so all four bits are ordering terms read back
// out.
const pickProjection = `host_id, ` + affinityWarmExpr + ` AS warm,
	        ` + preferStandardExpr + ` AS standard,
	        current_device_count > 0 AS occupied,
	        ` + provisionedExpr + ` AS ready`

// poolMatchExpr is the pool term: a column holding no name is the default pool,
// never a pool of its own. host.NormalizePool is the same rule in Go, down to
// the empty string it also folds. Written as a comparison on the folded value
// rather than as two ORed cases, because the fold is the definition of which
// pool a row is in and a row can only be in one.
const poolMatchExpr = `IFNULL(NULLIF(pool, ''), @poolDefault) = @pool`

// placementStatement builds the one placement predicate and ordering shared
// by every automatic-placement consumer. New terms land here and nowhere
// else. The pick and the fleet view can only diverge in the parameterized
// projection, retry filter and limit, which the parity test pins.
//
// Pool: the first hard term, because it says which machines are this create's
// fleet at all. Every other term below narrows within that partition, the GPU
// pair included. A create routed to the GPU pool is fenced onto accelerated
// machines by being in that pool, not by a second axis that could disagree with
// it.
//
// Capacity: current_device_count counts INSTANCES, so the whole group has to
// fit. A device wanting eight instances is not a candidate for a host with
// one slot free, which comparing the count alone would have said it was.
//
// GPU: the host row's own gpu_count and nothing else. It used to fall back to a
// machine_types subquery, because an integrated shape left the column at zero;
// the host row now states its accelerator at create, so the capability is a
// property of the host rather than something re-derived from a table an operator
// edits. Disabling g2 in the catalog no longer strips GPU-placeability from
// running g2 hosts. The cpuOnly fence reads that same column, so wanting a GPU
// and being fenced off one are answered by one fact.
//
// Provision-blocked: a host still coming up is admitted so concurrent creates
// pack onto the host the first one started, but only while GCE has not refused
// its provision for capacity. A stocked-out host otherwise absorbs every
// same-pool create until its slots fill, and each of them then waits out the
// whole retry ladder instead of reaching ProvisionSpec, where the stockout
// memory would have bought a shape that has capacity. The fence reads the host
// row's own marker rather than the zone-health cell, because failover may have
// moved the host to another zone since the refusal and the row is what the
// create binds to. A host whose VM exists is exempt: it serves creates whatever
// its history says, so a marker nothing cleared can never fence a running host.
//
// Ordering: STANDARD-first (interactive only) outranks affinity, because a
// session outlives a one-time fetch, then affinity, then the bin-pack default.
// Among hosts the bin-pack ranks alike, one whose VM exists comes before one
// still coming up. Below the bin-pack and not above it: ranked above, every
// create arriving while a healthy host provisioned would open an idle running
// standby host instead of packing onto it, and each of those spends a standby
// host and buys its replacement.
func placementStatement(q placementQuery, gpuOnly bool, prefs Prefs) spanner.Statement {
	if q.excluded == nil {
		q.excluded = []string{}
	}
	sql := `SELECT ` + q.selectList + ` FROM hosts
	      WHERE ` + poolMatchExpr + `
	        AND status IN UNNEST(@statuses)
	        AND (` + provisionedExpr + ` OR provision_blocked_at IS NULL)
	        AND degraded_at IS NULL
	        AND NOT exclusive
	        AND current_device_count + @needed <= max_devices
	        AND host_id NOT IN UNNEST(@excluded)
	        AND (@gpuOnly = false OR gpu_count > 0)
	        AND (@cpuOnly = false OR gpu_count = 0)
	      ORDER BY ` + preferStandardExpr + ` DESC,
	        ` + affinityWarmExpr + ` DESC,
	        current_device_count DESC,
	        ` + provisionedExpr + ` DESC, created_at ASC`
	params := map[string]any{
		"pool":           host.NormalizePool(prefs.Pool),
		"poolDefault":    host.PoolDefault,
		"statuses":       host.PlacementEligibleStatuses,
		"provisioned":    host.ProvisionedStatuses,
		"excluded":       q.excluded,
		"needed":         max(prefs.Instances, 1),
		"gpuOnly":        gpuOnly,
		"cpuOnly":        prefs.CPUOnly,
		"affineKey":      prefs.AffineFetchKey,
		"preferStandard": prefs.PreferStandard,
	}
	if q.limit > 0 {
		sql += `
	      LIMIT @limit`
		params["limit"] = q.limit
	}
	return spanner.Statement{SQL: sql, Params: params}
}

// pickWindow is how many of the ordering's leading rows the pick reads so it
// has something to spread concurrent creates across.
//
// One row, a LIMIT 1, is the maximal-contention shape: every create in
// flight is handed the same host, and it is the host with the LEAST headroom,
// because the bin-pack prefers the fullest. An attempt is spent only when the
// picked host fills between the pick and the atomic create, so concentrating
// the picks maximizes exactly the collision the caller's retry budget then
// absorbs, and a create that exhausts that budget provisions a host while the
// fleet still had room.
//
// Four divides a burst without loosening the packing the ordering exists for:
// the create still lands on one of the four fullest hosts of its class.
const pickWindow = 4

// placementCandidate is one row of the pick window: the host, and the class
// bits the shared ordering ranked it by.
type placementCandidate struct {
	hostID   string
	warm     bool
	standard bool
	occupied bool
	ready    bool
}

// spreadPick answers with one row of the window, drawn uniformly from the rows
// agreeing with the leading row on every class bit. The ordering ranks by those
// bits before it bin-packs (STANDARD-first, then affinity, then the count
// occupancy is the sign of), so the agreeing rows are a contiguous prefix.
//
// The class boundary is where the spread stops, because each crossing trades a
// guarantee the ordering makes for a coin flip:
//
//   - STANDARD: an interactive create would land on Spot while a standard host
//     stood free, and be told its session may be preempted. That warning is
//     only accurate once nothing standard was left.
//   - warm: the create would fetch artifacts a free host already held.
//   - occupied: the create would open an EMPTY host while an occupied one had
//     room. Standby membership is exactly current_device_count = 0, so on a
//     small fleet each such spread spends a standby host and buys its
//     replacement.
//   - ready: the create would wait on a host still coming up while one whose
//     VM exists, and that the ordering ranked alike on everything above, could
//     have served it now.
//
// One row in the class is a LIMIT 1 pick unchanged, which is what leaves a
// small fleet where it was; identical creates against a homogeneous fleet put
// the whole window in one class and get the whole spread.
func spreadPick(window []placementCandidate) (placementCandidate, bool) {
	if len(window) == 0 {
		return placementCandidate{}, false
	}
	class := 1
	for class < len(window) && sameClass(window[class], window[0]) {
		class++
	}
	return window[rand.IntN(class)], true //nolint:gosec // placement spread; not security-sensitive
}

// sameClass reports whether two candidates sit in one class: the terms that
// carry a guarantee rank them alike, leaving only how full and how old they
// are between them.
func sameClass(a, b placementCandidate) bool {
	return a.standard == b.standard && a.warm == b.warm && a.occupied == b.occupied && a.ready == b.ready
}

// PickHostForPlacement returns a placement candidate: room for the device's
// instances, preference-ordered (affinity, STANDARD-first) then most-loaded
// first (bin-pack, so hosts drain and get reaped), a host whose VM exists
// before one still coming up, oldest first as the tiebreak. It reads the
// leading pickWindow rows and spreads across those the ordering could not
// separate, so concurrent creates stop racing for one host.
// gpuOnly restricts to hosts carrying a GPU, attached or integrated, since
// both state it in gpu_count; prefs.CPUOnly is its fence, restricting to hosts
// carrying none. Empty when no candidate matches. It is advisory only; the
// create's atomic capacity re-check is what enforces capacity.
func (r *CapacityRepository) PickHostForPlacement(ctx context.Context, gpuOnly bool, excluded []string, prefs Prefs) (string, bool, error) {
	stmt := placementStatement(placementQuery{
		selectList: pickProjection,
		excluded:   excluded,
		limit:      pickWindow,
	}, gpuOnly, prefs)
	var window []placementCandidate
	if err := r.client.Single().Query(ctx, stmt).Do(func(row *spanner.Row) error {
		var c placementCandidate
		// provisioning_model is nullable, so the STANDARD term is NULL on a host
		// row that never stated its model. The ordering already ranks those
		// below STANDARD; reading NULL as not-standard keeps the class bit
		// agreeing with the rank it came from.
		var standard spanner.NullBool
		if err := row.Columns(&c.hostID, &c.warm, &standard, &c.occupied, &c.ready); err != nil {
			return err
		}
		c.standard = standard.Valid && standard.Bool
		window = append(window, c)
		return nil
	}); err != nil {
		return "", false, fmt.Errorf("picking placement host: %w", err)
	}
	pick, ok := spreadPick(window)
	if !ok {
		return "", false, nil
	}
	return pick.hostID, pick.warm, nil
}

// ListPlaceableCapacity returns every automatic-placement candidate with its
// free slot count, same ordering as PickHostForPlacement without the LIMIT:
// the batch pack planner needs the whole fleet view to spread N devices, and
// filling slots in order fills warm hosts first. Advisory in the same way;
// the create's atomic capacity re-check is what enforces capacity.
//
// The projected free count is free INSTANCES, as max_devices and the counter
// both are. The planner spends one per member, which is right only while
// batch members are single-instance (the batch slice has no env_config path);
// an env_config there has to spend the member's instance count instead.
func (r *CapacityRepository) ListPlaceableCapacity(ctx context.Context, gpuOnly bool, prefs Prefs) ([]batch.HostSlot, error) {
	stmt := placementStatement(placementQuery{
		selectList: "host_id, max_devices - current_device_count AS free",
	}, gpuOnly, prefs)
	var slots []batch.HostSlot
	if err := r.client.Single().Query(ctx, stmt).Do(func(row *spanner.Row) error {
		var hostID string
		var free int64
		if err := row.Columns(&hostID, &free); err != nil {
			return err
		}
		slots = append(slots, batch.HostSlot{HostID: hostID, Free: int(free)})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("listing placeable capacity: %w", err)
	}
	return slots, nil
}

// Ensure the repository satisfies the store port.
var _ CapacityStore = (*CapacityRepository)(nil)
