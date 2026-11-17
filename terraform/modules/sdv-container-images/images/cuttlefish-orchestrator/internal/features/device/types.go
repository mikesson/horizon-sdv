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

package devicefeature

import (
	"fmt"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/internal/platform/ice"
)

// ListDevicesInput is the input for listing devices.
type ListDevicesInput struct {
	Limit     int    `query:"limit" minimum:"1" maximum:"100" default:"20" doc:"Maximum number of devices to return"`
	Offset    int    `query:"offset" minimum:"0" default:"0" doc:"Number of devices to skip"`
	PageToken string `query:"page_token" doc:"Opaque token from a previous page's next_page_token; cannot be combined with a non-zero offset"`
	HostID    string `query:"host_id" doc:"Filter by host ID"`
	Name      string `query:"name" doc:"Filter by exact device name; never matches a deleted device"`
	Status    string `query:"status" enum:"pending,creating,starting,running,stopping,stopped,deleting,deleted,error" doc:"Filter by status"`
	BuildID   string `query:"build_id" doc:"Filter by build ID"`
	CreatedBy string `query:"created_by" doc:"Filter by creator email"`
	Mine      bool   `query:"mine" doc:"Only devices created by the caller (mutually exclusive with created_by)"`
}

// ListDevicesOutput is the output for listing devices.
type ListDevicesOutput struct {
	Body struct {
		Items  []*device.Device `json:"items" doc:"List of devices"`
		Limit  int              `json:"limit" doc:"Requested limit"`
		Offset int              `json:"offset" doc:"Requested offset"`
		Total  int              `json:"total,omitempty" doc:"Total number of items"`
		// NextPageToken continues after this page even when rows are created
		// or deleted between requests, which an offset cannot.
		NextPageToken string `json:"next_page_token" doc:"Token for the next page; empty on the last page"`
	}
}

// GetDeviceInput is the input for getting a device.
type GetDeviceInput struct {
	ID string `path:"id" format:"uuid" doc:"Device ID"`
}

// GetDeviceOutput is the output for getting a device.
type GetDeviceOutput struct {
	Body *device.Device
}

// AdvancedConfigInput contains the advanced CVD configuration for advanced mode creation.
// See: https://android.googlesource.com/device/google/cuttlefish/+/refs/heads/main/host/commands/cvd/parser/README.md
type AdvancedConfigInput struct {
	// EnvConfig is the CVD canonical configuration (EnvironmentSpecification).
	// This is passed directly to the Host Orchestrator's CreateCVD endpoint.
	EnvConfig map[string]any `json:"env_config" required:"true" doc:"CVD environment configuration following the canonical format"`
}

// CreateDeviceInput is the input for creating a device.
type CreateDeviceInput struct {
	IdempotencyKey string `header:"Idempotency-Key" doc:"Optional key making device creation safe to retry; a repeat returns the original device"`
	Body           struct {
		HostID string `json:"host_id,omitempty" format:"uuid" doc:"Host ID to create device on; required unless requirements is set (exactly one of the two)"`
		Name   string `json:"name" required:"true" minLength:"1" maxLength:"63" pattern:"^[a-z][a-z0-9-]*$" doc:"Device name"`

		// Declarative create: the platform resolves the build and places the
		// device on a suitable host (or provisions one from policy defaults).
		Requirements *RequirementsInput `json:"requirements,omitempty" doc:"Declarative create; mutually exclusive with host_id"`

		ValidateOnly bool `json:"validate_only,omitempty" doc:"Dry run (AIP-163): perform the checks and resolution a real create would, commit nothing, and return the would-be device and resolution"`

		BuildID     string `json:"build_id,omitempty" doc:"Build ID from Android CI or internal build UUID"`
		BuildTarget string `json:"build_target,omitempty" doc:"Build target (e.g., aosp_cf_x86_64_phone-userdebug)"`
		GPUMode     string `json:"gpu_mode,omitempty" enum:"gfxstream,drm_virgl,swiftshader,auto" default:"auto" doc:"GPU rendering mode; auto lets cvd match the host's hardware"`

		// Advanced mode fields
		Mode           string               `json:"mode,omitempty" enum:"simple,advanced" default:"simple" doc:"Creation mode: simple uses defaults, advanced allows full CVD configuration"`
		AdvancedConfig *AdvancedConfigInput `json:"advanced_config,omitempty" doc:"Advanced CVD configuration (required when mode=advanced)"`

		// Lifecycle fields
		TTLHours           int        `json:"ttl_hours,omitempty" minimum:"1" maximum:"8" doc:"Session TTL in hours for ephemeral devices (default 4). Ignored when pinned."`
		Pinned             bool       `json:"pinned,omitempty" doc:"Create the device pinned (exempt from TTL reaping)"`
		PinnedReason       string     `json:"pinned_reason,omitempty" enum:"overnight_test,memory_leak_debug,long_running_ci,demo_preparation,customer_support,other" doc:"Why the device is pinned (required when pinned)"`
		PinnedReasonDetail string     `json:"pinned_reason_detail,omitempty" maxLength:"500" doc:"Details for the pin reason (required when pinned_reason is other)"`
		PinnedUntil        *time.Time `json:"pinned_until,omitempty" format:"date-time" doc:"When the pin expires and the device returns to TTL reaping; omitted pins indefinitely. Requires pinned."`
		Tags               []string   `json:"tags,omitempty" maxItems:"10" doc:"Informational labels (each 1-64 characters)"`
	}
}

// Resolve validates the cross-field lifecycle rules JSON Schema cannot express.
func (i *CreateDeviceInput) Resolve(_ huma.Context) []error {
	errs := i.resolvePinning()
	errs = append(errs, i.resolveTags()...)
	errs = append(errs, i.resolveRequirements()...)
	return errs
}

// resolvePinning checks the pin fields against each other: the reason fields
// only mean anything on a pinned device, and a pin window must still be ahead.
func (i *CreateDeviceInput) resolvePinning() []error {
	var errs []error
	if i.Body.Pinned && i.Body.PinnedReason == "" {
		errs = append(errs, &huma.ErrorDetail{
			Location: "body.pinned_reason",
			Message:  "pinned_reason is required when pinned is true",
		})
	}
	if !i.Body.Pinned && (i.Body.PinnedReason != "" || i.Body.PinnedReasonDetail != "") {
		errs = append(errs, &huma.ErrorDetail{
			Location: "body.pinned",
			Message:  "pinned_reason and pinned_reason_detail require pinned to be true",
		})
	}
	if !i.Body.Pinned && i.Body.PinnedUntil != nil {
		errs = append(errs, &huma.ErrorDetail{
			Location: "body.pinned_until",
			Message:  "pinned_until requires pinned to be true",
		})
	}
	if i.Body.PinnedUntil != nil && !i.Body.PinnedUntil.After(time.Now()) {
		errs = append(errs, &huma.ErrorDetail{
			Location: "body.pinned_until",
			Message:  "pinned_until must be in the future",
		})
	}
	if i.Body.Pinned && i.Body.PinnedReason == string(device.PinReasonOther) &&
		strings.TrimSpace(i.Body.PinnedReasonDetail) == "" {
		errs = append(errs, &huma.ErrorDetail{
			Location: "body.pinned_reason_detail",
			Message:  `pinned_reason_detail is required when pinned_reason is "other"`,
		})
	}
	return errs
}

// resolveTags checks the per-tag bounds JSON Schema cannot express (maxItems
// is on the array, not on the elements).
func (i *CreateDeviceInput) resolveTags() []error {
	var errs []error
	for idx, tag := range i.Body.Tags {
		if strings.TrimSpace(tag) == "" || len(tag) > 64 {
			errs = append(errs, &huma.ErrorDetail{
				Location: fmt.Sprintf("body.tags[%d]", idx),
				Message:  "tags must be non-empty and at most 64 characters",
			})
		}
	}
	return errs
}

// resolveRequirements checks the declarative-create alternatives: requirements
// replaces host_id, and the build is selected one way or the other.
func (i *CreateDeviceInput) resolveRequirements() []error {
	var errs []error
	if i.Body.Requirements != nil {
		if i.Body.HostID != "" {
			errs = append(errs, &huma.ErrorDetail{
				Location: "body.requirements",
				Message:  "requirements and host_id are mutually exclusive",
			})
		}
		if b := i.Body.Requirements.Build; b != nil && b.Registered != "" && b.Track != nil {
			errs = append(errs, &huma.ErrorDetail{
				Location: "body.requirements.build",
				Message:  "at most one of registered and track may be set",
			})
		}
	}
	return errs
}

// RequirementsInput is the declarative alternative to host_id: the platform
// resolves the build and host placement from the declared intent.
type RequirementsInput struct {
	Build       *BuildRequirementInput `json:"build,omitempty" doc:"Build selection; omitted = the platform default build (auto-registered from the upstream default when none is registered)"`
	Interaction string                 `json:"interaction" enum:"interactive,headless" doc:"How the device will be used; interactive prefers GPU-capable hosts"`
	GPURequired bool                   `json:"gpu_required,omitempty" doc:"Fail instead of degrading to software rendering when no GPU capacity exists"`
	Location    *LocationInput         `json:"location,omitempty" doc:"Placement preference; omitted = capacity-ranked placement"`
	Capacity    string                 `json:"capacity,omitempty" enum:"auto,spot,standard" doc:"Provisioning economics for an auto-provisioned host: spot (discounted, preemptible), standard (on-demand), or auto, where operator policy decides by interaction (default)"`
	// Left untyped on the wire on purpose. Which pools exist is compiled
	// knowledge, but which of them this deployment offers is operator state, and
	// a schema enum could only ever state the first, so both refusals are the
	// server's, each naming the pool and the fix.
	Pool string `json:"pool,omitempty" doc:"Subset of the fleet this device's host must belong to; omitted routes the create from what it declared it needs"`
	// The empty value belongs in the enum. A client that sends the field
	// cleared means the same as one that omits it, and the domain reads both
	// as x86.
	Architecture string `json:"architecture,omitempty" enum:"x86,arm," doc:"CPU architecture of the guest; omitted is x86. An arm64 guest is routed to the arm pool's bare-metal machines and refused where this deployment cannot build one"`
}

// LocationInput biases host placement toward a region. A preference, never a
// constraint: proximity dominates for interactive intent and tie-breaks for
// headless, but placement still lands wherever capacity exists.
type LocationInput struct {
	PreferredRegion string `json:"preferred_region" minLength:"1" maxLength:"63" pattern:"^[a-z]+-[a-z]+[0-9]+$" doc:"GCE region (e.g. us-central1)"`
}

// BuildRequirementInput selects the build for a declarative create. At most
// one of registered and track may be set; omitting both means the platform
// default build.
type BuildRequirementInput struct {
	Registered string             `json:"registered,omitempty" doc:"ID of a registered build"`
	Track      *TrackedBuildInput `json:"track,omitempty" doc:"Track the latest build on an AOSP branch/target"`
}

// TrackedBuildInput identifies a branch/target whose latest build the device
// should use (resolved by cvd fetch at create time).
type TrackedBuildInput struct {
	Branch string `json:"branch" minLength:"1" maxLength:"100" doc:"AOSP branch (e.g. aosp-android-latest-release)"`
	Target string `json:"target" minLength:"1" maxLength:"100" doc:"AOSP build target (e.g. aosp_cf_x86_64_only_phone-userdebug)"`
}

// CreateDeviceOutput is the output for creating a device. The embedded
// device flattens to the same wire shape existing clients parse; resolution
// appears only on declarative creates. On validate_only the device carries
// the would-be values without generated fields (AIP-163).
type CreateDeviceOutput struct {
	Body struct {
		device.Device
		Resolution *device.Resolution `json:"resolution,omitempty" doc:"How a declarative create resolved (build/host actions, provisioning shape, warnings); absent on host_id creates"`
	}
}

// DeleteDeviceInput is the input for deleting a device.
type DeleteDeviceInput struct {
	ID string `path:"id" format:"uuid" doc:"Device ID"`
}

// DeleteDeviceOutput is the output for deleting a device.
type DeleteDeviceOutput struct{}

// StopDeviceInput is the input for stopping a device.
type StopDeviceInput struct {
	ID string `path:"id" format:"uuid" doc:"Device ID"`
}

// StopDeviceOutput is the output for stopping a device.
type StopDeviceOutput struct {
	Body struct {
		Status string `json:"status" doc:"Current device status"`
	}
}

// StartDeviceInput is the input for starting a device.
type StartDeviceInput struct {
	ID string `path:"id" format:"uuid" doc:"Device ID"`
}

// StartDeviceOutput is the output for starting a device.
type StartDeviceOutput struct {
	Body struct {
		Status string `json:"status" doc:"Current device status"`
	}
}

// RestartInstanceInput is the input for restarting one instance in place.
type RestartInstanceInput struct {
	ID       string `path:"id" format:"uuid" doc:"Device ID"`
	Instance string `path:"name" doc:"Instance name, as listed in the device's instances array"`
}

// VerbOperationOutput is the response for an async in-place device verb: the
// operation handle its progress and outcome are read from.
//
// It replaces the old {"status":"running"} body, which was a constant. The
// device row is identical before and after these verbs, so the field could
// never say whether the work happened.
type VerbOperationOutput struct {
	Body operation.Operation
}

// PowerwashInstanceInput is the input for powerwashing one instance.
type PowerwashInstanceInput struct {
	ID       string `path:"id" format:"uuid" doc:"Device ID"`
	Instance string `path:"name" doc:"Instance name, as listed in the device's instances array"`
}

// TakeSnapshotInput is the input for capturing a device snapshot.
type TakeSnapshotInput struct {
	ID string `path:"id" format:"uuid" doc:"Device ID"`
}

// ListSnapshotsInput is the input for listing a device's snapshots.
type ListSnapshotsInput struct {
	ID string `path:"id" format:"uuid" doc:"Device ID"`
}

// ListSnapshotsOutput lists a device's snapshot inventory.
type ListSnapshotsOutput struct {
	Body struct {
		Snapshots []*device.Snapshot `json:"snapshots" doc:"The device's snapshots, newest first"`
	}
}

// DeleteSnapshotInput is the input for deleting a device's snapshot.
type DeleteSnapshotInput struct {
	ID         string `path:"id" format:"uuid" doc:"Device ID"`
	SnapshotID string `path:"snapshotId" format:"uuid" doc:"Snapshot ID"`
}

// RestoreSnapshotInput is the input for starting a stopped device from one of
// its snapshots.
type RestoreSnapshotInput struct {
	ID   string `path:"id" format:"uuid" doc:"Device ID"`
	Body struct {
		SnapshotID string `json:"snapshot_id" format:"uuid" doc:"Snapshot to restore from"`
	}
}

// PowerbtnInstanceInput is the input for pressing one instance's power button.
type PowerbtnInstanceInput struct {
	ID       string `path:"id" format:"uuid" doc:"Device ID"`
	Instance string `path:"name" doc:"Instance name, as listed in the device's instances array"`
}

// PowerbtnDeviceOutput is the output for pressing a device's power button.
type PowerbtnDeviceOutput struct {
	Body struct {
		Status string `json:"status" doc:"Current device status"`
	}
}

// BugreportDeviceInput is the input for capturing a bugreport.
type BugreportDeviceInput struct {
	ID   string `path:"id" format:"uuid" doc:"Device ID"`
	Body struct {
		IncludeADB bool `json:"include_adb,omitempty" doc:"Additionally run a full on-device adb bugreport (richer, but needs a responsive Android and can take minutes)"`
	}
}

// BugreportDeviceOutput locates the captured bugreport in artifact storage.
type BugreportDeviceOutput struct {
	Body struct {
		Bucket       string `json:"bucket" doc:"GCS bucket the bugreport was stored in"`
		Object       string `json:"object" doc:"Object path within the bucket"`
		GSURI        string `json:"gs_uri" doc:"gs:// URI of the artifact"`
		DownloadPath string `json:"download_path" doc:"API path that streams the artifact"`
	}
}

// StartRecordingInput / StopRecordingInput start/stop a screen recording on one
// instance.
type StartRecordingInput struct {
	ID       string `path:"id" format:"uuid" doc:"Device ID"`
	Instance string `path:"name" doc:"Instance name, as listed in the device's instances array"`
}

// StartRecordingOutput echoes the device status (unchanged; recording is not
// a lifecycle transition).
type StartRecordingOutput struct {
	Body struct {
		Status string `json:"status" doc:"Current device status"`
	}
}

// StopRecordingInput is the input for stopping a screen recording.
type StopRecordingInput struct {
	ID       string `path:"id" format:"uuid" doc:"Device ID"`
	Instance string `path:"name" doc:"Instance name, as listed in the device's instances array"`
}

// StopRecordingOutput echoes the device status.
type StopRecordingOutput struct {
	Body struct {
		Status string `json:"status" doc:"Current device status"`
	}
}

// ListRecordingsInput is the input for listing one instance's screen recordings.
type ListRecordingsInput struct {
	ID       string `path:"id" format:"uuid" doc:"Device ID"`
	Instance string `path:"name" doc:"Instance name, as listed in the device's instances array"`
}

// ListRecordingsOutput carries the host-local recording names.
type ListRecordingsOutput struct {
	Body struct {
		Recordings []string `json:"recordings" doc:"Screen recording names on the host (host-local until fetched)"`
	}
}

// FetchRecordingInput archives one host-local recording to artifact storage.
// The instance path segment is `name` like every other instance-addressed
// route, so the recording takes `recording`. The two would otherwise collide
// in one path.
type FetchRecordingInput struct {
	ID       string `path:"id" format:"uuid" doc:"Device ID"`
	Instance string `path:"name" doc:"Instance name, as listed in the device's instances array"`
	Name     string `path:"recording" doc:"Recording name (from list-device-recordings)"`
}

// FetchRecordingOutput locates the archived recording in artifact storage.
type FetchRecordingOutput struct {
	Body struct {
		Bucket       string `json:"bucket" doc:"GCS bucket the recording was stored in"`
		Object       string `json:"object" doc:"Object path within the bucket"`
		GSURI        string `json:"gs_uri" doc:"gs:// URI of the artifact"`
		DownloadPath string `json:"download_path" doc:"API path that streams the artifact"`
	}
}

// DisplayPreset defines a common display configuration.
type DisplayPreset struct {
	Name          string `json:"name" doc:"Preset name"`
	Width         int    `json:"width" doc:"Display width in pixels"`
	Height        int    `json:"height" doc:"Display height in pixels"`
	DPI           int    `json:"dpi" doc:"Dots per inch"`
	RefreshRateHz int    `json:"refresh_rate_hz" doc:"Refresh rate in Hz"`
}

// ListDisplaysResponse contains displays and available presets.
type ListDisplaysResponse struct {
	Displays []hoclient.Display `json:"displays" doc:"Active displays on the device"`
	Presets  []DisplayPreset    `json:"presets" doc:"Available display presets"`
}

// ListDisplaysInput is the input for listing one instance's displays.
type ListDisplaysInput struct {
	ID       string `path:"id" format:"uuid" doc:"Device ID"`
	Instance string `path:"name" doc:"Instance name, as listed in the device's instances array"`
}

// ListDisplaysOutput is the output for listing a device's displays.
type ListDisplaysOutput struct {
	Body ListDisplaysResponse
}

// AddDisplayInput is the input for adding a display to one instance.
type AddDisplayInput struct {
	ID       string `path:"id" format:"uuid" doc:"Device ID"`
	Instance string `path:"name" doc:"Instance name, as listed in the device's instances array"`
	Body     struct {
		Width         int    `json:"width,omitempty" minimum:"1" doc:"Display width in pixels (required unless preset is set)"`
		Height        int    `json:"height,omitempty" minimum:"1" doc:"Display height in pixels (required unless preset is set)"`
		DPI           int    `json:"dpi,omitempty" minimum:"1" doc:"Dots per inch"`
		RefreshRateHz int    `json:"refresh_rate_hz,omitempty" minimum:"1" doc:"Refresh rate in Hz"`
		Preset        string `json:"preset,omitempty" doc:"Preset name (case-insensitive; see list-displays presets); overrides the dimension fields"`
	}
}

// AddDisplayResponse is the response from adding a display.
type AddDisplayResponse struct {
	DisplayNumber int `json:"display_number" doc:"Number assigned to the new display"`
}

// AddDisplayOutput is the output for adding a display.
type AddDisplayOutput struct {
	Body AddDisplayResponse
}

// RemoveDisplayInput is the input for removing a display from one instance.
type RemoveDisplayInput struct {
	ID            string `path:"id" format:"uuid" doc:"Device ID"`
	Instance      string `path:"name" doc:"Instance name, as listed in the device's instances array"`
	DisplayNumber int    `path:"displayNumber" minimum:"0" doc:"Display number to remove"`
}

// RemoveDisplayOutput is the output for removing a display (204, no body).
type RemoveDisplayOutput struct{}

// GetScreenshotInput is the input for capturing one instance's display.
type GetScreenshotInput struct {
	ID            string `path:"id" format:"uuid" doc:"Device ID"`
	Instance      string `path:"name" doc:"Instance name, as listed in the device's instances array"`
	DisplayNumber int    `path:"displayNumber" minimum:"0" doc:"Display number to capture"`
}

// GetScreenshotOutput is a captured display image (bytes base64-encoded).
type GetScreenshotOutput struct {
	Body struct {
		ScreenshotBytes string `json:"screenshot_bytes" doc:"Base64-encoded image bytes"`
		MimeType        string `json:"screenshot_mime_type" doc:"Image MIME type"`
	}
}

// GetLifecycleInput is the input for getting a device's lifecycle.
type GetLifecycleInput struct {
	ID string `path:"id" format:"uuid" doc:"Device ID"`
}

// GetLifecycleOutput is the output for getting a device's lifecycle.
type GetLifecycleOutput struct {
	Body *device.DeviceLifecycle
}

// PinDeviceInput is the input for pinning a device.
type PinDeviceInput struct {
	ID   string `path:"id" format:"uuid" doc:"Device ID"`
	Body struct {
		Reason       string     `json:"reason" required:"true" enum:"overnight_test,memory_leak_debug,long_running_ci,demo_preparation,customer_support,other" doc:"Reason for pinning the device"`
		ReasonDetail string     `json:"reason_detail,omitempty" doc:"Additional detail for 'other' reason"`
		PinnedUntil  *time.Time `json:"pinned_until,omitempty" format:"date-time" doc:"Optional RFC 3339 timestamp when pin should expire"`
	}
}

// PinDeviceOutput is the output for pinning a device.
type PinDeviceOutput struct {
	Body *device.Device
}

// UnpinDeviceInput is the input for unpinning a device.
type UnpinDeviceInput struct {
	ID string `path:"id" format:"uuid" doc:"Device ID"`
}

// UnpinDeviceOutput is the output for unpinning a device.
type UnpinDeviceOutput struct {
	Body *device.Device
}

// ExtendDeviceInput is the input for extending a device's TTL.
type ExtendDeviceInput struct {
	ID   string `path:"id" format:"uuid" doc:"Device ID"`
	Body struct {
		Seconds int64 `json:"seconds,omitempty" minimum:"0" maximum:"14400" doc:"Seconds to extend TTL (default 7200 = 2 hours)"`
	}
}

// ExtendDeviceOutput is the output for extending a device's TTL.
type ExtendDeviceOutput struct {
	Body *device.Device
}

// GetICEConfigInput is the input for fetching a device's ICE configuration.
type GetICEConfigInput struct {
	ID string `path:"id" format:"uuid" doc:"Device ID"`
}

// GetICEConfigOutput is the output for fetching a device's ICE configuration.
type GetICEConfigOutput struct {
	Body ice.Configuration
}
