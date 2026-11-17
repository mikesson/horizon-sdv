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
	"os/exec"
	"strings"
	"testing"
)

// TestValidateAcceptsTheDefaultCfctl is the regression net for a harness that
// could not start. validate runs before main builds cfctl, so an empty
// -cfctl (the documented default, and what scripts/ci.sh passes) was fed to
// exec.LookPath and rejected as `" " not executable`. The entry point named in
// the runbook could therefore never reach the first check.
func TestValidateAcceptsTheDefaultCfctl(t *testing.T) {
	t.Parallel()
	requireGcloud(t)

	cfg := Cfg{Project: "example-project", Region: "example-region", Scenario: scenarioFull}
	if err := validate(&cfg); err != nil {
		t.Fatalf("validate rejected the default config: %v", err)
	}
}

// An operator-supplied path is still checked: pointing the harness at a
// binary that is not there must fail before it provisions anything.
func TestValidateRejectsAMissingSuppliedCfctl(t *testing.T) {
	t.Parallel()
	requireGcloud(t)

	cfg := Cfg{Project: "example-project", Region: "example-region", Scenario: scenarioFull, Cfctl: "/nonexistent/cfctl"}
	err := validate(&cfg)
	if err == nil {
		t.Fatal("validate accepted a -cfctl that does not exist")
	}
	if !strings.Contains(err.Error(), "/nonexistent/cfctl") {
		t.Errorf("error does not name the offending path: %v", err)
	}
}

func TestValidateRequiresProject(t *testing.T) {
	t.Parallel()

	cfg := Cfg{Region: "example-region", Scenario: scenarioFull}
	if err := validate(&cfg); err == nil {
		t.Fatal("validate accepted an empty -project; this harness bills real VMs")
	}
}

func TestValidateRequiresRegion(t *testing.T) {
	t.Parallel()

	cfg := Cfg{Project: "example-project", Scenario: scenarioFull}
	if err := validate(&cfg); err == nil {
		t.Fatal("validate accepted an empty -region")
	}
}

// validate looks gcloud up unconditionally, so the two cases above assert
// nothing on a machine without it.
func requireGcloud(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("gcloud"); err != nil {
		t.Skip("gcloud not on PATH")
	}
}
