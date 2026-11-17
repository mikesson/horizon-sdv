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
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// AdminZoneCmd groups zone-catalog curation.
type AdminZoneCmd struct {
	List          AdminZoneListCmd          `cmd:"" aliases:"ls" help:"List every zone Compute Engine offers, enabled or not"`
	Enable        AdminZoneEnableCmd        `cmd:"" help:"Enable a zone for host creation"`
	Disable       AdminZoneDisableCmd       `cmd:"" help:"Disable a zone for host creation"`
	ClearCooldown AdminZoneClearCooldownCmd `cmd:"" help:"Lift a capacity cooldown steering placement away from a zone"`
}

// AdminZoneListCmd implements 'cfctl admin zone list'.
type AdminZoneListCmd struct{}

// Run lists every zone Compute Engine offers and how many of the fleet's
// curated shapes each one stocks.
func (c *AdminZoneListCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	list, err := client.AdminListZones(ctx)
	if err != nil {
		return fmt.Errorf("failed to list catalog zones: %w", err)
	}
	if cli.JSON {
		return printJSON(list)
	}
	curated := len(list.CuratedShapes)
	return table("ZONE\tREGION\tSTATUS\tENABLED\tSHAPES", func(w *tabwriter.Writer) {
		for _, z := range list.Zones {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				z.Zone, z.Region, z.Status, yesNo(z.IsEnabled), shapeCount(z, curated))
		}
	})
}

// shapeCount renders a zone's curated-shape availability, keeping "Compute
// Engine said nothing about this zone" distinct from "it offers none of them".
func shapeCount(z cco.AdminZone, curated int) string {
	if !z.ShapesReported {
		return "not reported"
	}
	return fmt.Sprintf("%d of %d", len(z.OfferedShapes), curated)
}

// setZoneEnablement enables or disables one zone. Enablement is stored as one
// set, so it reads the set and writes the whole set back.
func setZoneEnablement(ctx context.Context, cli *CLI, client *cco.Client, zone string, enabled bool) error {
	list, err := client.AdminListZones(ctx)
	if err != nil {
		return fmt.Errorf("failed to read the enabled zones: %w", err)
	}
	wanted := make([]string, 0, len(list.Zones)+1)
	for _, z := range list.Zones {
		if z.IsEnabled && z.Zone != zone {
			wanted = append(wanted, z.Zone)
		}
	}
	if enabled {
		wanted = append(wanted, zone)
	}

	stored, err := client.AdminSetEnabledZones(ctx, wanted)
	if err != nil {
		return fmt.Errorf("failed to update zone %s: %w", zone, err)
	}
	if cli.JSON {
		return printJSON(struct {
			Zones []string `json:"zones"`
		}{Zones: stored})
	}
	verb := "disabled"
	if enabled {
		verb = "enabled"
	}
	output.Success("Zone %s %s for host creation.", zone, verb)
	return nil
}

// AdminZoneEnableCmd implements 'cfctl admin zone enable'.
type AdminZoneEnableCmd struct {
	Zone string `arg:"" placeholder:"ZONE" help:"Zone name (e.g. us-central1-a)"`
}

// Run enables the zone.
func (c *AdminZoneEnableCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	return setZoneEnablement(ctx, cli, client, c.Zone, true)
}

// AdminZoneDisableCmd implements 'cfctl admin zone disable'.
type AdminZoneDisableCmd struct {
	Zone string `arg:"" placeholder:"ZONE" help:"Zone name (e.g. us-central1-a)"`
}

// Run disables the zone.
func (c *AdminZoneDisableCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	return setZoneEnablement(ctx, cli, client, c.Zone, false)
}

// AdminZoneClearCooldownCmd implements 'cfctl admin zone clear-cooldown'.
type AdminZoneClearCooldownCmd struct {
	Zone          string `arg:"" placeholder:"ZONE" help:"Zone the cooldown was recorded in (e.g. us-central1-a)"`
	MachineFamily string `arg:"" placeholder:"FAMILY" help:"Machine family the cooldown covers (e.g. n1)"`
}

// Run clears the cooldown. A quota cooldown is recorded in one zone but applies
// to the whole region, so clearing it reopens every zone in that region.
func (c *AdminZoneClearCooldownCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := client.AdminClearZoneCooldown(ctx, c.Zone, c.MachineFamily); err != nil {
		return fmt.Errorf("failed to clear the %s cooldown in %s: %w", c.MachineFamily, c.Zone, err)
	}
	if cli.JSON {
		return printActionResult("cooldown", c.Zone+"/"+c.MachineFamily, "clear")
	}
	output.Success("Cleared the %s cooldown in %s; its failure history is gone too.",
		c.MachineFamily, c.Zone)
	return nil
}

// AdminMachineTypeCmd groups machine-type curation.
type AdminMachineTypeCmd struct {
	Update AdminMachineTypeUpdateCmd `cmd:"" help:"Change a machine type's enablement or the catalog default"`
}

// AdminMachineTypeUpdateCmd implements 'cfctl admin machine-type update'.
// The PATCH is tri-state per field: an omitted flag leaves that field alone.
// Display order and GPU policy are set on the machine family.
type AdminMachineTypeUpdateCmd struct {
	ID      string `arg:"" placeholder:"MACHINE_TYPE" help:"Machine type ID (e.g. n1-standard-8)"`
	Enabled *bool  `negatable:"" help:"Offer (--enabled) or withhold (--no-enabled) the type for new hosts"`
	Default bool   `help:"Make this the default machine type (the previous default moves; a default is never cleared)"`
}

// Run curates the machine type.
func (c *AdminMachineTypeUpdateCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	req := cco.UpdateMachineTypeRequest{IsEnabled: c.Enabled}
	if c.Default {
		t := true
		req.IsDefault = &t
	}
	if req.IsEnabled == nil && req.IsDefault == nil {
		return errors.New("nothing to change: pass --enabled/--no-enabled or --default")
	}
	m, err := client.AdminUpdateMachineType(ctx, c.ID, req)
	if err != nil {
		return fmt.Errorf("failed to update machine type %s: %w", c.ID, err)
	}
	if cli.JSON {
		return printJSON(m)
	}
	output.Success("Updated %s: enabled=%s default=%s", m.ID, yesNo(m.IsEnabled), yesNo(m.IsDefault))
	return nil
}

// AdminCatalogFreshnessCmd implements 'cfctl admin catalog-freshness'.
type AdminCatalogFreshnessCmd struct{}

// Run reports how current the Compute Engine catalog snapshot is.
func (c *AdminCatalogFreshnessCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	f, err := client.AdminCatalogFreshness(ctx)
	if err != nil {
		return fmt.Errorf("failed to read catalog freshness: %w", err)
	}
	if cli.JSON {
		return printJSON(f)
	}
	if f.BuiltAt.IsZero() {
		fmt.Println("Catalog snapshot: not built yet.")
	} else {
		fmt.Printf("Catalog snapshot built: %s\n", f.BuiltAt.Format(time.RFC3339))
	}
	if len(f.Partial) == 0 {
		fmt.Println("Every catalog scope reported.")
		return nil
	}
	return table("SILENT SCOPE", func(w *tabwriter.Writer) {
		for _, scope := range f.Partial {
			fmt.Fprintf(w, "%s\n", scope)
		}
	})
}
