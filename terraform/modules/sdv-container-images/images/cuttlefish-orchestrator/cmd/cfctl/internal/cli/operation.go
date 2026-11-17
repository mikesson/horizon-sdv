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
)

// OperationCmd groups the long-running-operation subcommands. The async device
// verbs point here when run without --wait.
type OperationCmd struct {
	Get  OperationGetCmd  `cmd:"" aliases:"show,info" help:"Get a long-running operation's status"`
	List OperationListCmd `cmd:"" aliases:"ls" help:"List long-running operations, newest first"`
}

// OperationGetCmd implements 'cfctl operation get'.
type OperationGetCmd struct {
	ID string `arg:"" required:"" placeholder:"OPERATION_ID" help:"Operation ID"`
}

// Run executes the operation get command.
func (c *OperationGetCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	op, err := client.GetOperation(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("failed to get operation: %w", err)
	}
	if cli.JSON {
		return printJSON(op)
	}
	printOperation(op)
	return nil
}

// maxOperationsLimit mirrors the server's cap on the operations page size;
// a drift test pins the two together.
const maxOperationsLimit = 100

// OperationListCmd implements 'cfctl operation list'.
type OperationListCmd struct {
	ResourceID string `placeholder:"RESOURCE_ID" help:"Filter by the target resource (device) ID"`
	Limit      int    `default:"20" help:"Maximum operations to list (1-100)"`
}

// Run executes the operation list command.
func (c *OperationListCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if c.Limit < 1 || c.Limit > maxOperationsLimit {
		return fmt.Errorf("--limit must be between 1 and %d (the server's page cap)", maxOperationsLimit)
	}
	ops, err := client.ListOperations(ctx, c.ResourceID, c.Limit)
	if err != nil {
		return fmt.Errorf("failed to list operations: %w", err)
	}
	if cli.JSON {
		return printJSON(ops)
	}
	return printOperations(ops)
}
