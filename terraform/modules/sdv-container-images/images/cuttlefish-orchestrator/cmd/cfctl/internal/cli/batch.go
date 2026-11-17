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

// BatchCmd contains batch management subcommands.
type BatchCmd struct {
	Create  BatchCreateCmd  `cmd:"" aliases:"new" help:"Create a batch of devices"`
	List    BatchListCmd    `cmd:"" aliases:"ls" help:"List batches"`
	Get     BatchGetCmd     `cmd:"" aliases:"show,info" help:"Get a batch's status"`
	Devices BatchDevicesCmd `cmd:"" help:"List a batch's devices with connection info"`
	Delete  BatchDeleteCmd  `cmd:"" aliases:"rm,remove" help:"Delete a batch"`
}

// BatchCreateCmd creates (or plans) a batch of devices.
type BatchCreateCmd struct {
	Prefix       string `required:"" placeholder:"PREFIX" help:"Name prefix; members are <prefix>-000, <prefix>-001, ..."`
	Count        int    `required:"" help:"Number of devices (1-100)"`
	MinReady     int    `name:"min-ready" help:"Reject unless at least this many can be admitted (default: count)"`
	Interaction  string `default:"headless" enum:"interactive,headless" help:"How the devices will be used: interactive (prefers GPU hosts) or headless"`
	GPURequired  bool   `name:"gpu-required" help:"Fail instead of degrading to software rendering when no GPU capacity exists"`
	Region       string `placeholder:"REGION" help:"Prefer placement in this GCE region; a preference, never a constraint"`
	Capacity     string `enum:",auto,spot,standard" default:"" placeholder:"MODEL" help:"Provisioning economics for the batch's hosts: spot (discounted, preemptible), standard (on-demand), or auto (operator policy decides)"`
	Pool         string `placeholder:"POOL" help:"Fleet pool every device's host must belong to (default, gpu, arm); unset lets the requirements decide"`
	Architecture string `enum:",x86,arm" default:"" placeholder:"ARCH" help:"Guest CPU architecture: x86 (the default) or arm (needs an arm64 bare-metal host)"`

	BuildID     string `name:"build-id" placeholder:"BUILD_ID" help:"Use a registered build ID"`
	TrackBranch string `name:"track-branch" placeholder:"BRANCH" help:"Track the latest build on this AOSP branch (with --track-target)"`
	TrackTarget string `name:"track-target" placeholder:"TARGET" help:"Build target for --track-branch"`

	FillPolicy       string `name:"fill-policy" default:"best_effort" enum:"best_effort,atomic" help:"best_effort keeps whatever admits; atomic fails and tears down the whole batch if a member permanently fails or --ready-timeout lapses"`
	Placement        string `default:"isolated" enum:"isolated,pack" help:"isolated provisions dedicated exclusive hosts; pack fills existing shareable capacity first"`
	ReadyTimeout     int    `name:"ready-timeout" placeholder:"SECONDS" help:"atomic only: fail the batch unless fully running within this many seconds"`
	TTLHours         int    `name:"ttl-hours" help:"Per-member TTL in hours"`
	IdempotencyFlags `embed:""`

	DryRun bool `name:"dry-run" help:"Validate and plan without creating anything"`
	Wait   bool `help:"Block until the batch settles, then print the devices (combine with --timeout to bound)"`
}

// Run creates or plans the batch.
func (c *BatchCreateCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	reqs, err := buildRequirements(requirementFlags{
		interaction: c.Interaction, capacity: c.Capacity, gpuRequired: c.GPURequired,
		pool: c.Pool, architecture: c.Architecture,
		region: c.Region, buildID: c.BuildID, trackBranch: c.TrackBranch, trackTarget: c.TrackTarget,
	})
	if err != nil {
		return err
	}
	req := cco.CreateBatchRequest{
		NamePrefix:   c.Prefix,
		Count:        c.Count,
		MinReady:     c.MinReady,
		FillPolicy:   c.FillPolicy,
		Placement:    c.Placement,
		ReadyTimeout: c.ReadyTimeout,
		TTLHours:     c.TTLHours,
		ValidateOnly: c.DryRun,
		Requirements: *reqs,
	}

	if c.DryRun {
		output.Info("Planning batch %s (%d devices, dry run)...", c.Prefix, c.Count)
	} else {
		output.Info("Creating batch %s (%d devices)...", c.Prefix, c.Count)
	}

	summary, err := client.CreateBatch(ctx, req, c.IdempotencyKey)
	if err != nil {
		return fmt.Errorf("failed to create batch: %w", err)
	}

	if c.DryRun {
		if cli.JSON {
			return printJSON(summary)
		}
		printBatch(summary)
		printResolution(summary.Resolution)
		output.Success("Plan valid. Nothing was created (%d of %d admittable, %d new hosts)",
			summary.Admitted, c.Count, summary.NewHosts)
		return nil
	}

	if !cli.JSON {
		printBatch(summary)
	}

	if c.Wait {
		return c.runWait(ctx, cli, client, summary.BatchID)
	}

	if cli.JSON {
		return printJSON(summary)
	}
	output.Success("Batch created: %s (%d devices)", summary.BatchID, summary.Total)
	return nil
}

// runWait blocks until the batch settles, then prints its devices.
func (c *BatchCreateCmd) runWait(ctx context.Context, cli *CLI, client *cco.Client, batchID string) error {
	output.Info("Waiting for batch %s to settle...", batchID)
	settled, err := client.WaitBatch(ctx, batchID)
	if err != nil {
		return fmt.Errorf("batch %s: %w", batchID, err)
	}
	members, err := client.ListBatchDevices(ctx, batchID)
	if err != nil {
		return fmt.Errorf("listing batch devices: %w", err)
	}
	if cli.JSON {
		return printJSON(members)
	}
	if err := printBatchMembers(members); err != nil {
		return err
	}
	switch settled.Status {
	case cco.BatchStatusReady:
		output.Success("Batch ready: %d/%d running", settled.Running, settled.Total)
	case cco.BatchStatusFailed:
		// The atomic policy tore the batch down, so CI must get a non-zero exit.
		return fmt.Errorf("batch %s: %w", batchID, &cco.WaitFailedError{
			State: cco.BatchStatusFailed, Target: cco.BatchStatusReady, Detail: settled.FailureReason,
		})
	default:
		output.Warn("Batch %s: %d/%d running", settled.Status, settled.Running, settled.Total)
	}
	return nil
}

// BatchListCmd lists the active batches. It lists every caller's batches
// unless --mine is passed, so CI service accounts see the whole fleet.
type BatchListCmd struct {
	Mine      bool   `help:"Only batches you created (default: everyone's)"`
	CreatedBy string `name:"created-by" placeholder:"EMAIL" help:"Filter by creator email"`
}

// Run lists batches.
func (c *BatchListCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	batches, err := client.ListBatches(ctx, cco.BatchListOptions{CreatedBy: c.CreatedBy, Mine: c.Mine})
	if err != nil {
		return fmt.Errorf("failed to list batches: %w", err)
	}
	if cli.JSON {
		return printJSON(batches)
	}
	return printBatches(batches)
}

// BatchGetCmd shows a batch's status.
type BatchGetCmd struct {
	ID string `arg:"" required:"" placeholder:"BATCH_ID" help:"Batch ID"`
}

// Run gets a batch.
func (c *BatchGetCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	b, err := client.GetBatch(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("failed to get batch: %w", err)
	}
	if cli.JSON {
		return printJSON(b)
	}
	printBatch(b)
	return nil
}

// BatchDevicesCmd lists a batch's devices with connection info.
type BatchDevicesCmd struct {
	ID string `arg:"" required:"" placeholder:"BATCH_ID" help:"Batch ID"`
}

// Run lists the batch's devices.
func (c *BatchDevicesCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	members, err := client.ListBatchDevices(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("failed to list batch devices: %w", err)
	}
	if cli.JSON {
		return printJSON(members)
	}
	return printBatchMembers(members)
}

// BatchDeleteCmd deletes a batch and tears down every member device. It uses
// the same confirm, --force, --wait and --json flow as the other deletes.
type BatchDeleteCmd struct {
	ID        string `arg:"" required:"" placeholder:"BATCH_ID" help:"Batch ID"`
	Force     bool   `short:"f" help:"Skip confirmation prompt"`
	WaitFlags `embed:""`
}

// Run deletes the batch.
func (c *BatchDeleteCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	return deleteFlow{
		resource: "batch", id: c.ID, force: c.Force, wait: c.Wait,
		detail: func(ctx context.Context) string {
			b, err := client.GetBatch(ctx, c.ID)
			if err != nil {
				return ""
			}
			detail := foreignCreatorDetail(ctx, client, b.CreatedBy)
			if b.Total > 0 {
				detail += fmt.Sprintf(" and its %d devices", b.Total)
			}
			return detail
		},
		del: client.DeleteBatch,
		waitGone: func(ctx context.Context, id string) error {
			if err := client.WaitBatchDeleted(ctx, id); err != nil {
				return fmt.Errorf("batch %s: %w", id, err)
			}
			return nil
		},
	}.run(ctx, cli)
}

// Help augments batch create's help with the CI patterns it exists for.
func (c *BatchCreateCmd) Help() string {
	return `Creates count devices that settle together and are reclaimed together,
which is the CI shape. Every create is already replay-safe within its own run;
--idempotency-key extends that across runs, so a rerun with the same key
resumes the same batch instead of doubling the fleet.
--fill-policy atomic with --ready-timeout fails the whole batch unless
every member is running in time.

Examples:

  cfctl batch create --prefix nightly --count 8 --idempotency-key "$CI_JOB_ID" --wait
  cfctl batch create --prefix soak --count 4 --fill-policy atomic --ready-timeout 600

'cfctl batch devices ID' prints the members with their ADB serials, the
payload a CI job consumes.`
}
