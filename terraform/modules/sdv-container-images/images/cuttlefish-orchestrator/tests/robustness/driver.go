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
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	gspanner "cloud.google.com/go/spanner"

	hostdomain "cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
	"cloud-cuttlefish-orchestrator/tests/harness/fakegce"
	"cloud-cuttlefish-orchestrator/tests/harness/fakeho"
	"cloud-cuttlefish-orchestrator/tests/harness/faultproxy"
)

// driver owns a run: fakes, database, instance subprocesses, nemeses, the
// quiesce wait, and the checker suite.
type driver struct {
	cfg     Cfg
	baseCtx context.Context
	raw     *gspanner.Client
	gce     *fakegce.Server
	ho      *fakeho.Server
	proxies []*faultproxy.Proxy // per-instance DB fault proxies (db-fault mode)

	nemesis sync.WaitGroup

	// terminating marks the driver-commanded shutdown: instance exits after
	// this are expected, exits before it are findings.
	terminating atomic.Bool

	mu          sync.Mutex
	procs       []*instProc
	kills       int
	pauses      int
	dbFaults    int
	hoFaults    int
	stockouts   []string     // zones stocked out, in order, for the report
	drifts      []string     // human-readable drift events for the report
	driftEvents []driftEvent // structured events for the outcome checker
	instDeaths  []string     // uncommanded instance exits (panic, fatal, OOM kill)

	// Every instance observed holding the background plane's lease mid-run
	// (sampler.go). More than one means leadership moved while the run was
	// happening, which is the only place a handover is visible: by check time
	// every instance has been terminated and has released.
	leaseHolders map[string]bool

	// Mid-run snapshot sampling (sampler.go).
	samples        int      // consistent snapshots taken
	samplesFailed  int      // snapshots that errored (lost coverage, not violations)
	transientSeen  int      // always-invariant violations observed across all samples
	transientDrift []string // first maxSampleViolations of them
}

// driftEvent records one injected drift with the host that owned the victim
// instance at injection time, so the checker can assert DISTINCT outcomes:
// a preempted host must recover (never be marked lost), a vanished host must
// be marked lost (never recovered). A shared "lost or preempted" contract is
// how the preemption-recovery regression passed chaos unnoticed.
//
// hostStatus is the host row's status at injection time: the strict vanish
// contract applies only to hosts CCO had already observed serving
// (VM-expecting). A vanish landing inside the provisioning window races the
// provision owner, which legitimately re-drives its idempotent insert: the
// VM comes back under the same name and the host converges healthy. The
// reconciler leaves provisioning hosts to their provision task (the ExpectsVM
// gate, whose reversal was the harness's own first finding, the false-lost
// bug), so "honor the out-of-band delete" is not a promise CCO makes for a VM
// whose provisioning it has not yet observed complete.
type driftEvent struct {
	kind       string // "preempt" | "vanish"
	hostID     string
	instance   string
	hostStatus string // host row status when the drift was injected
}

type instProc struct {
	idx  int
	cmd  *exec.Cmd
	done chan struct{}

	mu   sync.Mutex
	dead bool
}

func runDriver(cfg Cfg) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if cfg.RunDir == "" {
		dir, err := os.MkdirTemp("", "robustness-")
		if err != nil {
			return fmt.Errorf("creating run dir: %w", err)
		}
		cfg.RunDir = dir
	} else if err := os.MkdirAll(cfg.RunDir, 0o750); err != nil {
		return fmt.Errorf("creating run dir: %w", err)
	}

	gce := fakegce.New()
	defer gce.Close()
	ho, err := fakeho.New()
	if err != nil {
		return fmt.Errorf("starting fake HO: %w", err)
	}
	defer func() { _ = ho.Close() }()
	// A Host Orchestrator dies with its VM: drop the fake HO's per-host state
	// whenever an instance ceases to exist, or the orphan checker would read
	// groups on dead hosts as production leaks.
	gce.OnInstanceRemoved(ho.Reset)
	if cfg.LoadDelay > 0 {
		// Slow loads with a 1s :wait hold. An interrupted create leaves a
		// Starting group behind, and any retry that actuates on it instead of
		// waiting is ledgered as a violation (the adopt-race).
		ho.SetLoadDelay(cfg.LoadDelay, time.Second)
	}
	cfg.GCEURL = gce.URL()
	cfg.HOPort = ho.Port()
	fmt.Printf("robustness: db=%s gce=%s ho-port=%d artifacts=%s\n",
		databasePath(cfg), cfg.GCEURL, cfg.HOPort, cfg.RunDir)

	if err := bootstrap(ctx, cfg); err != nil {
		return err
	}
	raw, err := gspanner.NewClient(ctx, databasePath(cfg))
	if err != nil {
		return fmt.Errorf("creating raw spanner client: %w", err)
	}
	defer raw.Close()
	if err := reset(ctx, raw, cfg); err != nil {
		return err
	}

	d := &driver{cfg: cfg, baseCtx: ctx, raw: raw, gce: gce, ho: ho, leaseHolders: map[string]bool{}}
	if cfg.DBFault != "" {
		defer func() {
			for _, p := range d.proxies {
				_ = p.Close()
			}
		}()
		for i := range cfg.Instances {
			p, err := faultproxy.Listen(cfg.Emulator)
			if err != nil {
				return fmt.Errorf("starting fault proxy for instance %d: %w", i, err)
			}
			d.proxies = append(d.proxies, p)
			d.cfg.ProxyAddrs = append(d.cfg.ProxyAddrs, p.Addr())
		}
	}
	return d.run(ctx)
}

func (d *driver) run(ctx context.Context) error {
	started := time.Now()
	for i := range d.cfg.Instances {
		if err := d.spawn(i); err != nil {
			d.terminate()
			return err
		}
	}

	// Nemeses run only during the workload window, then everything is
	// resumed/cleared so the fleet can converge.
	loadCtx, stopLoad := context.WithCancel(ctx)
	d.startNemeses(loadCtx)

	// The snapshot sampler runs through both the workload and quiesce
	// windows: the always-invariants must hold at every timestamp, and the
	// recovery window is where transient drift would hide.
	sampleCtx, stopSampling := context.WithCancel(ctx)
	var sampler sync.WaitGroup
	sampler.Go(func() { d.sampleLoop(sampleCtx) })

	select {
	case <-ctx.Done():
	case <-time.After(d.cfg.Duration):
	}
	stopLoad()
	d.nemesis.Wait()

	quiesced := d.awaitQuiesce(ctx)
	stopSampling()
	sampler.Wait()
	d.terminate()

	collectCtx := context.WithoutCancel(ctx)
	findings, err := d.check(collectCtx)
	if err != nil {
		return err
	}
	if len(findings.violations()) > 0 {
		d.dumpState(collectCtx)
	}
	return d.report(findings, time.Since(started), quiesced, ctx.Err() != nil)
}

// reclaimLoop is the reclamation nemesis: halfway through the workload window
// it drops the standby target to zero, so the second half runs the keeper's
// surplus arm against a fleet it built in the first.
//
// An operator edit rather than an injected fault, because that is what
// reclamation actually responds to, and because nothing else in this harness
// produces a surplus. Every host the workload creates is a user's, which is not
// fleet-class and never counts toward the target, so without this the shorten
// path is never reached and every assertion about it passes vacuously.
//
// Two phases, because the keeper's two reclamation directions need opposite
// worlds and a run that only ever built one of them exercised only one arm.
// Draining the fleet's leases while the target still stands is the only moment
// a deficit and a draining host coexist, which is the whole precondition of
// renewal; dropping the target afterwards is the surplus. Spaced so each has
// several keeper cycles to act in, and ordered so the same host can be renewed
// and later shortened, which is also the only way this run produces the
// oscillation the crossing bound measures.
//
// Dropping the target is only half of what the surplus arm needs; ageFleetHosts
// below supplies the other half, on every tick until the window closes.
func (d *driver) reclaimLoop(ctx context.Context) {
	started := time.Now()
	dropAt := started.Add(2 * d.cfg.Duration / 3)
	if !sleepUntil(ctx, started.Add(d.cfg.Duration/4)) {
		return
	}
	d.drainFleetLeases(ctx, dropAt)

	if !sleepUntil(ctx, dropAt) {
		return
	}
	now := time.Now()
	// The bounds travel in one stored value, so dropping the buffer restates the
	// floor rather than omitting it: written alone, a zero buffer would take the
	// fleet floor down with it and the run would measure a fleet nobody bounded.
	_, err := d.raw.Apply(ctx, []*gspanner.Mutation{
		gspanner.InsertOrUpdateMap("system_config", map[string]any{
			colConfigKey:   models.ConfigKeyFleetPoolBounds,
			colConfigValue: poolBoundsValue(0, d.cfg.FleetFloor),
			colCreatedAt:   now,
			colUpdatedAt:   now,
		}),
	})
	if err != nil {
		fmt.Printf("reclaim nemesis could not drop the standby target: %v\n", err)
		return
	}
	fmt.Println("reclaim nemesis: standby target dropped to 0")

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		d.ageFleetHosts(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// startNemeses launches whichever disturbances this run asked for, each bound to
// the workload window so everything is resumed or cleared before the fleet is
// given its chance to converge.
//
// The last two are not faults but scenario setup: nothing else in this harness
// produces a surplus to give back or a fenced host whose group has ended.
func (d *driver) startNemeses(loadCtx context.Context) {
	if d.cfg.KillEvery > 0 {
		d.nemesis.Go(func() { d.chaos(loadCtx) })
	}
	if d.cfg.PauseEvery > 0 {
		d.nemesis.Go(func() { d.pauseLoop(loadCtx) })
	}
	if d.cfg.DriftEvery > 0 {
		d.nemesis.Go(func() { d.driftLoop(loadCtx) })
	}
	if d.cfg.StockoutEvery > 0 {
		d.nemesis.Go(func() { d.stockoutLoop(loadCtx) })
	}
	if d.cfg.DBFault != "" {
		d.nemesis.Go(func() { d.dbFaultLoop(loadCtx) })
	}
	if d.cfg.HOFault != "" {
		d.nemesis.Go(func() { d.hoFaultLoop(loadCtx) })
	}
	if d.cfg.PoolFloor > 0 && d.cfg.PoolReclaim {
		d.nemesis.Go(func() { d.reclaimLoop(loadCtx) })
	}
	if d.cfg.FleetFloor > 0 {
		d.nemesis.Go(func() { d.floorLoop(loadCtx) })
	}
	if d.cfg.GroupCollapse {
		d.nemesis.Go(func() { d.collapseLoop(loadCtx) })
	}
}

// floorLoop is the whole setup for the fleet-floor scenario. It backdates the
// keeper's own hosts past host.MinShortenAge for the workload window.
//
// No target to drop, because the buffer is already zero, which is the
// configuration the claim needs. With nothing asking for a free host, a surplus
// read from the buffer alone would call every host the keeper owns spare and
// hand the fleet back within a few cycles. Nothing must happen. The floor
// holds and the fleet is still standing when the run ends.
//
// The aging is what makes that assertion able to fail. Without it every host is
// inside the shorten age, so a fleet that survived would prove only that its
// hosts were young. The vacuity arm in checkFleetFloor says so in as many
// words rather than trusting this loop to have run.
func (d *driver) floorLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		d.ageFleetHosts(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// collapseLoop is the dedicated-host scenario's setup: it ages exclusive hosts
// past host.MinShortenAge for the whole workload window.
//
// No target to move and no drain to run, because a fenced host is not inventory:
// nothing counts it, nothing renews it, and the only thing standing between it
// and the keeper is how young it is. The workload provides the rest: a group
// create fences a host, the next one ends that group, and the host is left
// exactly as the collapse describes it.
//
// From the first tick rather than partway in, and every second until the window
// closes, for the reason the fleet's aging runs that way: a host provisioned
// near the end settles during the quiesce wait, after every nemesis has stopped,
// and arrives there as a row nothing can reclaim unless it was already old.
func (d *driver) collapseLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		d.ageExclusiveHosts(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ageExclusiveHosts backdates every exclusive host past the shorten floor.
//
// EVERY exclusive host, on purpose wider than the set the code under test
// would pick. The collapse is scoped to hosts the orchestrator fenced, and the
// hosts a USER fenced are the ones it must leave alone; aging only the first
// group would leave the second permanently too young to be a candidate, so a
// collapse that reached for them could not fail this run. Aged alongside, they
// satisfy every term of the predicate except the one that has to save them.
//
// Not selected through the class expression for the same reason: a nemesis that
// picked rows the way the code picks them would prepare exactly the world that
// code was going to accept anyway.
func (d *driver) ageExclusiveHosts(ctx context.Context) {
	cutoff := time.Now().Add(-hostdomain.MinShortenAge - time.Minute)
	_, err := d.raw.ReadWriteTransaction(ctx, func(ctx context.Context, txn *gspanner.ReadWriteTransaction) error {
		_, err := txn.Update(ctx, gspanner.Statement{
			SQL: `UPDATE hosts SET created_at = @cutoff
			      WHERE exclusive AND created_at > @cutoff`,
			Params: map[string]any{"cutoff": cutoff},
		})
		return err
	})
	if err != nil && ctx.Err() == nil {
		fmt.Printf("collapse nemesis could not age the fenced hosts: %v\n", err)
	}
}

// sleepUntil waits for a point in the workload window, reporting false if the
// window closed first.
func sleepUntil(ctx context.Context, deadline time.Time) bool {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// drainFleetLeases runs the leases of the fleet's idle hosts down below the
// standby margin. That is the state renewal exists to answer, and one no run
// reaches on its own. A fleet host is minutes old with a lease measured in hours, so
// attrition cannot produce it inside a window this size, and the only other
// producer is the surplus arm, which fires when the target is zero and there is
// no deficit left to renew toward. Without this the fleet keeps its full
// complement all the way to the drop, every renewal bound passes without a
// single renewal, and the report says so as a zero rather than as a gap.
//
// It runs until a renewal actually lands, rather than once at a fixed instant,
// because which hosts are idle at any given moment belongs to the workload: a
// run where every standby host happened to be holding a device when this fired
// drained nothing at all. Re-running is free, because the statement's own
// expires_at > drain term makes a second pass over an already-drained host
// match no rows, so the loop costs one no-op statement per second and stops
// the moment the keeper has answered.
//
// Stopping on the answer rather than at the deadline is what keeps the
// oscillation measurement true. Left ticking, this would re-drain each host as
// the keeper renewed it, and the crossings the report bounds would be this
// loop's rather than the fleet's.
//
// Half the provisioning lead: far enough under it that the host leaves the
// standby count at once, and far enough above zero that the reaper does not
// collect it. This drains a lease and never deletes a host.
func (d *driver) drainFleetLeases(ctx context.Context, deadline time.Time) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	drained := int64(0)
	for {
		renewals, err := d.countLeaseExtends(ctx)
		if err != nil {
			fmt.Printf("drain nemesis could not read the lease trail: %v\n", err)
			return
		}
		if renewals > 0 {
			fmt.Printf("drain nemesis: %d idle fleet lease(s) run down, %d renewed by the keeper\n",
				drained, renewals)
			return
		}
		n, err := d.drainOnce(ctx)
		if err != nil {
			fmt.Printf("drain nemesis could not run the fleet's leases down: %v\n", err)
			return
		}
		drained += n
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !time.Now().Before(deadline) {
			fmt.Printf("drain nemesis: %d lease(s) run down and none renewed before the target dropped\n", drained)
			return
		}
	}
}

// drainOnce lowers every idle fleet lease below the standby margin, reporting
// how many it moved.
//
// This is a destructive write, the one thing in this file that can make a host
// expire sooner, so it answers to the rule destructive host operations
// answer to: emptiness is decided by live device ROWS, never by
// current_device_count. The counter is the placement-admission predicate and
// this is a promise question, and the harness learned the difference the way
// the report states it, as a host expiring three quarters of an hour before the
// device sitting on it.
//
// Everything is tested in the same statement that writes, not read first and
// written after: a device committing in between raises its host's expiry in its
// own transaction, so either this skips the host or the raise lands after it and
// wins. Written as a read-then-write, the two would race and the loser would be
// a device outliving its host.
//
// The live set is the shorten guard's, spelled the same way for the same
// reason: an error row is a failed create with no CVD behind it, which is
// poison evidence rather than a tenant.
func (d *driver) drainOnce(ctx context.Context) (int64, error) {
	drain := time.Now().Add(hostdomain.ProvisionLead / 2)
	var drained int64
	_, err := d.raw.ReadWriteTransaction(ctx, func(ctx context.Context, txn *gspanner.ReadWriteTransaction) error {
		var err error
		drained, err = txn.Update(ctx, gspanner.Statement{
			SQL: `UPDATE hosts SET expires_at = @drain
			      WHERE created_by = 'poolkeeper'
			        AND status NOT IN ('deleted', 'deleting')
			        AND current_device_count = 0
			        AND protected_device_count = 0
			        AND expires_at IS NOT NULL
			        AND expires_at > @drain
			        AND NOT EXISTS (
			          SELECT 1 FROM devices d
			          WHERE d.host_id = hosts.host_id
			            AND d.status NOT IN ('error', 'deleting', 'deleted'))`,
			Params: map[string]any{"drain": drain},
		})
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("draining fleet leases: %w", err)
	}
	return drained, nil
}

// countLeaseExtends reports how many renewals the keeper has audited so far.
// The same trail readLeaseTrail checks after the run, read live: host rows hard
// delete, so the audit entry is the only durable record that a renewal happened
// at all.
func (d *driver) countLeaseExtends(ctx context.Context) (int64, error) {
	iter := d.raw.Single().Query(ctx, gspanner.Statement{
		SQL: `SELECT COUNT(*) FROM audit_logs WHERE action = 'host.lease_extend'`,
	})
	defer iter.Stop()
	row, err := iter.Next()
	if err != nil {
		return 0, fmt.Errorf("counting renewals: %w", err)
	}
	var n int64
	if err := row.Columns(&n); err != nil {
		return 0, fmt.Errorf("scanning the renewal count: %w", err)
	}
	return n, nil
}

// ageFleetHosts backdates the keeper's own host rows past host.MinShortenAge.
//
// A shorten refuses a host too young for emptiness to mean "drained" rather
// than "provisioned moments ago", and that floor is ten minutes against a run
// that lasts one. Waiting it out is not available and lowering the constant for
// the harness would verify a guard the deployment does not have, so the run
// moves the only quantity it can: the clock those rows were stamped with. The
// guard itself is proven where seeded ages are cheap, in the repository's own
// tests; what this buys is reaching the code at all.
//
// Every tick rather than once, and regardless of status: a host still coming up
// when the window closes has to be old ALREADY, because it settles into the
// standby count during the quiesce wait, after the nemeses have stopped, and
// arrives there as inventory nothing can shorten.
//
// Selected by created_by, matching checkPool's census and for the same reason:
// picking these rows the way the code under test picks them would age exactly
// the set that code was going to choose anyway.
func (d *driver) ageFleetHosts(ctx context.Context) {
	cutoff := time.Now().Add(-hostdomain.MinShortenAge - time.Minute)
	_, err := d.raw.ReadWriteTransaction(ctx, func(ctx context.Context, txn *gspanner.ReadWriteTransaction) error {
		_, err := txn.Update(ctx, gspanner.Statement{
			SQL: `UPDATE hosts SET created_at = @cutoff
			      WHERE created_by = 'poolkeeper' AND created_at > @cutoff`,
			Params: map[string]any{"cutoff": cutoff},
		})
		return err
	})
	if err != nil && ctx.Err() == nil {
		fmt.Printf("reclaim nemesis could not age the fleet: %v\n", err)
	}
}

// dumpState writes the quiescent lifecycle tables to state.txt so a violation
// can be analyzed after the next run has wiped the database.
func (d *driver) dumpState(ctx context.Context) {
	f, err := os.Create(filepath.Join(d.cfg.RunDir, "state.txt"))
	if err != nil {
		fmt.Printf("state dump unavailable: %v\n", err)
		return
	}
	defer f.Close()
	for _, sql := range []string{
		"SELECT host_id, name, status, zone, requested_zone, zone_pinned, gce_instance_name, current_device_count, protected_device_count, error_message FROM hosts ORDER BY created_at",
		"SELECT device_id, host_id, name, status, pinned_at IS NOT NULL, error_message FROM devices ORDER BY created_at",
		"SELECT scope, scope_id, max_hosts, used_hosts FROM quotas",
		"SELECT task_id, task_type, state, attempts, resource_id, last_error FROM tasks",
	} {
		fmt.Fprintln(f, "--", sql)
		iter := d.raw.Single().Query(ctx, gspanner.Statement{SQL: sql})
		err := iter.Do(func(row *gspanner.Row) error {
			fmt.Fprintln(f, row.String())
			return nil
		})
		iter.Stop()
		if err != nil {
			fmt.Fprintln(f, "ERR", err)
		}
	}
	fmt.Printf("state dump: %s\n", filepath.Join(d.cfg.RunDir, "state.txt"))
}

// awaitQuiesce waits until the queue is empty and no resource sits in a
// transitional state. The fleet keeps working through this window, which is
// the recovery the harness verifies.
func (d *driver) awaitQuiesce(ctx context.Context) bool {
	fmt.Println("workload window done; waiting for fleet quiescence...")
	deadline := time.Now().Add(d.cfg.QuiesceTimeout)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		var tasks, transitional int64
		row, err := d.raw.Single().Query(ctx, gspanner.Statement{
			SQL: `SELECT (SELECT COUNT(*) FROM tasks WHERE state IN ('pending','leased')) +
			             0 AS tasks,
			             (SELECT COUNT(*) FROM devices WHERE status IN ('pending','creating','starting','stopping','deleting')) +
			             (SELECT COUNT(*) FROM hosts WHERE status IN ('pending','provisioning','starting','stopping','deleting','preempted')) AS transitional`,
		}).Next()
		if err == nil {
			if err := row.Columns(&tasks, &transitional); err == nil && tasks == 0 && transitional == 0 &&
				d.driftsResolved(ctx) {
				return true
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(2 * time.Second):
		}
	}
	fmt.Printf("quiesce timeout (%v) exceeded\n", d.cfg.QuiesceTimeout)
	return false
}

// driftsResolved reports whether every drift victim's host has left the
// state it was disturbed in: the vanished or preempted instance name is no
// longer claimed by a VM-expecting host row (the host recovered under a new
// incarnation name, was marked lost, or was user-deleted), or the claimed
// instance is alive again (a vanish inside the provisioning window that the
// provision owner legitimately re-drove; the fleet is settled, not
// classifying). Without this, a drift injected at the window's edge could
// pass the task/transitional quiesce before the detector's next cycle
// classified it, and the outcome checker would read an in-flight
// classification as a violation.
func (d *driver) driftsResolved(ctx context.Context) bool {
	d.mu.Lock()
	events := make([]driftEvent, len(d.driftEvents))
	copy(events, d.driftEvents)
	d.mu.Unlock()
	var alive map[string]bool
	for _, ev := range events {
		if ev.hostID == "" {
			continue
		}
		iter := d.raw.Single().Query(ctx, gspanner.Statement{
			SQL:    `SELECT status, COALESCE(gce_instance_name,'') FROM hosts WHERE host_id = @id`,
			Params: map[string]any{"id": ev.hostID},
		})
		row, err := iter.Next()
		if err != nil {
			iter.Stop()
			continue // row gone = resolved (user delete raced the drift)
		}
		var status, name string
		scanErr := row.Columns(&status, &name)
		iter.Stop()
		if scanErr != nil {
			continue
		}
		if name != ev.instance || !vmExpectingStatus(status) {
			continue
		}
		if alive == nil {
			alive = liveVMSet(d.gce.Live())
		}
		if alive[name] {
			continue // the claimed instance exists again: re-driven, settled
		}
		return false // still claiming the dead instance; classification in flight
	}
	return true
}

// liveVMSet indexes the fake's live instance names.
func liveVMSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

// vmExpectingStatus mirrors host.Status.ExpectsVM for the checker's raw rows.
func vmExpectingStatus(status string) bool {
	switch status {
	case "starting", "running", "stopping", "stopped", "unreachable":
		return true
	}
	return false
}

func (d *driver) spawn(idx int) error {
	cfgJSON, err := json.Marshal(d.cfg)
	if err != nil {
		return fmt.Errorf("serializing config: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating executable: %w", err)
	}
	cmd := exec.CommandContext(d.baseCtx, exe, "-role=instance", "-config="+string(cfgJSON),
		"-instance-index="+strconv.Itoa(idx))
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 30 * time.Second
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting instance %d: %w", idx, err)
	}
	p := &instProc{idx: idx, cmd: cmd, done: make(chan struct{})}
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			fmt.Fprintf(os.Stderr, "[inst%d] %s\n", idx, sc.Text())
		}
	}()
	go d.watchExit(p)
	d.mu.Lock()
	d.procs = append(d.procs, p)
	d.mu.Unlock()
	return nil
}

// watchExit reaps one instance subprocess. An exit nobody commanded (no
// SIGKILL nemesis (p.dead), no driver shutdown (terminating), no interrupt
// (baseCtx)) is a panic, fatal or OOM kill in production code. The fleet
// usually converges anyway on the surviving instance, which is why it must be
// a finding and not a log line.
func (d *driver) watchExit(p *instProc) {
	err := p.cmd.Wait()
	p.mu.Lock()
	commanded := p.dead
	p.mu.Unlock()
	if !commanded && !d.terminating.Load() && d.baseCtx.Err() == nil {
		status := "exit status 0"
		if err != nil {
			status = err.Error()
		}
		d.mu.Lock()
		d.instDeaths = append(d.instDeaths, fmt.Sprintf(
			"instance %d (pid %d) exited uncommanded mid-run: %s", p.idx, p.cmd.Process.Pid, status))
		d.mu.Unlock()
	}
	close(p.done)
}

func (d *driver) pickAlive() *instProc {
	d.mu.Lock()
	defer d.mu.Unlock()
	var alive []*instProc
	for _, p := range d.procs {
		p.mu.Lock()
		if !p.dead {
			alive = append(alive, p)
		}
		p.mu.Unlock()
	}
	if len(alive) == 0 {
		return nil
	}
	return alive[rand.IntN(len(alive))] //nolint:gosec // chaos selection
}

func (d *driver) chaos(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.KillEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		victim := d.pickAlive()
		if victim == nil {
			continue
		}
		d.mu.Lock()
		d.kills++
		d.mu.Unlock()
		victim.mu.Lock()
		victim.dead = true
		victim.mu.Unlock()
		fmt.Printf("chaos: SIGKILL instance %d (pid %d)\n", victim.idx, victim.cmd.Process.Pid)
		_ = victim.cmd.Process.Kill()

		idx := victim.idx
		d.nemesis.Go(func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(d.cfg.RestartAfter):
			}
			if ctx.Err() != nil {
				return
			}
			if err := d.spawn(idx); err != nil {
				fmt.Printf("chaos: respawn of instance %d failed: %v\n", idx, err)
			} else {
				fmt.Printf("chaos: instance %d respawned\n", idx)
			}
		})
	}
}

func (d *driver) pauseLoop(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.PauseEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		victim := d.pickAlive()
		if victim == nil {
			continue
		}
		if err := victim.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
			continue
		}
		d.mu.Lock()
		d.pauses++
		d.mu.Unlock()
		fmt.Printf("chaos: SIGSTOP instance %d for %v\n", victim.idx, d.cfg.PauseFor)
		select {
		case <-ctx.Done():
		case <-time.After(d.cfg.PauseFor):
		}
		_ = victim.cmd.Process.Signal(syscall.SIGCONT)
		fmt.Printf("chaos: SIGCONT instance %d\n", victim.idx)
	}
}

// stockoutLoop injects zonal capacity exhaustion: one zone at a time (never
// both, since ranked placement must always have somewhere to go), alternating,
// each stockout held for StockoutFor then lifted before the next tick. The
// cycle runs inline so stockouts can never overlap; every zone is restored
// before the loop exits, so the fleet converges during quiesce.
func (d *driver) stockoutLoop(ctx context.Context) {
	zones := []string{harnessZone, harnessZoneB}
	ticker := time.NewTicker(d.cfg.StockoutEvery)
	defer ticker.Stop()
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		zone := zones[i%len(zones)]
		d.gce.StockoutZone(zone)
		d.mu.Lock()
		d.stockouts = append(d.stockouts, zone)
		d.mu.Unlock()
		fmt.Printf("chaos: stockout %s for %v\n", zone, d.cfg.StockoutFor)
		select {
		case <-ctx.Done():
		case <-time.After(d.cfg.StockoutFor):
		}
		d.gce.RestoreZone(zone)
		fmt.Printf("chaos: stockout %s lifted\n", zone)
		if ctx.Err() != nil {
			return
		}
	}
}

// waitOrDone sleeps for d unless ctx ends first.
func waitOrDone(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// dbFaultLoop injects the configured fault on a random instance's Spanner
// link for DBFaultFor, then clears it. The cut is the ambiguous-commit
// fault: an actuation may have reached the Host Orchestrator while its
// commit never reported. It is the network twin of kill-after-effects, and the
// window the adopt-race detector watches.
func (d *driver) dbFaultLoop(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.DBFaultEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		idx := rand.IntN(len(d.proxies)) //nolint:gosec // chaos selection
		p := d.proxies[idx]
		d.mu.Lock()
		d.dbFaults++
		d.mu.Unlock()
		switch d.cfg.DBFault {
		case dbFaultLatency:
			fmt.Printf("chaos: +%v DB latency on instance %d for %v\n", d.cfg.DBFaultLatency, idx, d.cfg.DBFaultFor)
			p.SetLatency(d.cfg.DBFaultLatency)
			waitOrDone(ctx, d.cfg.DBFaultFor)
			p.SetLatency(0)
		case dbFaultBlackhole:
			fmt.Printf("chaos: DB blackhole on instance %d for %v\n", idx, d.cfg.DBFaultFor)
			p.SetBlackhole(true)
			waitOrDone(ctx, d.cfg.DBFaultFor)
			p.SetBlackhole(false)
		case dbFaultCut:
			n := p.CutConnections()
			fmt.Printf("chaos: cut %d DB connections on instance %d\n", n, idx)
		}
	}
}

// hoFaultHangDelay is the per-response hold in hang mode: comfortably past
// the harness hoclient timeout (10s), so every in-flight actuation times out
// with its effect on the CVD unknown.
const hoFaultHangDelay = 12 * time.Second

// hoFaultLoop degrades the Host Orchestrator boundary for HOFaultFor: hang
// holds every response past the client timeout (actuations end
// outcome-unknown), error 500s every request. Both burn attempts; the
// checkers judge what the retries left behind.
func (d *driver) hoFaultLoop(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.HOFaultEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		d.mu.Lock()
		d.hoFaults++
		d.mu.Unlock()
		switch d.cfg.HOFault {
		case hoFaultHang:
			fmt.Printf("chaos: HO hang (%v per response) for %v\n", hoFaultHangDelay, d.cfg.HOFaultFor)
			d.ho.SetHang(hoFaultHangDelay)
			waitOrDone(ctx, d.cfg.HOFaultFor)
			d.ho.SetHang(0)
		case hoFaultError:
			fmt.Printf("chaos: HO 500s for %v\n", d.cfg.HOFaultFor)
			d.ho.SetErrorAll(true)
			waitOrDone(ctx, d.cfg.HOFaultFor)
			d.ho.SetErrorAll(false)
		}
	}
}

// driftLoop injects cloud-side drift: a VM vanishes (deleted outside CCO, so
// the host reconciler must mark the host lost and cascade its devices) or is
// preempted (it must detect and re-provision, bounded by the recovery cap).
func (d *driver) driftLoop(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.DriftEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		name := d.gce.RandomLive()
		if name == "" {
			continue
		}
		// Resolve the owning host (and its status) BEFORE injecting:
		// recovery renames the instance (incarnation bump), so the name
		// alone cannot find the host afterward, and the vanish contract
		// depends on whether CCO had observed the host serving yet. The
		// read races the injection by a moment; a host promoted in that
		// gap is judged by the lenient contract, which still catches a
		// dangling claim.
		hostID, hostStatus := d.hostByInstance(ctx, name)
		var event driftEvent
		if rand.IntN(2) == 0 { //nolint:gosec // chaos selection
			d.gce.Vanish(name)
			event = driftEvent{kind: "vanish", hostID: hostID, instance: name, hostStatus: hostStatus}
		} else {
			d.gce.Preempt(name)
			event = driftEvent{kind: "preempt", hostID: hostID, instance: name, hostStatus: hostStatus}
		}
		fmt.Printf("chaos: drift %s %s\n", event.kind, name)
		d.mu.Lock()
		d.drifts = append(d.drifts, event.kind+" "+name)
		d.driftEvents = append(d.driftEvents, event)
		d.mu.Unlock()
	}
}

// hostByInstance resolves the host row claiming an instance name and its
// current status; ("", "") when none does (a VM the DB never knew, whose
// outcome is the orphan checker's).
func (d *driver) hostByInstance(ctx context.Context, name string) (string, string) {
	iter := d.raw.Single().Query(ctx, gspanner.Statement{
		SQL:    `SELECT host_id, status FROM hosts WHERE gce_instance_name = @name AND status != 'deleted'`,
		Params: map[string]any{"name": name},
	})
	defer iter.Stop()
	row, err := iter.Next()
	if err != nil {
		return "", ""
	}
	var id, status string
	if err := row.Columns(&id, &status); err != nil {
		return "", ""
	}
	return id, status
}

func (d *driver) terminate() {
	d.terminating.Store(true)
	d.mu.Lock()
	procs := make([]*instProc, len(d.procs))
	copy(procs, d.procs)
	d.mu.Unlock()
	for _, p := range procs {
		p.mu.Lock()
		dead := p.dead
		p.mu.Unlock()
		if !dead {
			_ = p.cmd.Process.Signal(syscall.SIGCONT)
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
		}
	}
	for _, p := range procs {
		select {
		case <-p.done:
		case <-time.After(30 * time.Second):
			_ = p.cmd.Process.Kill()
			<-p.done
		}
	}
}
