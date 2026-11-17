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
	"time"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// AdminTasksCmd groups task-queue operations.
type AdminTasksCmd struct {
	List   AdminTasksListCmd   `cmd:"" help:"List dead-lettered tasks"`
	Retry  AdminTasksRetryCmd  `cmd:"" help:"Retry a dead-lettered task"`
	Delete AdminTasksDeleteCmd `cmd:"" help:"Delete a task from the queue"`
	Stats  AdminTasksStatsCmd  `cmd:"" help:"Show task-queue depth"`
}

// maxFailedTasksLimit mirrors the server's cap on the failed-tasks page
// size; a drift test pins the two together.
const maxFailedTasksLimit = 500

// AdminTasksListCmd implements 'cfctl admin tasks list'.
type AdminTasksListCmd struct {
	Limit int `default:"100" help:"Maximum tasks to list (1-500)"`
}

// Run lists dead-lettered tasks.
func (c *AdminTasksListCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if c.Limit < 1 || c.Limit > maxFailedTasksLimit {
		return fmt.Errorf("--limit must be between 1 and %d (the server's page cap)", maxFailedTasksLimit)
	}
	resp, err := client.AdminListFailedTasks(ctx, c.Limit)
	if err != nil {
		return fmt.Errorf("failed to list dead-lettered tasks: %w", err)
	}
	if cli.JSON {
		return printJSON(resp)
	}
	return printFailedTasks(resp.Tasks)
}

// AdminTasksRetryCmd implements 'cfctl admin tasks retry <id>'.
type AdminTasksRetryCmd struct {
	ID string `arg:"" help:"Task ID to retry"`
}

// Run requeues a dead-lettered task.
func (c *AdminTasksRetryCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := client.AdminRetryTask(ctx, c.ID); err != nil {
		return fmt.Errorf("failed to retry task: %w", err)
	}
	if cli.JSON {
		return printActionResult("task", c.ID, "retry")
	}
	output.Success("Task %s requeued.", c.ID)
	return nil
}

// AdminTasksDeleteCmd implements 'cfctl admin tasks delete <id>'.
type AdminTasksDeleteCmd struct {
	ID string `arg:"" help:"Task ID to delete"`
}

// Run deletes a task from the queue.
func (c *AdminTasksDeleteCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := client.AdminDeleteTask(ctx, c.ID); err != nil {
		return fmt.Errorf("failed to delete task: %w", err)
	}
	if cli.JSON {
		return printActionResult("task", c.ID, "delete")
	}
	output.Success("Task %s deleted.", c.ID)
	return nil
}

// AdminStatsCmd implements 'cfctl admin stats'.
type AdminStatsCmd struct{}

// Run prints the platform-statistics snapshot.
func (c *AdminStatsCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	s, err := client.AdminStats(ctx)
	if err != nil {
		return fmt.Errorf("failed to read platform statistics: %w", err)
	}
	if cli.JSON {
		return printJSON(s)
	}
	printAdminStats(s)
	return nil
}

// AdminProvisioningCmd implements 'cfctl admin provisioning'.
type AdminProvisioningCmd struct{}

// Run prints recent provisioning outcome counts.
func (c *AdminProvisioningCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	s, err := client.AdminProvisioningStats(ctx)
	if err != nil {
		return fmt.Errorf("failed to read provisioning statistics: %w", err)
	}
	if cli.JSON {
		return printJSON(s)
	}
	fmt.Printf("Last %d days:\n", s.WindowDays)
	fmt.Printf("Devices:  %d created, %d now in error\n", s.Devices.Created, s.Devices.Errored)
	fmt.Printf("Hosts:    %d created, %d now in error\n", s.Hosts.Created, s.Hosts.Errored)
	return nil
}

// AdminReclaimableCmd implements 'cfctl admin reclaimable'.
type AdminReclaimableCmd struct{}

// Run prints the reclaimable-capacity worklist.
func (c *AdminReclaimableCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	r, err := client.AdminReclaimable(ctx)
	if err != nil {
		return fmt.Errorf("failed to read reclaimable capacity: %w", err)
	}
	if cli.JSON {
		return printJSON(r)
	}
	return printReclaimable(r)
}

// AdminTasksStatsCmd implements 'cfctl admin tasks stats'.
type AdminTasksStatsCmd struct{}

// Run prints the task-queue depth snapshot.
func (c *AdminTasksStatsCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	s, err := client.AdminTaskStats(ctx)
	if err != nil {
		return fmt.Errorf("failed to read task-queue statistics: %w", err)
	}
	if cli.JSON {
		return printJSON(s)
	}
	fmt.Printf("Pending:  %d (oldest %ds)\n", s.Pending, s.OldestPendingAgeSeconds)
	fmt.Printf("Leased:   %d\n", s.Leased)
	fmt.Printf("Failed:   %d\n", s.Failed)
	if s.Failed > 0 {
		cli.Hint("Inspect dead-lettered tasks with 'cfctl admin tasks list'")
	}
	return nil
}

// maxAuditLimit mirrors the server's cap on the audit page size; a drift
// test pins the two together.
const maxAuditLimit = 200

// AdminAuditCmd implements 'cfctl admin audit'.
type AdminAuditCmd struct {
	Actor        string `help:"Only entries by this actor email"`
	Action       string `help:"Only entries with this action (e.g. device.create)"`
	ResourceType string `enum:"host,device,quota,system,task," default:"" help:"Only entries for this resource type (host, device, quota, system, task)"`
	ResourceID   string `name:"resource-id" help:"Only entries for this resource"`
	Kind         string `enum:"system_config,host_image_config,machine_type,zone," default:"" help:"Only entries in this category (system_config = settings changes, host_image_config, machine_type, zone)"`
	Since        string `placeholder:"TIME" help:"Only entries at or after this time (RFC 3339)"`
	Until        string `placeholder:"TIME" help:"Only entries at or before this time (RFC 3339)"`
	Limit        int    `default:"50" help:"Maximum entries to return (1-200)"`
	Offset       int    `help:"Entries to skip"`
}

// Run queries the audit log, newest first.
func (c *AdminAuditCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if c.Limit < 1 || c.Limit > maxAuditLimit {
		return fmt.Errorf("--limit must be between 1 and %d (the server's page cap)", maxAuditLimit)
	}
	for flag, v := range map[string]string{"--since": c.Since, "--until": c.Until} {
		if v == "" {
			continue
		}
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			return fmt.Errorf("%s takes an RFC 3339 time (e.g. 2026-07-31T09:00:00Z), got %q", flag, v)
		}
	}
	resp, err := client.AdminListAudit(ctx, cco.AuditFilter{
		ActorEmail: c.Actor, Action: c.Action,
		ResourceType: c.ResourceType, ResourceID: c.ResourceID,
		Kind:      c.Kind,
		StartTime: c.Since, EndTime: c.Until,
		Limit: c.Limit, Offset: c.Offset,
	})
	if err != nil {
		return fmt.Errorf("failed to query the audit log: %w", err)
	}
	if cli.JSON {
		return printJSON(resp)
	}
	return printAuditEntries(resp)
}

// AdminCapacityCmd implements 'cfctl admin capacity'.
type AdminCapacityCmd struct{}

// Run prints the capacity snapshot.
func (c *AdminCapacityCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	snapshot, err := client.AdminCapacity(ctx)
	if err != nil {
		return fmt.Errorf("failed to read the capacity snapshot: %w", err)
	}
	if cli.JSON {
		return printJSON(snapshot)
	}
	return printCapacity(snapshot)
}

// AdminControlPlaneCmd implements 'cfctl admin control-plane'.
type AdminControlPlaneCmd struct{}

// Run prints the control-plane snapshot.
func (c *AdminControlPlaneCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	cp, err := client.AdminControlPlane(ctx)
	if err != nil {
		return fmt.Errorf("failed to read the control-plane snapshot: %w", err)
	}
	if cli.JSON {
		return printJSON(cp)
	}
	return printControlPlane(cp)
}
