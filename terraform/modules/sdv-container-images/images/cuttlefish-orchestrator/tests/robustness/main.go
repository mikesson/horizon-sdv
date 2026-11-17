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
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"
)

// Cfg carries every setting for a run. The driver resolves it once from flags and
// passes it to instance subprocesses verbatim as JSON.
type Cfg struct {
	Project  string `json:"project"`
	Instance string `json:"instance"`
	Database string `json:"database"`
	Emulator string `json:"emulator"`

	Instances int           `json:"instances"`
	Duration  time.Duration `json:"duration"` // workload window; jobs keep running through quiesce
	Seed      uint64        `json:"seed"`     // workload determinism (per-instance offset applied)

	WorkloadWorkers int     `json:"workload_workers"` // concurrent op generators per instance
	OpsRate         float64 `json:"ops_rate"`         // target ops/sec per generator
	MaxHosts        int64   `json:"max_hosts"`        // seeded quota ceiling

	Workers     int           `json:"workers"` // task workers per instance
	Shards      int           `json:"shards"`
	Lease       time.Duration `json:"lease"`
	HandleTO    time.Duration `json:"handle_timeout"`
	SweepEvery  time.Duration `json:"sweep_interval"`
	ReapEvery   time.Duration `json:"reap_interval"`
	SyncEvery   time.Duration `json:"sync_interval"`
	HealthEvery time.Duration `json:"health_interval"`
	LeaseTTL    time.Duration `json:"lease_ttl"` // background-plane leader lease (production: 45s)

	KillEvery     time.Duration `json:"kill_interval"`
	RestartAfter  time.Duration `json:"restart_delay"`
	PauseEvery    time.Duration `json:"pause_interval"`
	PauseFor      time.Duration `json:"pause_duration"`
	DriftEvery    time.Duration `json:"drift_interval"`    // vanish/preempt VMs at the fake boundary
	StockoutEvery time.Duration `json:"stockout_interval"` // zone-stockout nemesis cadence (0 = off)
	StockoutFor   time.Duration `json:"stockout_duration"` // how long each stockout holds
	LoadDelay     time.Duration `json:"load_delay"`        // slow CVD loads: Starting window before Running
	PoolFloor     int           `json:"pool_floor"`        // standing-capacity scenario: the seeded standby buffer (0 = off)
	FleetFloor    int           `json:"fleet_floor"`       // fleet-floor scenario: the seeded floor (0 = off)
	PoolReclaim   bool          `json:"pool_reclaim"`      // standing-capacity scenario: seed `on` and drop the target mid-run
	GroupCollapse bool          `json:"group_collapse"`    // dedicated-host scenario: run groups that fence a host, end them, and let the keeper reclaim it

	DBFault        string          `json:"db_fault"`          // latency | blackhole | cut ("" = off)
	DBFaultEvery   time.Duration   `json:"db_fault_interval"` // cadence of DB-link faults
	DBFaultFor     time.Duration   `json:"db_fault_duration"` // how long latency/blackhole hold
	DBFaultLatency time.Duration   `json:"db_fault_latency"`  // added per-chunk delay in latency mode
	ClockSkews     []time.Duration `json:"clock_skews"`       // per-instance lease-clock offsets (cycled by index)
	HOFault        string          `json:"ho_fault"`          // hang | error ("" = off)
	HOFaultEvery   time.Duration   `json:"ho_fault_interval"` // cadence of HO-boundary faults
	HOFaultFor     time.Duration   `json:"ho_fault_duration"` // how long each HO fault holds

	ProxyAddrs []string `json:"proxy_addrs"` // driver-populated; instance i dials ProxyAddrs[i]

	QuiesceTimeout time.Duration `json:"quiesce_timeout"`
	RunDir         string        `json:"run_dir"`

	// Driver-populated boundary endpoints.
	GCEURL string `json:"gce_url"`
	HOPort int    `json:"ho_port"`

	// InstanceIndex is excluded from the JSON round-trip: the flag binds into
	// this field and an unmarshal after flag.Parse would clobber it (the
	// queueload lesson).
	InstanceIndex int `json:"-"`
}

// attemptBurners reports whether any configured nemesis can legitimately
// exhaust a healthy task's attempt budget: crashes consume the attempt a
// claim persisted, pauses and clock skews force reclaims past the lease,
// drift makes recovery race fresh injections, a stockout can outlast a
// pinned host's retries, and DB or HO faults fail healthy attempts outright.
// Without one of these, a dead-letter is a handler bug and never collateral,
// the same conditioning queueload's history checker applies. Slow loads are
// excluded on purpose: a retry that dead-letters because it raced an
// in-flight launch instead of waiting it out is a bug.
func (c *Cfg) attemptBurners() bool {
	return c.KillEvery > 0 || c.PauseEvery > 0 || c.DriftEvery > 0 || c.StockoutEvery > 0 ||
		c.DBFault != "" || c.HOFault != "" || c.skewInjected()
}

// recoveryBurners is attemptBurners minus drift: drift is the fault the
// preemption-recovery contract tests, and the fake boundary accepts every
// re-provision, so in a drift-only run a recovery that dead-letters into
// error is a finding, not collateral.
func (c *Cfg) recoveryBurners() bool {
	return c.KillEvery > 0 || c.PauseEvery > 0 || c.StockoutEvery > 0 ||
		c.DBFault != "" || c.HOFault != "" || c.skewInjected()
}

func main() {
	var (
		role    = flag.String("role", "driver", "driver (default) or instance (internal)")
		cfgJSON = flag.String("config", "", "full config as JSON (internal, instance role)")
		cfg     Cfg
	)
	flag.StringVar(&cfg.Project, "project", "loadtest-project", "GCP project ID")
	flag.StringVar(&cfg.Instance, "spanner-instance", "loadtest-instance", "Spanner instance ID")
	flag.StringVar(&cfg.Database, "database", "robustness-loadtest", `Spanner database (name must contain "loadtest" unless targeting the emulator)`)

	flag.IntVar(&cfg.Instances, "instances", 2, "orchestrator instances (subprocesses)")
	flag.DurationVar(&cfg.Duration, "duration", time.Minute, "workload window")
	flag.Uint64Var(&cfg.Seed, "seed", 1, "workload seed (instance index is folded in)")
	flag.IntVar(&cfg.WorkloadWorkers, "workload-workers", 3, "concurrent op generators per instance")
	flag.Float64Var(&cfg.OpsRate, "ops-rate", 1.5, "target ops/sec per generator")
	flag.Int64Var(&cfg.MaxHosts, "max-hosts", 200, "seeded quota ceiling")

	flag.IntVar(&cfg.Workers, "workers", 8, "task workers per instance")
	flag.IntVar(&cfg.Shards, "shards", 16, "queue shards")
	flag.DurationVar(&cfg.Lease, "lease", 20*time.Second, "task lease")
	flag.DurationVar(&cfg.HandleTO, "handle-timeout", 15*time.Second, "handler timeout")
	flag.DurationVar(&cfg.SweepEvery, "sweep-interval", 5*time.Second, "task sweeper interval")
	flag.DurationVar(&cfg.ReapEvery, "reap-interval", 4*time.Second, "reaper interval")
	flag.DurationVar(&cfg.SyncEvery, "sync-interval", 4*time.Second, "host reconciler interval")
	flag.DurationVar(&cfg.HealthEvery, "health-interval", 5*time.Second, "device controller interval")
	flag.DurationVar(&cfg.LeaseTTL, "lease-ttl", 10*time.Second, "background-plane leader lease TTL, renewed at a third of it; scaled to this harness's clock (production runs 45s) so a killed leader hands over inside a run and a SIGSTOP outlasts the lease")

	flag.DurationVar(&cfg.KillEvery, "kill-interval", 0, "chaos: SIGKILL a random instance this often (0 = off)")
	flag.DurationVar(&cfg.RestartAfter, "restart-delay", 8*time.Second, "chaos: respawn delay")
	flag.DurationVar(&cfg.PauseEvery, "pause-interval", 0, "chaos: SIGSTOP a random instance this often (0 = off)")
	flag.DurationVar(&cfg.PauseFor, "pause-duration", 30*time.Second, "chaos: pause length")
	flag.DurationVar(&cfg.DriftEvery, "drift-interval", 0, "chaos: vanish or preempt a random VM this often (0 = off)")
	flag.DurationVar(&cfg.StockoutEvery, "stockout-interval", 0, "chaos: stock out one zone this often, alternating zones (0 = off)")
	flag.DurationVar(&cfg.StockoutFor, "stockout-duration", 20*time.Second, "chaos: how long each zone stockout holds")
	flag.DurationVar(&cfg.LoadDelay, "load-delay", 2*time.Second, "CVD loads stay Starting this long before Running; an interrupted attempt leaves an in-flight group the retry must wait out, not race (0 = instant creates)")
	flag.IntVar(&cfg.PoolFloor, "pool-floor", 0, "standing-capacity scenario: seed the default pool's standby buffer (and provision_only) so the fleet keeper holds this many idle hosts (0 = off)")
	flag.IntVar(&cfg.FleetFloor, "fleet-floor", 0, "fleet-floor scenario: seed the default pool's floor (with `on` and standby 0) so the keeper holds this many hosts whether or not devices fill them, and reclamation must stop there (0 = off). Its own flag: -pool-floor seeds the FREE-host buffer, which checkPool's bounds are stated against")
	flag.BoolVar(&cfg.PoolReclaim, "pool-reclaim", false, "standing-capacity scenario: seed `on` instead of provision_only and drop the standby buffer to 0 midway, so the run exercises reclamation as well as holding the target")
	flag.BoolVar(&cfg.GroupCollapse, "group-collapse", false, "dedicated-host scenario: seed `on`, run group creates too large to place (each provisions a host fenced to itself), end each group as the next begins, and age exclusive hosts past the shorten floor, so the keeper reclaims hosts whose groups are gone. Needs no fleet: on its own it seeds standby 0, which is the autoscaler configuration a deployment with the pool off actually runs")
	flag.StringVar(&cfg.DBFault, "db-fault", "", "chaos: DB-link fault on a random instance: latency | blackhole | cut (cut = the ambiguous-commit fault; \"\" = off)")
	flag.DurationVar(&cfg.DBFaultEvery, "db-fault-interval", 30*time.Second, "chaos: how often to inject the DB fault")
	flag.DurationVar(&cfg.DBFaultFor, "db-fault-duration", 10*time.Second, "chaos: how long latency/blackhole faults last")
	flag.DurationVar(&cfg.DBFaultLatency, "db-fault-latency", 2*time.Second, "chaos: added per-chunk delay in latency mode")
	skews := flag.String("clock-skew", "", "chaos: comma-separated per-instance clock offsets for lease arithmetic, cycled by index (e.g. \"0s,5s,-5s\")")
	flag.StringVar(&cfg.HOFault, "ho-fault", "", "chaos: Host Orchestrator boundary fault: hang (responses held past the client timeout) | error (every request 500s); \"\" = off")
	flag.DurationVar(&cfg.HOFaultEvery, "ho-fault-interval", 30*time.Second, "chaos: how often to inject the HO fault")
	flag.DurationVar(&cfg.HOFaultFor, "ho-fault-duration", 10*time.Second, "chaos: how long each HO fault holds")

	flag.DurationVar(&cfg.QuiesceTimeout, "quiesce-timeout", 4*time.Minute, "max wait for tasks + transitional states to settle")
	flag.StringVar(&cfg.RunDir, "out", "", "artifacts directory (default: fresh temp dir)")
	flag.IntVar(&cfg.InstanceIndex, "instance-index", 0, "instance ordinal (internal)")
	flag.Parse()

	if *cfgJSON != "" {
		if err := json.Unmarshal([]byte(*cfgJSON), &cfg); err != nil {
			fatalf("parsing -config: %v", err)
		}
	} else if *skews != "" {
		for s := range strings.SplitSeq(*skews, ",") {
			d, err := time.ParseDuration(strings.TrimSpace(s))
			if err != nil {
				fatalf("invalid -clock-skew: %v", err)
			}
			cfg.ClockSkews = append(cfg.ClockSkews, d)
		}
	}
	cfg.Emulator = os.Getenv("SPANNER_EMULATOR_HOST")

	if err := validate(&cfg); err != nil {
		fatalf("invalid config: %v", err)
	}

	var err error
	switch *role {
	case "driver":
		err = runDriver(cfg)
	case "instance":
		err = runInstance(cfg)
	default:
		err = fmt.Errorf("unknown role %q", *role)
	}
	if err != nil {
		fatalf("%v", err)
	}
}

func validate(cfg *Cfg) error {
	// Hard safety gate: the driver wipes every lifecycle table between runs.
	if cfg.Emulator == "" && !strings.Contains(cfg.Database, "loadtest") {
		return fmt.Errorf("database %q: name must contain \"loadtest\" (the harness wipes all lifecycle tables); set SPANNER_EMULATOR_HOST to target the emulator", cfg.Database)
	}
	if cfg.Lease <= cfg.HandleTO {
		return fmt.Errorf("lease (%v) must exceed handle-timeout (%v)", cfg.Lease, cfg.HandleTO)
	}
	if cfg.Instances < 1 {
		return errors.New("instances must be >= 1")
	}
	if cfg.MaxHosts < 1 {
		return errors.New("max-hosts must be >= 1")
	}
	switch cfg.DBFault {
	case "", dbFaultLatency, dbFaultBlackhole, dbFaultCut:
	default:
		return fmt.Errorf("db-fault %q: must be latency, blackhole or cut", cfg.DBFault)
	}
	switch cfg.HOFault {
	case "", hoFaultHang, hoFaultError:
	default:
		return fmt.Errorf("ho-fault %q: must be hang or error", cfg.HOFault)
	}
	return nil
}

const (
	dbFaultLatency   = "latency"
	dbFaultBlackhole = "blackhole"
	dbFaultCut       = "cut"

	hoFaultHang  = "hang"
	hoFaultError = "error"
)

// skewFor returns the clock offset for an instance ordinal, cycling the
// configured list.
func (c *Cfg) skewFor(idx int) time.Duration {
	if len(c.ClockSkews) == 0 {
		return 0
	}
	return c.ClockSkews[idx%len(c.ClockSkews)]
}

// skewInjected reports whether any instance runs with a shifted clock.
func (c *Cfg) skewInjected() bool {
	return slices.ContainsFunc(c.ClockSkews, func(s time.Duration) bool { return s != 0 })
}

// maxAbsSkew is the widest configured clock offset in either direction; the
// pin checker widens its release grace by it, since a skewed reaper
// legitimately runs that late.
func (c *Cfg) maxAbsSkew() time.Duration {
	var m time.Duration
	for _, s := range c.ClockSkews {
		if s < 0 {
			s = -s
		}
		m = max(m, s)
	}
	return m
}

func fatalf(format string, args ...any) {
	slog.Error(fmt.Sprintf(format, args...))
	os.Exit(1)
}
