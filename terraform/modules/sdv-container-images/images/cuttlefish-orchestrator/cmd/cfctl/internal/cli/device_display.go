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

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// DeviceDisplayCmd groups the display subcommands.
type DeviceDisplayCmd struct {
	List   DeviceDisplayListCmd   `cmd:"" aliases:"ls" help:"List a device's displays and the available presets"`
	Add    DeviceDisplayAddCmd    `cmd:"" help:"Add a display to a running device"`
	Remove DeviceDisplayRemoveCmd `cmd:"" aliases:"rm" help:"Remove a display from a device"`
}

// DeviceDisplayListCmd implements 'cfctl device display list'.
type DeviceDisplayListCmd struct {
	ID           string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
	InstanceFlag `embed:""`
}

// Run lists the instance's displays.
func (c *DeviceDisplayListCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	instance, err := c.resolve(ctx, client, c.ID)
	if err != nil {
		return err
	}
	resp, err := client.ListDisplays(ctx, c.ID, instance)
	if err != nil {
		return fmt.Errorf("failed to list displays: %w", err)
	}
	if cli.JSON {
		return wrapJSONErr(output.PrintJSON(resp))
	}
	return printDisplays(resp)
}

// DeviceDisplayAddCmd implements 'cfctl device display add'.
type DeviceDisplayAddCmd struct {
	ID           string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
	Preset       string `help:"Preset name (see 'device display list'); instead of the dimension flags"`
	Width        int    `placeholder:"PX" help:"Display width in pixels"`
	Height       int    `placeholder:"PX" help:"Display height in pixels"`
	DPI          int    `placeholder:"DPI" help:"Dots per inch"`
	Refresh      int    `name:"refresh-rate" placeholder:"HZ" help:"Refresh rate in Hz"`
	InstanceFlag `embed:""`
}

// Run adds a display. The server lets a preset override the dimension fields,
// so the CLI rejects passing both.
func (c *DeviceDisplayAddCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	dimensioned := c.Width != 0 || c.Height != 0 || c.DPI != 0 || c.Refresh != 0
	if c.Preset != "" && dimensioned {
		return errors.New("--preset already defines the dimensions; drop it or drop the dimension flags")
	}
	if c.Preset == "" && (c.Width == 0 || c.Height == 0) {
		return errors.New("a display needs --width and --height, or a --preset from 'device display list'")
	}
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	instance, err := c.resolve(ctx, client, c.ID)
	if err != nil {
		return err
	}
	number, err := client.AddDisplay(ctx, c.ID, instance, cco.AddDisplayRequest{
		Width: c.Width, Height: c.Height, DPI: c.DPI, RefreshRateHz: c.Refresh, Preset: c.Preset,
	})
	if err != nil {
		return fmt.Errorf("failed to add display: %w", err)
	}
	if cli.JSON {
		return printJSON(map[string]int{"display_number": number})
	}
	output.Success("Added display %d", number)
	cli.Hint("Capture it with 'cfctl device screenshot %s --display %d'", c.ID, number)
	return nil
}

// DeviceDisplayRemoveCmd implements 'cfctl device display remove'.
type DeviceDisplayRemoveCmd struct {
	ID            string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
	DisplayNumber int    `arg:"" required:"" placeholder:"NUMBER" help:"Display number (from 'device display list')"`
	InstanceFlag  `embed:""`
}

// Run removes the display.
func (c *DeviceDisplayRemoveCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	instance, err := c.resolve(ctx, client, c.ID)
	if err != nil {
		return err
	}
	if err := client.RemoveDisplay(ctx, c.ID, instance, c.DisplayNumber); err != nil {
		return fmt.Errorf("failed to remove display: %w", err)
	}
	if cli.JSON {
		return printActionResult("display", fmt.Sprintf("%s/%d", c.ID, c.DisplayNumber), "remove")
	}
	output.Success("Removed display %d from %s", c.DisplayNumber, c.ID)
	return nil
}
