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
	"strings"
	"time"
)

// Load shapes, enqueue commit shapes, and DB fault modes.
const (
	scenarioSteady = "steady"
	scenarioDrain  = "drain"
	scenarioBurst  = "burst"
	scenarioIdle   = "idle"
	scenarioKeyed  = "keyed"

	shapeCounter = "counter"
	shapePlain   = "plain"

	dbFaultLatency   = "latency"
	dbFaultBlackhole = "blackhole"
	dbFaultCut       = "cut"
)

// Cfg carries every setting for a run. The driver resolves it once from flags and
// passes it to instance subprocesses verbatim as JSON, so both roles always
// agree on shard count, lease durations, and handler behavior.
type Cfg struct {
	Project  string `json:"project"`
	Instance string `json:"instance"`
	Database string `json:"database"`
	Emulator string `json:"emulator"`

	Scenario string        `json:"scenario"` // steady | drain | burst | idle | keyed
	Rate     float64       `json:"rate"`     // enqueues/sec (steady, burst, keyed)
	Duration time.Duration `json:"duration"`
	Preload  int           `json:"preload"` // pending tasks before start (drain)
	Burst    int           `json:"burst"`   // extra tasks injected at mid-run (burst)

	KeyedResources int           `json:"keyed_resources"` // resource set the keyed churn cycles over
	KeyedRate      float64       `json:"keyed_rate"`      // keyed reconcile filings per second
	KeyedHandler   time.Duration `json:"keyed_handler"`   // leased-sibling hold time

	Instances  int           `json:"instances"`   // simulated orchestrator instances
	MinWorkers int           `json:"min_workers"` // pool floor; equal to Workers pins the pool fixed
	Workers    int           `json:"workers"`     // pool ceiling
	Shards     int           `json:"shards"`
	ClaimBatch int           `json:"claim_batch"`
	Lease      time.Duration `json:"lease"`
	HandleTO   time.Duration `json:"handle_timeout"`
	PollMin    time.Duration `json:"poll_min"`
	PollMax    time.Duration `json:"poll_max"`
	RetryBase  time.Duration `json:"retry_base"`
	RetryMax   time.Duration `json:"retry_max"`
	SweepEvery time.Duration `json:"sweep_interval"`

	HandlerMin    time.Duration `json:"handler_min"` // synthetic work latency bounds
	HandlerMax    time.Duration `json:"handler_max"`
	TransientRate float64       `json:"transient_rate"` // fail first attempt(s), then succeed
	TransientN    int           `json:"transient_failures"`
	PoisonRate    float64       `json:"poison_rate"` // always fail -> dead-letter
	MaxAttempts   int           `json:"max_attempts"`

	EnqueueShape string `json:"enqueue_shape"` // counter | plain
	HotRows      int    `json:"hot_rows"`      // counter rows the enqueue txn contends on
	Enqueuers    int    `json:"enqueuers"`

	KillEvery    time.Duration `json:"kill_interval"` // chaos: SIGKILL a random instance
	RestartAfter time.Duration `json:"restart_delay"`

	PauseEvery       time.Duration   `json:"pause_interval"`     // chaos: SIGSTOP a random instance
	PauseFor         time.Duration   `json:"pause_duration"`     // how long a paused instance stays stopped
	ClockSkews       []time.Duration `json:"clock_skews"`        // per-instance clock offsets (cycled by index)
	KillAfterEffects int             `json:"kill_after_effects"` // instance 0 self-SIGKILLs after its Nth effect
	DBFault          string          `json:"db_fault"`           // latency | blackhole | cut ("" = off)
	DBFaultEvery     time.Duration   `json:"db_fault_interval"`
	DBFaultFor       time.Duration   `json:"db_fault_duration"`
	DBFaultLatency   time.Duration   `json:"db_fault_latency"`
	ProxyAddrs       []string        `json:"proxy_addrs"` // driver-populated; instance i dials ProxyAddrs[i]

	SnapshotEvery time.Duration `json:"snapshot_interval"`
	DrainTimeout  time.Duration `json:"drain_timeout"`
	RunDir        string        `json:"run_dir"`

	// InstanceIndex is kept out of the JSON round-trip. The flag binds straight
	// into this field, and an unmarshal AFTER flag.Parse would clobber the
	// parsed value (json.Unmarshal writes through the same pointer the flag
	// reads back). Each child gets its ordinal only via the -instance-index
	// flag.
	InstanceIndex int `json:"-"` // instance role only
}

func main() {
	var cfg Cfg
	f := registerFlags(&cfg)
	flag.Parse()

	if err := resolveCfg(&cfg, *f.cfgJSON, *f.skews); err != nil {
		fatalf("%v", err)
	}
	if err := validate(&cfg); err != nil {
		fatalf("invalid config: %v", err)
	}
	if err := runRole(*f.role, cfg); err != nil {
		fatalf("%v", err)
	}
}

// processFlags are the flags that steer the process itself rather than the
// run: which role this binary plays, and the two values that still need
// parsing once flag.Parse has run.
type processFlags struct {
	role    *string
	cfgJSON *string
	skews   *string
}

// registerFlags binds every run setting onto cfg.
func registerFlags(cfg *Cfg) processFlags {
	f := processFlags{
		role:    flag.String("role", "driver", "driver (default) or instance (internal, spawned by the driver)"),
		cfgJSON: flag.String("config", "", "full config as JSON (internal, instance role)"),
	}
	flag.StringVar(&cfg.Project, "project", "loadtest-project", "GCP project ID")
	flag.StringVar(&cfg.Instance, "spanner-instance", "loadtest-instance", "Spanner instance ID")
	flag.StringVar(&cfg.Database, "database", "orchestrator-loadtest", `Spanner database (name must contain "loadtest" unless targeting the emulator)`)

	flag.StringVar(&cfg.Scenario, "scenario", scenarioSteady, "load shape: steady | drain | burst | idle | keyed")
	flag.Float64Var(&cfg.Rate, "rate", 25, "target enqueue rate per second")
	flag.DurationVar(&cfg.Duration, "duration", 2*time.Minute, "load phase duration")
	flag.IntVar(&cfg.Preload, "preload", 50000, "pending tasks to preload (drain scenario)")
	flag.IntVar(&cfg.Burst, "burst", 5000, "extra tasks injected at mid-run (burst scenario)")
	flag.IntVar(&cfg.KeyedResources, "keyed-resources", 16, "distinct reconcile resources the keyed churn cycles over (keyed scenario)")
	flag.Float64Var(&cfg.KeyedRate, "keyed-rate", 10, "keyed reconcile filings per second (keyed scenario)")
	flag.DurationVar(&cfg.KeyedHandler, "keyed-handler", 10*time.Second, "keyed handler latency, i.e. how long a leased sibling blocks its pending head (keyed scenario)")

	flag.IntVar(&cfg.Instances, "instances", 2, "simulated orchestrator instances (subprocesses); 0 = enqueue only")
	flag.IntVar(&cfg.Workers, "workers", 8, "worker ceiling per instance")
	flag.IntVar(&cfg.MinWorkers, "min-workers", 0, "worker floor per instance; equal to -workers pins the pool fixed, 0 takes the pool default")
	flag.IntVar(&cfg.Shards, "shards", 16, "queue shard fan-out (enqueue and claim side)")
	flag.IntVar(&cfg.ClaimBatch, "claim-batch", 1, "tasks leased per claim")
	flag.DurationVar(&cfg.Lease, "lease", 90*time.Second, "task lease duration (bounds chaos redelivery)")
	flag.DurationVar(&cfg.HandleTO, "handle-timeout", 60*time.Second, "handler timeout; must be under lease")
	flag.DurationVar(&cfg.PollMin, "poll-min", 200*time.Millisecond, "empty-shard poll backoff floor")
	flag.DurationVar(&cfg.PollMax, "poll-max", 2*time.Second, "empty-shard poll backoff ceiling")
	flag.DurationVar(&cfg.RetryBase, "retry-base", 500*time.Millisecond, "failed-task backoff base (compressed vs prod)")
	flag.DurationVar(&cfg.RetryMax, "retry-max", 10*time.Second, "failed-task backoff cap (compressed vs prod)")
	flag.DurationVar(&cfg.SweepEvery, "sweep-interval", 15*time.Second, "sweeper interval (leader-gated across instances)")

	flag.DurationVar(&cfg.HandlerMin, "handler-min", 2*time.Second, "synthetic handler latency floor")
	flag.DurationVar(&cfg.HandlerMax, "handler-max", 5*time.Second, "synthetic handler latency ceiling")
	flag.Float64Var(&cfg.TransientRate, "transient-rate", 0, "fraction of tasks failing their first attempt(s)")
	flag.IntVar(&cfg.TransientN, "transient-failures", 1, "attempts a transient task fails before succeeding")
	flag.Float64Var(&cfg.PoisonRate, "poison-rate", 0, "fraction of tasks that always fail (dead-letter)")
	flag.IntVar(&cfg.MaxAttempts, "max-attempts", 3, "MaxAttempts on enqueued tasks (compressed vs prod 10)")

	flag.StringVar(&cfg.EnqueueShape, "enqueue-shape", shapeCounter, "counter = production shape (read+increment a counter row in the enqueue txn) | plain = bare insert")
	flag.IntVar(&cfg.HotRows, "hot-rows", 64, "counter rows enqueues spread over; low values concentrate write contention")
	flag.IntVar(&cfg.Enqueuers, "enqueuers", 16, "concurrent enqueue goroutines")

	flag.DurationVar(&cfg.KillEvery, "kill-interval", 0, "chaos: SIGKILL a random instance this often (0 = off)")
	flag.DurationVar(&cfg.RestartAfter, "restart-delay", 10*time.Second, "chaos: delay before a killed instance is replaced")
	flag.DurationVar(&cfg.PauseEvery, "pause-interval", 0, "chaos: SIGSTOP a random instance this often (0 = off)")
	flag.DurationVar(&cfg.PauseFor, "pause-duration", 45*time.Second, "chaos: how long a paused instance stays stopped (exceed -lease to force reclaim + fencing)")
	f.skews = flag.String("clock-skew", "", "chaos: comma-separated per-instance clock offsets for lease arithmetic, cycled by index (e.g. \"0s,2s,-2s\")")
	flag.IntVar(&cfg.KillAfterEffects, "kill-after-effects", 0, "chaos: instance 0 SIGKILLs itself right after its Nth effect, before acking (needs -instances >= 2; 0 = off)")
	flag.StringVar(&cfg.DBFault, "db-fault", "", "chaos: DB-link fault on a random instance: latency | blackhole | cut (emulator only; \"\" = off)")
	flag.DurationVar(&cfg.DBFaultEvery, "db-fault-interval", 30*time.Second, "chaos: how often to inject the DB fault")
	flag.DurationVar(&cfg.DBFaultFor, "db-fault-duration", 10*time.Second, "chaos: how long latency/blackhole faults last")
	flag.DurationVar(&cfg.DBFaultLatency, "db-fault-latency", 250*time.Millisecond, "chaos: added per-chunk latency in latency mode")

	flag.DurationVar(&cfg.SnapshotEvery, "snapshot-interval", 5*time.Second, "stats sampling interval")
	flag.DurationVar(&cfg.DrainTimeout, "drain-timeout", 5*time.Minute, "max wait for the queue to empty after the load phase")
	flag.StringVar(&cfg.RunDir, "out", "", "artifacts directory (default: a fresh temp dir)")
	flag.IntVar(&cfg.InstanceIndex, "instance-index", 0, "instance ordinal (internal)")
	return f
}

// resolveCfg completes the parsed flags: an instance subprocess takes the
// driver's serialized config verbatim, the emulator endpoint comes from the
// environment, and the clock-skew list needs parsing.
func resolveCfg(cfg *Cfg, cfgJSON, skews string) error {
	if cfgJSON != "" {
		// InstanceIndex survives the unmarshal (json:"-"): the flag already
		// parsed it into cfg, and the serialized config cannot clobber it.
		if err := json.Unmarshal([]byte(cfgJSON), cfg); err != nil {
			return fmt.Errorf("parsing -config: %w", err)
		}
	}
	cfg.Emulator = os.Getenv("SPANNER_EMULATOR_HOST")
	if skews != "" {
		parsed, err := parseSkews(skews)
		if err != nil {
			return fmt.Errorf("invalid -clock-skew: %w", err)
		}
		cfg.ClockSkews = parsed
	}
	return nil
}

// runRole runs the leg this process was started for: the driver orchestrates
// the run, an instance is one of the subprocesses it spawned.
func runRole(role string, cfg Cfg) error {
	switch role {
	case "driver":
		return runDriver(cfg)
	case "instance":
		return runInstance(cfg)
	default:
		return fmt.Errorf("unknown role %q", role)
	}
}

// validate rejects configs that would corrupt a real database or violate the
// pool's invariants before any subprocess spawns.
func validate(cfg *Cfg) error {
	// Hard safety gate: the driver deletes every task row between runs. Only a
	// dedicated database (or the emulator) may be targeted; there is no override.
	if cfg.Emulator == "" && !strings.Contains(cfg.Database, "loadtest") {
		return fmt.Errorf("database %q: name must contain \"loadtest\" (the harness wipes the tasks table); set SPANNER_EMULATOR_HOST to target the emulator", cfg.Database)
	}
	if err := validateScenario(cfg); err != nil {
		return err
	}
	if err := validateWorkload(cfg); err != nil {
		return err
	}
	return validateChaos(cfg)
}

// validateScenario checks the load shape and the settings only one shape reads.
func validateScenario(cfg *Cfg) error {
	switch cfg.Scenario {
	case scenarioSteady, scenarioDrain, scenarioBurst, scenarioIdle:
	case scenarioKeyed:
		if cfg.KeyedResources < 1 {
			return fmt.Errorf("keyed-resources must be >= 1, got %d", cfg.KeyedResources)
		}
		if cfg.KeyedRate <= 0 {
			return fmt.Errorf("keyed-rate must be positive, got %v", cfg.KeyedRate)
		}
		if cfg.KeyedHandler <= 0 || cfg.KeyedHandler >= cfg.HandleTO {
			return fmt.Errorf("keyed-handler (%v) must be positive and stay under handle-timeout (%v)",
				cfg.KeyedHandler, cfg.HandleTO)
		}
	default:
		return fmt.Errorf("unknown scenario %q", cfg.Scenario)
	}
	return nil
}

// validateWorkload checks the enqueue shape and the lease/timeout/latency
// ordering the pool's invariants rest on.
func validateWorkload(cfg *Cfg) error {
	if cfg.EnqueueShape != shapeCounter && cfg.EnqueueShape != shapePlain {
		return fmt.Errorf("unknown enqueue shape %q", cfg.EnqueueShape)
	}
	if cfg.Lease <= cfg.HandleTO {
		return fmt.Errorf("lease (%v) must exceed handle-timeout (%v)", cfg.Lease, cfg.HandleTO)
	}
	if cfg.HandlerMax >= cfg.HandleTO {
		return fmt.Errorf("handler-max (%v) must stay under handle-timeout (%v)", cfg.HandlerMax, cfg.HandleTO)
	}
	if cfg.HandlerMin > cfg.HandlerMax {
		return fmt.Errorf("handler-min (%v) exceeds handler-max (%v)", cfg.HandlerMin, cfg.HandlerMax)
	}
	if cfg.PoisonRate+cfg.TransientRate > 1 {
		return fmt.Errorf("poison-rate + transient-rate exceed 1 (%v + %v)", cfg.PoisonRate, cfg.TransientRate)
	}
	if cfg.HotRows < 1 {
		return fmt.Errorf("hot-rows must be >= 1, got %d", cfg.HotRows)
	}
	if cfg.MaxAttempts < 1 {
		return fmt.Errorf("max-attempts must be >= 1, got %d", cfg.MaxAttempts)
	}
	return nil
}

// validateChaos checks the nemesis settings, including the ones that only work
// against the emulator.
func validateChaos(cfg *Cfg) error {
	if cfg.PauseEvery > 0 && cfg.PauseFor <= 0 {
		return errors.New("pause-duration must be positive when pause-interval is set")
	}
	if cfg.KillAfterEffects > 0 && cfg.Instances < 2 {
		return errors.New("kill-after-effects needs -instances >= 2 (instance 0 dies and is not respawned)")
	}
	switch cfg.DBFault {
	case "", dbFaultLatency, dbFaultBlackhole, dbFaultCut:
	default:
		return fmt.Errorf("unknown db-fault %q", cfg.DBFault)
	}
	if cfg.DBFault != "" {
		if cfg.Emulator == "" {
			return errors.New("db-fault requires the emulator (the endpoint override is emulator-only)")
		}
		if cfg.DBFaultEvery <= 0 {
			return errors.New("db-fault-interval must be positive when db-fault is set")
		}
	}
	return nil
}

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
	for _, s := range c.ClockSkews {
		if s != 0 {
			return true
		}
	}
	return false
}

// historyOpts derives the checker's tolerance from the faults this run
// injected.
func (c *Cfg) historyOpts() historyOpts {
	return historyOpts{
		pausesInjected:  c.PauseEvery > 0,
		skewInjected:    c.skewInjected(),
		crashesPossible: c.KillEvery > 0 || c.KillAfterEffects > 0 || c.DBFault != "",
	}
}

func parseSkews(s string) ([]time.Duration, error) {
	parts := strings.Split(s, ",")
	out := make([]time.Duration, 0, len(parts))
	for _, p := range parts {
		d, err := time.ParseDuration(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("parsing %q: %w", p, err)
		}
		out = append(out, d)
	}
	return out, nil
}

func fatalf(format string, args ...any) {
	slog.Error(fmt.Sprintf(format, args...))
	os.Exit(1)
}
