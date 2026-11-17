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

// AdminHostImageCmd curates the mappings from machine family to GCE image
// family that hosts are provisioned from.
type AdminHostImageCmd struct {
	List   AdminHostImageListCmd   `cmd:"" aliases:"ls" help:"List the image mappings"`
	Get    AdminHostImageGetCmd    `cmd:"" aliases:"show,info" help:"Show one machine family's image mapping"`
	Create AdminHostImageCreateCmd `cmd:"" aliases:"new" help:"Register an image mapping for a machine family"`
	Update AdminHostImageUpdateCmd `cmd:"" help:"Change an image mapping"`
	Delete AdminHostImageDeleteCmd `cmd:"" aliases:"rm,remove" help:"Remove a machine family's image mapping"`
}

// maxHostImageLimit mirrors the server's cap on the listing page size; a
// drift test pins the two together.
const maxHostImageLimit = 200

// AdminHostImageListCmd implements 'cfctl admin host-image list'.
type AdminHostImageListCmd struct {
	Limit       int  `default:"100" help:"Maximum mappings to return (1-200)"`
	Offset      int  `help:"Mappings to skip"`
	EnabledOnly bool `help:"Only enabled mappings"`
}

// Run lists the image mappings.
func (c *AdminHostImageListCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if c.Limit < 1 || c.Limit > maxHostImageLimit {
		return fmt.Errorf("--limit must be between 1 and %d (the server's page cap)", maxHostImageLimit)
	}
	list, err := client.ListHostImageConfigs(ctx, cco.HostImageConfigListOptions{
		Limit: c.Limit, Offset: c.Offset, EnabledOnly: c.EnabledOnly,
	})
	if err != nil {
		return fmt.Errorf("failed to list host-image configs: %w", err)
	}
	if cli.JSON {
		return printJSON(list)
	}
	err = table("FAMILY\tIMAGE FAMILY\tPROJECT\tENABLED\tIMAGE\tUPDATED", func(w *tabwriter.Writer) {
		for _, cfg := range list.Items {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				cfg.MachineFamily, cfg.ImageFamily, cfg.Resolved.Project,
				yesNo(cfg.Enabled), imageCell(cfg.Resolved), cfg.UpdatedAt.Format(time.RFC3339))
		}
	})
	if err != nil {
		return err
	}
	if list.Total > int64(len(list.Items)) {
		fmt.Printf("Showing %d of %d (offset %d); page with --offset/--limit\n",
			len(list.Items), list.Total, list.Offset)
	}
	return nil
}

// imageCell is the one-word image status a listing row carries: the image the
// family boots, or the reason it boots nothing.
func imageCell(st cco.ImageStatus) string {
	switch {
	case st.Ok:
		return st.ImageName
	case st.Reason != "":
		return string(st.Reason)
	default:
		return "lookup failed"
	}
}

// printHostImageConfig renders one mapping in full.
func printHostImageConfig(cfg *cco.HostImageConfig) {
	fmt.Printf("Family:        %s\n", cfg.MachineFamily)
	fmt.Printf("Image family:  %s\n", cfg.ImageFamily)
	if cfg.SourceProject != "" {
		fmt.Printf("Project:       %s\n", cfg.SourceProject)
	} else {
		fmt.Printf("Project:       %s (CCO_HOST_IMAGE_PROJECT)\n", cfg.Resolved.Project)
	}
	if cfg.Resolved.Ok {
		fmt.Printf("Image:         %s", cfg.Resolved.ImageName)
		if cfg.Resolved.Architecture != "" {
			fmt.Printf(" (%s)", cfg.Resolved.Architecture)
		}
		fmt.Println()
	} else if cfg.Resolved.Message != "" {
		fmt.Printf("Image:         none: %s\n", cfg.Resolved.Message)
	}
	if cfg.Description != "" {
		fmt.Printf("Description:   %s\n", cfg.Description)
	}
	fmt.Printf("Enabled:       %s\n", yesNo(cfg.Enabled))
	fmt.Printf("Updated:       %s", cfg.UpdatedAt.Format(time.RFC3339))
	if cfg.UpdatedBy != "" {
		fmt.Printf(" by %s", cfg.UpdatedBy)
	}
	fmt.Println()
}

// AdminHostImageGetCmd implements 'cfctl admin host-image get'.
type AdminHostImageGetCmd struct {
	Family string `arg:"" placeholder:"MACHINE_FAMILY" help:"Machine family (e.g. n1, g2, c4a)"`
}

// Run shows one mapping.
func (c *AdminHostImageGetCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	cfg, err := client.GetHostImageConfig(ctx, c.Family)
	if err != nil {
		return fmt.Errorf("failed to get host-image config %s: %w", c.Family, err)
	}
	if cli.JSON {
		return printJSON(cfg)
	}
	printHostImageConfig(cfg)
	return nil
}

// AdminHostImageCreateCmd implements 'cfctl admin host-image create'.
type AdminHostImageCreateCmd struct {
	Family        string `arg:"" placeholder:"MACHINE_FAMILY" help:"Machine family (e.g. n1, g2, c4a)"`
	ImageFamily   string `arg:"" placeholder:"IMAGE_FAMILY" help:"GCE image family (e.g. cco-host)"`
	SourceProject string `placeholder:"PROJECT" help:"GCP project holding the image family (default: the deployment's CCO_HOST_IMAGE_PROJECT)"`
	Description   string `help:"What this mapping is for"`
	Disabled      bool   `help:"Register the mapping without activating it"`
}

// Run registers the mapping.
func (c *AdminHostImageCreateCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	req := cco.CreateHostImageConfigRequest{
		MachineFamily: c.Family, ImageFamily: c.ImageFamily,
		SourceProject: c.SourceProject, Description: c.Description,
	}
	if c.Disabled {
		f := false
		req.Enabled = &f
	}
	cfg, err := client.CreateHostImageConfig(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create host-image config: %w", err)
	}
	if cli.JSON {
		return printJSON(cfg)
	}
	output.Success("Mapped %s hosts to image family %s.", cfg.MachineFamily, cfg.ImageFamily)
	return nil
}

// AdminHostImageUpdateCmd implements 'cfctl admin host-image update'. The
// PUT replaces every mutable field, so the current config is read first and
// only the flagged fields change.
type AdminHostImageUpdateCmd struct {
	Family        string  `arg:"" placeholder:"MACHINE_FAMILY" help:"Machine family (e.g. n1, g2, c4a)"`
	ImageFamily   *string `placeholder:"IMAGE_FAMILY" help:"GCE image family"`
	SourceProject *string `placeholder:"PROJECT" help:"GCP project holding the image family (empty = the deployment's CCO_HOST_IMAGE_PROJECT)"`
	Description   *string `help:"What this mapping is for"`
	Enabled       *bool   `negatable:"" help:"Activate (--enabled) or deactivate (--no-enabled) the mapping"`
}

// Run changes the mapping.
func (c *AdminHostImageUpdateCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if c.ImageFamily == nil && c.SourceProject == nil && c.Description == nil && c.Enabled == nil {
		return errors.New("nothing to change: pass --image-family, --source-project, --description or --enabled/--no-enabled")
	}
	cur, err := client.GetHostImageConfig(ctx, c.Family)
	if err != nil {
		return fmt.Errorf("failed to read host-image config %s before update: %w", c.Family, err)
	}
	req := cco.UpdateHostImageConfigRequest{
		ImageFamily:   cur.ImageFamily,
		SourceProject: cur.SourceProject,
		Description:   cur.Description,
		Enabled:       &cur.Enabled,
	}
	if c.ImageFamily != nil {
		req.ImageFamily = *c.ImageFamily
	}
	if c.SourceProject != nil {
		req.SourceProject = *c.SourceProject
	}
	if c.Description != nil {
		req.Description = *c.Description
	}
	if c.Enabled != nil {
		req.Enabled = c.Enabled
	}
	cfg, err := client.UpdateHostImageConfig(ctx, c.Family, req)
	if err != nil {
		return fmt.Errorf("failed to update host-image config %s: %w", c.Family, err)
	}
	if cli.JSON {
		return printJSON(cfg)
	}
	output.Success("Updated %s.", cfg.MachineFamily)
	printHostImageConfig(cfg)
	return nil
}

// AdminHostImageDeleteCmd implements 'cfctl admin host-image delete'.
type AdminHostImageDeleteCmd struct {
	Family string `arg:"" placeholder:"MACHINE_FAMILY" help:"Machine family (e.g. n1, g2, c4a)"`
	Force  bool   `short:"f" help:"Skip confirmation"`
}

// Run removes the mapping.
func (c *AdminHostImageDeleteCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	return deleteFlow{
		resource: "host-image config", id: c.Family, force: c.Force,
		del: client.DeleteHostImageConfig,
	}.run(ctx, cli)
}
