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
	"os"
	"path"
	"path/filepath"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// artifactDownload names one stored artifact to fetch to a local file.
type artifactDownload struct {
	bucket string
	object string
	gsURI  string
	out    string // target path; empty = the object's base name
	what   string // failure-message noun: "bugreport" / "recording"
	label  string // Success prefix: "Bugreport" / "Recording"
}

// downloadArtifact streams a stored artifact into a local file for `device
// bugreport` and `device record fetch`. It writes a temp file and renames it
// over the target on success, so a failed transfer leaves any old copy intact.
func downloadArtifact(ctx context.Context, cli *CLI, client *cco.Client, a artifactDownload) error {
	out := a.out
	if out == "" {
		out = path.Base(a.object)
	}
	f, err := os.CreateTemp(filepath.Dir(out), filepath.Base(out)+".partial-*")
	if err != nil {
		return fmt.Errorf("creating a staging file for %s: %w", out, err)
	}
	tmp := f.Name()
	defer func() {
		if tmp != "" {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err := client.DownloadStorageObject(ctx, a.bucket, a.object, f); err != nil {
		return fmt.Errorf("downloading %s: %w", a.what, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, out); err != nil {
		return fmt.Errorf("moving the downloaded %s into place: %w", a.what, err)
	}
	tmp = "" // Renamed, so skip the deferred cleanup.
	if cli.JSON {
		return printJSON(map[string]any{"file": out, "gs_uri": a.gsURI})
	}
	output.Success("%s saved to %s", a.label, out)
	return nil
}

// DeviceRecordStartCmd implements 'cfctl device record start'.
type DeviceRecordStartCmd struct {
	ID           string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
	InstanceFlag `embed:""`
}

// Run starts a screen recording.
func (c *DeviceRecordStartCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	instance, err := c.resolve(ctx, client, c.ID)
	if err != nil {
		return err
	}
	if err := client.StartDeviceRecording(ctx, c.ID, instance); err != nil {
		return fmt.Errorf("failed to start recording: %w", err)
	}
	if cli.JSON {
		return printActionResult("device", c.ID, "record_start")
	}
	output.Success("Recording started: %s", c.ID)
	return nil
}

// DeviceRecordStopCmd implements 'cfctl device record stop'.
type DeviceRecordStopCmd struct {
	ID           string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
	InstanceFlag `embed:""`
}

// Run stops a screen recording.
func (c *DeviceRecordStopCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	instance, err := c.resolve(ctx, client, c.ID)
	if err != nil {
		return err
	}
	if err := client.StopDeviceRecording(ctx, c.ID, instance); err != nil {
		return fmt.Errorf("failed to stop recording: %w", err)
	}
	if cli.JSON {
		return printActionResult("device", c.ID, "record_stop")
	}
	output.Success("Recording stopped: %s", c.ID)
	return nil
}

// DeviceRecordListCmd implements 'cfctl device record list'.
type DeviceRecordListCmd struct {
	ID           string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
	InstanceFlag `embed:""`
}

// Run lists the host-local recordings.
func (c *DeviceRecordListCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	instance, err := c.resolve(ctx, client, c.ID)
	if err != nil {
		return err
	}
	recordings, err := client.ListDeviceRecordings(ctx, c.ID, instance)
	if err != nil {
		return fmt.Errorf("failed to list recordings: %w", err)
	}
	if cli.JSON {
		return printJSON(recordings)
	}
	if len(recordings) == 0 {
		output.Info("No recordings on the host for device %s", c.ID)
		return nil
	}
	for _, r := range recordings {
		output.Info("%s", r)
	}
	return nil
}

// DeviceRecordFetchCmd implements 'cfctl device record fetch'.
type DeviceRecordFetchCmd struct {
	ID            string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
	Name          string `arg:"" required:"" placeholder:"RECORDING" help:"Recording name (from 'record list')"`
	InstanceFlag  `embed:""`
	DownloadFlags `embed:""`
}

// Run archives a recording to storage, then downloads it unless suppressed.
func (c *DeviceRecordFetchCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	instance, err := c.resolve(ctx, client, c.ID)
	if err != nil {
		return err
	}
	rec, err := client.FetchDeviceRecording(ctx, c.ID, instance, c.Name)
	if err != nil {
		return fmt.Errorf("failed to archive recording: %w", err)
	}
	output.Info("Stored: %s", rec.GSURI)

	if c.NoDownload {
		if cli.JSON {
			return printJSON(rec)
		}
		output.Success("Recording archived: %s", rec.GSURI)
		return nil
	}

	return downloadArtifact(ctx, cli, client, artifactDownload{
		bucket: rec.Bucket, object: rec.Object, gsURI: rec.GSURI, out: c.Output,
		what: "recording", label: "Recording",
	})
}
