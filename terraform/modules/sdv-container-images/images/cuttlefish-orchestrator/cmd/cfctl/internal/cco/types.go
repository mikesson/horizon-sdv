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

// Package cco provides the HTTP client for the Cloud Cuttlefish
// Orchestrator API.
package cco

import (
	"encoding/json"
	"time"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco/ccogen"
)

// The response types alias the models generated from the server's OpenAPI
// document by scripts/generate-ccogen.sh. Request bodies stay hand-written
// because their pointer fields and omitempty tags decide what a PATCH sends,
// and the generated input models drop those pointers.
//
//nolint:revive // Per-alias doc comments would only restate the generated types' own docs, which godoc already surfaces through the alias.
type (
	Host                       = ccogen.Host
	HostCVD                    = ccogen.HostCVD
	ListHostCVDsResponse       = ccogen.ListHostCVDsOutputBody
	Device                     = ccogen.Device
	DeviceLifecycle            = ccogen.DeviceLifecycle
	Instance                   = ccogen.Instance
	Screenshot                 = ccogen.GetScreenshotOutputBody
	Resolution                 = ccogen.Resolution
	CreateDeviceOutcome        = ccogen.CreateDeviceOutputBody
	BatchSummary               = ccogen.BatchSummary
	BatchMember                = ccogen.BatchMember
	Snapshot                   = ccogen.Snapshot
	ListSnapshotsResponse      = ccogen.ListSnapshotsOutputBody
	Bugreport                  = ccogen.BugreportDeviceOutputBody
	RecordingList              = ccogen.ListRecordingsOutputBody
	FetchedRecording           = ccogen.FetchRecordingOutputBody
	UserInfo                   = ccogen.GetMeOutputBody
	HealthResponse             = ccogen.HealthResponse
	InfoResponse               = ccogen.InfoResponse
	Build                      = ccogen.Build
	ArtifactSpec               = ccogen.ArtifactSpec
	StartBuildUploadResponse   = ccogen.StartBuildUploadOutputBody
	MachineType                = ccogen.MachineType
	FamilySummary              = ccogen.FamilySummary
	ZoneAvailability           = ccogen.ZoneAvailability
	QuotaResponse              = ccogen.QuotaResponse
	ListDisplaysResponse       = ccogen.ListDisplaysResponse
	VerifyAccessResponse       = ccogen.VerifyAccessResponse
	ListStorageBucketsResponse = ccogen.ListBucketsResponse
	ListStorageObjectsResponse = ccogen.ListObjectsResponse
	StorageObject              = ccogen.Object

	FailedTask                   = ccogen.FailedTaskItem
	AdminListFailedTasksResponse = ccogen.ListFailedTasksOutputBody
	AdminStats                   = ccogen.StatsOutputBody
	ProvisioningStats            = ccogen.ProvisioningStatsOutputBody
	ReclaimableDevice            = ccogen.ReclaimableDevice
	ReclaimableHost              = ccogen.ReclaimableHost
	Reclaimable                  = ccogen.ReclaimableOutputBody
	TaskQueueStats               = ccogen.TaskStatsOutputBody
	AuditListResponse            = ccogen.ListAuditOutputBody
	LiveTask                     = ccogen.LiveTaskItem
	LeaderLease                  = ccogen.LeaderLeaseItem
	SpotPanel                    = ccogen.SpotPanelItem
	ControlPlane                 = ccogen.ControlPlaneOutputBody
	Capacity                     = ccogen.CapacityOutputBody
	CapacityZone                 = ccogen.CapacityZoneItem
	CapacityRegion               = ccogen.CapacityRegionItem
	CapacityPool                 = ccogen.CapacityPoolItem
	AdminZone                    = ccogen.AdminZone
	AdminZoneList                = ccogen.ListAdminZonesOutputBody
	CatalogFreshness             = ccogen.CatalogFreshnessOutputBody
	HostImageConfig              = ccogen.HostImageConfig
	HostImageConfigList          = ccogen.ListHostImageConfigsOutputBody
	ImageStatus                  = ccogen.ImageStatus
	UpstreamBuild                = ccogen.GetDefaultUpstreamBuildOutputBody
	SpotMinUptime                = ccogen.SpotMinUptimeBody
	Lifetimes                    = ccogen.LifetimesBody
	DockerHost                   = ccogen.DockerHostBody
	ArtifactBuckets              = ccogen.ArtifactBucketsBody
	SweepPolicy                  = ccogen.SweepPolicy
	FleetPolicy                  = ccogen.FleetPolicyBody
	FleetTier                    = ccogen.FleetTier
	FleetSchedule                = ccogen.FleetSchedule
)

type batchDevicesResponse = ccogen.ListBatchDevicesOutputBody

// CreateHostRequest is the request body for creating a host. An empty Zone
// lets the server rank placement across the enabled catalog; naming one pins
// the host to it.
type CreateHostRequest struct {
	Name           string `json:"name"`
	Zone           string `json:"zone,omitempty"`
	MachineType    string `json:"machine_type,omitempty"`
	GPUType        string `json:"gpu_type,omitempty"`
	GPUCount       int64  `json:"gpu_count,omitzero"`
	MinCPUPlatform string `json:"min_cpu_platform,omitempty"`
	BootDiskSizeGB int64  `json:"boot_disk_size_gb,omitzero"`
	MaxDevices     int64  `json:"max_devices,omitzero"`
	TTLHours       int64  `json:"ttl_hours,omitzero"`
	Exclusive      bool   `json:"exclusive,omitempty"`
}

// CreateDeviceRequest is the request body for creating a device. Exactly one
// of HostID and Requirements must be set.
type CreateDeviceRequest struct {
	HostID       string        `json:"host_id,omitempty"`
	Name         string        `json:"name"`
	BuildID      string        `json:"build_id,omitempty"`
	BuildTarget  string        `json:"build_target,omitempty"`
	GPUMode      string        `json:"gpu_mode,omitempty"`
	Requirements *Requirements `json:"requirements,omitempty"`
	ValidateOnly bool          `json:"validate_only,omitempty"`

	// Mode and AdvancedConfig carry a CVD canonical configuration, which is
	// how cfctl creates a multi-instance device.
	Mode           string          `json:"mode,omitempty"`
	AdvancedConfig *AdvancedConfig `json:"advanced_config,omitempty"`
}

// AdvancedConfig carries the CVD canonical configuration, the
// EnvironmentSpecification the Host Orchestrator's create takes verbatim.
// It stays raw JSON because the schema is upstream's and cfctl never reads it.
type AdvancedConfig struct {
	EnvConfig json.RawMessage `json:"env_config"`
}

// Requirements is the declarative alternative to host_id: the platform
// resolves the build and host placement from the declared intent.
type Requirements struct {
	Build       *BuildRequirement `json:"build,omitempty"`
	Interaction string            `json:"interaction"`
	GPURequired bool              `json:"gpu_required,omitempty"`
	Location    *Location         `json:"location,omitempty"`
	// Capacity selects the provisioning economics for an auto-provisioned
	// host: "spot", "standard", or "auto" (operator policy decides).
	Capacity string `json:"capacity,omitempty"`
	// Pool names the fleet subset the device's host must belong to; absent
	// lets the requirements above decide.
	Pool string `json:"pool,omitempty"`
	// Architecture is the guest's CPU architecture, "x86" (the default) or
	// "arm"; an arm64 guest needs an arm64 bare-metal host.
	Architecture string `json:"architecture,omitempty"`
}

// Location biases host placement toward a region as a preference.
type Location struct {
	PreferredRegion string `json:"preferred_region"`
}

// BuildRequirement selects the build for a declarative create.
type BuildRequirement struct {
	Registered string        `json:"registered,omitempty"`
	Track      *TrackedBuild `json:"track,omitempty"`
}

// TrackedBuild identifies a branch/target whose latest build the device uses.
type TrackedBuild struct {
	Branch string `json:"branch"`
	Target string `json:"target"`
}

// CreateBatchRequest is the request body for creating a batch of devices.
type CreateBatchRequest struct {
	NamePrefix   string       `json:"name_prefix"`
	Count        int          `json:"count"`
	MinReady     int          `json:"min_ready,omitempty"`
	Requirements Requirements `json:"requirements"`
	FillPolicy   string       `json:"fill_policy,omitempty"`
	Placement    string       `json:"placement,omitempty"`
	ReadyTimeout int          `json:"ready_timeout_seconds,omitempty"`
	TTLHours     int          `json:"ttl_hours,omitempty"`
	ValidateOnly bool         `json:"validate_only,omitempty"`
}

// ListResponse is one page of a collection endpoint. NextPageToken is empty
// on the last page and on endpoints that do not page by token.
type ListResponse[T any] struct {
	Items         []T    `json:"items"`
	NextPageToken string `json:"next_page_token,omitempty"`
}

// DeviceListOptions narrows ListDevices; the zero value lists everything.
type DeviceListOptions struct {
	HostID string
	// Name matches one exact device name and never a deleted device.
	Name      string
	Status    string
	CreatedBy string
	Mine      bool
}

// HostListOptions narrows ListHosts; the zero value lists everything.
type HostListOptions struct {
	// Name matches one exact host name and never a deleted host.
	Name      string
	Zone      string
	Status    string
	CreatedBy string
	Mine      bool
}

// BatchListOptions narrows ListBatches; the zero value lists everything.
type BatchListOptions struct {
	CreatedBy string
	Mine      bool
}

// RegisterBuildRequest registers a build.
type RegisterBuildRequest struct {
	Name          string         `json:"name"`
	Source        string         `json:"source"`
	BuildID       string         `json:"build_id,omitempty"`
	Target        string         `json:"target,omitempty"`
	Branch        string         `json:"branch,omitempty"`
	ArtifactSpecs []ArtifactSpec `json:"artifact_specs,omitempty"`
	Fingerprint   string         `json:"fingerprint,omitempty"`
	Description   string         `json:"description,omitempty"`
	IsDefault     bool           `json:"is_default,omitempty"`
}

// StartBuildUploadRequest mints a staged-artifact upload session.
type StartBuildUploadRequest struct {
	FileName    string `json:"file_name"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type,omitempty"`
}

// MachineTypeListOptions narrows ListMachineTypes; the zero value lists
// everything. GPUSupport is "true", "false", or "" for all.
type MachineTypeListOptions struct {
	Family       string
	Category     string
	Architecture string
	Zone         string
	GPUSupport   string
}

// PinDeviceRequest is the request body for pinning a device.
type PinDeviceRequest struct {
	Reason       string     `json:"reason"`
	ReasonDetail string     `json:"reason_detail,omitempty"`
	PinnedUntil  *time.Time `json:"pinned_until,omitempty"`
}

// AddDisplayRequest adds a display; Preset overrides the dimension fields
// server-side, so the CLI sends one or the other, never both.
type AddDisplayRequest struct {
	Width         int    `json:"width,omitempty"`
	Height        int    `json:"height,omitempty"`
	DPI           int    `json:"dpi,omitempty"`
	RefreshRateHz int    `json:"refresh_rate_hz,omitempty"`
	Preset        string `json:"preset,omitempty"`
}

// VerifyAccessRequest asks the server to probe its own (and optionally a
// host's) access to a bucket.
type VerifyAccessRequest struct {
	Bucket     string `json:"bucket"`
	ObjectPath string `json:"object_path,omitempty"`
	HostID     string `json:"host_id,omitempty"`
	CheckWrite bool   `json:"check_write,omitempty"`
}

// StringValue mirrors the scalar system-config bodies carrying one string.
// IsSet reports whether an operator stored the value or the server is serving
// its compiled default.
type StringValue struct {
	Value string `json:"value"`
	IsSet bool   `json:"is_set,omitempty"`
}

// Int64Value mirrors the scalar system-config bodies carrying one integer.
type Int64Value struct {
	Value int64 `json:"value"`
	IsSet bool  `json:"is_set,omitempty"`
}

// BoolValue mirrors the scalar system-config bodies carrying one boolean.
type BoolValue struct {
	Value bool `json:"value"`
	IsSet bool `json:"is_set,omitempty"`
}

// UpdateSweepPolicyRequest is the sweeps PUT body. A nil field leaves that
// switch unchanged.
type UpdateSweepPolicyRequest struct {
	DeleteOrphanVMs *bool `json:"delete_orphan_vms,omitempty"`
	OrphanCVDs      *bool `json:"orphan_cvds,omitempty"`
	ZombieRecreate  *bool `json:"zombie_recreate,omitempty"`
	ReaperDryRun    *bool `json:"reaper_dry_run,omitempty"`
}

// UpdateFleetAutoscalerRequest is the autoscaler PUT body. It replaces the
// mode and one pool's two lower bounds together, so callers overlay onto the
// current read first. An empty pool is the default one, and the first write
// for a named pool switches that pool on.
type UpdateFleetAutoscalerRequest struct {
	Mode    string `json:"mode"`
	Pool    string `json:"pool,omitempty"`
	Standby int64  `json:"standby"`
	Floor   int64  `json:"floor"`
}

// UpdateFleetTuningRequest is the fleet tuning PUT body. It has its own PUT so
// that setting the autoscaler mode cannot revert a concurrent horizon change.
type UpdateFleetTuningRequest struct {
	DemandHorizonSec int64 `json:"demand_horizon_sec"`
	DrainGraceSec    int64 `json:"drain_grace_sec"`
}

// UpdateFleetSchedulesRequest replaces a pool's schedule windows whole. It has
// its own PUT so a scalar tuning write cannot clear the list.
type UpdateFleetSchedulesRequest struct {
	Pool      string          `json:"pool,omitempty"`
	Schedules []FleetSchedule `json:"schedules"`
}

// UpdateFleetLadderRequest replaces a pool's machine-type preference order
// whole. It has its own PUT so a scalar tuning write cannot clear the list.
type UpdateFleetLadderRequest struct {
	Pool        string   `json:"pool,omitempty"`
	ShapeLadder []string `json:"shape_ladder"`
}

// UpdateLifetimesRequest is the lifetimes PUT body. The four defaults are
// replaced together, so callers overlay onto the current read first.
type UpdateLifetimesRequest struct {
	DeviceTTLHours int64 `json:"device_ttl_hours"`
	ExtendHours    int64 `json:"extend_hours"`
	ExtendCount    int64 `json:"extend_count"`
	HostTTLHours   int64 `json:"host_ttl_hours"`
}

// UpdateDockerHostRequest is the docker-host PUT body, the GCE shape a legacy
// docker-only host create uses. It is replaced whole, so callers overlay onto
// the current read first.
type UpdateDockerHostRequest struct {
	MachineType string `json:"machine_type"`
	TTLHours    int64  `json:"ttl_hours"`
}

// UpdateArtifactBucketsRequest is the artifact-buckets PUT body. Every field
// is replaced, so callers overlay onto the current read first.
type UpdateArtifactBucketsRequest struct {
	BugreportBucket string `json:"bugreport_bucket"`
	RecordingBucket string `json:"recording_bucket"`
	CaptureOnError  bool   `json:"capture_on_error"`
}

// AuditFilter narrows the audit-log query; zero values are omitted.
type AuditFilter struct {
	ActorEmail   string
	Action       string
	ResourceType string
	ResourceID   string
	// Kind is the metadata kind the writer stamped (system_config,
	// host_image_config, machine_type, zone); empty means every category.
	Kind      string
	StartTime string
	EndTime   string
	Limit     int
	Offset    int
}

// UpdateMachineTypeRequest is the admin curation PATCH; nil fields are left
// unchanged. The server rejects IsDefault false.
type UpdateMachineTypeRequest struct {
	IsEnabled *bool `json:"is_enabled,omitempty"`
	IsDefault *bool `json:"is_default,omitempty"`
}

// HostImageConfigListOptions narrows the listing; zero values take the
// server defaults.
type HostImageConfigListOptions struct {
	Limit       int
	Offset      int
	EnabledOnly bool
}

// CreateHostImageConfigRequest registers a mapping from a machine family to
// an image family; nil Enabled defaults to true server-side.
type CreateHostImageConfigRequest struct {
	MachineFamily string `json:"machine_family"`
	ImageFamily   string `json:"image_family"`
	SourceProject string `json:"source_project,omitempty"`
	Description   string `json:"description,omitempty"`
	Enabled       *bool  `json:"enabled,omitempty"`
}

// UpdateHostImageConfigRequest replaces a mapping's mutable fields; callers
// overlay onto the current config first.
type UpdateHostImageConfigRequest struct {
	ImageFamily   string `json:"image_family"`
	SourceProject string `json:"source_project,omitempty"`
	Description   string `json:"description,omitempty"`
	Enabled       *bool  `json:"enabled,omitempty"`
}

// UpdateBuildRequest patches a build's mutable fields; nil fields are left
// unchanged, and a non-nil ArtifactSpecs replaces the whole set.
type UpdateBuildRequest struct {
	Name          *string        `json:"name,omitempty"`
	Description   *string        `json:"description,omitempty"`
	IsDefault     *bool          `json:"is_default,omitempty"`
	ArtifactSpecs []ArtifactSpec `json:"artifact_specs,omitempty"`
}
