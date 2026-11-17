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
	"os"
	"os/exec"
	"strings"
	"time"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// HostListCmd implements 'cfctl host list'. It lists every host unless --mine
// is passed, so CI service accounts and scripts see the whole fleet.
type HostListCmd struct {
	Zone      string `default:"" help:"Filter by zone"`
	Status    string `default:"" enum:"pending,provisioning,starting,running,stopping,stopped,deleting,deleted,error,unreachable,preempted," help:"Filter by status: pending, provisioning, starting, running, stopping, stopped, deleting, deleted, error, unreachable, or preempted"`
	Mine      bool   `help:"Only hosts you created (default: everyone's)"`
	CreatedBy string `name:"created-by" placeholder:"EMAIL" help:"Filter by creator email"`
}

// Run executes the host list command.
func (c *HostListCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	hosts, err := client.ListHosts(ctx, cco.HostListOptions{
		Zone: c.Zone, Status: c.Status, CreatedBy: c.CreatedBy, Mine: c.Mine,
	})
	if err != nil {
		return fmt.Errorf("failed to list hosts: %w", err)
	}

	if cli.JSON {
		return printJSON(hosts)
	}
	return printHosts(hosts)
}

// HostGetCmd implements 'cfctl host get'.
type HostGetCmd struct {
	ID string `arg:"" required:"" placeholder:"HOST" help:"Host ID or name"`
}

// Run executes the host get command.
func (c *HostGetCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveHostArg(ctx, client, &c.ID); err != nil {
		return err
	}
	host, err := client.GetHost(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("failed to get host: %w", err)
	}

	if cli.JSON {
		return printJSON(host)
	}
	printHost(host)
	return nil
}

// HostCreateCmd implements 'cfctl host create'. Omitting --gpu-type and
// --gpu-count creates a CPU-only host.
type HostCreateCmd struct {
	Name             string `required:"" placeholder:"NAME" help:"Host name"`
	Zone             string `placeholder:"ZONE" help:"GCP zone. Naming one pins the host to it; omit to let the server rank placement"`
	MachineType      string `placeholder:"TYPE" help:"GCE machine type (default: the catalog's default machine type, set by the operator)"`
	GPUType          string `placeholder:"GPU" help:"GPU to attach on N1 machine types (e.g., nvidia-tesla-t4); omit for CPU-only"`
	GPUCount         int64  `help:"Number of GPUs to attach (0 = CPU-only)"`
	MinCPUPlatform   string `name:"min-cpu-platform" placeholder:"PLATFORM" help:"Minimum CPU platform (e.g. 'Intel Cascade Lake'), passed to GCE verbatim; omit for no constraint"`
	BootDiskSizeGB   int64  `name:"boot-disk-size-gb" placeholder:"GB" help:"Boot disk size in GB (0 = the server default)"`
	MaxDevices       int64  `default:"4" help:"Maximum CVD instances (a multi-instance device costs one slot each)"`
	TTLHours         int64  `help:"Host TTL in hours before automatic teardown (default 10)"`
	Exclusive        bool   `help:"Fence the host from automatic placement (only creates targeting it by host_id land here)"`
	IdempotencyFlags `embed:""`
	WaitFlags        `embed:""`
}

// Run executes the host create command.
func (c *HostCreateCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	// The server reads the pair as one setting, and either flag alone silently
	// means CPU-only.
	if (c.GPUType != "") != (c.GPUCount > 0) {
		return errors.New("--gpu-type and --gpu-count must be set together (both for a GPU host, neither for CPU-only)")
	}
	if c.Zone != "" {
		output.Info("Creating host %s in %s...", c.Name, c.Zone)
	} else {
		output.Info("Creating host %s (server-ranked zone)...", c.Name)
	}

	host, err := client.CreateHost(ctx, cco.CreateHostRequest{
		Name:           c.Name,
		Zone:           c.Zone,
		MachineType:    c.MachineType,
		GPUType:        c.GPUType,
		GPUCount:       c.GPUCount,
		MinCPUPlatform: c.MinCPUPlatform,
		BootDiskSizeGB: c.BootDiskSizeGB,
		MaxDevices:     c.MaxDevices,
		TTLHours:       c.TTLHours,
		Exclusive:      c.Exclusive,
	}, c.IdempotencyKey)
	if err != nil {
		return fmt.Errorf("failed to create host: %w", err)
	}

	if c.Wait {
		output.Info("Waiting for host %s to reach running...", host.ID)
		if host, err = client.WaitHost(ctx, host.ID, cco.StatusRunning); err != nil {
			return fmt.Errorf("host %s: %w", c.Name, err)
		}
	}

	if cli.JSON {
		return printJSON(host)
	}
	if c.Wait {
		output.Success("Host running: %s", host.ID)
	} else {
		output.Success("Host created: %s", host.ID)
	}
	return nil
}

// HostDeleteCmd implements 'cfctl host delete'.
type HostDeleteCmd struct {
	ID        string `arg:"" required:"" placeholder:"HOST" help:"Host ID or name"`
	Force     bool   `short:"f" help:"Skip confirmation prompt"`
	WaitFlags `embed:""`
}

// Run executes the host delete command.
func (c *HostDeleteCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveHostArg(ctx, client, &c.ID); err != nil {
		return err
	}
	return deleteFlow{
		resource: kindHost, id: c.ID, force: c.Force, wait: c.Wait,
		detail:   creatorDetail(client, c.ID, client.GetHost, func(h *cco.Host) string { return h.CreatedBy }),
		del:      client.DeleteHost,
		waitGone: waitDeleted(kindHost, client.WaitHost),
	}.run(ctx, cli)
}

// HostStartCmd implements 'cfctl host start'.
type HostStartCmd struct {
	ID        string `arg:"" required:"" placeholder:"HOST" help:"Host ID or name"`
	WaitFlags `embed:""`
}

// Run executes the host start command.
func (c *HostStartCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveHostArg(ctx, client, &c.ID); err != nil {
		return err
	}
	f := actionFlow{
		resource: kindHost, id: c.ID, action: "start", gerund: "Starting",
		target: cco.StatusRunning, act: client.StartHost,
	}
	if c.Wait {
		f.wait = func(ctx context.Context, id, target string) (any, error) {
			return client.WaitHost(ctx, id, target)
		}
	}
	return f.run(ctx, cli)
}

// HostStopCmd implements 'cfctl host stop'.
type HostStopCmd struct {
	ID        string `arg:"" required:"" placeholder:"HOST" help:"Host ID or name"`
	WaitFlags `embed:""`
}

// Run executes the host stop command.
func (c *HostStopCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveHostArg(ctx, client, &c.ID); err != nil {
		return err
	}
	f := actionFlow{
		resource: kindHost, id: c.ID, action: "stop", gerund: "Stopping",
		target: cco.StatusStopped, act: client.StopHost,
	}
	if c.Wait {
		f.wait = func(ctx context.Context, id, target string) (any, error) {
			return client.WaitHost(ctx, id, target)
		}
	}
	return f.run(ctx, cli)
}

// HostSSHCmd implements 'cfctl host ssh'.
type HostSSHCmd struct {
	ID      string   `arg:"" required:"" placeholder:"HOST" help:"Host ID or name"`
	Tunnel  bool     `negatable:"" default:"true" help:"Tunnel SSH through IAP TCP forwarding, the only route that works from outside the VPC, since hosts never expose port 22 to the internet. --no-tunnel restores gcloud's own dial for VPC/VPN callers"`
	Command []string `arg:"" optional:"" passthrough:"" placeholder:"CMD" help:"Run this command on the host instead of an interactive shell (write it after --). Each argument is quoted, so use sh -c 'CMD' for pipes or globs"`
}

// buildSSHArgs assembles the gcloud compute ssh arguments for a host. Port 22
// admits only the VPC and Google's IAP range, so the IAP tunnel is the default.
// --no-tunnel restores gcloud's own dial for callers inside the VPC.
func buildSSHArgs(host *cco.Host, tunnel bool, command []string) []string {
	// The VM name is the generated gce_instance_name. The deployment project
	// may differ from the local gcloud default.
	instance := host.GCEInstanceName
	if instance == "" {
		instance = host.Name
	}
	args := []string{"compute", "ssh", instance, "--zone", host.Zone}
	if tunnel {
		args = append(args, "--tunnel-through-iap")
	}
	if host.GCEProject != "" {
		args = append(args, "--project", host.GCEProject)
	}
	if len(command) > 0 {
		quoted := make([]string, len(command))
		for i, a := range command {
			quoted[i] = shellQuote(a)
		}
		args = append(args, "--command", strings.Join(quoted, " "))
	}
	return args
}

// shellQuote single-quotes s for a POSIX shell. The remote sshd hands the
// --command string to the login shell, which would otherwise re-split it.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Run executes the host ssh command.
func (c *HostSSHCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveHostArg(ctx, client, &c.ID); err != nil {
		return err
	}
	host, err := client.GetHost(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("failed to get host: %w", err)
	}

	args := buildSSHArgs(host, c.Tunnel, passthroughCommand(c.Command))

	output.Info("Connecting to %s (%s)...", args[2], host.Zone)

	cmd := exec.CommandContext(ctx, "gcloud", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gcloud compute ssh: %w", err)
	}
	return nil
}

// HostRetryCmd implements 'cfctl host retry'.
type HostRetryCmd struct {
	ID                 string `arg:"" required:"" placeholder:"HOST" help:"Host ID or name"`
	FallbackToStandard bool   `help:"Fall back to standard provisioning if Spot capacity is unavailable"`
}

// Run retries the host's provisioning.
func (c *HostRetryCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveHostArg(ctx, client, &c.ID); err != nil {
		return err
	}
	host, err := client.RetryHost(ctx, c.ID, c.FallbackToStandard)
	if err != nil {
		return fmt.Errorf("failed to retry host: %w", err)
	}
	if cli.JSON {
		return wrapJSONErr(output.PrintJSON(host))
	}
	output.Success("Retry accepted for %s (status: %s)", host.ID, host.Status)
	cli.Hint("Watch it come up with 'cfctl host get %s'", host.ID)
	return nil
}

// HostSyncCmd implements 'cfctl host sync'.
type HostSyncCmd struct {
	ID string `arg:"" required:"" placeholder:"HOST" help:"Host ID or name"`
}

// Run asks the server to reconcile the host now.
func (c *HostSyncCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveHostArg(ctx, client, &c.ID); err != nil {
		return err
	}
	if err := client.SyncHost(ctx, c.ID); err != nil {
		return fmt.Errorf("failed to sync host: %w", err)
	}
	output.Success("Sync requested for %s", c.ID)
	cli.Hint("See the reconciled state with 'cfctl host get %s'", c.ID)
	return nil
}

// HostUpdateCmd implements 'cfctl host update'.
type HostUpdateCmd struct {
	ID         string `arg:"" required:"" placeholder:"HOST" help:"Host ID or name"`
	MaxDevices int64  `required:"" placeholder:"N" help:"Maximum CVD instances the host accepts (1-10); never below the instances already on it"`
}

// Run reshapes the host's device capacity.
func (c *HostUpdateCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveHostArg(ctx, client, &c.ID); err != nil {
		return err
	}
	host, err := client.UpdateHost(ctx, c.ID, c.MaxDevices)
	if err != nil {
		return fmt.Errorf("failed to update host: %w", err)
	}
	if cli.JSON {
		return wrapJSONErr(output.PrintJSON(host))
	}
	output.Success("Updated %s: max devices %d", host.ID, host.MaxDevices)
	return nil
}

// HostExtendCmd implements 'cfctl host extend'.
type HostExtendCmd struct {
	ID    string `arg:"" required:"" placeholder:"HOST" help:"Host ID or name"`
	Hours int64  `required:"" placeholder:"N" help:"Hours to add to the host's lease (1-168 per request), counted from the later of now and its current expiry"`
}

// Run raises the host's lease.
func (c *HostExtendCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if c.Hours < 1 {
		return fmt.Errorf("--hours must be at least 1 (got %d)", c.Hours)
	}
	if err := resolveHostArg(ctx, client, &c.ID); err != nil {
		return err
	}
	host, err := client.ExtendHost(ctx, c.ID, c.Hours)
	if err != nil {
		return fmt.Errorf("failed to extend host lease: %w", err)
	}
	if cli.JSON {
		return printJSON(host)
	}
	if !host.Lifecycle.ExpiresAt.IsZero() {
		output.Success("Lease extended: %s now expires at %s", host.ID, host.Lifecycle.ExpiresAt.Format(time.RFC3339))
	} else {
		output.Success("Lease extended for %s", host.ID)
	}
	return nil
}

// Help explains which hosts take an extend.
func (c *HostExtendCmd) Help() string {
	return `Only a reserved host takes an extend: one created with 'cfctl host create'.
Fleet, dedicated and batch hosts have leases the platform manages, and a host
protected by pinned devices has no expiry to extend. Extend or pin the device
instead ('cfctl device extend', 'cfctl device pin'). A lease that would pass
the time Compute Engine stops the VM is refused; restarting the host resets
that time.

Exit codes: 4 when the host does not exist, 5 when the host refuses the
extend, 1 when the server rejects --hours.

Example:

  cfctl host extend gpu-1 --hours 24`
}

// HostCVDsCmd implements 'cfctl host cvds'.
type HostCVDsCmd struct {
	ID string `arg:"" required:"" placeholder:"HOST" help:"Host ID or name"`
}

// Run lists the CVDs on the host.
func (c *HostCVDsCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveHostArg(ctx, client, &c.ID); err != nil {
		return err
	}
	cvds, err := client.ListHostCVDs(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("failed to list CVDs on host: %w", err)
	}
	if cli.JSON {
		return printJSON(cvds)
	}
	return printHostCVDs(cvds)
}

// Help explains what host cvds shows beyond 'device list'.
func (c *HostCVDsCmd) Help() string {
	return `Lists every CVD on the host, including ones created by cvdr through the
legacy /v1 API, which CCO does not track as devices. DEVICE is the CCO
device that owns the CVD, or '-' for a CVD CCO did not create.

The server queries the host's Host Orchestrator live, so the host must be
running and each call costs a 'cvd fleet' run on it.`
}

// Help adds to host create's help where the legal values come from.
func (c *HostCreateCmd) Help() string {
	return `Most creates should be declarative device creates, because the platform
provisions hosts as needed. Create a host directly when you want a specific
shape or a fenced (--exclusive) machine.

The server knows the legal values: 'cfctl machine-type list' for
--machine-type (and its GPU shape; omit the flag for the catalog default), 'cfctl zone list' for --zone (omit the
flag to let the server rank placement), 'cfctl quota show' for headroom.

Example:

  cfctl host create --name gpu-1 --machine-type g2-standard-8 --wait`
}
