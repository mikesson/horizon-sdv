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

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	gspanner "cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	builddomain "cloud-cuttlefish-orchestrator/internal/domain/build"
	devicedomain "cloud-cuttlefish-orchestrator/internal/domain/device"
	hostdomain "cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/features/device/placement"
	hostfeature "cloud-cuttlefish-orchestrator/internal/features/host"
	"cloud-cuttlefish-orchestrator/internal/jobs/hostreconciler"
	"cloud-cuttlefish-orchestrator/internal/jobs/leaderelection"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

const (
	statusDeleted   = "deleted"
	statusError     = "error"
	statusPreempted = "preempted"
)

// newPlatformClient builds a platform Spanner client for checkers that replay
// production repositories against the final database.
func newPlatformClient(ctx context.Context, cfg Cfg) (*spannerplatform.Client, error) {
	scfg := spannerplatform.DefaultConfig()
	scfg.ProjectID, scfg.InstanceID = cfg.Project, cfg.Instance
	scfg.DatabaseID, scfg.Emulator = cfg.Database, cfg.Emulator
	return spannerplatform.NewClient(ctx, scfg) //nolint:wrapcheck // harness plumbing; callers annotate.
}

// findings is the invariant-checker output for one run.
type findings struct {
	hostsSeen, devicesSeen int

	// Conservation.
	deviceCountDrift    []string // hosts.current_device_count vs the instances its live devices desire
	protectedCountDrift []string // hosts.protected_device_count vs pinned live devices
	quotaDrift          []string // quotas.used_hosts vs live hosts
	overCapacity        []string // hosts packed past max_devices (placement invariant)

	// Stranded states.
	stranded []string // transitional resource with no pending/leased task

	// Promises.
	lapsedPins []string // pin expired long ago yet still pinned

	// Leadership: one election for the whole background plane, plus a handover
	// to a survivor in a run that killed its leader.
	electionViolations []string
	leaseNames         []string // lease rows surviving at quiesce (report stat)
	leaseHolders       int      // instances observed leading mid-run (report stat)

	// Boundary orphans (fake ledgers vs database).
	leakedVMs      []string // live VM without a live host row
	leakedCVDs     []string // live CVD group without a live device
	actuationRaces []string // start/stop/delete against an in-flight launch (adopt-race)
	tasksLeft      int64    // pending+leased leftovers
	underAttDLQ    []string // dead-letter with attempts < max

	// Minimum evidence: a run that passes because nothing happened is not a
	// pass (Jepsen's "set was never read" verdict). Dead-letters and error
	// resources are violations outright unless an attempt-burning nemesis ran.
	vacuity        []string // op families that never once succeeded; an actuator that never launched
	unexpectedDLQ  []string // dead-letters with no attempt-burning nemesis configured
	unexpectedErrs []string // error-status resources with no attempt-burning nemesis configured
	launchedGroups int      // CVD groups the fake HO actually launched (report stat)
	instanceDeaths []string // instances that exited without being killed or terminated

	// Client-perspective conservation, the set checker's discipline: an
	// acknowledged create must still have a row (ok means it definitely
	// happened); an errored create may or may not have committed and is
	// never judged; a delete ATTEMPT (ok, or an ambiguous err that may have
	// committed) excuses absence, on the resource or on the host whose
	// physical row removal cascades its interleaved devices. Exclusive-verb
	// operations must all settle, because an open row at quiesce blocks that
	// resource's admission forever.
	ackedHostIDs      map[string]struct{} // ok host-create acks from the ledgers
	ackedDeviceIDs    map[string]string   // ok device-create acks (incl. idem probes) -> creating host
	deviceDeleteTried map[string]struct{} // device_delete attempts, any outcome
	hostDeleteTried   map[string]struct{} // host_delete attempts, any outcome
	idemKeyIDs        map[string][]string // idempotency key -> every device ID acked under it
	ackedLost         []string            // acked create whose row vanished uncommanded
	openOps           []string            // operations rows still open at quiesce
	openOpsSeen       int64               // open rows counted (report stat; violations gated on chaos)

	// Mid-run snapshots (sampler.go): always-invariant violations seen at a
	// consistent timestamp during the run. This is drift the quiesce read
	// would never see, because something later healed or masked it.
	samples        int      // snapshots taken (report stat)
	samplesFailed  int      // snapshots that errored (report stat)
	transientSeen  int      // violations observed across all samples
	transientDrift []string // first samples of them (capped)

	// Drift outcomes: each injected drift must reach ITS outcome. Preempt
	// recovers, vanish is honored as deletion. A shared contract verified
	// nothing about which.
	driftOutcomes []string

	// Placement outcomes under the stockout nemesis: a pinned host must never
	// leave its requested zone, and a failover-eligible host must never
	// dead-letter on a capacity error while the other zone had room.
	placementViolations []string
	failovers           int64 // host.zone_failover audit entries (report stat)

	// Idempotency probes.
	idemViolations []string

	// Warm-pool scenario (pool-floor > 0): the keeper must hold the floor
	// without runaway provisioning.
	poolViolations []string
	poolIdle       int64 // final idle inventory (report stat)
	poolKeeperLive int64 // live keeper-created hosts (report stat)

	// Fleet-floor scenario (fleet-floor > 0): the fleet the keeper RUNS, held
	// against a bound the free-host buffer knows nothing about. Its own fields
	// because it is a different population from the one above. That one counts
	// idle hosts, this one counts hosts a device may already be sitting on.
	floorViolations []string
	floorTotal      int64         // fleet-class hosts still standing (report stat)
	floorDraining   int64         // ... of which are running their leases out (report stat)
	floorKeeperLive int64         // live keeper-created hosts (report stat)
	floorOldest     time.Duration // age of the oldest keeper host (report stat)

	// Reclamation: no lowered lease may leave a live device promised more life
	// than the host under it, and the reclaim scenario must actually reclaim.
	reclaimViolations []string
	shortens          int64 // host.lease_shorten entries, both reclamations (report stat)
	surplusShortens   int64 // ... of which the fleet gave back against its target
	renewals          int64 // host.lease_extend entries (report stat)
	leaseCrossings    int   // worst shorten/renew oscillation on any one host

	// The dedicated-host collapse. Which hosts were fenced by a USER and which
	// were provisioned FOR A GROUP cannot be recovered at quiesce, because host
	// rows hard-delete and the lease trail carries no origin. Both therefore
	// come from the client's own ledger, the discipline the acked-create
	// checker already uses.
	collapses     int64               // lease_shorten entries stamped as a dedicated-empty collapse
	collapsedIDs  []string            // the hosts those entries name, in trail order
	groupHostIDs  map[string]struct{} // hosts this workload provisioned for a group
	fencedHostIDs map[string]struct{} // hosts this workload fenced as a user would

	opCounts map[string]int // "<kind>/<outcome>" -> n
}

func (f *findings) violations() []string {
	var out []string
	add := func(kind string, items []string) {
		for _, it := range items {
			out = append(out, kind+": "+it)
		}
	}
	add("device-count", f.deviceCountDrift)
	add("protected-count", f.protectedCountDrift)
	add("quota", f.quotaDrift)
	add("over-capacity", f.overCapacity)
	add("stranded", f.stranded)
	add("lapsed-pin", f.lapsedPins)
	add("leaked-vm", f.leakedVMs)
	add("leaked-cvd", f.leakedCVDs)
	add("actuation-race", f.actuationRaces)
	add("dlq-under-attempted", f.underAttDLQ)
	add("drift-outcome", f.driftOutcomes)
	add("placement", f.placementViolations)
	add("idempotency", f.idemViolations)
	add("election", f.electionViolations)
	add("pool", f.poolViolations)
	add("fleet-floor", f.floorViolations)
	add("reclaim", f.reclaimViolations)
	add("vacuity", f.vacuity)
	add("dlq-unexpected", f.unexpectedDLQ)
	add("error-unexpected", f.unexpectedErrs)
	add("instance-death", f.instanceDeaths)
	add("acked-lost", f.ackedLost)
	add("open-operation", f.openOps)
	add("transient", f.transientDrift)
	if extra := f.transientSeen - len(f.transientDrift); extra > 0 {
		out = append(out, fmt.Sprintf("transient: ... %d more sample violations elided", extra))
	}
	if f.tasksLeft > 0 {
		out = append(out, fmt.Sprintf("queue: %d pending/leased tasks survived quiescence", f.tasksLeft))
	}
	return out
}

// check runs the invariant suite against the quiescent database, the fake
// boundaries' ledgers, and the ops ledgers.
func (d *driver) check(ctx context.Context) (*findings, error) {
	f := &findings{opCounts: make(map[string]int)}

	hosts, err := d.loadHosts(ctx)
	if err != nil {
		return nil, err
	}
	devices, err := d.loadDevices(ctx)
	if err != nil {
		return nil, err
	}
	f.hostsSeen, f.devicesSeen = len(hosts), len(devices)

	d.checkConservation(ctx, f, hosts, devices)
	checkCapacity(f, hosts)
	if err := d.checkStranded(ctx, f, hosts, devices); err != nil {
		return nil, err
	}
	checkPins(f, devices, 3*d.cfg.ReapEvery+d.cfg.maxAbsSkew())
	checkOrphans(f, hosts, devices, d.gce.Live(), d.ho.LiveGroups(), d.ho.ActuationViolations())
	d.checkDriftOutcomes(f, hosts)
	d.checkPlacement(ctx, f, hosts)
	if err := d.checkElection(ctx, f); err != nil {
		return nil, err
	}
	d.checkPool(ctx, f)
	d.checkFleetFloor(ctx, f)
	d.checkReclaim(ctx, f)
	if err := d.checkTasks(ctx, f); err != nil {
		return nil, err
	}
	if err := d.readOpsLedgers(f); err != nil {
		return nil, err
	}
	checkAckedCreates(f, hosts, devices)
	// After the ledgers: the collapse's scope is judged against what the client
	// asked for, not against rows that may no longer exist.
	d.checkCollapse(f)
	checkIdem(f)
	if err := d.checkOpenOperations(ctx, f); err != nil {
		return nil, err
	}
	created, _ := d.ho.Ledger()
	checkEvidence(f, &d.cfg, hosts, devices, created)
	d.mu.Lock()
	f.instanceDeaths = append(f.instanceDeaths, d.instDeaths...)
	f.samples, f.samplesFailed = d.samples, d.samplesFailed
	f.transientSeen = d.transientSeen
	f.transientDrift = append(f.transientDrift, d.transientDrift...)
	d.mu.Unlock()
	return f, nil
}

// checkEvidence is the minimum-evidence gate. Every other checker asserts an
// absence, so a run in which nothing ever worked (every create refused, every
// device dead on arrival) satisfies all of them vacuously and would gate CI
// green. Creates must have been admitted, the CVD actuator must have
// launched at least one real CVD group (admission oks alone don't prove that:
// a fleet whose every device dead-lettered into error still collected them),
// and deletes attempted often enough must have succeeded at least once. In a
// run with no attempt-burning nemesis, dead-letters and error-status
// resources are violations outright, because nothing legitimate fails
// permanently on a healthy fleet. Under chaos they remain tolerated
// collateral, judged by the attempt-exhaustion and drift/placement checkers
// instead.
func checkEvidence(f *findings, cfg *Cfg, hosts []hostRow, devices []deviceRow, created map[string][]string) {
	for name, kinds := range map[string][]string{
		opHostCreate:   {opHostCreate, opHostCreateAuto},
		opDeviceCreate: {opDeviceCreate, opDeviceCreateAuto, opDeviceCreateAffine, opDeviceCreateGroup},
	} {
		var ok int
		for _, k := range kinds {
			ok += f.opCounts[k+"/ok"]
		}
		if ok == 0 {
			f.vacuity = append(f.vacuity, fmt.Sprintf("no %s ever succeeded", name))
		}
	}
	// Deletes gate only past an attempt floor so a short exploratory run
	// whose few deletes all raced doesn't trip.
	for _, kind := range []string{opDeviceDelete, opHostDelete} {
		okN, errN := f.opCounts[kind+"/ok"], f.opCounts[kind+"/err"]
		if okN == 0 && errN >= 5 {
			f.vacuity = append(f.vacuity, fmt.Sprintf("%s attempted %d times, never once succeeded", kind, errN))
		}
	}
	for _, groups := range created {
		f.launchedGroups += len(groups)
	}
	if f.launchedGroups == 0 {
		f.vacuity = append(f.vacuity, "no CVD group was ever launched at the HO boundary")
	}

	if cfg.attemptBurners() {
		return
	}
	for _, h := range hosts {
		if h.status == statusError {
			f.unexpectedErrs = append(f.unexpectedErrs, fmt.Sprintf(
				"host %s ended error with no fault injection configured: %q", h.id, h.errMsg))
		}
	}
	for _, v := range devices {
		if v.status == statusError {
			f.unexpectedErrs = append(f.unexpectedErrs, fmt.Sprintf(
				"device %s ended error with no fault injection configured", v.id))
		}
	}
}

type hostRow struct {
	id, status, gceName string
	errMsg              string
	zone, requestedZone string
	zonePinned          bool
	deviceCount         int64
	protectedCount      int64
	maxDevices          int64
}

type deviceRow struct {
	id, hostID, status string
	pinned             bool
	pinnedUntil        gspanner.NullTime
	// instances is the device's DESIRED instance count as the row records it,
	// zero when the row records none; deviceSlots applies the floor.
	instances int64
}

// deviceSlots is what one device costs its host's current_device_count: its
// desired instance count, floored at one. A device is a group of N Android VMs
// and is charged N at admission, refunded the same N at delete; a row whose
// spec never recorded a count still holds its single slot.
func deviceSlots(desired int64) int64 {
	return max(desired, 1)
}

// desiredInstances reads a device row's desired instance count out of its
// opaque desired_spec JSON. Zero when the spec is absent, empty or malformed.
//
// Decoded here rather than summed in SQL on purpose: desired_spec is opaque to
// every query in the system, and a checker that re-derives the invariant from
// the persisted spec is what keeps it independent of the code it judges.
func desiredInstances(desiredSpec string) int64 {
	var spec struct {
		InstanceCount int64 `json:"instance_count"`
	}
	if err := json.Unmarshal([]byte(desiredSpec), &spec); err != nil {
		return 0
	}
	return spec.InstanceCount
}

func (d *driver) loadHosts(ctx context.Context) ([]hostRow, error) {
	iter := d.raw.Single().Query(ctx, gspanner.Statement{
		SQL: `SELECT host_id, status, COALESCE(gce_instance_name, ''),
		             COALESCE(error_message, ''),
		             zone, COALESCE(requested_zone, ''), zone_pinned,
		             current_device_count, COALESCE(protected_device_count, 0),
		             max_devices FROM hosts`,
	})
	defer iter.Stop()
	var out []hostRow
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("loading hosts: %w", err)
		}
		var h hostRow
		if err := row.Columns(&h.id, &h.status, &h.gceName, &h.errMsg,
			&h.zone, &h.requestedZone, &h.zonePinned,
			&h.deviceCount, &h.protectedCount, &h.maxDevices); err != nil {
			return nil, fmt.Errorf("scanning host: %w", err)
		}
		out = append(out, h)
	}
}

// checkDriftOutcomes asserts each injected drift reached the outcome its
// kind demands. The contracts are DISTINCT on purpose. "Lost or preempted"
// verified nothing about which, and that shared contract is how silent
// preemption-recovery loss passed every chaos run:
//
//   - preempt: the host must never carry the lost-mark tombstone (deleted +
//     the outside-CCO reason), which would mean the detector misclassified a
//     preemption as out-of-band deletion. It must also not still sit in
//     'preempted' (a recovery that never completed; the quiesce wait treats
//     'preempted' as transitional for the same reason, so this arm names
//     what the timeout alone would only imply). An error outcome is
//     tolerated only when a nemesis besides drift itself was burning
//     attempts, since recovery dead-lettering in a drift-only run is a
//     failure to recover rather than collateral. Recovery renames and
//     re-provisions, so a live state or a later user deletion (row gone) are
//     always acceptable.
//   - vanish of an OBSERVED host (VM-expecting at injection): the
//     out-of-band deletion must be honored, with the host gone (user delete
//     raced) or tombstoned/tearing down. Any VM-expecting state means the
//     loss was never observed or was resurrected.
//   - vanish inside the provisioning window (host not yet VM-expecting at
//     injection): the provision owner legitimately re-drives its idempotent
//     insert, because the host reconciler's ExpectsVM gate leaves
//     provisioning hosts to their task. A healthy host whose claimed instance
//     is ALIVE again is therefore a correct self-heal and not a resurrection.
//     The violation there is a dangling claim: VM-expecting with no live
//     instance.
func (d *driver) checkDriftOutcomes(f *findings, hosts []hostRow) {
	byID := make(map[string]hostRow, len(hosts))
	byInstance := make(map[string]hostRow, len(hosts))
	for _, h := range hosts {
		byID[h.id] = h
		if h.gceName != "" {
			byInstance[h.gceName] = h
		}
	}
	d.mu.Lock()
	events := make([]driftEvent, len(d.driftEvents))
	copy(events, d.driftEvents)
	d.mu.Unlock()
	var alive map[string]bool
	liveNow := func() map[string]bool {
		if alive == nil {
			alive = liveVMSet(d.gce.Live())
		}
		return alive
	}
	for _, ev := range events {
		h, exists := byID[ev.hostID]
		if ev.hostID == "" {
			// The injection-time owner lookup can fail transiently (a
			// stalled emulator read answers ("","")), which used to leave
			// the event UNJUDGED, so a recovery-less run sailed past this
			// checker and only the quiesce wait caught it. The dead
			// instance's name is still authoritative at check time:
			// recovery renames and deletion clears, so only a host that
			// never resolved the loss still claims it.
			h, exists = byInstance[ev.instance]
		}
		if !exists {
			continue // row fully removed: user deletion completed; fine for both kinds
		}
		switch ev.kind {
		case "preempt":
			judgePreempt(f, &d.cfg, ev, h)
		case "vanish":
			judgeVanish(f, ev, h, liveNow)
		}
	}
}

// judgePreempt applies the preempt arm of the drift contract; the outcome
// taxonomy lives on checkDriftOutcomes.
func judgePreempt(f *findings, cfg *Cfg, ev driftEvent, h hostRow) {
	switch {
	case h.status == statusDeleted && strings.Contains(h.errMsg, hostreconciler.LostMarkReason):
		f.driftOutcomes = append(f.driftOutcomes, fmt.Sprintf(
			"preempted host %s (%s) was marked lost instead of recovered", h.id, ev.instance))
	case h.status == statusPreempted:
		f.driftOutcomes = append(f.driftOutcomes, fmt.Sprintf(
			"preempted host %s (%s) still 'preempted' at quiesce; recovery never completed", h.id, ev.instance))
	case h.status == statusError && !cfg.recoveryBurners():
		f.driftOutcomes = append(f.driftOutcomes, fmt.Sprintf(
			"preempted host %s (%s) dead-lettered %q with only drift chaos configured; recovery should have re-provisioned",
			h.id, ev.instance, h.errMsg))
	}
}

// judgeVanish applies the vanish arm of the drift contract; liveNow lazily
// snapshots the fake's live VM set.
func judgeVanish(f *findings, ev driftEvent, h hostRow, liveNow func() map[string]bool) {
	if !vmExpectingStatus(h.status) && h.status != statusPreempted {
		return // honored: tombstoned, tearing down, or error
	}
	if !vmExpectingStatus(ev.hostStatus) {
		// Injected mid-provision: acceptable iff the owner re-drove the
		// insert and the claimed instance is alive again.
		if h.status != statusPreempted && liveNow()[h.gceName] {
			return
		}
	}
	f.driftOutcomes = append(f.driftOutcomes, fmt.Sprintf(
		"vanished host %s (%s) ended %q; out-of-band deletion not honored", h.id, ev.instance, h.status))
}

// checkPlacement asserts the zone-stockout nemesis's run-level invariants:
//
//   - A pinned host never moves: its physical zone must equal the requested
//     zone in every state, including tombstones. A pin that failed over is a
//     broken promise regardless of how the host ended.
//   - A failover-eligible (non-pinned) host never dead-letters on zonal
//     capacity: the nemesis stocks out at most one zone at a time, so the
//     ladder always had a surviving zone to move to. An error host whose
//     message carries the stockout code means failover never fired.
//
// The complementary invariants come free from the existing checkers: a host
// stranded in provisioning is caught by checkStranded/awaitQuiesce, and a
// stray old-zone instance surviving a failover is caught by the leaked-VM
// orphan check (the row's gce_instance_name moves to the new incarnation, so
// the old VM has no live claimant). Failover volume is read from the audit
// log as a report statistic. Timing decides whether a given run provoked
// any, so a zero is not a violation.
func (d *driver) checkPlacement(ctx context.Context, f *findings, hosts []hostRow) {
	placementRowChecks(f, hosts)

	row, err := d.raw.Single().Query(ctx, gspanner.Statement{
		SQL: `SELECT COUNT(*) FROM audit_logs WHERE action = 'host.zone_failover'`,
	}).Next()
	if err != nil {
		return // stat only; a missing count never fails the run
	}
	_ = row.Columns(&f.failovers)

	d.checkAffinityAdmits(ctx, f)
}

// placementRowChecks is the pure half of checkPlacement: the per-row zone
// contracts, separated from the audit count and the affinity replay so the
// self-tests can drive them.
func placementRowChecks(f *findings, hosts []hostRow) {
	for _, h := range hosts {
		if h.zonePinned && h.zone != h.requestedZone {
			f.placementViolations = append(f.placementViolations, fmt.Sprintf(
				"pinned host %s moved: requested %s, ended in %s (%s)", h.id, h.requestedZone, h.zone, h.status))
		}
		if !h.zonePinned && h.status == statusError &&
			strings.Contains(strings.ToUpper(h.errMsg), "RESOURCE_POOL_EXHAUSTED") {
			f.placementViolations = append(f.placementViolations, fmt.Sprintf(
				"host %s dead-lettered on stockout in %s instead of failing over: %q", h.id, h.zone, h.errMsg))
		}
	}
}

// checkAffinityAdmits replays the PRODUCTION pick with the affinity arm's
// fetch key against the run's final fleet and ledger: affinity must reorder
// candidates and never admit one, so any answer has to satisfy the same
// eligibility predicate a cold pick enforces. The ledger rows behind the key
// are whatever the run's affine creates and chaos left; when none survive on
// an ineligible host the check passes without exercising the temptation, so
// the deterministic burden stays with the emulator tests.
func (d *driver) checkAffinityAdmits(ctx context.Context, f *findings) {
	client, err := newPlatformClient(ctx, d.cfg)
	if err != nil {
		f.placementViolations = append(f.placementViolations,
			fmt.Sprintf("affinity check could not build a client: %v", err))
		return
	}
	defer client.Close()

	key := builddomain.FetchKey(harnessPinnedBuildID, harnessPinnedTarget)
	id, _, err := placement.NewCapacityRepository(client).PickHostForPlacement(
		ctx, false, nil, placement.Prefs{AffineFetchKey: key})
	if err != nil {
		f.placementViolations = append(f.placementViolations,
			fmt.Sprintf("affine pick failed against the final fleet: %v", err))
		return
	}
	if id == "" {
		return // no capacity at quiesce is a legal final state
	}
	row, err := d.raw.Single().ReadRow(ctx, "hosts", gspanner.Key{id},
		[]string{"status", "degraded_at", "exclusive", "current_device_count", "max_devices"})
	if err != nil {
		f.placementViolations = append(f.placementViolations,
			fmt.Sprintf("affine pick returned unreadable host %s: %v", id, err))
		return
	}
	var status string
	var degradedAt gspanner.NullTime
	var exclusive bool
	var count, maxDev int64
	if err := row.Columns(&status, &degradedAt, &exclusive, &count, &maxDev); err != nil {
		f.placementViolations = append(f.placementViolations,
			fmt.Sprintf("scanning affine pick %s: %v", id, err))
		return
	}
	if !slices.Contains(hostdomain.PlacementEligibleStatuses, status) || degradedAt.Valid ||
		exclusive || count >= maxDev {
		f.placementViolations = append(f.placementViolations, fmt.Sprintf(
			"affinity admitted ineligible host %s (status=%s degraded=%v exclusive=%v %d/%d)",
			id, status, degradedAt.Valid, exclusive, count, maxDev))
	}
}

// checkPool asserts the standing-capacity scenario's invariants after
// quiescence: the keeper held the target (inventory >= target once the nemeses
// stop, since nothing else is deleting), and never ran away, so live
// keeper-created hosts stay within target + the per-cycle overshoot bound.
// Conservation, quota and orphan-VM coverage come free: keeper hosts are
// ordinary rows and VMs under the existing checkers.
//
// The census below still matches created_by = 'poolkeeper'. That literal is
// frozen on the orchestrator side for the same reason it is copied here: it is
// what makes this check able to fail. The class the keeper actually manages is
// derived from hosts.origin, and counting by that instead would make this
// check agree with StandbyInventory by construction rather than corroborate it.
func (d *driver) checkPool(ctx context.Context, f *findings) {
	if d.cfg.PoolFloor <= 0 {
		return
	}
	client, err := newPlatformClient(ctx, d.cfg)
	if err != nil {
		f.poolViolations = append(f.poolViolations,
			fmt.Sprintf("pool check could not build a client: %v", err))
		return
	}
	defer client.Close()
	pools, err := hostfeature.NewHostRepository(client).StandbyInventory(ctx, hostdomain.ProvisionLead)
	if err != nil {
		f.poolViolations = append(f.poolViolations, fmt.Sprintf("counting pool inventory: %v", err))
		return
	}
	// The whole fleet, because the census below counts every host the keeper
	// created whatever pool it put them in: comparing one pool's inventory
	// against all of them would report an overshoot the moment a second pool
	// held anything.
	inventory := pools.Fleet()
	idle := inventory.Standby
	f.poolIdle = idle
	switch {
	case d.cfg.PoolReclaim:
		// The reclaim scenario lowered the target to zero mid-run, so holding
		// the seeded floor is the wrong outcome. What must have happened
		// instead is convergence the other way: a shortened lease
		// falls below the provisioning lead at once, so the host stops counting
		// as inventory long before the reaper collects it.
		if idle > 0 {
			f.poolViolations = append(f.poolViolations, fmt.Sprintf(
				"target was dropped to 0 and %d idle fleet hosts still count as inventory", idle))
		}
	case idle < int64(d.cfg.PoolFloor):
		f.poolViolations = append(f.poolViolations, fmt.Sprintf(
			"pool floor not held at quiesce: %d idle < floor %d", idle, d.cfg.PoolFloor))
	}

	row, err := d.raw.Single().Query(ctx, gspanner.Statement{
		SQL: `SELECT COUNT(*) FROM hosts
		      WHERE created_by = 'poolkeeper' AND status NOT IN ('deleted', 'deleting')`,
	}).Next()
	if err != nil {
		f.poolViolations = append(f.poolViolations, fmt.Sprintf("counting keeper hosts: %v", err))
		return
	}
	_ = row.Columns(&f.poolKeeperLive)
	// The overshoot bound is still floor + what one cycle may provision, but that
	// budget is now a share of the fleet standing rather than a constant. It is
	// evaluated against the SAME inventory this check read, so the harness cannot
	// drift from the keeper by measuring the fleet at a different instant. At
	// the fleet sizes this harness runs, the share is under its floor of two,
	// so the arm asserts the same number it always did while surviving a fleet
	// where it would not.
	perCycle := hostconfig.TopUpBudget(inventory.Total)
	bound := int64(d.cfg.PoolFloor) + perCycle
	if f.poolKeeperLive > bound {
		f.poolViolations = append(f.poolViolations, fmt.Sprintf(
			"keeper overshoot: %d live keeper hosts > floor %d + cycle budget %d (fleet %d)",
			f.poolKeeperLive, d.cfg.PoolFloor, perCycle, inventory.Total))
	}
}

// checkFleetFloor asserts the OTHER lower bound: the fleet the keeper runs,
// occupied or free, must still be standing at quiesce.
//
// A separate scenario and separate fields from checkPool above, because it
// bounds a separate population and would otherwise be smuggled into a check
// whose stated bounds are about idle hosts. The two flags are separate for the
// same reason: -pool-floor seeds the free-host buffer, and repurposing it would
// silently restate every bound above against a different promise.
//
// The scenario runs with the buffer at ZERO, which is what makes this able to
// fail. Nothing asks for a free host, so a surplus read from the buffer alone
// calls every host the keeper owns spare and gives the fleet back inside a few
// cycles; the floor is the only thing standing between `on` and an empty fleet.
//
// Two vacuity arms, because the assertion has two ways of passing for the wrong
// reason. If the keeper never built anything, nothing was held. And if every
// host it built is younger than host.MinShortenAge, the age guard is what kept
// the fleet and the floor was never consulted, which is the run the aging
// nemesis exists to prevent, so its failure has to be reported as a finding
// about the harness rather than as a passing floor.
func (d *driver) checkFleetFloor(ctx context.Context, f *findings) {
	if d.cfg.FleetFloor <= 0 {
		return
	}
	client, err := newPlatformClient(ctx, d.cfg)
	if err != nil {
		f.floorViolations = append(f.floorViolations,
			fmt.Sprintf("fleet-floor check could not build a client: %v", err))
		return
	}
	defer client.Close()
	pools, err := hostfeature.NewHostRepository(client).StandbyInventory(ctx, hostdomain.ProvisionLead)
	if err != nil {
		f.floorViolations = append(f.floorViolations,
			fmt.Sprintf("counting the standing fleet: %v", err))
		return
	}
	inventory := pools.Fleet()
	f.floorTotal, f.floorDraining = inventory.Total, inventory.Draining
	if inventory.Total < int64(d.cfg.FleetFloor) {
		f.floorViolations = append(f.floorViolations, fmt.Sprintf(
			"the fleet fell below its floor: %d fleet-class hosts standing < floor %d",
			inventory.Total, d.cfg.FleetFloor))
	}

	// The keeper's own hosts, by created_by, for the reason checkPool states:
	// deriving the class from hosts.origin here would make this agree with
	// StandbyInventory by construction rather than corroborate it.
	row, err := d.raw.Single().Query(ctx, gspanner.Statement{
		SQL: `SELECT COUNT(*), IFNULL(MIN(created_at), CURRENT_TIMESTAMP()) FROM hosts
		      WHERE created_by = 'poolkeeper' AND status NOT IN ('deleted', 'deleting')`,
	}).Next()
	if err != nil {
		f.floorViolations = append(f.floorViolations,
			fmt.Sprintf("counting keeper hosts: %v", err))
		return
	}
	var oldest time.Time
	if cerr := row.Columns(&f.floorKeeperLive, &oldest); cerr != nil {
		f.floorViolations = append(f.floorViolations,
			fmt.Sprintf("reading the keeper census: %v", cerr))
		return
	}
	f.floorOldest = time.Since(oldest)
	if f.floorKeeperLive == 0 {
		// Both readings, because at quiesce they are indistinguishable and both
		// are findings. Either the keeper never built a host, in which case the
		// floor was never what held this fleet and every bound above passed
		// without reaching the code it bounds, or it built hosts and gave them
		// all back, which is the reclamation the floor exists to stop.
		f.floorViolations = append(f.floorViolations,
			"the keeper holds none of its own hosts: it either never built one, so nothing here reached the floor, or it gave back everything it built to hold it")
		return
	}
	if f.floorOldest < hostdomain.MinShortenAge {
		f.floorViolations = append(f.floorViolations, fmt.Sprintf(
			"no keeper host reached the shorten age (oldest %s < %s): the age guard is what kept this fleet, not the floor",
			f.floorOldest.Round(time.Second), hostdomain.MinShortenAge))
	}
	// And the arm that actually catches a surplus computed from the buffer
	// alone. The count above cannot: a lowered lease does not remove a host, it
	// leaves it standing and placement-eligible for the whole drain grace, so a
	// fleet already handed to the reaper still totals its floor for minutes
	// after the decision that gave it away.
	//
	// The keeper's own hosts are what the scenario controls. With the buffer at
	// zero and demand off, the floor is the only reason any of them exists, and
	// nothing but a surplus shorten can take one away inside a run this short:
	// the leases it hands out are hours long and no nemesis touches them. So
	// holding fewer than the floor asked for is the reclamation the floor was
	// supposed to stop, seen through the one population it cannot be confused
	// about.
	if f.floorKeeperLive < int64(d.cfg.FleetFloor) {
		f.floorViolations = append(f.floorViolations, fmt.Sprintf(
			"the keeper gave back what it built to hold the floor: %d of its own hosts live < floor %d",
			f.floorKeeperLive, d.cfg.FleetFloor))
	}
}

// maxLeaseCrossings bounds how often one host may be shortened and renewed back
// in a single run. Crossings are legitimate, since a create landing inside a
// drain window is what renewal is for, but a host oscillating past this is
// the sawtooth the warm plane's bookkeeping exists to survive, and a run that
// produced it is worth reading rather than passing.
const maxLeaseCrossings = 6

// checkReclaim asserts what a lowered lease must never do, and that the
// reclaim scenario lowered any at all.
//
// The promise invariant runs in EVERY scenario, and not only in the reclaim
// one. Any lease write can break it, and a check present only in the run nobody
// does by default is a check that is not there. A host must outlive every live device on
// it by the teardown margin, so a violation here means a device's final minutes
// are scheduled on a host already queued for the reaper.
//
// The vacuity arm is the other half, and it is the one that has been wrong
// before: the scenario seeds a mode, and a checker asserting bounds under a mode
// that can never reach the code it bounds passes forever. If reclamation was
// asked for and no lease was ever lowered, that is a finding about the harness.
func (d *driver) checkReclaim(ctx context.Context, f *findings) {
	// Clock skew moves the two timestamps independently, so the margin the
	// checker demands is the promise less the worst skew injected, the same
	// tolerance checkPins already applies for the same reason.
	margin := time.Duration(hostdomain.GracePeriodSeconds)*time.Second - d.cfg.maxAbsSkew()
	iter := d.raw.Single().Query(ctx, gspanner.Statement{
		SQL: `SELECT d.host_id, d.device_id, d.expires_at, h.expires_at
		      FROM devices d JOIN hosts h ON h.host_id = d.host_id
		      WHERE d.status NOT IN ('error', 'deleting', 'deleted')
		        AND d.expires_at IS NOT NULL
		        AND h.expires_at IS NOT NULL
		        AND h.status NOT IN ('deleted', 'deleting')
		        AND TIMESTAMP_ADD(h.expires_at, INTERVAL @margin SECOND) < d.expires_at`,
		Params: map[string]any{"margin": int64(margin.Seconds())},
	})
	defer iter.Stop()
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			f.reclaimViolations = append(f.reclaimViolations,
				fmt.Sprintf("reading device promises: %v", err))
			break
		}
		var hostID, deviceID string
		var deviceExp, hostExp gspanner.NullTime
		if err := row.Columns(&hostID, &deviceID, &deviceExp, &hostExp); err != nil {
			f.reclaimViolations = append(f.reclaimViolations,
				fmt.Sprintf("scanning device promises: %v", err))
			break
		}
		f.reclaimViolations = append(f.reclaimViolations, fmt.Sprintf(
			"host %s expires %v, before device %s expires %v plus the %s teardown margin",
			hostID, hostExp.Time, deviceID, deviceExp.Time, margin))
	}

	if err := d.readLeaseTrail(ctx, f); err != nil {
		f.reclaimViolations = append(f.reclaimViolations, err.Error())
		return
	}
	if f.leaseCrossings > maxLeaseCrossings {
		f.reclaimViolations = append(f.reclaimViolations, fmt.Sprintf(
			"a host crossed between shorten and renew %d times, over the bound of %d",
			f.leaseCrossings, maxLeaseCrossings))
	}
	// The fleet's OWN shortens, never the total. Once a second reclamation shares
	// this action, a total lets the other direction satisfy this arm, which is
	// this check failing in the exact way it exists to detect.
	if d.cfg.PoolFloor > 0 && d.cfg.PoolReclaim && f.surplusShortens == 0 {
		f.reclaimViolations = append(f.reclaimViolations,
			"the reclaim scenario ran and no SURPLUS lease was ever shortened: every bound above passed without reaching the code it bounds")
	}
	// The same arm for the other direction, and it is the one that was missing.
	// Renewal needs a deficit and a draining host in the SAME cycle, which the
	// scenario produces only in the window between the drain and the drop, so
	// this is also the check that catches those two phases drifting until the
	// window closes, which no assertion about leases would notice.
	if d.cfg.PoolFloor > 0 && d.cfg.PoolReclaim && f.renewals == 0 {
		f.reclaimViolations = append(f.reclaimViolations,
			"the reclaim scenario ran and no lease was ever renewed: the fleet was drained below the standby margin while the target still stood, so a deficit met from the fleet's own hosts was reachable and was not reached")
	}
}

// The two reasons a lease is ever cut, spelled out here rather than imported
// from the production constants. These strings are the durable record, read
// back long after the host rows are gone, so a rename that silently
// reclassified every historical entry would pass a check written against the
// constant.
//
// Telling them apart is not presentation. One action now carries two
// reclamations that answer to different worlds, and each scenario's vacuity arm
// asks whether ITS OWN direction fired. A total would let either one satisfy
// the other's arm, which is how a scenario passes while never reaching the code
// it bounds. The first composed run did exactly that: four lease cuts, all of
// them collapses, and a fleet-surplus check that read four and was satisfied.
const (
	reclaimFleetSurplus   = "fleet_surplus"
	reclaimDedicatedEmpty = "dedicated_empty"
)

// checkCollapse judges the dedicated-host scenario: what the collapse was
// allowed to reach, and whether it reached anything at all.
//
// The scope assertion is a SUBSET and not a blacklist. Every host the collapse
// reclaimed has to be one this workload provisioned for a group, which catches
// any over-reach rather than the one kind somebody thought to name: a fleet
// host, an unstamped row, a host a user fenced. The last of those is planted on
// purpose and aged alongside the rest, so the assertion has something to fail
// on.
//
// The vacuity arms are two, because the two failures are not the same finding.
// A run that provisioned no dedicated host never reached the code at all and
// that is the harness's fault; a run that provisioned several, ended their
// groups and reclaimed none is the product's. Collapsing both into "collapses ==
// 0" would report the first as the second, which is how the renewal arm read
// before its two phases were separated.
func (d *driver) checkCollapse(f *findings) {
	if !d.cfg.GroupCollapse {
		return
	}
	for _, id := range f.collapsedIDs {
		if _, ours := f.groupHostIDs[id]; ours {
			continue
		}
		reached := "a host this workload never provisioned for a group"
		if _, fenced := f.fencedHostIDs[id]; fenced {
			reached = "a host a USER fenced for their own test, which nothing may reclaim"
		}
		f.reclaimViolations = append(f.reclaimViolations,
			fmt.Sprintf("the collapse reclaimed %s: %s", reached, id))
	}
	switch {
	case len(f.groupHostIDs) == 0:
		f.reclaimViolations = append(f.reclaimViolations,
			"the collapse scenario ran and no dedicated host was ever provisioned: every group create placed onto existing capacity instead, so the run reached none of the code it bounds")
	case f.collapses == 0:
		f.reclaimViolations = append(f.reclaimViolations, fmt.Sprintf(
			"%d dedicated hosts were provisioned and their groups ended, and not one was reclaimed",
			len(f.groupHostIDs)))
	}
}

// checkElection reads the leases the run left behind and judges leader
// election against them. The bootstrap truncates leader_leases, so every row here
// was written by this run.
func (d *driver) checkElection(ctx context.Context, f *findings) error {
	iter := d.raw.Single().Query(ctx, gspanner.Statement{
		SQL: `SELECT lease_name FROM leader_leases ORDER BY lease_name`,
	})
	defer iter.Stop()
	var names []string
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return fmt.Errorf("reading the leader leases: %w", err)
		}
		var name string
		if err := row.Columns(&name); err != nil {
			return fmt.Errorf("scanning a leader lease: %w", err)
		}
		names = append(names, name)
	}

	d.mu.Lock()
	holders, kills := len(d.leaseHolders), d.kills
	d.mu.Unlock()
	judgeElection(f, names, holders, handoverForcers(kills))
	return nil
}

// handoverForcers names the nemeses this run used that must have taken the
// background plane away from whichever instance held it.
//
// Only a kill qualifies: the process is gone, its connections drop, and its
// lease stands until it expires, so a survivor must take it. A pause outlasting
// the lease WOULD force the same handover on real Spanner, but not here, and
// requiring it was proven wrong live: a SIGSTOPped instance parks its
// in-flight read-write transaction with its sockets open, the emulator
// serializes read-write transactions globally, and so the SURVIVOR'S acquire
// times out ("DeadlineExceeded ... transaction outcome unknown") for exactly
// as long as the pause lasts. The overlapping-leadership window the pause
// nemesis forces is therefore proven by the kill arm and the convergence
// checkers, never by demanding a handover the emulator cannot allow. If this
// harness ever runs against real Spanner, a pause longer than the lease with
// a survivor belongs back in this list, and it will need the pause count, the
// pause duration and the instance count back too.
func handoverForcers(kills int) []string {
	var forcers []string
	if kills > 0 {
		forcers = append(forcers, fmt.Sprintf("%d kill(s)", kills))
	}
	return forcers
}

// judgeElection: ONE election, actually held, and moved when its holder was
// stopped.
//
// The lease names come first because a second row is the exact shape this plane
// no longer has. A job wired to a lease of its own runs on a leadership nothing
// else observes, and the run would report nothing about it.
//
// The other two arms are vacuity arms, and they are why leadership is sampled
// mid-run at all. A plane no instance ever led is a run in which every
// leader-gated assertion passed because nothing ran, and at quiesce it is
// indistinguishable from a healthy one: the instances have been terminated and
// have released, leaving the same unheld row either way. A run whose nemesis
// took the plane from its holder and still only ever saw one holder is that
// failure one step in: the lease was never taken from the stopped instance, so
// the plane stopped with it, and the convergence the other checkers assert was
// never put under an overlapping leadership at all.
func judgeElection(f *findings, names []string, holders int, forcers []string) {
	f.leaseNames = names
	f.leaseHolders = holders
	for _, name := range names {
		if name != leaderelection.BackgroundPlaneLease {
			f.electionViolations = append(f.electionViolations, fmt.Sprintf(
				"lease %q exists beside the background plane's: a job is electing on its own", name))
		}
	}
	if holders == 0 {
		f.electionViolations = append(f.electionViolations,
			"no instance was ever observed leading the background plane: nothing was cycling")
	}
	if len(forcers) > 0 && holders < 2 {
		f.electionViolations = append(f.electionViolations,
			"the background plane never changed hands under "+strings.Join(forcers, " and "))
	}
}

// readLeaseTrail counts the run's lease writes, the worst oscillation on any one
// host, and which of the shortens were dedicated-empty collapses.
//
// The audit trail and not the host rows, for the reason the demand signal reads
// it too: host rows hard-delete, so the faster reclamation works the less of it
// a row-based count can see. That applies twice over here, because a collapsed
// host is one nothing will ever place on again, so its row is the first to go.
func (d *driver) readLeaseTrail(ctx context.Context, f *findings) error {
	iter := d.raw.Single().Query(ctx, gspanner.Statement{
		SQL: `SELECT COALESCE(resource_id, ''), action,
		             COALESCE(JSON_VALUE(metadata, '$.reclaim'), '') FROM audit_logs
		      WHERE action IN ('host.lease_shorten', 'host.lease_extend')
		      ORDER BY resource_id, timestamp`,
	})
	defer iter.Stop()
	last := make(map[string]string)
	crossings := make(map[string]int)
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return fmt.Errorf("reading the lease trail: %w", err)
		}
		var hostID, action, reclaim string
		if err := row.Columns(&hostID, &action, &reclaim); err != nil {
			return fmt.Errorf("scanning the lease trail: %w", err)
		}
		if action == "host.lease_shorten" {
			f.shortens++
			switch reclaim {
			case reclaimFleetSurplus:
				f.surplusShortens++
			case reclaimDedicatedEmpty:
				f.collapses++
				f.collapsedIDs = append(f.collapsedIDs, hostID)
			default:
				// An entry that does not say why the lease was cut is one nothing
				// can read later, and it makes both vacuity arms below blind to
				// whichever direction stopped stamping.
				f.reclaimViolations = append(f.reclaimViolations, fmt.Sprintf(
					"a lease was cut on host %s with no reclamation reason recorded", hostID))
			}
		} else {
			f.renewals++
		}
		if prev, seen := last[hostID]; seen && prev != action {
			crossings[hostID]++
			f.leaseCrossings = max(f.leaseCrossings, crossings[hostID])
		}
		last[hostID] = action
	}
	return nil
}

func (d *driver) loadDevices(ctx context.Context) ([]deviceRow, error) {
	iter := d.raw.Single().Query(ctx, gspanner.Statement{
		SQL: `SELECT device_id, host_id, status, pinned_at IS NOT NULL, pinned_until,
		             COALESCE(desired_spec, '') FROM devices`,
	})
	defer iter.Stop()
	var out []deviceRow
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("loading devices: %w", err)
		}
		var v deviceRow
		var spec string
		if err := row.Columns(&v.id, &v.hostID, &v.status, &v.pinned, &v.pinnedUntil, &spec); err != nil {
			return nil, fmt.Errorf("scanning device: %w", err)
		}
		v.instances = desiredInstances(spec)
		out = append(out, v)
	}
}

// checkConservation is the bank test: the denormalized counters must equal
// what they summarize, exactly, because no reconciler heals them in this
// composition.
func (d *driver) checkConservation(ctx context.Context, f *findings, hosts []hostRow, devices []deviceRow) {
	liveHosts := conserveCounters(f, hosts, devices)

	var used int64
	row, err := d.raw.Single().Query(ctx, gspanner.Statement{
		SQL: `SELECT used_hosts FROM quotas WHERE scope='platform' AND scope_id='global'`,
	}).Next()
	if err != nil {
		f.quotaDrift = append(f.quotaDrift, "quota row unreadable: "+err.Error())
		return
	}
	if err := row.Columns(&used); err == nil && used != liveHosts {
		f.quotaDrift = append(f.quotaDrift,
			fmt.Sprintf("quotas.used_hosts=%d, live hosts=%d", used, liveHosts))
	}
}

// conserveCounters is the pure half of checkConservation: per-host counter
// agreement, returning the live-host count for the quota comparison.
//
// current_device_count is the sum of the DESIRED instance counts of a host's
// live devices, and never how many device rows it carries. The two agreed only
// while every device was one Android VM. A device is now a group of N, charged
// N at admission and refunded N at delete, so counting rows would call a
// correctly charged two-instance device drift, and would call the
// oversubscription defect (that device charged one slot) conserved.
// protected_device_count stays a device count: protection is a property of the
// device, not of how many VMs it runs.
func conserveCounters(f *findings, hosts []hostRow, devices []deviceRow) int64 {
	instancesByHost := make(map[string]int64)
	pinnedByHost := make(map[string]int64)
	for _, v := range devices {
		if v.status == statusDeleted {
			continue
		}
		instancesByHost[v.hostID] += deviceSlots(v.instances)
		if v.pinned {
			pinnedByHost[v.hostID]++
		}
	}
	var liveHosts int64
	for _, h := range hosts {
		if h.status == statusDeleted {
			continue
		}
		liveHosts++
		if h.deviceCount != instancesByHost[h.id] {
			f.deviceCountDrift = append(f.deviceCountDrift,
				fmt.Sprintf("host %s: current_device_count=%d, instances desired by live devices=%d",
					h.id, h.deviceCount, instancesByHost[h.id]))
		}
		if h.protectedCount != pinnedByHost[h.id] {
			f.protectedCountDrift = append(f.protectedCountDrift,
				fmt.Sprintf("host %s: protected_device_count=%d, pinned live devices=%d", h.id, h.protectedCount, pinnedByHost[h.id]))
		}
	}
	return liveHosts
}

// checkStranded flags transitional resources with no queue task left to move
// them. This is the silently-stuck class (dead-context writes, retry
// self-defeat, error-message overflow).
func (d *driver) checkStranded(ctx context.Context, f *findings, hosts []hostRow, devices []deviceRow) error {
	backed := make(map[string]bool)
	iter := d.raw.Single().Query(ctx, gspanner.Statement{
		SQL: `SELECT COALESCE(resource_id, '') FROM tasks WHERE state IN ('pending','leased')`,
	})
	defer iter.Stop()
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return fmt.Errorf("loading task resources: %w", err)
		}
		var id string
		if err := row.Columns(&id); err != nil {
			return fmt.Errorf("scanning task resource: %w", err)
		}
		backed[id] = true
	}
	transitional := map[string]bool{
		"pending": true, "creating": true, "provisioning": true,
		"starting": true, "stopping": true, "deleting": true,
	}
	for _, v := range devices {
		if transitional[v.status] && !backed[v.id] {
			f.stranded = append(f.stranded, fmt.Sprintf("device %s stuck in %q with no task", v.id, v.status))
		}
	}
	for _, h := range hosts {
		if transitional[h.status] && !backed[h.id] {
			f.stranded = append(f.stranded, fmt.Sprintf("host %s stuck in %q with no task", h.id, h.status))
		}
	}
	return nil
}

// checkPins: a pin whose deadline lapsed longer than grace ago must have
// been released. The caller derives grace from the reaper cadence, widened
// by the largest configured clock skew, because a skewed reaper legitimately
// runs late by its offset.
func checkPins(f *findings, devices []deviceRow, grace time.Duration) {
	for _, v := range devices {
		if v.status == statusDeleted || !v.pinned || !v.pinnedUntil.Valid {
			continue
		}
		if lapsed := time.Since(v.pinnedUntil.Time); lapsed > grace {
			f.lapsedPins = append(f.lapsedPins,
				fmt.Sprintf("device %s still pinned %v past pinned_until", v.id, lapsed.Round(time.Second)))
		}
	}
}

// checkOrphans reconciles the fake boundaries against the database: every
// surviving VM belongs to a live host, every surviving CVD group belongs to a
// live device (production names groups cvd<deviceID[:8]>).
func checkOrphans(f *findings, hosts []hostRow, devices []deviceRow,
	liveVMs []string, liveGroups map[string][]string, actuations []string,
) {
	liveHostVMs := make(map[string]bool)
	for _, h := range hosts {
		if h.status != statusDeleted && h.gceName != "" {
			liveHostVMs[h.gceName] = true
		}
	}
	for _, vm := range liveVMs {
		if !liveHostVMs[vm] {
			f.leakedVMs = append(f.leakedVMs, fmt.Sprintf("VM %s alive with no live host row", vm))
		}
	}

	livePrefix := make(map[string]bool)
	for _, v := range devices {
		if v.status != statusDeleted && len(v.id) >= 8 {
			livePrefix["cvd"+v.id[:8]] = true
		}
	}
	for ip, groups := range liveGroups {
		for _, g := range groups {
			// The warm plane's reserved prefetch group has no device row by
			// design; the plane owns its cleanup and re-adopts a leftover on
			// the next warm walk.
			if g == devicedomain.WarmGroupName {
				continue
			}
			if !livePrefix[g] {
				f.leakedCVDs = append(f.leakedCVDs, fmt.Sprintf("CVD group %s on %s with no live device", g, ip))
			}
		}
	}

	// Every actuation against an in-flight launch is the adopt-race collision
	// that poisons a real host. None is tolerated.
	f.actuationRaces = actuations
}

func (d *driver) checkTasks(ctx context.Context, f *findings) error {
	iter := d.raw.Single().Query(ctx, gspanner.Statement{
		SQL: `SELECT task_id, task_type, state, attempts, max_attempts, COALESCE(last_error, '') FROM tasks`,
	})
	defer iter.Stop()
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("loading tasks: %w", err)
		}
		var (
			id, taskType, state, lastErr string
			attempts, maxAttempts        int64
		)
		if err := row.Columns(&id, &taskType, &state, &attempts, &maxAttempts, &lastErr); err != nil {
			return fmt.Errorf("scanning task: %w", err)
		}
		judgeTask(f, &d.cfg, taskRow{
			id: id, taskType: taskType, state: state, lastErr: lastErr,
			attempts: attempts, maxAttempts: maxAttempts,
		})
	}
}

// taskRow is one quiescent tasks-table row as the checker sees it.
type taskRow struct {
	id, taskType, state, lastErr string
	attempts, maxAttempts        int64
}

// judgeTask applies the queue contract to one quiescent task row. Chaos
// burns attempt budgets; a clean run has no excuse. The baseline (the CI
// gate) drains to an empty DLQ, and a dead-letter there is a handler bug the
// old unconditional tolerance hid.
func judgeTask(f *findings, cfg *Cfg, t taskRow) {
	switch t.state {
	case "pending", "leased":
		f.tasksLeft++
	case "failed":
		if t.attempts < t.maxAttempts {
			f.underAttDLQ = append(f.underAttDLQ,
				fmt.Sprintf("task %s dead-lettered at %d/%d attempts", t.id, t.attempts, t.maxAttempts))
		}
		if !cfg.attemptBurners() {
			f.unexpectedDLQ = append(f.unexpectedDLQ, fmt.Sprintf(
				"%s task %s dead-lettered with no fault injection configured: %.120q", t.taskType, t.id, t.lastErr))
		}
	}
}

// readOpsLedgers tallies op outcomes, validates the idempotency probes, and
// collects acknowledged create IDs for the conservation checker. An errored
// create is never collected, because an ambiguous commit may or may not have
// happened and only an ok shrinks the possible-non-effects set.
func (d *driver) readOpsLedgers(f *findings) error {
	f.ackedHostIDs = make(map[string]struct{})
	f.ackedDeviceIDs = make(map[string]string)
	f.deviceDeleteTried = make(map[string]struct{})
	f.hostDeleteTried = make(map[string]struct{})
	f.idemKeyIDs = make(map[string][]string)
	f.groupHostIDs = make(map[string]struct{})
	f.fencedHostIDs = make(map[string]struct{})
	matches, err := filepath.Glob(filepath.Join(d.cfg.RunDir, "ops-*.log"))
	if err != nil {
		return fmt.Errorf("globbing ops ledgers: %w", err)
	}
	for _, path := range matches {
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("opening %s: %w", path, err)
		}
		sc := bufio.NewScanner(file)
		for sc.Scan() {
			d.readOpsLine(f, strings.Fields(sc.Text()))
		}
		err = sc.Err()
		_ = file.Close()
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
	}
	return nil
}

func (d *driver) readOpsLine(f *findings, fields []string) {
	if len(fields) < 2 {
		return
	}
	// The two-field marker lines carry a host's provenance, which the database
	// cannot be asked for once the row is gone.
	switch fields[0] {
	case markerGroupHost:
		f.groupHostIDs[fields[1]] = struct{}{}
		return
	case markerFencedHost:
		f.fencedHostIDs[fields[1]] = struct{}{}
		return
	}
	if len(fields) < 3 {
		return
	}
	switch fields[0] {
	case "op":
		kind, outcome := fields[1], fields[2]
		f.opCounts[kind+"/"+outcome]++
		if len(fields) < 4 {
			return
		}
		id := fields[3]
		switch kind {
		case opDeviceDelete:
			f.deviceDeleteTried[id] = struct{}{}
			return
		case opHostDelete:
			f.hostDeleteTried[id] = struct{}{}
			return
		}
		if outcome != "ok" {
			return
		}
		switch kind {
		case opHostCreate, opHostCreateAuto:
			f.ackedHostIDs[id] = struct{}{}
		case opDeviceCreate, opDeviceCreateAuto, opDeviceCreateAffine, opDeviceCreateGroup:
			f.ackedDeviceIDs[id] = hostField(fields[4:])
		}
	case "idem":
		if len(fields) < 4 {
			return
		}
		host := hostField(fields[4:])
		for _, id := range fields[2:4] {
			f.ackedDeviceIDs[id] = host
			f.idemKeyIDs[fields[1]] = append(f.idemKeyIDs[fields[1]], id)
		}
	}
}

// hostField extracts the host=<id> annotation from a ledger line's trailing
// fields ("" when absent).
func hostField(rest []string) string {
	for _, s := range rest {
		if after, ok := strings.CutPrefix(s, "host="); ok {
			return after
		}
	}
	return ""
}

// checkIdem asserts one device per idempotency key across every ledger,
// covering probes on one instance and the shared-key probes that collide
// across processes alike.
func checkIdem(f *findings) {
	for key, ids := range f.idemKeyIDs {
		distinct := make(map[string]struct{})
		for _, id := range ids {
			distinct[id] = struct{}{}
		}
		if len(distinct) > 1 {
			f.idemViolations = append(f.idemViolations, fmt.Sprintf(
				"key %s resolved to %d devices: %s",
				key, len(distinct), strings.Join(slices.Sorted(maps.Keys(distinct)), " ")))
		}
	}
}

// checkAckedCreates is the set checker's lost-element rule applied to
// resources, with the indeterminacy discipline on deletes: an ok create
// means the row definitely existed, so its absence at quiesce is a violation
// unless something COMMANDED it. Lost-mark and device tombstones persist as
// rows, but a user host delete removes the row physically and its INTERLEAVE
// cascade takes the host's device rows with it, so absence is excused by a
// delete attempted on the resource itself or on the device's creating host.
// Attempted is enough, ok or err: an errored delete may still have committed.
func checkAckedCreates(f *findings, hosts []hostRow, devices []deviceRow) {
	hostRows := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		hostRows[h.id] = struct{}{}
	}
	deviceRows := make(map[string]struct{}, len(devices))
	for _, v := range devices {
		deviceRows[v.id] = struct{}{}
	}
	for id := range f.ackedHostIDs {
		if _, ok := hostRows[id]; ok {
			continue
		}
		if _, tried := f.hostDeleteTried[id]; tried {
			continue
		}
		f.ackedLost = append(f.ackedLost, fmt.Sprintf(
			"acked host %s has no row and no delete was attempted", id))
	}
	for id, host := range f.ackedDeviceIDs {
		if _, ok := deviceRows[id]; ok {
			continue
		}
		if _, tried := f.deviceDeleteTried[id]; tried {
			continue
		}
		if _, tried := f.hostDeleteTried[host]; tried {
			continue
		}
		f.ackedLost = append(f.ackedLost, fmt.Sprintf(
			"acked device %s has no row and neither it nor host %s saw a delete attempt", id, host))
	}
}

// checkOpenOperations reconciles the EXCLUSIVE-verb operations table: every
// row must settle by quiesce. There is no periodic sweep, and a leaked open
// row heals only at the NEXT exclusive admission on the same resource, so
// possibly never. In a clean run any open row is therefore a violation. Under
// an attempt-burning nemesis a kill mid-verb leaves one legitimately (the
// count still reports).
func (d *driver) checkOpenOperations(ctx context.Context, f *findings) error {
	iter := d.raw.Single().Query(ctx, gspanner.Statement{
		SQL: `SELECT operation_id, verb, resource_id FROM operations WHERE done = false`,
	})
	defer iter.Stop()
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("loading open operations: %w", err)
		}
		var id, verb, resource string
		if err := row.Columns(&id, &verb, &resource); err != nil {
			return fmt.Errorf("scanning open operation: %w", err)
		}
		f.openOpsSeen++
		if !d.cfg.attemptBurners() {
			f.openOps = append(f.openOps, fmt.Sprintf(
				"operation %s (%s on %s) still open at quiesce with no fault injection configured", id, verb, resource))
		}
	}
}

func (d *driver) report(f *findings, elapsed time.Duration, quiesced, interrupted bool) error {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	check := func(n int) string {
		if n == 0 {
			return "OK"
		}
		return "VIOLATION"
	}
	violations := f.violations()

	w("")
	w("=== robustness report ==================================================")
	w("run:      elapsed=%v instances=%d window=%v seed=%d%s",
		elapsed.Round(time.Second), d.cfg.Instances, d.cfg.Duration, d.cfg.Seed,
		map[bool]string{true: " (INTERRUPTED)", false: ""}[interrupted])
	d.mu.Lock()
	w("nemesis:  kills=%d pauses=%d drifts=%d %v stockouts=%d %v db-faults=%d ho-faults=%d skews=%v",
		d.kills, d.pauses, len(d.drifts), d.drifts, len(d.stockouts), d.stockouts,
		d.dbFaults, d.hoFaults, d.cfg.ClockSkews)
	d.mu.Unlock()
	w("fleet:    host-rows=%d device-rows=%d live-vms=%d", f.hostsSeen, f.devicesSeen, len(d.gce.Live()))
	ops := make([]string, 0, len(f.opCounts))
	for k, n := range f.opCounts {
		ops = append(ops, fmt.Sprintf("%s=%d", k, n))
	}
	w("ops:      %s", strings.Join(ops, " "))
	w("conserve: device-count-drift=%d [%s] protected-drift=%d [%s] quota-drift=%d [%s]",
		len(f.deviceCountDrift), check(len(f.deviceCountDrift)),
		len(f.protectedCountDrift), check(len(f.protectedCountDrift)),
		len(f.quotaDrift), check(len(f.quotaDrift)))
	w("state:    stranded=%d [%s] lapsed-pins=%d [%s] tasks-left=%d",
		len(f.stranded), check(len(f.stranded)), len(f.lapsedPins), check(len(f.lapsedPins)), f.tasksLeft)
	w("orphans:  leaked-vms=%d [%s] leaked-cvds=%d [%s] actuation-races=%d [%s] dlq-under-attempted=%d",
		len(f.leakedVMs), check(len(f.leakedVMs)), len(f.leakedCVDs), check(len(f.leakedCVDs)),
		len(f.actuationRaces), check(len(f.actuationRaces)), len(f.underAttDLQ))
	w("drift:    outcome-violations=%d [%s] (preempt must recover, vanish must be honored)",
		len(f.driftOutcomes), check(len(f.driftOutcomes)))
	w("place:    violations=%d [%s] failovers=%d (pins never move, capacity never dead-letters the eligible)",
		len(f.placementViolations), check(len(f.placementViolations)), f.failovers)
	w("idem:     violations=%d [%s]", len(f.idemViolations), check(len(f.idemViolations)))
	w("evidence: launched-groups=%d vacuity=%d [%s] dlq-unexpected=%d [%s] error-unexpected=%d [%s]",
		f.launchedGroups, len(f.vacuity), check(len(f.vacuity)),
		len(f.unexpectedDLQ), check(len(f.unexpectedDLQ)),
		len(f.unexpectedErrs), check(len(f.unexpectedErrs)))
	w("procs:    uncommanded-exits=%d [%s]", len(f.instanceDeaths), check(len(f.instanceDeaths)))
	w("election: violations=%d [%s] leases=%v holders-seen=%d (one plane, held, and moved by whatever stopped its holder)",
		len(f.electionViolations), check(len(f.electionViolations)), f.leaseNames, f.leaseHolders)
	w("acked:    hosts=%d devices=%d lost=%d [%s] open-operations=%d [%s]",
		len(f.ackedHostIDs), len(f.ackedDeviceIDs), len(f.ackedLost), check(len(f.ackedLost)),
		f.openOpsSeen, check(len(f.openOps)))
	w("samples:  taken=%d failed=%d transient-drift=%d [%s] (always-invariants at consistent mid-run snapshots)",
		f.samples, f.samplesFailed, f.transientSeen, check(f.transientSeen))
	if d.cfg.PoolFloor > 0 {
		w("pool:     violations=%d [%s] idle=%d keeper-hosts=%d floor=%d (held, never overshot)",
			len(f.poolViolations), check(len(f.poolViolations)), f.poolIdle, f.poolKeeperLive, d.cfg.PoolFloor)
	}
	if d.cfg.FleetFloor > 0 {
		w("floor:    violations=%d [%s] standing=%d draining=%d keeper-hosts=%d oldest=%s floor=%d (reclamation stops here)",
			len(f.floorViolations), check(len(f.floorViolations)), f.floorTotal, f.floorDraining,
			f.floorKeeperLive, f.floorOldest.Round(time.Second), d.cfg.FleetFloor)
	}
	// Printed in every run, and not only in the reclaim scenario. The promise
	// invariant applies to any lease write, and zero shortens is itself the
	// fact worth seeing beside a scenario that asked for reclamation.
	w("reclaim:  violations=%d [%s] shortens=%d (surplus=%d collapse=%d) renewals=%d worst-oscillation=%d (no device outlives its host)",
		len(f.reclaimViolations), check(len(f.reclaimViolations)), f.shortens,
		f.surplusShortens, f.collapses, f.renewals, f.leaseCrossings)
	if d.cfg.GroupCollapse {
		w("collapse: group-hosts=%d fenced-hosts=%d reclaimed=%d (only hosts fenced FOR a group, never BY a user)",
			len(f.groupHostIDs), len(f.fencedHostIDs), f.collapses)
	}
	w("quiesced: %v", quiesced)
	const maxShown = 25
	for i, v := range violations {
		if i == maxShown {
			w("VIOLATION: ... %d more elided", len(violations)-maxShown)
			break
		}
		w("VIOLATION: %s", v)
	}
	w("artifacts: %s (ops ledgers)", d.cfg.RunDir)
	fmt.Print(b.String())

	if interrupted {
		return nil
	}
	if !quiesced {
		return fmt.Errorf("fleet did not quiesce within %v", d.cfg.QuiesceTimeout)
	}
	if len(violations) > 0 {
		return fmt.Errorf("verification failed with %d violation(s)", len(violations))
	}
	return nil
}

// checkCapacity: no host may ever hold more devices than max_devices. The
// placement path is advisory and the create commit's capacity re-check is the
// enforcement. This is the quiesce read; the mid-run snapshot sampler holds
// the same line at every timestamp.
func checkCapacity(f *findings, hosts []hostRow) {
	for _, h := range hosts {
		if h.status == statusDeleted {
			continue
		}
		if h.deviceCount > h.maxDevices {
			f.overCapacity = append(f.overCapacity,
				fmt.Sprintf("host %s: current_device_count=%d > max_devices=%d", h.id, h.deviceCount, h.maxDevices))
		}
	}
}
