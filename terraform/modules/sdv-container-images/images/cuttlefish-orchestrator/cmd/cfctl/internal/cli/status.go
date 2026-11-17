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

package cli

import (
	"context"
	"fmt"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// statusResult is the --json shape of 'cfctl status'.
type statusResult struct {
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
}

// Run checks /health, then reads the server version from /info. A version
// fetch failure is a warning and does not fail the command.
func (c *StatusCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	health, err := client.Health(ctx)
	if err != nil {
		return fmt.Errorf("server health check failed: %w", err)
	}

	var version string
	if info, err := client.Info(ctx); err != nil {
		output.Warn("could not fetch server version: %v", err)
	} else {
		version = info.Version
	}

	if cli.JSON {
		return printJSON(statusResult{Status: health.Status, Version: version})
	}

	output.Success("Server is healthy")
	fmt.Printf("Server:  %s\n", cli.Server)
	fmt.Printf("Status:  %s\n", health.Status)
	if version != "" {
		fmt.Printf("Version: %s\n", version)
	}

	return nil
}
