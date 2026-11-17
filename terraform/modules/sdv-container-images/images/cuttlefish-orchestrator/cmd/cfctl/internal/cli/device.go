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
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// DeviceListCmd implements 'cfctl device list'. It lists every device unless
// --mine is passed, so CI service accounts and scripts see the whole fleet.
type DeviceListCmd struct {
	Host      string `placeholder:"HOST" help:"Filter by host ID or name"`
	Status    string `default:"" enum:"pending,creating,starting,running,stopping,stopped,deleting,deleted,error," help:"Filter by status: pending, creating, starting, running, stopping, stopped, deleting, deleted, or error"`
	Mine      bool   `help:"Only devices you created (default: everyone's)"`
	CreatedBy string `name:"created-by" placeholder:"EMAIL" help:"Filter by creator email"`
}

// Run executes the device list command.
func (c *DeviceListCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveHostArg(ctx, client, &c.Host); err != nil {
		return err
	}
	devices, err := client.ListDevices(ctx, cco.DeviceListOptions{
		HostID: c.Host, Status: c.Status, CreatedBy: c.CreatedBy, Mine: c.Mine,
	})
	if err != nil {
		return fmt.Errorf("failed to list devices: %w", err)
	}

	if cli.JSON {
		return printJSON(devices)
	}
	return printDevices(devices)
}

// DeviceGetCmd implements 'cfctl device get'.
type DeviceGetCmd struct {
	ID string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
}

// Run executes the device get command.
func (c *DeviceGetCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	device, err := client.GetDevice(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("failed to get device: %w", err)
	}

	if cli.JSON {
		return printJSON(device)
	}
	printDevice(device)
	return nil
}

// DeviceCreateCmd implements 'cfctl device create'. With --host the device
// lands on that host. Otherwise the platform picks or provisions a host and
// resolves the build, for --interaction headless unless told otherwise.
type DeviceCreateCmd struct {
	Name             string `placeholder:"NAME" help:"Device name (lowercase letters, digits, hyphens; must start with a letter; default: cvd- plus 8 random characters)"`
	GPUMode          string `default:"auto" enum:"gfxstream,drm_virgl,swiftshader,auto" help:"GPU rendering mode: gfxstream, drm_virgl, swiftshader, or auto (matches the host's hardware)"`
	DryRun           bool   `name:"dry-run" xor:"plan" help:"Validate and resolve without creating anything; prints the plan"`
	Connect          bool   `help:"Wait for the device, then attach ADB to every instance through background agents (implies --wait)"`
	IdempotencyFlags `embed:""`
	WaitFlags        `embed:""`

	Host string `group:"placement-imperative" placeholder:"HOST" help:"Host ID or name to create the device on; mutually exclusive with --interaction"`

	Interaction  string `group:"placement-declarative" enum:",interactive,headless" default:"" placeholder:"MODE" help:"interactive (browser use, prefers GPU hosts) or headless (ADB/automation); headless when neither --host nor --interaction is given"`
	GPURequired  bool   `group:"placement-declarative" name:"gpu-required" help:"Fail instead of degrading to software rendering when no GPU capacity exists"`
	Region       string `group:"placement-declarative" placeholder:"REGION" help:"Prefer placement in this GCE region; a preference, never a constraint"`
	Capacity     string `group:"placement-declarative" enum:",auto,spot,standard" default:"" placeholder:"MODEL" help:"Provisioning economics for an auto-provisioned host: spot (discounted, preemptible), standard (on-demand), or auto (operator policy decides)"`
	Pool         string `group:"placement-declarative" placeholder:"POOL" help:"Fleet pool the device's host must belong to (default, gpu, arm); unset lets the requirements decide"`
	Architecture string `group:"placement-declarative" enum:",x86,arm" default:"" placeholder:"ARCH" help:"Guest CPU architecture: x86 (the default) or arm (needs an arm64 bare-metal host)"`

	BuildID     string         `group:"build-source" placeholder:"BUILD_ID" help:"A registered build ID, or an Android CI build ID, which is registered first (needs --build-target)"`
	BuildTarget string         `group:"build-source" placeholder:"TARGET" help:"Android build target (e.g., aosp_cf_x86_64_phone-trunk_staging-userdebug)"`
	TrackBranch string         `group:"build-source" name:"track-branch" placeholder:"BRANCH" help:"Track the latest build on this AOSP branch (declarative mode; with --track-target)"`
	TrackTarget string         `group:"build-source" name:"track-target" placeholder:"TARGET" help:"Build target for --track-branch"`
	LocalImage  []string       `group:"build-source" name:"local-image" xor:"plan" type:"existingfile" placeholder:"FILE" help:"Push these local artifacts (the images zip and cvd-host_package.tar.gz) and create from the resulting build; repeatable"`
	ProductOut  ProductOutFlag `group:"build-source" name:"product-out" xor:"plan" help:"Pack the required images from an Android product-out directory (--product-out=DIR, or $ANDROID_PRODUCT_OUT when no DIR is given), push them with the host package and create from the resulting build"`
	HostPackage string         `group:"build-source" name:"host-package" type:"existingfile" placeholder:"FILE" help:"Host package for --product-out (default: $ANDROID_HOST_OUT/cvd-host_package.tar.gz)"`

	// Ungrouped because they describe the device's shape, which neither the
	// placement groups nor the build-source group covers.
	Instances *int   `placeholder:"N" help:"Number of identical Android instances in the device (1-8, default 1)"`
	EnvConfig string `name:"env-config" type:"existingfile" placeholder:"FILE" help:"CVD canonical configuration (JSON) describing the instances to launch; mutually exclusive with --instances"`
}

// maxInstances is the server's limit on instances per device.
const maxInstances = 8

// Shape of each --instances instance. It matches the server's single-instance
// create and cvdr's multi-instance template, so N instances size like N
// single-instance devices.
const (
	instanceVCPUs    = 8
	instanceMemoryMB = 8192
)

// Run executes the device create command.
func (c *DeviceCreateCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	req, err := c.prepare(ctx, cli, client)
	if err != nil {
		return err
	}

	switch {
	case c.DryRun:
		output.Info("Planning device %s (dry run)...", c.Name)
	case c.Interaction != "":
		output.Info("Creating device %s (%s)...", c.Name, c.Interaction)
	default:
		output.Info("Creating device on host %s...", c.Host)
	}

	outcome, err := client.CreateDevice(ctx, req, c.IdempotencyKey)
	if err != nil {
		return fmt.Errorf("failed to create device: %w", err)
	}

	if c.DryRun {
		if cli.JSON {
			return printJSON(outcome)
		}
		printResolution(outcome.Resolution)
		output.Success("Plan valid. Nothing was created")
		return nil
	}

	if !cli.JSON {
		printResolution(outcome.Resolution)
	}
	deviceID := outcome.ID
	var settled *cco.Device
	if c.Wait {
		output.Info("Waiting for device %s to reach running...", deviceID)
		if settled, err = client.WaitDevice(ctx, deviceID, cco.StatusRunning); err != nil {
			return fmt.Errorf("device %s: %w", c.Name, err)
		}
	}

	var conns []connectionState
	if c.Connect {
		instances := instanceNames(settled)
		if len(instances) == 0 {
			return fmt.Errorf("device %s is running but reports no instances, so there is nothing to attach ADB to", deviceID)
		}
		if conns, err = startBackgroundConnections(ctx, cli, client, deviceID, instances, backgroundOptions{attach: true, waitOnline: defaultWaitOnline}); err != nil {
			return fmt.Errorf("device %s is running, but attaching ADB failed: %w", deviceID, err)
		}
	}

	return c.report(cli, outcome, settled, conns)
}

// prepare checks the flags, resolves --host to a host ID, pushes or registers
// the build, and returns the create request.
func (c *DeviceCreateCmd) prepare(ctx context.Context, cli *CLI, client *cco.Client) (cco.CreateDeviceRequest, error) {
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return cco.CreateDeviceRequest{}, err
	}
	if err := resolveHostArg(ctx, client, &c.Host); err != nil {
		return cco.CreateDeviceRequest{}, err
	}
	if err := c.resolveBuild(ctx, cli, client); err != nil {
		return cco.CreateDeviceRequest{}, err
	}
	return c.buildRequest()
}

// report prints the created device, settled when --wait was given, and the
// ADB connections --connect started.
func (c *DeviceCreateCmd) report(cli *CLI, outcome *cco.CreateDeviceOutcome, settled *cco.Device, conns []connectionState) error {
	deviceID := outcome.ID
	if cli.JSON {
		if settled == nil {
			return printJSON(outcome)
		}
		// The settled device, with the create's resolution carried forward.
		return printJSON(struct {
			*cco.Device
			Resolution  cco.Resolution    `json:"resolution,omitzero"`
			Connections []connectionState `json:"connections,omitempty"`
		}{settled, outcome.Resolution, conns})
	}
	if c.Wait {
		output.Success("Device running: %s", deviceID)
	} else {
		output.Success("Device created: %s", deviceID)
	}
	if c.Connect {
		printBackgroundConnections(conns, true, output.Success)
		cli.Hint("Stop the ADB agents with 'cfctl device disconnect %s'; open the screen with 'cfctl device webrtc %s'", deviceID, deviceID)
		return nil
	}
	cli.Hint("Attach ADB with 'cfctl device connect %s'; open the screen with 'cfctl device webrtc %s'", deviceID, deviceID)
	return nil
}

// applyDefaults fills the flags a bare 'cfctl device create' leaves empty: a
// generated name, and headless declarative placement when no host is named.
// --connect needs the running device, so it turns on --wait.
func (c *DeviceCreateCmd) applyDefaults() {
	if c.Connect {
		c.Wait = true
	}
	if c.Name == "" {
		c.Name = generateDeviceName(c.IdempotencyKey)
	}
	if c.Host == "" && c.Interaction == "" {
		c.Interaction = "headless"
	}
}

// generateDeviceName returns "cvd-" plus 8 lowercase letters and digits. With
// an idempotency key the name derives from the key, because the server
// refuses a repeated key whose request differs from the first.
func generateDeviceName(idempotencyKey string) string {
	if idempotencyKey != "" {
		sum := sha256.Sum256([]byte(idempotencyKey))
		return "cvd-" + hex.EncodeToString(sum[:4])
	}
	return "cvd-" + strings.ToLower(rand.Text()[:8])
}

// validate checks the flag combinations before anything is pushed or
// registered.
func (c *DeviceCreateCmd) validate() error {
	if c.Connect && c.DryRun {
		return errors.New("--connect attaches to the created device, and --dry-run creates nothing. Drop one of them")
	}
	if c.Instances != nil {
		if c.EnvConfig != "" {
			return errors.New("--instances and --env-config are mutually exclusive: the config declares its own instances")
		}
		if *c.Instances < 1 || *c.Instances > maxInstances {
			return fmt.Errorf("--instances must be between 1 and %d (got %d)", maxInstances, *c.Instances)
		}
	}
	return c.validateBuildSource()
}

// validateBuildSource checks the build flags against each other. Kong's xor
// group already keeps --local-image and --product-out apart.
func (c *DeviceCreateCmd) validateBuildSource() error {
	local := len(c.LocalImage) > 0 || c.ProductOut.Set()
	switch {
	case local && (c.BuildID != "" || c.TrackBranch != "" || c.TrackTarget != ""):
		return errors.New("--local-image and --product-out are mutually exclusive with --build-id and --track-*: the pushed build is the build")
	case c.HostPackage != "" && !c.ProductOut.Set():
		return errors.New("--host-package only applies to --product-out")
	}
	if c.BuildID != "" && !isRegisteredBuildID(c.BuildID) {
		if c.BuildTarget == "" {
			return fmt.Errorf("--build-id %s is not a registered build ID, so it is registered as an Android CI build, which needs --build-target", c.BuildID)
		}
		if c.DryRun {
			return fmt.Errorf("--dry-run commits nothing, so it cannot register Android CI build %s. Register it with 'cfctl build register' and plan with the returned ID", c.BuildID)
		}
	}
	return nil
}

// isRegisteredBuildID reports whether id has the shape of a registry ID.
// Android CI build IDs never parse as UUIDs.
func isRegisteredBuildID(id string) bool {
	return uuid.Validate(id) == nil
}

// resolveBuild turns the local build flags and an Android CI build ID into a
// registered build ID, pushing or registering as needed.
func (c *DeviceCreateCmd) resolveBuild(ctx context.Context, cli *CLI, client *cco.Client) error {
	var b *cco.Build
	var err error
	switch {
	case len(c.LocalImage) > 0:
		b, err = pushBuild(ctx, cli, client, c.LocalImage, pushOptions{})
	case c.ProductOut.Set():
		b, err = pushProductOut(ctx, cli, client, c.ProductOut, c.HostPackage, pushOptions{})
	case c.BuildID != "" && !isRegisteredBuildID(c.BuildID):
		if b, err = registerAndroidBuild(ctx, client, androidBuild{buildID: c.BuildID, target: c.BuildTarget}); err != nil {
			return err
		}
		// Printed under --json too: it goes to stderr, and a script needs the
		// ID to reuse the build.
		output.Info("Registered Android CI build %s as %s", c.BuildID, b.ID)
		c.BuildID = b.ID
		return nil
	default:
		return nil
	}
	if err != nil {
		return err
	}
	if !cli.JSON {
		output.Info("Pushed build %s (%s)", b.Name, b.ID)
	}
	c.BuildID = b.ID
	return nil
}

// buildRequest maps the flags to the create request, enforcing the
// imperative/declarative split.
func (c *DeviceCreateCmd) buildRequest() (cco.CreateDeviceRequest, error) {
	req := cco.CreateDeviceRequest{
		HostID:       c.Host,
		Name:         c.Name,
		BuildID:      c.BuildID,
		BuildTarget:  c.BuildTarget,
		GPUMode:      c.GPUMode,
		ValidateOnly: c.DryRun,
	}
	if err := c.applyEnvConfig(&req); err != nil {
		return req, err
	}
	if c.Interaction == "" {
		if c.GPURequired || c.TrackBranch != "" || c.TrackTarget != "" || c.Region != "" || c.Capacity != "" || c.Pool != "" || c.Architecture != "" {
			return req, errors.New("--gpu-required, --region, --capacity, --pool, --architecture, and --track-* apply only to declarative placement; drop --host")
		}
		return req, nil
	}
	if c.Host != "" {
		return req, errors.New("--host and --interaction are mutually exclusive")
	}

	reqs, err := buildRequirements(requirementFlags{
		interaction: c.Interaction, capacity: c.Capacity, gpuRequired: c.GPURequired,
		pool: c.Pool, architecture: c.Architecture,
		region: c.Region, buildID: c.BuildID, trackBranch: c.TrackBranch, trackTarget: c.TrackTarget,
	})
	if err != nil {
		return req, err
	}
	// The declarative request carries the build inside requirements.
	req.BuildID, req.BuildTarget = "", ""
	req.Requirements = reqs
	return req, nil
}

// applyEnvConfig loads --env-config, or builds the config for --instances
// above 1, and switches the create to advanced mode. Only JSON syntax is
// checked here. The server validates the content.
func (c *DeviceCreateCmd) applyEnvConfig(req *cco.CreateDeviceRequest) error {
	if c.Instances != nil && *c.Instances > 1 {
		raw, err := identicalInstancesConfig(*c.Instances)
		if err != nil {
			return err
		}
		req.Mode = "advanced"
		req.AdvancedConfig = &cco.AdvancedConfig{EnvConfig: raw}
		return nil
	}
	if c.EnvConfig == "" {
		return nil
	}
	raw, err := os.ReadFile(c.EnvConfig)
	if err != nil {
		return fmt.Errorf("reading %s: %w", c.EnvConfig, err)
	}
	if !json.Valid(raw) {
		return fmt.Errorf("%s is not valid JSON", c.EnvConfig)
	}
	req.Mode = "advanced"
	req.AdvancedConfig = &cco.AdvancedConfig{EnvConfig: raw}
	return nil
}

// identicalInstancesConfig is the canonical config for n identical instances.
// It names no instances, so cvd names them, and carries no build: the server
// merges the create's build into every instance.
func identicalInstancesConfig(n int) (json.RawMessage, error) {
	type vmConfig struct {
		CPUs            int    `json:"cpus"`
		MemoryMB        int    `json:"memory_mb"`
		SetupWizardMode string `json:"setupwizard_mode"`
	}
	type instance struct {
		VM vmConfig `json:"vm"`
	}
	instances := make([]instance, n)
	for i := range instances {
		instances[i].VM = vmConfig{CPUs: instanceVCPUs, MemoryMB: instanceMemoryMB, SetupWizardMode: "OPTIONAL"}
	}
	raw, err := json.Marshal(struct {
		Instances []instance `json:"instances"`
	}{instances})
	if err != nil {
		return nil, fmt.Errorf("encoding the instances config: %w", err)
	}
	return raw, nil
}

// DeviceDeleteCmd implements 'cfctl device delete'.
type DeviceDeleteCmd struct {
	ID        string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
	Force     bool   `short:"f" help:"Skip confirmation prompt"`
	WaitFlags `embed:""`
}

// Run executes the device delete command.
func (c *DeviceDeleteCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	return deleteFlow{
		resource: kindDevice, id: c.ID, force: c.Force, wait: c.Wait,
		detail:   creatorDetail(client, c.ID, client.GetDevice, func(d *cco.Device) string { return d.CreatedBy }),
		del:      client.DeleteDevice,
		waitGone: waitDeleted(kindDevice, client.WaitDevice),
	}.run(ctx, cli)
}

// DeviceStartCmd implements 'cfctl device start'.
type DeviceStartCmd struct {
	ID        string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
	WaitFlags `embed:""`
}

// Run executes the device start command.
func (c *DeviceStartCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	f := actionFlow{
		resource: kindDevice, id: c.ID, action: "start", gerund: "Starting",
		target: cco.StatusRunning, act: client.StartDevice,
	}
	if c.Wait {
		f.wait = func(ctx context.Context, id, target string) (any, error) {
			return client.WaitDevice(ctx, id, target)
		}
	}
	return f.run(ctx, cli)
}

// DeviceStopCmd implements 'cfctl device stop'.
type DeviceStopCmd struct {
	ID        string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
	WaitFlags `embed:""`
}

// Run executes the device stop command.
func (c *DeviceStopCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	f := actionFlow{
		resource: kindDevice, id: c.ID, action: "stop", gerund: "Stopping",
		target: cco.StatusStopped, act: client.StopDevice,
	}
	if c.Wait {
		f.wait = func(ctx context.Context, id, target string) (any, error) {
			return client.WaitDevice(ctx, id, target)
		}
	}
	return f.run(ctx, cli)
}

// DeviceRestartCmd implements 'cfctl device restart'.
type DeviceRestartCmd struct {
	ID           string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
	InstanceFlag `embed:""`
	WaitFlags    `embed:""`
}

// Run executes the device restart command. The host serves no group-level
// restart, so this relaunches one Android VM.
func (c *DeviceRestartCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	instance, err := c.resolve(ctx, client, c.ID)
	if err != nil {
		return err
	}
	return runVerbOperation(ctx, cli, verbFlow{
		id: c.ID, action: "restart", gerund: "Restarting", past: "restarted",
		wait: c.Wait, settle: client.WaitOperation,
		act: func(ctx context.Context, id string) (*cco.Operation, error) {
			return client.RestartDevice(ctx, id, instance)
		},
	})
}

// DevicePowerwashCmd implements 'cfctl device powerwash'.
type DevicePowerwashCmd struct {
	ID           string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
	Force        bool   `short:"f" help:"Skip confirmation prompt"`
	InstanceFlag `embed:""`
	WaitFlags    `embed:""`
}

// Run executes the device powerwash command.
func (c *DevicePowerwashCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	instance, err := c.resolve(ctx, client, c.ID)
	if err != nil {
		return err
	}
	// The prompt names the instance because on a multi-instance device the
	// device ID alone does not say which Android VM is wiped.
	if !c.Force {
		proceed, err := confirmOrCancel(cli, kindDevice, c.ID, "powerwash",
			"Powerwash wipes all userdata on instance %s of device %s. Continue?", instance, c.ID)
		if err != nil || !proceed {
			return err
		}
	}
	return runVerbOperation(ctx, cli, verbFlow{
		id: c.ID, action: "powerwash", gerund: "Powerwashing", past: "powerwashed",
		wait: c.Wait, settle: client.WaitOperation,
		act: func(ctx context.Context, id string) (*cco.Operation, error) {
			return client.PowerwashDevice(ctx, id, instance)
		},
	})
}

// DevicePowerbtnCmd implements 'cfctl device powerbtn'.
type DevicePowerbtnCmd struct {
	ID           string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
	InstanceFlag `embed:""`
}

// Run executes the device powerbtn command.
func (c *DevicePowerbtnCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	instance, err := c.resolve(ctx, client, c.ID)
	if err != nil {
		return err
	}
	if err := client.PowerbtnDevice(ctx, c.ID, instance); err != nil {
		return fmt.Errorf("failed to press power button: %w", err)
	}
	if cli.JSON {
		return printActionResult(kindDevice, c.ID, "powerbtn")
	}
	output.Success("Power button pressed: %s", c.ID)
	return nil
}

// DeviceScreenshotCmd implements 'cfctl device screenshot'.
type DeviceScreenshotCmd struct {
	ID           string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
	Display      int    `default:"0" help:"Display number to capture"`
	Output       string `short:"o" default:"screenshot.png" help:"Output file path"`
	InstanceFlag `embed:""`
}

// Run executes the device screenshot command.
func (c *DeviceScreenshotCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	instance, err := c.resolve(ctx, client, c.ID)
	if err != nil {
		return err
	}
	shot, err := client.GetDeviceScreenshot(ctx, c.ID, instance, c.Display)
	if err != nil {
		return fmt.Errorf("failed to capture screenshot: %w", err)
	}
	img, err := base64.StdEncoding.DecodeString(shot.ScreenshotBytes)
	if err != nil {
		return fmt.Errorf("decoding screenshot bytes: %w", err)
	}
	// The server rejects an empty capture, so this only catches a regression
	// on that path. A 0-byte file reported as success hides a broken device.
	if len(img) == 0 {
		return fmt.Errorf("host returned an empty screenshot for device %s display %d", c.ID, c.Display)
	}
	if err := os.WriteFile(c.Output, img, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", c.Output, err)
	}
	if cli.JSON {
		return printJSON(map[string]any{"file": c.Output, "mime_type": shot.ScreenshotMimeType, "bytes": len(img)})
	}
	if shot.ScreenshotMimeType == "" {
		output.Success("Screenshot saved to %s (%d bytes)", c.Output, len(img))
		return nil
	}
	output.Success("Screenshot saved to %s (%s, %d bytes)", c.Output, shot.ScreenshotMimeType, len(img))
	return nil
}

// DeviceBugreportCmd implements 'cfctl device bugreport'.
type DeviceBugreportCmd struct {
	ID            string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
	IncludeADB    bool   `name:"include-adb" help:"Additionally run a full on-device adb bugreport (richer, but needs a responsive Android and can take minutes)"`
	DownloadFlags `embed:""`
}

// Run executes the device bugreport command.
func (c *DeviceBugreportCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	output.Info("Capturing bugreport for device %s (this can take a while)...", c.ID)
	br, err := client.BugreportDevice(ctx, c.ID, c.IncludeADB)
	if err != nil {
		return fmt.Errorf("failed to capture bugreport: %w", err)
	}
	output.Info("Stored: %s", br.GSURI)

	if c.NoDownload {
		if cli.JSON {
			return printJSON(br)
		}
		output.Success("Bugreport captured: %s", br.GSURI)
		return nil
	}

	return downloadArtifact(ctx, cli, client, artifactDownload{
		bucket: br.Bucket, object: br.Object, gsURI: br.GSURI, out: c.Output,
		what: "bugreport", label: "Bugreport",
	})
}

// DeviceWebRTCCmd implements 'cfctl device webrtc'.
type DeviceWebRTCCmd struct {
	ID string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
}

// Run opens the console's device player page, which works in every ingress
// mode and lets IAP authenticate the browser.
func (c *DeviceWebRTCCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	// Read the device first so a bad ID fails before the browser opens.
	device, err := client.GetDevice(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("failed to get device: %w", err)
	}

	url := cli.Server + "/devices/" + device.ID + "/player"
	output.Info("Opening %s in browser...", url)

	if err := openBrowser(url); err != nil {
		output.Info("Could not open browser automatically.")
		output.Info("Please open this URL manually: %s", url)
	}

	return nil
}

// openBrowser opens a URL in the default browser.
func openBrowser(url string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{url}
	case "linux":
		name, args = "xdg-open", []string{url}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		return errors.New("unsupported platform")
	}

	cmd := exec.Command(name, args...) //nolint:noctx // The browser must outlive the command context.
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launching browser: %w", err)
	}

	// Reap the child in the background to prevent zombies.
	go func() { _ = cmd.Wait() }()
	return nil
}

// DeviceExtendCmd implements 'cfctl device extend'.
type DeviceExtendCmd struct {
	ID string        `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
	By time.Duration `help:"How much to extend the TTL (up to 4h; default: the server's 2h)" default:"0"`
}

// Run extends the device's TTL.
func (c *DeviceExtendCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := requireWholeSeconds("--by", c.By); err != nil {
		return err
	}
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	device, err := client.ExtendDevice(ctx, c.ID, int64(c.By.Seconds()))
	if err != nil {
		return fmt.Errorf("failed to extend device TTL: %w", err)
	}
	if cli.JSON {
		return wrapJSONErr(output.PrintJSON(device))
	}
	if !device.Lifecycle.ExpiresAt.IsZero() {
		output.Success("TTL extended: %s now expires at %s (extension #%d)",
			device.ID, device.Lifecycle.ExpiresAt.Format(time.RFC3339), device.Lifecycle.ExtendCount)
	} else {
		output.Success("TTL extended for %s", device.ID)
	}
	return nil
}

// requireWholeSeconds rejects a negative or fractional-second duration flag.
// The server takes whole seconds, and zero already means its default, so
// either would otherwise fall back silently.
func requireWholeSeconds(flag string, d time.Duration) error {
	if d < 0 {
		return fmt.Errorf("%s must not be negative (got %s)", flag, d)
	}
	if d%time.Second != 0 {
		return fmt.Errorf("%s must be a whole number of seconds (got %s)", flag, d)
	}
	return nil
}

// DevicePinCmd implements 'cfctl device pin'.
type DevicePinCmd struct {
	ID     string        `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
	Reason string        `required:"" enum:"overnight_test,memory_leak_debug,long_running_ci,demo_preparation,customer_support,other" placeholder:"REASON" help:"Why the device must survive reclamation: overnight_test, memory_leak_debug, long_running_ci, demo_preparation, customer_support, or other"`
	Detail string        `help:"Free-text detail (expected with --reason other)"`
	For    time.Duration `help:"Auto-expire the pin after this long (default: until unpinned)" default:"0"`
}

// Run pins the device.
func (c *DevicePinCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := requireWholeSeconds("--for", c.For); err != nil {
		return err
	}
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	req := cco.PinDeviceRequest{Reason: c.Reason, ReasonDetail: c.Detail}
	if c.For > 0 {
		req.PinnedUntil = new(time.Time)
		*req.PinnedUntil = time.Now().Add(c.For)
	}
	device, err := client.PinDevice(ctx, c.ID, req)
	if err != nil {
		return fmt.Errorf("failed to pin device: %w", err)
	}
	if cli.JSON {
		return wrapJSONErr(output.PrintJSON(device))
	}
	if !device.Lifecycle.PinnedUntil.IsZero() {
		output.Success("Pinned %s until %s", device.ID, device.Lifecycle.PinnedUntil.Format(time.RFC3339))
	} else {
		output.Success("Pinned %s until unpinned", device.ID)
	}
	return nil
}

// DeviceUnpinCmd implements 'cfctl device unpin'.
type DeviceUnpinCmd struct {
	ID string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
}

// Run unpins the device.
func (c *DeviceUnpinCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	device, err := client.UnpinDevice(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("failed to unpin device: %w", err)
	}
	if cli.JSON {
		return wrapJSONErr(output.PrintJSON(device))
	}
	output.Success("Unpinned %s; normal reclamation applies again", device.ID)
	return nil
}

// DeviceLifecycleCmd implements 'cfctl device lifecycle'.
type DeviceLifecycleCmd struct {
	ID string `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
}

// Run shows the device's lifecycle state.
func (c *DeviceLifecycleCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	lc, err := client.GetDeviceLifecycle(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("failed to get device lifecycle: %w", err)
	}
	if cli.JSON {
		return wrapJSONErr(output.PrintJSON(lc))
	}
	printLifecycle(lc)
	return nil
}

// Help adds the placement model to device create's help.
func (c *DeviceCreateCmd) Help() string {
	return `Placement is either imperative or declarative. Imperative: you name a
host with --host and the device lands on it. Declarative: you state the
interaction mode with --interaction and the platform places the device,
provisioning a host if none fits. With neither, the create is declarative
and headless. Without --name the device gets a generated cvd-XXXXXXXX name.

Examples:

  cfctl device create --wait
  cfctl device create --connect
  cfctl device create --name ci-1 --build-id 12345678 --build-target aosp_cf_x86_64_phone-trunk_staging-userdebug
  cfctl device create --instances 2 --wait
  cfctl device create --product-out --wait
  cfctl device create --name dbg-1 --host cf-host-3
  cfctl device create --name demo --interaction interactive --gpu-required --region REGION

--host takes a host ID or name. A device create has no machine-type flag. To
run on an exact machine shape, create the host first and place the device on
it:

  cfctl host create --name cf-host-3 --machine-type TYPE [--exclusive] --wait
  cfctl device create --host cf-host-3

--exclusive keeps automatic placement off that host. Discover legal values
with 'cfctl machine-type list', 'cfctl zone list' and 'cfctl build list'.`
}
