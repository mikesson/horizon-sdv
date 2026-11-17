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
	"encoding/json"
	"fmt"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// Snapshots live on the device's host VM and are lost with its disk. The list
// shows such snapshots as `lost`. Restore works only onto the same stopped
// device.

// DeviceSnapshotTakeCmd implements 'cfctl device snapshot take'.
type DeviceSnapshotTakeCmd struct {
	ID   string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
	Wait bool   `help:"Block until the capture completes and print the snapshot id"`
}

// Run executes the snapshot take command. With --wait it prints the snapshot
// id from the settled operation's response.
func (c *DeviceSnapshotTakeCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	op, err := client.TakeDeviceSnapshot(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("failed to snapshot device: %w", err)
	}
	if !cli.JSON {
		output.Info("Snapshotting device %s (operation %s)...", c.ID, op.ID())
	}
	if !c.Wait {
		if cli.JSON {
			return wrapJSONErr(output.PrintJSON(op))
		}
		output.Success("Device snapshot accepted: %s. Poll it with 'cfctl operation get %s'",
			c.ID, op.ID())
		return nil
	}

	output.Info("Waiting for operation %s to complete...", op.ID())
	settled, err := client.WaitOperation(ctx, op.ID())
	if err != nil {
		return fmt.Errorf("device snapshot: %w", err)
	}
	if cli.JSON {
		return wrapJSONErr(output.PrintJSON(settled))
	}
	var resp struct {
		SnapshotID string `json:"snapshot_id"`
	}
	if err := json.Unmarshal(settled.Response, &resp); err == nil && resp.SnapshotID != "" {
		output.Success("Device snapshotted: %s (snapshot %s)", c.ID, resp.SnapshotID)
		return nil
	}
	output.Success("Device snapshotted: %s", c.ID)
	return nil
}

// DeviceSnapshotListCmd implements 'cfctl device snapshot list'.
type DeviceSnapshotListCmd struct {
	ID string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
}

// Run executes the snapshot list command.
func (c *DeviceSnapshotListCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	snaps, err := client.ListDeviceSnapshots(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("failed to list snapshots: %w", err)
	}
	if cli.JSON {
		return wrapJSONErr(output.PrintJSON(snaps))
	}
	return printSnapshots(snaps)
}

// DeviceSnapshotDeleteCmd implements 'cfctl device snapshot delete'.
type DeviceSnapshotDeleteCmd struct {
	ID         string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
	SnapshotID string `arg:"" required:"" placeholder:"SNAPSHOT_ID" help:"Snapshot ID"`
	Force      bool   `short:"f" help:"Skip confirmation prompt"`
	WaitFlags  `embed:""`
}

// Run executes the snapshot delete command.
func (c *DeviceSnapshotDeleteCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	if !c.Force {
		proceed, err := confirmOrCancel(cli, kindDevice, c.ID, "snapshot-delete",
			"Delete snapshot %s of device %s? It cannot be recovered.", c.SnapshotID, c.ID)
		if err != nil || !proceed {
			return err
		}
	}
	return runVerbOperation(ctx, cli, verbFlow{
		id: c.ID, action: "snapshot-delete", gerund: "Deleting a snapshot on", past: "snapshot deleted",
		wait: c.Wait,
		act: func(ctx context.Context, id string) (*cco.Operation, error) {
			return client.DeleteDeviceSnapshot(ctx, id, c.SnapshotID)
		},
		settle: client.WaitOperation,
	})
}

// DeviceSnapshotRestoreCmd implements 'cfctl device snapshot restore'.
type DeviceSnapshotRestoreCmd struct {
	ID         string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name (must be stopped)"`
	SnapshotID string `arg:"" required:"" placeholder:"SNAPSHOT_ID" help:"Snapshot to restore from"`
	Force      bool   `short:"f" help:"Skip confirmation prompt"`
	WaitFlags  `embed:""`
}

// Run executes the snapshot restore command.
func (c *DeviceSnapshotRestoreCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	if !c.Force {
		proceed, err := confirmOrCancel(cli, kindDevice, c.ID, "restore",
			"Restore device %s from snapshot %s? Its current state is replaced.", c.ID, c.SnapshotID)
		if err != nil || !proceed {
			return err
		}
	}
	return runVerbOperation(ctx, cli, verbFlow{
		id: c.ID, action: "restore", gerund: "Restoring", past: "restored",
		wait: c.Wait,
		act: func(ctx context.Context, id string) (*cco.Operation, error) {
			return client.RestoreDeviceSnapshot(ctx, id, c.SnapshotID)
		},
		settle: client.WaitOperation,
	})
}
