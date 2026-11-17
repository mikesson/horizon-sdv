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

package host

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// Status represents the lifecycle state of a host VM.
type Status string

// Desired states, the SPEC vocabulary: what the user asked for, as
// opposed to the observed/derived Status. Delete is expressed as
// DesiredAbsent; the row is the finalizer until teardown confirms.
const (
	DesiredRunning = "running"
	DesiredStopped = "stopped"
	DesiredAbsent  = "absent"
)

const (
	// StatusPending indicates that creation has been requested but not started.
	StatusPending Status = "pending"
	// StatusProvisioning indicates the GCE instance is currently being created.
	StatusProvisioning Status = "provisioning"
	// StatusStarting indicates the instance is booting up.
	StatusStarting Status = "starting"
	// StatusRunning indicates the host is ready to accept devices.
	StatusRunning Status = "running"
	// StatusStopping indicates the host is shutting down.
	StatusStopping Status = "stopping"
	// StatusStopped indicates the instance is stopped (but not deleted).
	StatusStopped Status = "stopped"
	// StatusDeleting indicates the host is in the process of being deleted.
	StatusDeleting Status = "deleting"
	// StatusDeleted indicates the host and its resources have been removed.
	StatusDeleted Status = "deleted"
	// StatusError indicates the host encountered an unrecoverable error.
	StatusError Status = "error"
	// StatusUnreachable indicates the orchestrator cannot connect to the host.
	StatusUnreachable Status = "unreachable"
	// StatusPreempted indicates the Spot VM was preempted by GCE.
	StatusPreempted Status = "preempted"
)

// ProvisioningModel represents the GCE VM provisioning model.
type ProvisioningModel string

const (
	// ProvisioningModelStandard is a regular on-demand VM (default).
	ProvisioningModelStandard ProvisioningModel = "STANDARD"
	// ProvisioningModelSpot is a preemptible Spot VM with discounted pricing.
	ProvisioningModelSpot ProvisioningModel = "SPOT"
)

// ValidTransitions defines the allowed state machine transitions for a Host.
// Any pre-terminal state may enter deleting: a user can cancel a host that is
// still pending or mid-provision. provisioning->running reflects the provision
// task observing the VM come up. Creation persists no intermediate starting
// state.
var ValidTransitions = map[Status][]Status{
	StatusPending:      {StatusProvisioning, StatusDeleting, StatusError, StatusDeleted},
	StatusProvisioning: {StatusStarting, StatusRunning, StatusDeleting, StatusError, StatusDeleted, StatusPreempted},
	StatusStarting:     {StatusRunning, StatusDeleting, StatusError, StatusDeleted, StatusPreempted},
	// running->stopped and stopped->running cover externally observed drift:
	// a VM stopped or started outside the orchestrator between GCE sync polls
	// skips the transient STOPPING/STAGING window.
	//
	// stopping->preempted and stopped->preempted exist because a dying Spot
	// VM is OBSERVED before it is understood: the drift writer legitimately
	// adopts running->stopping/stopped from the VM's STOPPING/TERMINATED
	// window, and the preemption detector must still be able to reclassify.
	// Without these, whichever observation landed first foreclosed
	// recovery. MarkPreempted remains the only writer targeting
	// preempted, gated on Spot hosts with operation evidence.
	//
	// stopping->running is a stop that did not happen: a failed GCE stop
	// returns desired_state to running, and a VM seen mid-stop outside CCO
	// can come back RUNNING. stopping->starting stays illegal, so a user
	// start never races a stop's device settle.
	StatusRunning:     {StatusStopping, StatusStopped, StatusDeleting, StatusDeleted, StatusError, StatusUnreachable, StatusPreempted},
	StatusStopping:    {StatusStopped, StatusRunning, StatusDeleting, StatusDeleted, StatusError, StatusPreempted},
	StatusStopped:     {StatusStarting, StatusRunning, StatusDeleting, StatusDeleted, StatusPreempted},
	StatusDeleting:    {StatusDeleted, StatusError},
	StatusError:       {StatusDeleting, StatusDeleted, StatusProvisioning},
	StatusUnreachable: {StatusRunning, StatusError, StatusDeleting, StatusDeleted, StatusPreempted},
	StatusPreempted:   {StatusProvisioning, StatusDeleting, StatusDeleted, StatusError},
}

// CanTransitionTo checks if the status transition is valid
func (s Status) CanTransitionTo(target Status) bool {
	allowed, ok := ValidTransitions[s]
	if !ok {
		return false
	}
	return slices.Contains(allowed, target)
}

// vmExpectedStatuses are the statuses in which the host's GCE instance must
// exist: reachable only after a successful provision and before any teardown
// has begun. Everywhere else an absent VM is a normal lifecycle window, not
// out-of-band deletion. Pending/provisioning precede the instance insert,
// deleting/deleted/error may follow its removal, and a preempted Spot VM is
// deleted by GCE itself (termination action DELETE) pending re-provisioning.
var vmExpectedStatuses = map[Status]bool{
	StatusStarting:    true,
	StatusRunning:     true,
	StatusStopping:    true,
	StatusStopped:     true,
	StatusUnreachable: true,
}

// ExpectsVM reports whether an absent GCE instance in this status means the
// VM was deleted outside the orchestrator, rather than simply not created yet
// or already torn down. Only in these statuses may a host be marked lost.
func (s Status) ExpectsVM() bool { return vmExpectedStatuses[s] }

// IsTerminal returns true if no further transitions are expected
func (s Status) IsTerminal() bool {
	return s == StatusDeleted
}

// IsHealthy returns true if the host is in a healthy state
func (s Status) IsHealthy() bool {
	return s == StatusRunning
}

// IsRetryable returns true if the host can be retried (recreated).
func (s Status) IsRetryable() bool {
	return s == StatusPreempted || s == StatusError
}

// IsSpot returns true if the provisioning model is Spot.
func (p ProvisioningModel) IsSpot() bool {
	return p == ProvisioningModelSpot
}

// IsValid returns true if the provisioning model is valid.
func (p ProvisioningModel) IsValid() bool {
	return p == ProvisioningModelStandard || p == ProvisioningModelSpot || p == ""
}

// Host represents a GCE VM running the Cuttlefish Host Orchestrator
//
// This struct is intended to be used as a Data Transfer Object (DTO)
// within the scope of a single request or operation.
type Host struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Zone            string `json:"zone"`
	GCEInstanceName string `json:"gce_instance_name,omitempty"`

	// RequestedZone is the placement intent the caller supplied (a /v2 pin, a
	// location hint, or a /v1 zone token); empty = auto (server-ranked). It is
	// SPEC (immutable after admission), where Zone above is the mutable
	// binding failover may rewrite. ZonePinned means the caller named an exact
	// zone: placement uses it verbatim and failover retries in place instead
	// of moving the host.
	RequestedZone string `json:"requested_zone,omitempty"`
	ZonePinned    bool   `json:"zone_pinned,omitempty"`

	// Incarnation counts re-provisions (retry, preemption recovery). The GCE
	// instance name embeds it (incarnation 0 = the legacy unsuffixed name),
	// so a recovered host gets a FRESH instance: the old husk and the new VM
	// can never collide (immutable-infrastructure model; kills the F10 husk
	// race structurally). Stale-incarnation husks are unclaimed names. Spot
	// auto-delete removes them, and the orphan GC catches the rest.
	Incarnation int64 `json:"incarnation,omitempty"`

	// DesiredState is the SPEC half of the spec/status split: what the user
	// asked for (running | stopped | absent), written only by intent paths
	// (create/start/stop/delete/retry). DerivePhase reads it; those same
	// intent paths also transition status directly.
	DesiredState string `json:"desired_state,omitempty"`

	// VMStatus is the raw GCE instance state as last observed (RUNNING,
	// TERMINATED, ...). It is an observation column written by the GCE-observation
	// writers (the host reconciler's drift sweep, the provision read-back, and the manual
	// Sync, all via ApplyVMObservation or planGCEUpdate), recorded even when the
	// lifecycle-status write is blocked by an app-owned state. VMObservedAt
	// stamps the last change; newest observation wins.
	VMStatus     string     `json:"vm_status,omitempty"`
	VMObservedAt *time.Time `json:"vm_observed_at,omitempty"`

	GCEProject  string `json:"gce_project,omitempty"`
	MachineType string `json:"machine_type,omitempty"`
	GPUType     string `json:"gpu_type,omitempty"`
	GPUCount    int64  `json:"gpu_count"`
	// MinCPUPlatform is passed to GCE verbatim; empty = no constraint. Like
	// the machine shape, it is spec applied at incarnation birth, never
	// drift-corrected on a live VM.
	MinCPUPlatform string `json:"min_cpu_platform,omitempty"`
	// BootDiskSizeGB is the requested boot disk size; 0 = DefaultBootDiskSizeGB.
	BootDiskSizeGB int64 `json:"boot_disk_size_gb,omitzero"`
	// BootImage is the self link of the image the current incarnation was
	// inserted from, resolved from its machine family's image family at
	// provision time. Empty until the first provision attempt.
	BootImage            string            `json:"boot_image,omitempty" doc:"Self link of the GCE image this host's VM was created from; empty until provisioning starts"`
	ServiceAccount       string            `json:"service_account,omitempty"` // GCE VM service account
	ProvisioningModel    ProvisioningModel `json:"provisioning_model,omitempty"`
	Status               Status            `json:"status"`
	IPAddress            string            `json:"ip_address,omitempty"`
	InternalIP           string            `json:"internal_ip,omitempty"`
	HostOrchestratorPort int64             `json:"host_orchestrator_port"`
	MaxDevices           int64             `json:"max_devices"`
	CurrentDeviceCount   int64             `json:"current_device_count"`

	// Exclusive fences the host from automatic placement (declarative single
	// creates, batch pack placement); targeting it by id still works. Set by
	// batch isolated provisioning and by users protecting in-flight tests
	// from noisy neighbors.
	Exclusive bool `json:"exclusive,omitempty"`
	// ForeignCVDs marks a host whose CVDs CCO does not create or own. It is
	// set only by the legacy docker-shape create path, whose clients drive the
	// Host Orchestrator directly through the /v1 proxy. The device
	// controller's orphan census skips such a host: with no device rows ever
	// claiming its CVDs, every one would read as a leak.
	ForeignCVDs bool `json:"foreign_cvds,omitempty"`
	// BatchID is the batch that provisioned this host (empty for user-created
	// hosts). The reaper early-reaps emptied batch hosts by it.
	BatchID string `json:"batch_id,omitempty"`

	// Origin is which path created the host. Stamped at create and never
	// rewritten; empty means the row predates the column or a create site
	// failed to stamp, which Class reads as reserved.
	Origin Origin `json:"origin,omitempty" enum:"user,auto,pool,batch" doc:"Which path created this host: user (an API create), auto (a declarative create that found no capacity), pool (a fleet top-up) or batch"`

	// Pool is which subset of the fleet this host belongs to, the one whose
	// shape, floor and buffer it was bought against. Stamped at create and
	// never rewritten; a row naming none reads as PoolDefault.
	Pool string `json:"pool" doc:"Which subset of the fleet this host belongs to; each pool carries its own shape, floor and standby buffer"`

	// Class is whose lifecycle rules apply. It is derived from Origin,
	// Exclusive and BatchID (DeriveClass), never stored, computed on read like
	// Reconciling. Output-only: no request sets it.
	Class Class `json:"class,omitempty" enum:"reserved,fleet,dedicated,batch" doc:"Whose lifecycle rules apply: reserved (runs its full lease, only a user delete reclaims it), fleet (managed standing capacity), dedicated (fenced to one group, reclaimed when empty) or batch (owned by batch settlement)"`

	// CreatedBy is the email of the creator (informational/audit only).
	CreatedBy    *string `json:"created_by,omitempty"`
	ErrorMessage string  `json:"error_message,omitempty"`
	// ErrorType is the GCE provisioning classification, written only by the
	// provision-failure path from gce.ClassifyError. The enum is spelled out
	// because domain cannot import platform; gce.ErrorTypes() is the source and
	// an agreement test in the host slice binds the two.
	ErrorType             string     `json:"error_type,omitempty" enum:"zone_not_found,zone_down,machine_type_not_in_zone,gpu_not_in_zone,gpu_count_exceeded,gpu_not_attachable,quota_cpu_exceeded,quota_gpu_exceeded,quota_ip_exceeded,quota_ssd_exceeded,quota_hyperdisk_exceeded,quota_instances_exceeded,permission_denied,resource_exhausted,resource_not_ready,rate_limited,network_not_found,subnet_not_found,image_not_found,unknown"`
	ErrorCode             string     `json:"error_code,omitempty"`
	ErrorSuggestion       string     `json:"error_suggestion,omitempty"`
	ErrorAlternativeZones []string   `json:"error_alternative_zones,omitempty"`
	ErrorRetryable        bool       `json:"error_retryable,omitempty"`
	ErrorOccurredAt       *time.Time `json:"error_occurred_at,omitempty"`
	StoppedAt             *time.Time `json:"stopped_at,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`

	// VMTerminatesAt is when GCE will stop the VM under its max_run_duration,
	// as the reconciler last observed it. Nil means not yet observed or not
	// running. No lease raise may pass it (CheckVMHorizon).
	VMTerminatesAt *time.Time `json:"vm_terminates_at,omitempty" doc:"When Compute Engine will stop this host's VM under its maximum run duration; a restart resets it. Absent until observed, and while the VM is not running"`

	// Spot VM preemption tracking fields. PreemptedIncarnation is the
	// ho_reachable-style CONDITION that makes 'preempted' a derived phase: the
	// incarnation with an unrecovered preemption. DerivePhase yields preempted
	// while it equals Incarnation (immediate, with no vm_status lag); RetryDelayed
	// bumps Incarnation on recovery, which flips the derivation off without a
	// clear and without a re-preemption loop. PreemptedAt/PreemptionCount drive
	// the auto-recovery cap and its 24h decay window; LastPreemptionID dedupes
	// the ops scan.
	PreemptedAt          *time.Time `json:"preempted_at,omitempty"`
	PreemptedIncarnation *int64     `json:"preempted_incarnation,omitempty"`
	PreemptionCount      int64      `json:"preemption_count"`
	LastPreemptionID     string     `json:"last_preemption_id,omitempty"`

	// Host-poison condition. A CVD launch killed uncleanly leaks
	// run_cvd/crosvm holding tap devices + the operator device-ID
	// registration with no group record, invisible to every drift check, so
	// every future create on the host fails the same way. These count that
	// failure signature; at a threshold DegradedAt/DegradedReason mark the host
	// degraded (a CONDITION, not a Status: the GCE lifecycle stays running so
	// the host reconciler keeps reconciling it), which fences it from placement and queues
	// remediation. Cleared by a successful create.
	ConsecutiveCreateFails int64      `json:"consecutive_create_fails,omitzero"`
	LastCreateFailReason   string     `json:"last_create_fail_reason,omitempty"`
	DegradedAt             *time.Time `json:"degraded_at,omitempty"`
	DegradedReason         string     `json:"degraded_reason,omitempty"`

	// Provision-blocked condition. A host still coming up is capacity
	// placement may land on, so concurrent creates pack onto the host the
	// first one started instead of each buying another. That holds only while
	// the provision is making progress: once GCE has refused it for capacity
	// (a stockout, an exhausted quota, a zone that is down) the host is a bet
	// that may not pay off for the whole retry ladder, and every create placed
	// onto it waits that ladder out instead of provisioning a shape that has
	// capacity. Set by the provision-failure path on a classified capacity
	// failure; cleared when a provision succeeds or a retry starts a fresh
	// incarnation. Only a pending or provisioning host is fenced by it: a host
	// whose VM exists serves creates whatever its history says.
	ProvisionBlockedAt     *time.Time `json:"provision_blocked_at,omitempty" doc:"When GCE last refused this host's provision for capacity; while set, automatic placement skips the host until its VM exists"`
	ProvisionBlockedReason string     `json:"provision_blocked_reason,omitempty" doc:"The classified capacity failure that blocked the provision"`

	// StalledReason is the kstatus Stalled condition: the host is still
	// converging toward DesiredState but is stuck (a teardown whose delete
	// keeps failing, a recovery that cannot find capacity). It never changes
	// the derived phase. A stalled teardown stays 'deleting' and keeps being
	// re-driven. The field only records why convergence is not progressing.
	StalledReason string `json:"stalled_reason,omitempty"`

	// HOReadyAt is the ho_reachable condition: when the Host Orchestrator first
	// answered this incarnation. nil => a RUNNING VM derives 'starting' (HO
	// still booting); set => 'running'. It is a latch, set once by the device
	// controller and reset to nil only on a fresh VM (retry/recovery/Start), so
	// a later HO drop degrades device health rather than regressing the host phase.
	HOReadyAt *time.Time `json:"ho_ready_at,omitempty"`

	// Lifecycle management fields
	Lifecycle HostLifecycle `json:"lifecycle"`

	// Reconciling is the AIP-128 output-only signal that the system is actively
	// converging this host toward its desired state. Derived (IsReconciling),
	// never stored; computed on read.
	Reconciling bool `json:"reconciling,omitempty"`
}

// IsReconciling reports whether the system is actively working to converge this
// host toward its desired state (AIP-128): transitional statuses, preemption
// recovery, and a teardown that keeps failing (still re-driven toward absent)
// are "still settling"; a steady running host and the terminal
// stopped/error/deleted states are not.
func (h *Host) IsReconciling() bool {
	switch h.Status {
	case StatusPending, StatusProvisioning, StatusStarting, StatusStopping, StatusDeleting, StatusPreempted:
		return true
	case StatusRunning, StatusStopped, StatusDeleted, StatusError, StatusUnreachable:
		// Steady or terminal; only a stalled teardown still counts as converging.
	}
	return h.StalledReason != ""
}

// HostOrchestratorURL returns the full URL for the host orchestrator agent.
// It prefers the internal IP address for Cloud Run VPC connectivity.
// Cloud Run is configured with vpc-access-egress: private-ranges-only,
// which means it can only reach private IPs (10.x.x.x), not external IPs.
func (h *Host) HostOrchestratorURL() string {
	ip := h.InternalIP // Internal IP for VPC connectivity
	if ip == "" {
		ip = h.IPAddress // Fallback to external IP
	}
	if ip == "" {
		return ""
	}
	return fmt.Sprintf("http://%s:%d", ip, h.HostOrchestratorPort)
}

// HasCapacity returns true if the host can accept more devices
func (h *Host) HasCapacity() bool {
	return h.CurrentDeviceCount < h.MaxDevices && h.Status == StatusRunning
}

// AvailableSlots returns the number of device slots available
func (h *Host) AvailableSlots() int64 {
	if h.CurrentDeviceCount >= h.MaxDevices {
		return 0
	}
	return h.MaxDevices - h.CurrentDeviceCount
}

// TransitionTo attempts to transition to a new status. A rejection is an
// *InvalidStateError, so it matches ErrInvalidState wherever a caller returns
// it unwrapped (the provision task's mid-delete case, the retry path) instead
// of reaching the transport as an unclassified 500.
func (h *Host) TransitionTo(newStatus Status) error {
	if !h.Status.CanTransitionTo(newStatus) {
		return &InvalidStateError{Current: string(h.Status), Expected: string(newStatus)}
	}
	h.Status = newStatus
	h.UpdatedAt = time.Now()
	return nil
}

// PreemptionCountWindow bounds how long past preemptions count toward the
// auto-recovery cap: a host quiet for longer starts a fresh window. The
// chronic-thrash protection must not become a lifetime ban, because capacity
// pressure in a zone is episodic and not a property of the host.
const PreemptionCountWindow = 24 * time.Hour

// SetPreempted records a GCE preemption as a CONDITION and lets the phase fall
// out of the derivation. It no longer writes Status directly.
// PreemptedIncarnation pins the current incarnation as unrecovered;
// RecomputeStatus then publishes
// 'preempted' unless desired=absent or an error condition takes precedence (which
// is what stops a host the user is deleting from being resurrected). MarkPreempted's
// CanTransitionTo guard has already rejected an illegal preemption before this runs.
func (h *Host) SetPreempted(operationID string) {
	now := time.Now()
	if h.PreemptedAt != nil && now.Sub(*h.PreemptedAt) > PreemptionCountWindow {
		h.PreemptionCount = 0
	}
	h.PreemptedAt = &now
	inc := h.Incarnation
	h.PreemptedIncarnation = &inc
	h.PreemptionCount++
	h.LastPreemptionID = operationID
	h.UpdatedAt = now
	h.RecomputeStatus()
}

// IsSpotVM returns true if this host is a Spot VM.
func (h *Host) IsSpotVM() bool {
	return h.ProvisioningModel == ProvisioningModelSpot
}

// HostPoisonThreshold is the number of consecutive poison-signature create
// failures that marks a host degraded. A couple of transient collisions
// should not condemn a host, and a truly poisoned one fails every time.
const HostPoisonThreshold = 3

// IsDegraded reports whether the host is fenced from placement pending
// poison remediation.
func (h *Host) IsDegraded() bool { return h.DegradedAt != nil }

// MarkDegraded fences the host from placement and records why. Idempotent:
// re-marking keeps the original DegradedAt so remediation timing is stable.
func (h *Host) MarkDegraded(reason string) {
	if h.DegradedAt == nil {
		now := time.Now()
		h.DegradedAt = &now
	}
	h.DegradedReason = capReason(reason)
	h.UpdatedAt = time.Now()
}

// MarkProvisionBlocked records that GCE refused this host's provision for
// capacity, which fences the host from automatic placement until its VM
// exists. Idempotent: re-marking keeps the first ProvisionBlockedAt, so the
// condition dates from the first refusal, and updates the reason to the
// latest one.
func (h *Host) MarkProvisionBlocked(reason string) {
	if h.ProvisionBlockedAt == nil {
		now := time.Now()
		h.ProvisionBlockedAt = &now
	}
	h.ProvisionBlockedReason = capReason(reason)
	h.UpdatedAt = time.Now()
}

// ClearProvisionBlocked lifts the provision-blocked condition, because a
// provision succeeded or a retry is starting a fresh incarnation.
func (h *Host) ClearProvisionBlocked() {
	h.ProvisionBlockedAt = nil
	h.ProvisionBlockedReason = ""
}

// ClearDegraded lifts the degraded condition and resets the failure counter,
// because remediation succeeded or a create landed cleanly.
func (h *Host) ClearDegraded() {
	h.DegradedAt = nil
	h.DegradedReason = ""
	h.ConsecutiveCreateFails = 0
	h.LastCreateFailReason = ""
	h.UpdatedAt = time.Now()
}

// capReason bounds a reason to the 500-char condition columns.
func capReason(s string) string {
	const maxLen = 500
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen-1]) + "…"
}

// RecordCreateFailure counts one poison-signature create failure against the
// host and, at the threshold, marks it degraded. It reports whether THIS call
// newly degraded the host (so the caller can trigger remediation once).
func (h *Host) RecordCreateFailure(reason string) bool {
	h.ConsecutiveCreateFails++
	h.LastCreateFailReason = capReason(reason)
	h.UpdatedAt = time.Now()
	if h.ConsecutiveCreateFails >= HostPoisonThreshold && !h.IsDegraded() {
		h.MarkDegraded("repeated CVD-create failures (leaked taps / stale registration): " + reason)
		return true
	}
	return false
}

// CanRetry returns true if this host can be retried.
// For preempted hosts, retry is always allowed.
// For error hosts, retry is only allowed if ErrorRetryable is true.
func (h *Host) CanRetry() bool {
	if h.Status == StatusPreempted {
		return true
	}
	if h.Status == StatusError {
		return h.ErrorRetryable
	}
	return false
}

// Validate ensures the host data is valid
func (h *Host) Validate() error {
	if h.Name == "" {
		return errors.New("host name is required")
	}
	if h.Zone == "" {
		return errors.New("zone is required")
	}
	if h.MaxDevices <= 0 {
		return errors.New("max devices must be positive")
	}
	return nil
}

// MinBootDiskGB and MaxBootDiskGB bound the create-time boot-disk size
// (0 stays valid and means DefaultBootDiskSizeGB). The floor keeps a
// request from asking for less than any bootable host image; the ceiling is
// GCE's persistent-disk maximum. The API layer's minimum/maximum tags mirror
// these, and the agreement test in the host feature binds the two copies.
// The default is domain vocabulary because two surfaces consume it: the
// provisioning builder sizes the disk with it, and the legacy facade echoes
// it as the effective size of a default-sized host.
const (
	MinBootDiskGB         int64 = 10
	MaxBootDiskGB         int64 = 65536
	DefaultBootDiskSizeGB int64 = 400
)

// ValidateBootDiskSizeGB rejects an out-of-bounds boot-disk request. It backs
// the same rule the /v2 minimum/maximum tags enforce, for the paths that do
// not go through Huma (/v1 createHost).
func ValidateBootDiskSizeGB(gb int64) error {
	if gb != 0 && (gb < MinBootDiskGB || gb > MaxBootDiskGB) {
		return &BootDiskSizeError{RequestedGB: gb}
	}
	return nil
}

// CreateRequest represents a request to create a new host.
//
// Zone semantics: empty Zone means the service ranks placement across the
// enabled catalog (LocationHint biases the ranking). A non-empty Zone with
// ZonePinned means the caller named an exact zone: placement uses it
// verbatim and failover never moves the host. A non-empty Zone without
// ZonePinned is a pre-ranked binding (a caller that already ranked, e.g. a
// batch replicating one ranking across its host template): the host binds
// there but remains failover-eligible.
type CreateRequest struct {
	Name       string `json:"name"`
	Zone       string `json:"zone"`
	ZonePinned bool   `json:"zone_pinned,omitempty"`
	// LocationHint is a preferred region for ranked placement; recorded as
	// the host's requested_zone intent.
	LocationHint      string            `json:"location_hint,omitempty"`
	MachineType       string            `json:"machine_type,omitempty"`
	GPUType           string            `json:"gpu_type,omitempty"`
	GPUCount          int64             `json:"gpu_count,omitempty"`
	MinCPUPlatform    string            `json:"min_cpu_platform,omitempty"`
	BootDiskSizeGB    int64             `json:"boot_disk_size_gb,omitzero"`
	MaxDevices        int64             `json:"max_devices,omitempty"`
	ProvisioningModel ProvisioningModel `json:"provisioning_model,omitempty"`
	// TTLHours overrides the default 10h host TTL (0 = default). Device
	// activity can still raise the resulting expiry beyond it.
	TTLHours int64 `json:"ttl_hours,omitempty"`

	// Exclusive fences the host from automatic placement for its lifetime.
	Exclusive bool `json:"exclusive,omitempty"`

	// ForeignCVDs exempts the host from the orphan-CVD census (service-set
	// only, by the legacy docker-shape create path, never from the JSON
	// body, so a /v2 caller cannot opt out of leak detection).
	ForeignCVDs bool `json:"-"`

	// BatchID marks a host provisioned for a batch (service-set only, never
	// from the JSON body).
	BatchID string `json:"-"`

	// Pool is the subset of the fleet this host joins, as the placement
	// resolution judged it. Empty is the default pool.
	//
	// The service sets Pool from what a create needs; the host create body
	// has no such field. A caller asking for a machine by shape wants that
	// machine alone, so their host is reserved-class and enters no pool's
	// arithmetic.
	Pool string `json:"-"`

	// Resolved is what the placement resolver concluded about this create,
	// frozen at resolution time. Nil on every path a user drives, and on
	// anything else that builds a request by hand, because nobody judged it.
	//
	// One struct rather than loose fields, because they are stamped together or
	// not at all. Two of them are booleans, whose zero value is a claim
	// ("required no GPU", "did not fit"), so a request
	// that set only some would stamp a judgement nobody made, and the one it
	// would stamp reads as demand this fleet cannot serve.
	Resolved *ResolvedFacts `json:"-"`

	// IdempotencyKey, when set, makes Create safe to retry: a repeated request
	// with the same key returns the originally created host. Sourced from the
	// Idempotency-Key header, not the JSON body.
	IdempotencyKey string `json:"-"`
}

// ResolvedFacts is what the placement resolver established about one create,
// carried to the create's audit entry because the host row hard-deletes and the
// entry outlives it. Together they answer one question asked much later: could
// standing capacity have absorbed this create, or was provisioning the only
// possible answer?
//
// Each field is a different axis of that question, and they do not overlap. A
// create is beyond a standing buffer when ANY of them says so, so they are
// counted per reason and never first-fail.
//
// All three are frozen here rather than re-derived later because their inputs
// (the packing policy, the catalog's default shape, the set of enabled GPU
// types) are all runtime state. Re-reading them would let an operator changing
// a number reclassify a whole horizon of history.
type ResolvedFacts struct {
	// Density is how many instance slots a STANDBY host in this create's pool
	// would have offered. Compared against the create's own MaxDevices: a
	// ceiling above this is a create that outgrew what the buffer holds. Always
	// at least 1, so the entry can distinguish "judged" from "absent".
	Density int64

	// GPURequired is the create's HARD requirement. Never "the resolver picked
	// a GPU shape": an interactive create prefers a GPU and provisions one, yet
	// is servable by CPU standby, because placement degrades an unmet GPU
	// preference onto a CPU host. Stamping the provisioned shape would discard
	// exactly the demand a buffer can answer.
	GPURequired bool

	// FitsDefaultHost is whether the standing template of this create's pool
	// held its shape, its vCPU width and its summed guest RAM. It records a
	// verdict rather than its inputs, and it does not mean "did the resolver
	// grow the shape". Growth is relative to the RESOLVED base, and a GPU
	// create's base holds no default-shaped guest at all, so a grown-flag would
	// mark every interactive miss unservable.
	FitsDefaultHost bool

	// PreferredType is the machine shape this create WANTED and could not have,
	// empty when it got the one the fleet prefers. The only field here whose
	// zero value is an absence rather than a claim, which is why it is stamped
	// only when it is set: a create that landed on its preferred shape has no
	// second shape to name, and writing one would report a fallback that never
	// happened.
	PreferredType string
}

// SpecResolution is a resolved host-provisioning template: the create
// request, any shape-degradation warnings (e.g. a software-rendering
// fallback), and the zone ranking's trace for the chosen zone.
type SpecResolution struct {
	Spec             *CreateRequest
	Warnings         []string
	PlacementReasons []string

	// GPUDegraded reports that this resolution answered a GPU want without a
	// GPU. A typed fact rather than a warning to match on: the warning is
	// prose, and the count it feeds decides whether standing GPU capacity is
	// worth buying.
	GPUDegraded bool
}
