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

// QuotaCmd groups the quota subcommands.
type QuotaCmd struct {
	Show QuotaShowCmd `cmd:"" help:"Show host-capacity quota and current usage"`
	Set  QuotaSetCmd  `cmd:"" help:"Set the platform host quota"`
}

// QuotaShowCmd implements 'cfctl quota show'.
type QuotaShowCmd struct{}

// Run shows the platform quota.
func (c *QuotaShowCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	q, err := client.GetQuota(ctx)
	if err != nil {
		return fmt.Errorf("failed to get quota: %w", err)
	}
	if cli.JSON {
		return wrapJSONErr(output.PrintJSON(q))
	}
	printQuota(q)
	return nil
}

// QuotaSetCmd implements 'cfctl quota set'.
type QuotaSetCmd struct {
	MaxHosts int64 `arg:"" placeholder:"MAX_HOSTS" help:"Maximum hosts the platform may run"`
}

// Run sets the platform host quota.
func (c *QuotaSetCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if c.MaxHosts < 0 {
		return fmt.Errorf("MAX_HOSTS must be at least 0, got %d", c.MaxHosts)
	}
	q, err := client.UpdatePlatformQuota(ctx, c.MaxHosts)
	if err != nil {
		return fmt.Errorf("failed to set the platform quota: %w", err)
	}
	if cli.JSON {
		return printJSON(q)
	}
	output.Success("Platform quota set to %d hosts.", q.MaxHosts)
	printQuota(q)
	return nil
}
