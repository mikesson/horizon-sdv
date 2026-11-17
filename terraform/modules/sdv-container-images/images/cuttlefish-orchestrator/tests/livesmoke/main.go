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
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"time"
)

// Cfg carries every setting for a run, resolved once from flags.
type Cfg struct {
	Project string
	Region  string
	Service string

	Cfctl    string // path to the shipped binary
	SA       string // service account for the /v1 bearer; empty skips the drift check
	AuthNone bool   // network-mode deployment: send no credentials anywhere

	Scenario    string
	NamePrefix  string
	Interaction string

	CreateTimeout time.Duration
	VerbTimeout   time.Duration
	HealTimeout   time.Duration

	Keep bool
}

// Scenarios. Anything short of full is for a cheap targeted re-check after a
// fix and is never a default.
const (
	scenarioFull       = "full"
	scenarioVerbs      = "verbs"
	scenarioScreenshot = "screenshot"
)

func main() {
	var cfg Cfg
	flag.StringVar(&cfg.Project, "project", "", "GCP project ID (REQUIRED: this run bills real VMs)")
	flag.StringVar(&cfg.Region, "region", "", "Cloud Run region (REQUIRED)")
	flag.StringVar(&cfg.Service, "service", "cuttlefish-orchestrator", "Cloud Run service name")
	flag.StringVar(&cfg.Cfctl, "cfctl", "", "path to a cfctl binary (default: build ./cmd/cfctl into a temp dir)")
	flag.StringVar(&cfg.SA, "sa", "", "service account whose signed JWT reaches /v1 (drift check; skipped when empty)")
	flag.BoolVar(&cfg.AuthNone, "auth-none", false, "network-mode deployment: cfctl and the /v1 checks send no credentials")
	flag.StringVar(&cfg.Scenario, "scenario", scenarioFull, "full, verbs, or screenshot")
	flag.StringVar(&cfg.NamePrefix, "name", "livesmoke", "device name prefix")
	flag.StringVar(&cfg.Interaction, "interaction", "headless", "headless or interactive")
	flag.DurationVar(&cfg.CreateTimeout, "create-timeout", 15*time.Minute, "bound on create reaching running")
	flag.DurationVar(&cfg.VerbTimeout, "verb-timeout", 5*time.Minute, "bound on one lifecycle verb")
	flag.DurationVar(&cfg.HealTimeout, "heal-timeout", 4*time.Minute, "bound on the drift check converging")
	flag.BoolVar(&cfg.Keep, "keep", false, "leave the device and host running (debugging)")
	flag.Parse()

	if err := validate(&cfg); err != nil {
		fatalf("invalid config: %v", err)
	}
	if cfg.Cfctl == "" {
		dir, err := os.MkdirTemp("", "livesmoke-cfctl-")
		if err != nil {
			fatalf("temp dir for cfctl: %v", err)
		}
		defer os.RemoveAll(dir)
		if cfg.Cfctl, err = buildCfctl(context.Background(), dir); err != nil {
			fatalf("%v", err)
		}
		slog.Info("built cfctl from the tree under test", "path", cfg.Cfctl)
	}
	if err := run(cfg); err != nil {
		fatalf("%v", err)
	}
}

func validate(cfg *Cfg) error {
	// No default project: a mistyped or inherited one would provision VMs in
	// somebody else's account.
	if cfg.Project == "" {
		return errors.New("-project is required (this harness creates real, billable VMs)")
	}
	if cfg.Region == "" {
		return errors.New("-region is required (the Cloud Run service's region)")
	}
	switch cfg.Scenario {
	case scenarioFull, scenarioVerbs, scenarioScreenshot:
	default:
		return fmt.Errorf("unknown -scenario %q", cfg.Scenario)
	}
	if cfg.AuthNone && cfg.SA != "" {
		return errors.New("-auth-none and -sa are mutually exclusive (a network deployment accepts bare /v1 requests)")
	}
	// Only an operator-supplied -cfctl is looked up here. The empty default
	// means "build one from the tree", which main does after this returns, so
	// checking it now would reject the documented way to run the harness.
	bins := []string{"gcloud"}
	if cfg.Cfctl != "" {
		bins = append(bins, cfg.Cfctl)
	}
	for _, bin := range bins {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("%s not executable: %w", bin, err)
		}
	}
	return nil
}

func fatalf(format string, args ...any) {
	slog.Error(fmt.Sprintf(format, args...))
	os.Exit(1)
}
