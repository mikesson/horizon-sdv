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

package models

import (
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// HostRow represents the Spanner table schema for hosts.
type HostRow struct {
	HostID                string             `spanner:"host_id"`
	Name                  string             `spanner:"name"`
	Zone                  string             `spanner:"zone"`
	RequestedZone         spanner.NullString `spanner:"requested_zone"`
	ZonePinned            bool               `spanner:"zone_pinned"`
	GCEInstanceName       spanner.NullString `spanner:"gce_instance_name"`
	GCEProject            spanner.NullString `spanner:"gce_project"`
	MachineType           spanner.NullString `spanner:"machine_type"`
	GPUType               spanner.NullString `spanner:"gpu_type"`
	GPUCount              int64              `spanner:"gpu_count"`
	MinCPUPlatform        spanner.NullString `spanner:"min_cpu_platform"`
	BootDiskSizeGB        spanner.NullInt64  `spanner:"boot_disk_size_gb"`
	BootImage             spanner.NullString `spanner:"boot_image"`
	ProvisioningModel     spanner.NullString `spanner:"provisioning_model"`
	Status                string             `spanner:"status"`
	IPAddress             spanner.NullString `spanner:"ip_address"`
	InternalIP            spanner.NullString `spanner:"internal_ip"`
	HostOrchestratorPort  int64              `spanner:"host_orchestrator_port"`
	MaxDevices            int64              `spanner:"max_devices"`
	CurrentDeviceCount    int64              `spanner:"current_device_count"`
	Exclusive             bool               `spanner:"exclusive"`
	ForeignCVDs           bool               `spanner:"foreign_cvds"`
	BatchID               spanner.NullString `spanner:"batch_id"`
	Origin                spanner.NullString `spanner:"origin"`
	Pool                  spanner.NullString `spanner:"pool"`
	CreatedBy             spanner.NullString `spanner:"created_by"`
	ErrorMessage          spanner.NullString `spanner:"error_message"`
	ErrorType             spanner.NullString `spanner:"error_type"`
	ErrorCode             spanner.NullString `spanner:"error_code"`
	ErrorSuggestion       spanner.NullString `spanner:"error_suggestion"`
	ErrorAlternativeZones []string           `spanner:"error_alternative_zones"`
	ErrorRetryable        spanner.NullBool   `spanner:"error_retryable"`
	ErrorOccurredAt       spanner.NullTime   `spanner:"error_occurred_at"`
	StoppedAt             spanner.NullTime   `spanner:"stopped_at"`
	VMTerminatesAt        spanner.NullTime   `spanner:"vm_terminates_at"`
	PreemptedAt           spanner.NullTime   `spanner:"preempted_at"`
	PreemptedIncarnation  spanner.NullInt64  `spanner:"preempted_incarnation"`
	PreemptionCount       int64              `spanner:"preemption_count"`
	LastPreemptionID      spanner.NullString `spanner:"last_preemption_id"`
	// Host-poison condition.
	ConsecutiveCreateFails int64              `spanner:"consecutive_create_fails"`
	LastCreateFailReason   spanner.NullString `spanner:"last_create_fail_reason"`
	DegradedAt             spanner.NullTime   `spanner:"degraded_at"`
	DegradedReason         spanner.NullString `spanner:"degraded_reason"`
	// Provision-blocked condition.
	ProvisionBlockedAt     spanner.NullTime   `spanner:"provision_blocked_at"`
	ProvisionBlockedReason spanner.NullString `spanner:"provision_blocked_reason"`
	Incarnation            int64              `spanner:"incarnation"`
	DesiredState           spanner.NullString `spanner:"desired_state"`
	VMStatus               spanner.NullString `spanner:"vm_status"`
	VMObservedAt           spanner.NullTime   `spanner:"vm_observed_at"`
	StalledReason          spanner.NullString `spanner:"stalled_reason"`
	HOReadyAt              spanner.NullTime   `spanner:"ho_ready_at"`
	CreatedAt              time.Time          `spanner:"created_at"`
	UpdatedAt              time.Time          `spanner:"updated_at"`
	LifecycleMode          spanner.NullString `spanner:"lifecycle_mode"`
	TTLSeconds             spanner.NullInt64  `spanner:"ttl_seconds"`
	ExpiresAt              spanner.NullTime   `spanner:"expires_at"`
	ProtectedDeviceCount   spanner.NullInt64  `spanner:"protected_device_count"`
}

// ToDomain reads a host row. The lifecycle columns are flat in Spanner and
// assembled into Host.Lifecycle here, and Reconciling is derived on read rather
// than stored. A NULL error_retryable reads as false, which is also what a host
// that has never failed carries, and a NULL pool reads as the default pool,
// the fleet as it stood before there were several.
func (r *HostRow) ToDomain() *host.Host {
	h := &host.Host{
		ID:                     r.HostID,
		Name:                   r.Name,
		Zone:                   r.Zone,
		RequestedZone:          FromNullString(r.RequestedZone),
		ZonePinned:             r.ZonePinned,
		GCEInstanceName:        FromNullString(r.GCEInstanceName),
		GCEProject:             FromNullString(r.GCEProject),
		MachineType:            FromNullString(r.MachineType),
		GPUType:                FromNullString(r.GPUType),
		GPUCount:               r.GPUCount,
		MinCPUPlatform:         FromNullString(r.MinCPUPlatform),
		BootDiskSizeGB:         FromNullInt64(r.BootDiskSizeGB),
		BootImage:              FromNullString(r.BootImage),
		ProvisioningModel:      host.ProvisioningModel(FromNullString(r.ProvisioningModel)),
		Status:                 host.Status(r.Status),
		IPAddress:              FromNullString(r.IPAddress),
		InternalIP:             FromNullString(r.InternalIP),
		HostOrchestratorPort:   r.HostOrchestratorPort,
		MaxDevices:             r.MaxDevices,
		CurrentDeviceCount:     r.CurrentDeviceCount,
		Exclusive:              r.Exclusive,
		ForeignCVDs:            r.ForeignCVDs,
		BatchID:                FromNullString(r.BatchID),
		Origin:                 host.Origin(FromNullString(r.Origin)),
		Pool:                   host.NormalizePool(FromNullString(r.Pool)),
		CreatedBy:              FromNullStringPtr(r.CreatedBy),
		ErrorMessage:           FromNullString(r.ErrorMessage),
		ErrorType:              FromNullString(r.ErrorType),
		ErrorCode:              FromNullString(r.ErrorCode),
		ErrorSuggestion:        FromNullString(r.ErrorSuggestion),
		ErrorAlternativeZones:  r.ErrorAlternativeZones,
		ErrorRetryable:         FromNullBool(r.ErrorRetryable),
		ErrorOccurredAt:        FromNullTimePtr(r.ErrorOccurredAt),
		StoppedAt:              FromNullTimePtr(r.StoppedAt),
		VMTerminatesAt:         FromNullTimePtr(r.VMTerminatesAt),
		PreemptedAt:            FromNullTimePtr(r.PreemptedAt),
		PreemptedIncarnation:   FromNullInt64Ptr(r.PreemptedIncarnation),
		PreemptionCount:        r.PreemptionCount,
		LastPreemptionID:       FromNullString(r.LastPreemptionID),
		ConsecutiveCreateFails: r.ConsecutiveCreateFails,
		LastCreateFailReason:   FromNullString(r.LastCreateFailReason),
		DegradedAt:             FromNullTimePtr(r.DegradedAt),
		DegradedReason:         FromNullString(r.DegradedReason),
		ProvisionBlockedAt:     FromNullTimePtr(r.ProvisionBlockedAt),
		ProvisionBlockedReason: FromNullString(r.ProvisionBlockedReason),
		Incarnation:            r.Incarnation,
		DesiredState:           FromNullString(r.DesiredState),
		VMStatus:               FromNullString(r.VMStatus),
		VMObservedAt:           FromNullTimePtr(r.VMObservedAt),
		StalledReason:          FromNullString(r.StalledReason),
		HOReadyAt:              FromNullTimePtr(r.HOReadyAt),
		CreatedAt:              r.CreatedAt,
		UpdatedAt:              r.UpdatedAt,
	}
	h.Lifecycle = host.HostLifecycle{
		Mode:                 host.LifecycleMode(FromNullString(r.LifecycleMode)),
		TTLSeconds:           FromNullInt64(r.TTLSeconds),
		ExpiresAt:            FromNullTimePtr(r.ExpiresAt),
		ProtectedDeviceCount: FromNullInt64(r.ProtectedDeviceCount),
	}
	h.Reconciling = h.IsReconciling() // derived output-only, computed on read
	// Same contract as Reconciling: derived from three columns already read, so
	// no caller can hold a class that disagrees with the row it came from.
	h.Class = host.DeriveClass(h.Origin, h.Exclusive, h.BatchID)
	return h
}

// FromHostDomain writes the host row, flattening Host.Lifecycle back into its
// own columns. error_retryable is the one nullable written unconditionally:
// every other empty value becomes NULL, but a false there means "not
// retryable", not "unknown".
//
// pool goes back through host.StoredPool, so the default pool writes the NULL
// ToDomain read it from. Every update writes every column, so storing the
// normalized name instead would rewrite a column stamped at create, and
// backfill each row that predates it the first time anything touched the host.
//
// Host.Class is absent because it has no column: it is derived on read from
// origin, exclusive and batch_id, so writing it would create the second copy
// the derivation exists to avoid.
func FromHostDomain(h *host.Host) *HostRow {
	r := &HostRow{
		HostID:               h.ID,
		Name:                 h.Name,
		Zone:                 h.Zone,
		RequestedZone:        ToNullString(h.RequestedZone),
		ZonePinned:           h.ZonePinned,
		GPUCount:             h.GPUCount,
		Status:               string(h.Status),
		HostOrchestratorPort: h.HostOrchestratorPort,
		MaxDevices:           h.MaxDevices,
		CurrentDeviceCount:   h.CurrentDeviceCount,
		Exclusive:            h.Exclusive,
		ForeignCVDs:          h.ForeignCVDs,
		BatchID:              ToNullString(h.BatchID),
		Origin:               ToNullString(string(h.Origin)),
		Pool:                 ToNullString(host.StoredPool(h.Pool)),
		PreemptionCount:      h.PreemptionCount,
		Incarnation:          h.Incarnation,
		DesiredState:         ToNullString(h.DesiredState),
		VMStatus:             ToNullString(h.VMStatus),
		VMObservedAt:         ToNullTime(h.VMObservedAt),
		StalledReason:        ToNullString(h.StalledReason),
		HOReadyAt:            ToNullTime(h.HOReadyAt),
		CreatedAt:            h.CreatedAt,
		UpdatedAt:            h.UpdatedAt,
	}
	r.GCEInstanceName = ToNullString(h.GCEInstanceName)
	r.GCEProject = ToNullString(h.GCEProject)
	r.MachineType = ToNullString(h.MachineType)
	r.GPUType = ToNullString(h.GPUType)
	r.MinCPUPlatform = ToNullString(h.MinCPUPlatform)
	r.BootDiskSizeGB = ToNullInt64(h.BootDiskSizeGB)
	r.BootImage = ToNullString(h.BootImage)
	r.ProvisioningModel = ToNullString(string(h.ProvisioningModel))
	r.IPAddress = ToNullString(h.IPAddress)
	r.InternalIP = ToNullString(h.InternalIP)
	r.CreatedBy = ToNullStringPtr(h.CreatedBy)
	r.ErrorMessage = ToNullString(h.ErrorMessage)
	r.ErrorType = ToNullString(h.ErrorType)
	r.ErrorCode = ToNullString(h.ErrorCode)
	r.ErrorSuggestion = ToNullString(h.ErrorSuggestion)
	r.ErrorAlternativeZones = h.ErrorAlternativeZones
	r.ErrorRetryable = spanner.NullBool{Bool: h.ErrorRetryable, Valid: true}
	r.ErrorOccurredAt = ToNullTime(h.ErrorOccurredAt)
	r.StoppedAt = ToNullTime(h.StoppedAt)
	r.VMTerminatesAt = ToNullTime(h.VMTerminatesAt)
	r.PreemptedAt = ToNullTime(h.PreemptedAt)
	r.PreemptedIncarnation = ToNullInt64Ptr(h.PreemptedIncarnation)
	r.LastPreemptionID = ToNullString(h.LastPreemptionID)
	r.ConsecutiveCreateFails = h.ConsecutiveCreateFails
	r.LastCreateFailReason = ToNullString(h.LastCreateFailReason)
	r.DegradedAt = ToNullTime(h.DegradedAt)
	r.DegradedReason = ToNullString(h.DegradedReason)
	r.ProvisionBlockedAt = ToNullTime(h.ProvisionBlockedAt)
	r.ProvisionBlockedReason = ToNullString(h.ProvisionBlockedReason)
	r.LifecycleMode = ToNullString(string(h.Lifecycle.Mode))
	r.TTLSeconds = ToNullInt64(h.Lifecycle.TTLSeconds)
	r.ExpiresAt = ToNullTime(h.Lifecycle.ExpiresAt)
	r.ProtectedDeviceCount = ToNullInt64(h.Lifecycle.ProtectedDeviceCount)
	return r
}
