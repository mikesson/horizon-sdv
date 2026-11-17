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
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The self-build is the harness's first act; a rot here would surface as a
// live run dying before provisioning, so it is pinned by the ordinary suite.
func TestBuildCfctlProducesRunnableBinary(t *testing.T) {
	// buildCfctl assumes the repo root; tests run in the package directory.
	t.Chdir("../..")
	bin, err := buildCfctl(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "--version")
	// An isolated HOME keeps the run off the developer's real cfctl config.
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("built cfctl failed --version: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) == "" {
		t.Fatal("built cfctl printed nothing for --version")
	}
}
