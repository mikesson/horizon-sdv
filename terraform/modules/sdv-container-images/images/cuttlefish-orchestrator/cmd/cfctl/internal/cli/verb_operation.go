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

// verbFlow describes one async in-place device verb, which is admitted and
// then optionally waited on until its operation settles.
type verbFlow struct {
	id     string
	action string
	gerund string
	past   string
	wait   bool
	act    func(context.Context, string) (*cco.Operation, error)
	// settle polls the handle to completion; nil means no --wait.
	settle func(context.Context, string) (*cco.Operation, error)
}

// runVerbOperation admits the verb and reports its operation handle. Without
// --wait it exits zero and reports the verb as accepted, since it has not
// happened yet.
func runVerbOperation(ctx context.Context, cli *CLI, f verbFlow) error {
	op, err := f.act(ctx, f.id)
	if err != nil {
		return fmt.Errorf("failed to %s device: %w", f.action, err)
	}
	if !cli.JSON {
		output.Info("%s device %s (operation %s)...", f.gerund, f.id, op.ID())
	}

	if !f.wait || f.settle == nil {
		if cli.JSON {
			return wrapJSONErr(output.PrintJSON(op))
		}
		output.Success("Device %s accepted: %s. Poll it with 'cfctl operation get %s'",
			f.action, f.id, op.ID())
		return nil
	}

	output.Info("Waiting for operation %s to complete...", op.ID())
	settled, err := f.settle(ctx, op.ID())
	if err != nil {
		// A FAILED operation returns an error here, so a failed verb exits
		// non-zero.
		return fmt.Errorf("device %s: %w", f.action, err)
	}
	if cli.JSON {
		return wrapJSONErr(output.PrintJSON(settled))
	}
	output.Success("Device %s: %s", f.past, f.id)
	return nil
}

// wrapJSONErr adds context to a --json render failure.
func wrapJSONErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("rendering operation as JSON: %w", err)
}
