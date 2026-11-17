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

package device

import (
	"errors"
	"slices"
	"strings"
	"time"
)

// Status represents the lifecycle state of a Cuttlefish device.
//
// Devices progress through a defined state machine from creation to deletion.
// The valid transitions are defined in ValidTransitions. Status values are
// stored as lowercase strings in the database and JSON responses.
type Status string

// The device lifecycle states, declared in lifecycle order. StatusDeleted is
// the only terminal one: an error device can still be retried or torn down.
const (
	StatusPending  Status = "pending"  // Creation requested
	StatusCreating Status = "creating" // Device being created
	StatusStarting Status = "starting" // Device starting
	StatusRunning  Status = "running"  // Device running
	StatusStopping Status = "stopping" // Device stopping
	StatusStopped  Status = "stopped"  // Device stopped
	StatusDeleting Status = "deleting" // Device being deleted
	StatusDeleted  Status = "deleted"  // Device deleted
	StatusError    Status = "error"    // Error state
)

// ValidTransitions defines allowed state transitions.
//
// CVD start/stop against the Host Orchestrator is synchronous, so
// creating/stopped go straight to running and running straight to stopped.
// No intermediate state is persisted mid-call. Any live state may enter
// deleting (user-initiated teardown) or error.
var ValidTransitions = map[Status][]Status{
	StatusPending:  {StatusCreating, StatusDeleting, StatusError, StatusDeleted},
	StatusCreating: {StatusStarting, StatusRunning, StatusDeleting, StatusError, StatusDeleted},
	StatusStarting: {StatusRunning, StatusDeleting, StatusError, StatusDeleted},
	// running -> creating: the health-driven zombie recovery re-drives the
	// CVD create from desired_spec when the device's CVD died under it.
	StatusRunning:  {StatusCreating, StatusStopping, StatusStopped, StatusDeleting, StatusError, StatusDeleted},
	StatusStopping: {StatusStopped, StatusDeleting, StatusError},
	StatusStopped:  {StatusStarting, StatusRunning, StatusDeleting, StatusError, StatusDeleted},
	StatusDeleting: {StatusDeleted, StatusError},
	// error -> running: a retried creation task recovers a device whose CVD
	// failed to start on an earlier attempt.
	StatusError: {StatusDeleting, StatusDeleted, StatusCreating, StatusRunning},
}

// CanTransitionTo checks if the status transition is valid
func (s Status) CanTransitionTo(target Status) bool {
	allowed, ok := ValidTransitions[s]
	if !ok {
		return false
	}
	return slices.Contains(allowed, target)
}

// IsTerminal returns true if no further transitions are expected
func (s Status) IsTerminal() bool {
	return s == StatusDeleted
}

// IsRunning returns true if the device is running
func (s Status) IsRunning() bool {
	return s == StatusRunning
}

// IsRetired reports whether the device is being deleted or is deleted. Its CVD
// is being torn down or is gone, so nothing on its host serves it any more.
func (s Status) IsRetired() bool {
	return s == StatusDeleting || s == StatusDeleted
}

// GPUMode represents the GPU rendering mode for a Cuttlefish device.
//
// The GPU mode determines how graphics are rendered within the virtual device.
// Different modes have different performance characteristics and hardware
// requirements. The default mode depends on the host's GPU availability.
type GPUMode string

const (
	// GPUModeGfxstream uses NVIDIA's gfxstream for GPU virtualization.
	// Recommended for hosts with NVIDIA T4 or similar GPUs.
	GPUModeGfxstream GPUMode = "gfxstream"

	// GPUModeVirgl uses the VirGL virtual GPU for rendering.
	// Compatible with a wider range of GPU hardware.
	GPUModeVirgl GPUMode = "drm_virgl"

	// GPUModeSwift uses SwiftShader for software-based rendering.
	// Does not require GPU hardware but has lower performance.
	GPUModeSwift GPUMode = "swiftshader"

	// GPUModeAuto automatically selects the best available GPU mode
	// based on the host's hardware configuration.
	GPUModeAuto GPUMode = "auto"
)

// SupportsSnapshot reports whether a guest running in this mode can be
// captured. Only swiftshader gets crosvm's 2D virtio-gpu backend, and 2D is the
// only mode crosvm serializes. Every other mode stands up rutabaga 3D
// contexts whose snapshot is unimplemented, and the attempt does not fail
// politely: crosvm dies mid-capture and takes the launcher and the whole
// instance with it.
//
// Auto is refused because it defers the decision to the host. The host
// resolves it from a candidate list the guest image itself can supply, and
// several of the candidates it can land on render in software yet still build a
// gfxstream device, so neither "the host has no GPU" nor anything else CCO
// knows makes auto safe. The host never reports the resolved mode back either,
// so this cannot be answered by observation.
//
// The list is an allow-list on purpose: a mode nobody has classified must not
// be snapshotted by default.
func (m GPUMode) SupportsSnapshot() bool { return m == GPUModeSwift }

// RequiresCPUHost reports whether placement must keep a device in this mode off
// GPU capacity. The create pins the mode, but only for that launch: any later
// `cvd start` re-assembles from defaults, and the GPU auto walk on a host that
// has an accelerator resolves to an accelerated mode, whose snapshot is the
// crash SupportsSnapshot exists to avoid. The host a snapshottable device
// lands on is therefore part of the guarantee, alongside the flags it was
// launched with.
func (m GPUMode) RequiresCPUHost() bool { return m == GPUModeSwift }

// GPUModes lists every mode the API accepts. It must name every GPUMode
// constant above: the transport declares the same set a second time, in the
// `enum` tag on the create body, because a struct tag cannot reference a
// constant. A test in the device feature slice asserts the two agree, so a
// mode added here without updating the tag fails the build rather than
// becoming a value the domain knows and the API rejects.
func GPUModes() []GPUMode {
	return []GPUMode{GPUModeGfxstream, GPUModeVirgl, GPUModeSwift, GPUModeAuto}
}

// Instance is one Android VM inside a device's CVD group, as the Host
// Orchestrator reported it. A device is one leased, placed, billed unit
// containing N of these; at N=1 the array holds the single instance whose
// values the device row also carries directly.
//
// This is an OBSERVATION, in the same category as CVDStatus and
// BootObservation. It is empty from admission until the host answers, which is
// why the DESIRED count is Device.InstanceCount and never len(Instances).
type Instance struct {
	// Name is the instance's name within the group. It is caller-chosen in
	// advanced mode, and the device's name otherwise. Every per-instance host
	// route addresses this, so a device whose instances were named by the
	// caller is only reachable through the persisted value.
	Name string `json:"name"`

	// State is the raw CVD wire-state string for this instance (one of the
	// CVDState* values). Empty means the instance was absent from a fleet
	// listing that did answer, which is the zombie signal, per instance.
	State string `json:"state,omitempty"`

	// WebRTCDeviceID is the id the host's operator registered this instance
	// under: "<group>-<name>-<instanceID>", where the instance id is unique
	// across the host's whole fleet and is NOT derivable from the group and
	// name. Signaling and the ADB bridge address the operator by this exact
	// string, so it is persisted rather than rebuilt.
	WebRTCDeviceID string `json:"webrtc_device_id,omitempty"`

	// ADBPort is the host-side ADB port, allocated by instance slot from 6520.
	// It records provenance and is not a dial target. The host firewall opens
	// 6520 alone, so instances past the first are reachable only over the
	// operator WebSocket bridge.
	//
	// Derived from the host's adb_serial ("0.0.0.0:6520"), whose address half
	// is always the wildcard and so carries nothing worth storing twice.
	ADBPort int64 `json:"adb_port,omitzero"`

	// Slot is the Cuttlefish instance number this instance occupies, which is
	// what binds it to its TAP device, its vsock CID and its group's radio
	// ports. Slots are HOST-GLOBAL and cvd takes them one at a time,
	// lowest-free-first, so a group created on a churned host holds
	// non-contiguous slots: 2, 5, 8 is an ordinary three-instance group, not a
	// pathological one, and anything keyed on position mis-seats every entry
	// past the first hole. Zero means unknown, because the host published no
	// serial to recover it from.
	Slot int64 `json:"slot,omitzero"`

	// Displays are the host's own rendered descriptions ("720 x 1280 ( 320 )"),
	// one per display. The structured form lives behind the per-instance
	// displays route; this is the cheap copy that arrives with the create.
	Displays []string `json:"displays,omitempty"`
}

// Device represents a Cuttlefish virtual device instance running on a cloud host.
//
// A Device is a virtual Android device created from a specific Android build.
// It runs on a Host VM and can be accessed via ADB, WebRTC streaming, or VNC.
// Devices have a lifecycle that includes creation, running, stopping, and deletion,
// with optional TTL-based expiration for ephemeral devices.
type Device struct {
	// ID is the unique identifier for this device (UUID format).
	ID string `json:"id"`

	// HostID is the ID of the host VM running this device.
	HostID string `json:"host_id"`

	// Name is the human-readable name for this device.
	Name string `json:"name"`

	// CVDID is the Cuttlefish Virtual Device identifier assigned by the
	// host orchestrator (e.g., "cvd-1", "cvd-2").
	CVDID string `json:"cvd_id,omitempty"`

	// Status is the current lifecycle state of the device.
	Status Status `json:"status"`

	// BuildID is the Android build identifier from AOSP CI.
	BuildID string `json:"build_id,omitempty"`

	// BuildTarget is the Android build target (e.g., "aosp_cf_x86_64_phone-trunk_staging-userdebug").
	BuildTarget string `json:"build_target,omitempty"`

	// ADBPort is the TCP port for ADB connections to this device.
	ADBPort int64 `json:"adb_port,omitzero"`

	// WebRTCPort is the TCP port for WebRTC streaming connections.
	WebRTCPort int64 `json:"webrtc_port,omitzero"`

	// GPUMode is the GPU rendering mode for this device.
	GPUMode GPUMode `json:"gpu_mode"`

	// CreatedBy is the email of the creator (informational/audit only).
	CreatedBy *string `json:"created_by,omitempty"`

	// ErrorMessage contains details when the device is in error state.
	ErrorMessage string `json:"error_message,omitempty"`

	// StartedAt is the timestamp when the device was last started.
	StartedAt *time.Time `json:"started_at,omitempty"`

	// StoppedAt is the timestamp when the device was last stopped.
	StoppedAt *time.Time `json:"stopped_at,omitempty"`

	// Tags are informational labels attached at creation (display/search only).
	Tags []string `json:"tags,omitempty"`

	// CreatedAt is the timestamp when the device record was created.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is the timestamp when the device record was last updated.
	UpdatedAt time.Time `json:"updated_at"`

	// HealthStatus is the current health monitoring status.
	HealthStatus HealthStatus `json:"health_status,omitempty"`

	// LastHealthCheck is the timestamp of the last health check.
	LastHealthCheck *time.Time `json:"last_health_check,omitempty"`

	// ConsecutiveFails is the number of consecutive health check failures.
	ConsecutiveFails int `json:"consecutive_fails,omitzero"`

	// LastFailureReason describes the most recent health check failure.
	LastFailureReason string `json:"last_failure_reason,omitempty"`

	// LastHealthyAt is the timestamp when the device was last healthy.
	LastHealthyAt *time.Time `json:"last_healthy_at,omitempty"`

	// CVDStatus is the raw CVD fleet status last observed on the host (one of
	// the CVDState* values). It is the device's observation column, written by
	// the device controller and, during the create window, by the create-wait
	// sampler (always in the same write as BootObservation: the derivation
	// reads both, and a fresh enum against a stale count settles wrongly).
	// device.DerivePhase consumes it. Empty means the CVD was absent from the
	// host's fleet listing.
	CVDStatus string `json:"cvd_status,omitempty"`

	// BootObservation is the boot-progress observation beside CVDStatus:
	// marker counts and log growth sampled from the CVD's own logs by
	// the create-wait sampler during create and by the device controller
	// after. Nil until something enriches.
	BootObservation *BootObservation `json:"boot_observation,omitempty"`

	// Lifecycle contains TTL and pinning state for the device.
	Lifecycle DeviceLifecycle `json:"lifecycle"`

	// Mode indicates how the device was created: "simple" or "advanced".
	// Simple mode uses sensible defaults, advanced mode allows full CVD configuration.
	Mode string `json:"mode,omitempty"`

	// InstanceCount is the DESIRED number of CVD instances, derived from
	// env_config at admission and written before any instance exists. Simple
	// mode is always 1.
	//
	// Not redundant with len(Instances), and not replaceable by it: this is
	// spec and that is observation, and the create separates them in time. The
	// host's device counter is charged this many slots at admission and
	// refunded the same number at delete, so a refund derived from the
	// observed array would refund zero for a create that failed before the
	// host answered, and the counter floors at 0, making that leak permanent.
	InstanceCount int `json:"instance_count,omitzero"`

	// Instances is what the host actually reported: one entry per Android VM
	// in this device's CVD group, in the order the host listed them. Empty
	// until the create response arrives. Every reachability and health path
	// reads this; the accounting paths read InstanceCount above.
	Instances []Instance `json:"instances,omitempty"`

	// EnvConfig stores the full CVD configuration used for advanced mode creation.
	// Persisted (with Mode/InstanceCount) as the row's desired_spec so a stranded
	// create is re-derivable from the row, which is how device.reconcile heals it.
	EnvConfig map[string]any `json:"env_config,omitempty"`

	// RecreateCount / RecreateWindowStart bound health-driven CVD recreation:
	// a zombie device is recreated from DesiredSpec at most a budget of
	// times per rolling window before it is failed explicitly.
	RecreateCount       int64      `json:"recreate_count,omitzero"`
	RecreateWindowStart *time.Time `json:"recreate_window_start,omitempty"`

	// RestartCount / RestartWindowStart bound the cheap first rung of zombie
	// recovery: a present-but-unresponsive CVD (cvd_status Unreachable) gets an
	// in-place restart at most a budget of times per rolling window before
	// falling through to a full recreate.
	RestartCount       int64      `json:"restart_count,omitzero"`
	RestartWindowStart *time.Time `json:"restart_window_start,omitempty"`

	// BugreportCapturedAt marks that policy-gated capture-on-error has captured
	// (or determined it cannot capture) a bugreport for this error device. An
	// internal reconciler marker, never surfaced in the API. It is the durable
	// dedup that stops the failed-capture detector re-enqueuing the same error
	// row.
	BugreportCapturedAt *time.Time `json:"-"`

	// HostName is the name of the host VM (enrichment field, not stored in DB).
	HostName string `json:"host_name,omitempty"`

	// HostZone is the GCE zone of the host VM (enrichment field, not stored in DB).
	HostZone string `json:"host_zone,omitempty"`

	// HostExternalIP is the external IP of the host VM (enrichment field, not stored in DB).
	HostExternalIP string `json:"host_external_ip,omitempty"`

	// HostInternalIP is the internal IP of the host VM (enrichment field, not stored in DB).
	HostInternalIP string `json:"host_internal_ip,omitempty"`

	// Reconciling is the AIP-128 output-only signal that the system is actively
	// converging this device toward its desired state. Derived (IsReconciling),
	// never stored; computed on read.
	Reconciling bool `json:"reconciling,omitempty"`

	// BatchID is the batch this device belongs to, or "" for a standalone
	// device. Set once at create; a batch is a label over its member devices.
	BatchID string `json:"batch_id,omitempty"`
}

// DesiredInstanceCount is how many instance slots this device costs its host,
// the quantity charged against the host's device counter at admission and
// refunded, unchanged, at delete. Both ends must call this and neither may use
// len(Instances): the observed array is empty until the host answers, so a
// refund derived from it refunds nothing for a create that failed early, and
// the counter floors at zero, which makes that leak permanent.
//
// Zero reads as one. A device row written before the spec carried a count
// restores InstanceCount as 0, and charging zero slots would let a host accept
// devices without ever filling up. An under-count is the same defect as an
// over-count, pointing the other way.
func (d *Device) DesiredInstanceCount() int {
	return max(1, d.InstanceCount)
}

// IsReconciling reports whether the system is actively working to converge this
// device toward its desired state (AIP-128): transitional statuses and
// health-driven recovery are "still settling"; a steady running-healthy device,
// a stopped one, and the terminal error/deleted states are not.
func (d *Device) IsReconciling() bool {
	switch d.Status {
	case StatusPending, StatusCreating, StatusStarting, StatusStopping, StatusDeleting:
		return true
	case StatusRunning:
		return d.HealthStatus == HealthStatusZombie // health-driven restart/recreate in flight
	case StatusStopped, StatusDeleted, StatusError:
		return false
	}
	return false
}

// TransitionTo attempts to transition the device to a new status.
// It returns an *InvalidStateError if the transition is not allowed by the
// state machine, so the rejection matches ErrInvalidState and reaches the 409
// arm and the reaper's tolerance. On success, it updates the device's
// UpdatedAt timestamp.
func (d *Device) TransitionTo(newStatus Status) error {
	if !d.Status.CanTransitionTo(newStatus) {
		return &InvalidStateError{Current: d.Status, Expected: newStatus}
	}
	d.Status = newStatus
	d.UpdatedAt = time.Now()
	return nil
}

// SetError sets the device to error state with a descriptive message.
// This bypasses the normal state transition checks and directly sets StatusError.
func (d *Device) SetError(message string) {
	d.Status = StatusError
	d.ErrorMessage = message
	d.UpdatedAt = time.Now()
}

// Validate ensures the device has all required fields set correctly.
// Returns an error describing the first validation failure found.
func (d *Device) Validate() error {
	if d.Name == "" {
		return errors.New("device name is required")
	}
	if d.HostID == "" {
		return errors.New("host ID is required")
	}
	return nil
}

// CreateRequest contains the parameters for creating a new device.
//
// The build is named either explicitly, by BuildID and BuildTarget, or by
// Requirements: a declarative create resolves the requirement into those same
// two fields before the device is committed.
type CreateRequest struct {
	// HostID is the ID of the host to create the device on. Required unless
	// Requirements is set (declarative create: the platform picks or
	// provisions the host). Exactly one of HostID and Requirements must be
	// provided.
	HostID string `json:"host_id"`

	// Requirements is the declarative alternative to HostID: intent the
	// platform resolves into a build and a host placement.
	Requirements *Requirements `json:"requirements,omitempty"`

	// Name is the human-readable name for the device (required).
	Name string `json:"name"`

	// BuildID is the Android build ID from AOSP CI (required for AOSP builds).
	BuildID string `json:"build_id,omitempty"`

	// BuildTarget is the Android build target (required for AOSP builds).
	BuildTarget string `json:"build_target,omitempty"`

	// GPUMode specifies the GPU rendering mode. Defaults to auto-detect.
	GPUMode GPUMode `json:"gpu_mode,omitempty"`

	// Mode indicates whether this is a simple or advanced mode creation.
	// Simple mode uses sensible defaults, advanced mode uses the full EnvConfig.
	Mode string `json:"mode,omitempty"`

	// EnvConfig is the full CVD configuration for advanced mode creation.
	// This follows the CVD canonical configuration format and is passed
	// directly to the Host Orchestrator.
	EnvConfig map[string]any `json:"env_config,omitempty"`

	// TTLHours overrides the default session TTL for ephemeral devices.
	// Zero means the default TTL. Ignored when Pinned is set.
	TTLHours int `json:"ttl_hours,omitempty"`

	// Pinned creates the device already pinned (exempt from TTL reaping).
	Pinned bool `json:"pinned,omitempty"`

	// PinnedReason is required when Pinned is set.
	PinnedReason PinReason `json:"pinned_reason,omitempty"`

	// PinnedReasonDetail is required when PinnedReason is PinReasonOther.
	PinnedReasonDetail string `json:"pinned_reason_detail,omitempty"`

	// PinnedUntil bounds a born-pinned device's exemption; nil pins it
	// indefinitely. Ignored unless Pinned is set.
	PinnedUntil *time.Time `json:"pinned_until,omitempty"`

	// Tags are informational labels stored on the device.
	Tags []string `json:"tags,omitempty"`

	// IdempotencyKey, when set, makes Create safe to retry: a repeated request
	// with the same key returns the originally created device. Sourced from the
	// Idempotency-Key header, not the JSON body.
	IdempotencyKey string `json:"-"`

	// FingerprintOverride carries the Idempotency-Key fingerprint of the
	// user's ORIGINAL declarative request through placement resolution:
	// commitResolved rewrites HostID and clears Requirements before
	// re-entering Create, and the stored key must record what the user sent.
	// Fingerprinting the resolved form made a valid replay that resolved to
	// a different host read as a key conflict. json:"-" keeps it off the wire
	// and out of the fingerprint itself. Set only by CreateDeclarative.
	FingerprintOverride string `json:"-"`

	// BatchID tags the device as a member of a batch. Set only by the batch
	// service (batch membership is managed via /v2/batches), never from the
	// device request body.
	BatchID string `json:"-"`

	// GPUDegraded records whether this create wanted a GPU and got a host
	// without one. Three states, so a pointer: nil means the question does not
	// apply (the create named its own host, or never wanted a GPU), and only
	// those creates stay out of the ratio this feeds.
	//
	// It rides the request for the same reason FingerprintOverride does:
	// commitResolved clears Requirements before re-entering Create, so by the
	// time the audit entry is built the intent is gone and nothing there can
	// re-derive it. json:"-" keeps it off the wire and out of the fingerprint,
	// which is a hash of the JSON form. Set only by the declarative path.
	GPUDegraded *bool `json:"-"`
}

// BugreportArtifact locates a captured bugreport in artifact storage.
type BugreportArtifact struct {
	// Bucket is the GCS bucket the bugreport was stored in.
	Bucket string `json:"bucket"`
	// Object is the object path within the bucket.
	Object string `json:"object"`
}

// GSURI returns the artifact's gs:// URI.
func (a *BugreportArtifact) GSURI() string {
	return "gs://" + a.Bucket + "/" + a.Object
}

// RecordingArtifact locates a fetched screen recording in artifact storage.
type RecordingArtifact struct {
	// Bucket is the GCS bucket the recording was stored in.
	Bucket string `json:"bucket"`
	// Object is the object path within the bucket.
	Object string `json:"object"`
}

// GSURI returns the artifact's gs:// URI.
func (a *RecordingArtifact) GSURI() string {
	return "gs://" + a.Bucket + "/" + a.Object
}

// CVDGroupName is the deterministic host-side CVD group for a device. Group
// names must start with a letter, so prefix with "cvd". Create, cleanup, and
// the device controller's census all derive it from the device ID, so a group
// left by a create attempt that died before recording CVDID is still
// findable.
//
// The ID is truncated rather than sliced: an unchecked deviceID[:8] panics on
// anything shorter, and the census derives this for EVERY device row on a host
// (including deleted ones) inside an unrecovered errgroup goroutine, so one
// short id would take the process down, and the process also serves the API.
// Production ids are UUIDs, which is the only reason that has never happened.
// A shorter id yields a shorter group, which is still deterministic and still
// starts with a letter; nothing depends on the length.
func CVDGroupName(deviceID string) string {
	return "cvd" + deviceID[:min(8, len(deviceID))]
}

// WarmGroupName is the reserved CVD group the warm plane's throwaway
// prefetch creates use. No device row ever claims it, so the orphan-CVD
// census must know the name to exempt it, so it lives in the domain
// vocabulary where both the warm plane and the census can read it. Device
// groups are always "cvd"-prefixed, so the name can never collide with one.
const WarmGroupName = "ccowarm"

// ParseCVDID splits a device's recorded CVDID ("group/name") into its group
// and name, in that order. A value with no slash yields an empty group and
// the whole value as the name. Callers treat that as unaddressable, and must
// refuse rather than substitute an instance name of their own: the host names
// a group's instances itself, so any guess addresses a different device.
func ParseCVDID(cvdID string) (string, string) {
	if g, n, ok := strings.Cut(cvdID, "/"); ok {
		return g, n
	}
	return "", cvdID
}
