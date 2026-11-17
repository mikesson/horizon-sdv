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
	"strings"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// confirmOrCancel prompts for confirmation. A decline returns false with a
// nil error (exit 0), and under --json it prints a canceled ActionResult.
func confirmOrCancel(cli *CLI, resource, id, action, format string, args ...any) (bool, error) {
	ok, err := confirm(format, args...)
	if err != nil {
		return false, err
	}
	if !ok {
		if cli.JSON {
			return false, printJSON(output.ActionResult{Resource: resource, ID: id, Action: action, Status: "canceled"})
		}
		output.Info("Canceled")
		return false, nil
	}
	return true, nil
}

// deleteFlow is the confirm, delete, wait and report flow shared by the host,
// device and batch delete commands.
type deleteFlow struct {
	resource string
	id       string
	force    bool
	wait     bool
	// detail optionally adds to the confirmation prompt. It runs only when
	// prompting, so --force makes a single API call.
	detail   func(context.Context) string
	del      func(context.Context, string) error
	waitGone func(context.Context, string) error
}

func (f deleteFlow) run(ctx context.Context, cli *CLI) error {
	if !f.force {
		extra := ""
		if f.detail != nil {
			extra = f.detail(ctx)
		}
		proceed, err := confirmOrCancel(cli, f.resource, f.id, "delete",
			"Are you sure you want to delete %s %s%s?", f.resource, f.id, extra)
		if err != nil || !proceed {
			return err
		}
	}

	output.Info("Deleting %s %s...", f.resource, f.id)
	if err := f.del(ctx, f.id); err != nil {
		return fmt.Errorf("failed to delete %s: %w", f.resource, err)
	}

	if f.wait {
		output.Info("Waiting for %s %s to be deleted...", f.resource, f.id)
		if err := f.waitGone(ctx, f.id); err != nil {
			return err
		}
	}

	if cli.JSON {
		return printActionResult(f.resource, f.id, "delete")
	}
	output.Success("Deleted %s %s", f.resource, f.id)
	return nil
}

// creatorDetail returns a deleteFlow detail that reads the resource with get
// and names its creator when that is someone other than the caller.
func creatorDetail[T any](client *cco.Client, id string, get func(context.Context, string) (*T, error), createdBy func(*T) string) func(context.Context) string {
	return func(ctx context.Context) string {
		v, err := get(ctx, id)
		if err != nil {
			return ""
		}
		return foreignCreatorDetail(ctx, client, createdBy(v))
	}
}

// waitDeleted returns a deleteFlow waitGone that polls with wait until the
// resource reads deleted.
func waitDeleted[T any](resource string, wait func(context.Context, string, string) (*T, error)) func(context.Context, string) error {
	return func(ctx context.Context, id string) error {
		if _, err := wait(ctx, id, cco.StatusDeleted); err != nil {
			return fmt.Errorf("%s %s: %w", resource, id, err)
		}
		return nil
	}
}

// actionFlow is the act, optional wait and report flow shared by the host and
// device start and stop commands.
type actionFlow struct {
	resource string // printActionResult resource and message noun
	id       string
	action   string // ActionResult verb ("start"/"stop"), also the failure label
	gerund   string // progress label ("Starting"/"Stopping")
	target   string // wait target status; doubles as the reached-state word
	act      func(context.Context, string) error
	// wait blocks until the resource reaches target and returns the final
	// entity for --json output. Nil skips the wait.
	wait func(ctx context.Context, id, target string) (any, error)
}

func (f actionFlow) run(ctx context.Context, cli *CLI) error {
	output.Info("%s %s %s...", f.gerund, f.resource, f.id)
	if err := f.act(ctx, f.id); err != nil {
		return fmt.Errorf("failed to %s %s: %w", f.action, f.resource, err)
	}

	capRes := strings.ToUpper(f.resource[:1]) + f.resource[1:]
	if f.wait != nil {
		output.Info("Waiting for %s %s to reach %s...", f.resource, f.id, f.target)
		v, err := f.wait(ctx, f.id, f.target)
		if err != nil {
			return fmt.Errorf("%s %s: %w", f.resource, f.id, err)
		}
		if cli.JSON {
			return printJSON(v)
		}
		output.Success("%s %s: %s", capRes, f.target, f.id)
		return nil
	}

	if cli.JSON {
		return printActionResult(f.resource, f.id, f.action)
	}
	output.Success("%s %s: %s", capRes, strings.ToLower(f.gerund), f.id)
	return nil
}

// foreignCreatorDetail returns a confirmation-prompt fragment when someone
// other than the caller created the resource. A failed identity lookup
// returns "" so it never blocks the delete.
func foreignCreatorDetail(ctx context.Context, client *cco.Client, createdBy string) string {
	if createdBy == "" {
		return ""
	}
	me, err := client.Me(ctx)
	if err != nil || me.Email == "" || strings.EqualFold(me.Email, createdBy) {
		return ""
	}
	return fmt.Sprintf(" (created by %s, not you)", createdBy)
}

// requirementFlags is the declarative flag family shared by device create and
// batch create.
type requirementFlags struct {
	interaction  string
	capacity     string
	gpuRequired  bool
	pool         string
	architecture string
	region       string
	buildID      string
	trackBranch  string
	trackTarget  string
}

// buildRequirements maps the shared declarative flags to cco.Requirements.
// It rejects a lone --track-* flag and a build ID combined with tracking.
func buildRequirements(f requirementFlags) (*cco.Requirements, error) {
	reqs := &cco.Requirements{
		Interaction: f.interaction, Capacity: f.capacity, GPURequired: f.gpuRequired,
		Pool: f.pool, Architecture: f.architecture,
	}
	if f.region != "" {
		reqs.Location = &cco.Location{PreferredRegion: f.region}
	}
	switch {
	case f.trackBranch != "" || f.trackTarget != "":
		if f.trackBranch == "" || f.trackTarget == "" {
			return nil, errors.New("--track-branch and --track-target must be set together")
		}
		if f.buildID != "" {
			return nil, errors.New("a registered build ID and --track-* are mutually exclusive")
		}
		reqs.Build = &cco.BuildRequirement{Track: &cco.TrackedBuild{Branch: f.trackBranch, Target: f.trackTarget}}
	case f.buildID != "":
		reqs.Build = &cco.BuildRequirement{Registered: f.buildID}
	}
	return reqs, nil
}
