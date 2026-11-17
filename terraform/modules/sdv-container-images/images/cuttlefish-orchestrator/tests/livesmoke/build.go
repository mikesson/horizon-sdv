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
	"fmt"
	"os/exec"
	"path/filepath"
)

// buildCfctl compiles ./cmd/cfctl into dir and returns the binary's path.
//
// The harness used to expect a cfctl on PATH, which meant every run depended
// on an untracked repo-root binary someone remembered to rebuild, and runs
// exited before provisioning when nobody had. Building from the tree under
// test removes both failure modes: the binary always exists and always
// matches the code being smoked. Assumes the working directory is the repo
// root, which `go run ./tests/livesmoke` already requires.
func buildCfctl(ctx context.Context, dir string) (string, error) {
	bin := filepath.Join(dir, "cfctl")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/cfctl")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building cfctl from ./cmd/cfctl: %w\n%s", err, out)
	}
	return bin, nil
}
