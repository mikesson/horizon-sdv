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

// ZoneCmd groups the zone catalog subcommands.
type ZoneCmd struct {
	List ZoneListCmd `cmd:"" aliases:"ls" help:"List zones and their availability"`
}

// ZoneListCmd implements 'cfctl zone list'.
type ZoneListCmd struct {
	MachineType string `placeholder:"MACHINE_TYPE" help:"Also report whether this machine type is available per zone"`
	GPUType     string `name:"gpu-type" placeholder:"GPU_TYPE" help:"Also report whether this GPU type is available per zone, and its maximum count"`
}

// Run lists the zone catalog.
func (c *ZoneListCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	zones, err := client.ListZoneAvailability(ctx, c.MachineType, c.GPUType)
	if err != nil {
		return fmt.Errorf("failed to list zones: %w", err)
	}
	if cli.JSON {
		return wrapJSONErr(output.PrintJSON(zones))
	}
	return printZones(zones, c.MachineType != "", c.GPUType != "")
}
