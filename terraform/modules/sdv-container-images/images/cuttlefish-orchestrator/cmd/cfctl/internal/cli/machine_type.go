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

// MachineTypeCmd groups the machine-type catalog subcommands.
type MachineTypeCmd struct {
	List     MachineTypeListCmd     `cmd:"" aliases:"ls" help:"List the machine-type catalog"`
	Get      MachineTypeGetCmd      `cmd:"" aliases:"show,info" help:"Get machine type details"`
	Default  MachineTypeDefaultCmd  `cmd:"" help:"Show the machine type new hosts get by default"`
	Families MachineTypeFamiliesCmd `cmd:"" help:"List machine families with type counts"`
}

// MachineTypeListCmd implements 'cfctl machine-type list'.
type MachineTypeListCmd struct {
	Family       string `placeholder:"FAMILY" help:"Filter by machine family (e.g., n1, c4, g2)"`
	Category     string `enum:",general_purpose,compute_optimized,memory_optimized,accelerator_optimized,storage_optimized,cost_optimized" default:"" placeholder:"CATEGORY" help:"Filter by workload category: general_purpose, compute_optimized, memory_optimized, accelerator_optimized, storage_optimized, or cost_optimized"`
	Architecture string `enum:",x86,arm" default:"" placeholder:"ARCH" help:"Filter by CPU architecture: x86 or arm"`
	Zone         string `placeholder:"ZONE" help:"Only machine types available in this zone"`
	GPUSupport   string `name:"gpu-support" enum:",true,false" default:"" placeholder:"BOOL" help:"Filter by GPU support: true or false"`
}

// Run lists the machine-type catalog.
func (c *MachineTypeListCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	mts, err := client.ListMachineTypes(ctx, cco.MachineTypeListOptions{
		Family:       c.Family,
		Category:     c.Category,
		Architecture: c.Architecture,
		Zone:         c.Zone,
		GPUSupport:   c.GPUSupport,
	})
	if err != nil {
		return fmt.Errorf("failed to list machine types: %w", err)
	}
	if cli.JSON {
		return wrapJSONErr(output.PrintJSON(mts))
	}
	return printMachineTypes(mts)
}

// MachineTypeGetCmd implements 'cfctl machine-type get'.
type MachineTypeGetCmd struct {
	ID string `arg:"" required:"" placeholder:"MACHINE_TYPE" help:"Machine type ID (e.g., n1-standard-8)"`
}

// Run shows one machine type.
func (c *MachineTypeGetCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	mt, err := client.GetMachineType(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("failed to get machine type: %w", err)
	}
	if cli.JSON {
		return wrapJSONErr(output.PrintJSON(mt))
	}
	printMachineType(mt)
	return nil
}

// MachineTypeDefaultCmd implements 'cfctl machine-type default'.
type MachineTypeDefaultCmd struct{}

// Run shows the default machine type.
func (c *MachineTypeDefaultCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	mt, err := client.GetDefaultMachineType(ctx)
	if err != nil {
		return fmt.Errorf("failed to get the default machine type: %w", err)
	}
	if cli.JSON {
		return wrapJSONErr(output.PrintJSON(mt))
	}
	printMachineType(mt)
	return nil
}

// MachineTypeFamiliesCmd implements 'cfctl machine-type families'.
type MachineTypeFamiliesCmd struct{}

// Run lists the machine families.
func (c *MachineTypeFamiliesCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	families, err := client.ListMachineFamilies(ctx)
	if err != nil {
		return fmt.Errorf("failed to list machine families: %w", err)
	}
	if cli.JSON {
		return wrapJSONErr(output.PrintJSON(families))
	}
	return printMachineFamilies(families)
}
