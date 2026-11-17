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
	"encoding/json"
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
)

// deviceSpec is the persisted create-time spec (the desired_spec column): the
// fields the CVD-creation actuator needs that are otherwise absent from the
// row. Stored as opaque JSON, since nothing queries into it, so a stranded
// create is re-derivable from the row without a task payload.
type deviceSpec struct {
	EnvConfig     map[string]any `json:"env_config,omitempty"`
	Mode          string         `json:"mode,omitempty"`
	InstanceCount int            `json:"instance_count,omitempty"`
}

// DeviceRow represents the Spanner table schema for devices.
type DeviceRow struct {
	DeviceID     string             `spanner:"device_id"`
	HostID       string             `spanner:"host_id"`
	Name         string             `spanner:"name"`
	CVDID        spanner.NullString `spanner:"cvd_id"`
	Status       string             `spanner:"status"`
	BuildID      spanner.NullString `spanner:"build_id"`
	BuildTarget  spanner.NullString `spanner:"build_target"`
	ADBPort      spanner.NullInt64  `spanner:"adb_port"`
	WebRTCPort   spanner.NullInt64  `spanner:"webrtc_port"`
	GPUMode      string             `spanner:"gpu_mode"`
	CreatedBy    spanner.NullString `spanner:"created_by"`
	ErrorMessage spanner.NullString `spanner:"error_message"`
	StartedAt    spanner.NullTime   `spanner:"started_at"`
	StoppedAt    spanner.NullTime   `spanner:"stopped_at"`
	Tags         []string           `spanner:"tags"`
	CreatedAt    time.Time          `spanner:"created_at"`
	UpdatedAt    time.Time          `spanner:"updated_at"`

	// Declarative intent: opaque JSON (env_config, mode, instance_count).
	DesiredSpec spanner.NullString `spanner:"desired_spec"`

	// Health-driven recreate + restart budgets.
	RecreateCount       int64            `spanner:"recreate_count"`
	RecreateWindowStart spanner.NullTime `spanner:"recreate_window_start"`
	RestartCount        int64            `spanner:"restart_count"`
	RestartWindowStart  spanner.NullTime `spanner:"restart_window_start"`

	// BugreportCapturedAt is the capture-on-error dedup marker (NULL until a
	// capture is taken or the device is found uncapturable).
	BugreportCapturedAt spanner.NullTime `spanner:"bugreport_captured_at"`

	// Health fields
	HealthStatus      spanner.NullString `spanner:"health_status"`
	LastHealthCheck   spanner.NullTime   `spanner:"last_health_check"`
	ConsecutiveFails  spanner.NullInt64  `spanner:"consecutive_fails"`
	LastFailureReason spanner.NullString `spanner:"last_failure_reason"`
	LastHealthyAt     spanner.NullTime   `spanner:"last_healthy_at"`
	// CVDStatus is the raw CVD fleet observation (written by the health
	// monitor, and by the create-wait sampler during the create window).
	CVDStatus spanner.NullString `spanner:"cvd_status"`
	// BootObservation is the boot-progress observation as opaque JSON
	// (device.BootObservation), always written in the same mutation as
	// CVDStatus so the derivation reads a coherent sample.
	BootObservation spanner.NullString `spanner:"boot_observation"`
	// Instances is the observed instance array as opaque JSON
	// ([]device.Instance), what the host reported for this device's CVD
	// group. NULL until the create response arrives, and on every pre-v71 row.
	Instances spanner.NullString `spanner:"instances"`

	// Lifecycle fields
	LifecycleMode      spanner.NullString `spanner:"lifecycle_mode"`
	TTLSeconds         spanner.NullInt64  `spanner:"ttl_seconds"`
	ExpiresAt          spanner.NullTime   `spanner:"expires_at"`
	PinnedAt           spanner.NullTime   `spanner:"pinned_at"`
	PinnedBy           spanner.NullString `spanner:"pinned_by"`
	PinnedReason       spanner.NullString `spanner:"pinned_reason"`
	PinnedReasonDetail spanner.NullString `spanner:"pinned_reason_detail"`
	PinnedUntil        spanner.NullTime   `spanner:"pinned_until"`
	ExtendCount        spanner.NullInt64  `spanner:"extend_count"`

	// BatchID is the batch this device belongs to, NULL for a standalone
	// device. Set once at create; a batch is label-only.
	BatchID spanner.NullString `spanner:"batch_id"`
}

// ToDomain reads a device row. The desired_spec and boot_observation columns
// are opaque JSON decoded leniently, so a malformed value leaves those fields
// unset instead of failing the row. Reconciling is derived here, never
// stored.
func (r *DeviceRow) ToDomain() *device.Device {
	d := &device.Device{
		ID:                r.DeviceID,
		HostID:            r.HostID,
		Name:              r.Name,
		Status:            device.Status(r.Status),
		GPUMode:           device.GPUMode(r.GPUMode),
		CVDID:             FromNullString(r.CVDID),
		BuildID:           FromNullString(r.BuildID),
		BuildTarget:       FromNullString(r.BuildTarget),
		ADBPort:           FromNullInt64(r.ADBPort),
		WebRTCPort:        FromNullInt64(r.WebRTCPort),
		CreatedBy:         FromNullStringPtr(r.CreatedBy),
		ErrorMessage:      FromNullString(r.ErrorMessage),
		StartedAt:         FromNullTimePtr(r.StartedAt),
		StoppedAt:         FromNullTimePtr(r.StoppedAt),
		HealthStatus:      device.HealthStatus(FromNullString(r.HealthStatus)),
		LastHealthCheck:   FromNullTimePtr(r.LastHealthCheck),
		ConsecutiveFails:  int(FromNullInt64(r.ConsecutiveFails)),
		LastFailureReason: FromNullString(r.LastFailureReason),
		LastHealthyAt:     FromNullTimePtr(r.LastHealthyAt),
		CVDStatus:         FromNullString(r.CVDStatus),
		CreatedAt:         r.CreatedAt,
		UpdatedAt:         r.UpdatedAt,
	}
	if len(r.Tags) > 0 {
		d.Tags = r.Tags
	}

	d.Lifecycle = device.DeviceLifecycle{
		Mode:               device.LifecycleMode(FromNullString(r.LifecycleMode)),
		TTLSeconds:         FromNullInt64(r.TTLSeconds),
		ExpiresAt:          FromNullTimePtr(r.ExpiresAt),
		PinnedAt:           FromNullTimePtr(r.PinnedAt),
		PinnedBy:           FromNullStringPtr(r.PinnedBy),
		PinnedReasonDetail: FromNullStringPtr(r.PinnedReasonDetail),
		PinnedUntil:        FromNullTimePtr(r.PinnedUntil),
		ExtendCount:        int(FromNullInt64(r.ExtendCount)),
	}
	if r.PinnedReason.Valid {
		reason := device.PinReason(r.PinnedReason.StringVal)
		d.Lifecycle.PinnedReason = &reason
	}

	// A malformed observation is left unset rather than failing the read, so
	// an un-enriched observation degrades to today's behavior by shape.
	if r.BootObservation.Valid && r.BootObservation.StringVal != "" {
		var obs device.BootObservation
		if err := json.Unmarshal([]byte(r.BootObservation.StringVal), &obs); err == nil {
			d.BootObservation = &obs
		}
	}

	// A malformed instance array is left unset rather than failing the read,
	// the same lenient contract as the two observations above; the synthesis
	// below then covers it.
	if r.Instances.Valid && r.Instances.StringVal != "" {
		var instances []device.Instance
		if err := json.Unmarshal([]byte(r.Instances.StringVal), &instances); err == nil {
			d.Instances = instances
		}
	}
	// A row written before the column existed, or by a create that never
	// reached the host, carries its one instance in the flat columns. Read it
	// as the single instance it is. No backfill, and no migration window in
	// which every reachability path has to handle an empty array.
	//
	// WebRTCDeviceID is left empty rather than rebuilt from the
	// group and name: the host appends a fleet-unique instance id that is not
	// derivable here, so a constructed value would address a device the
	// operator has never registered. Callers treat empty as "ask the host".
	if len(d.Instances) == 0 && d.CVDID != "" {
		_, name := device.ParseCVDID(d.CVDID)
		d.Instances = []device.Instance{{
			Name:    name,
			State:   d.CVDStatus,
			ADBPort: d.ADBPort,
		}}
	}

	// Restore the create-time spec so the actuator (and device.reconcile) can
	// re-derive the CVD config from the row. A malformed spec is left unset
	// rather than failing the read; the create reports the absence plainly.
	if r.DesiredSpec.Valid && r.DesiredSpec.StringVal != "" {
		var spec deviceSpec
		if err := json.Unmarshal([]byte(r.DesiredSpec.StringVal), &spec); err == nil {
			d.EnvConfig = spec.EnvConfig
			d.Mode = spec.Mode
			d.InstanceCount = spec.InstanceCount
		}
	}
	d.RecreateCount = r.RecreateCount
	d.RecreateWindowStart = FromNullTimePtr(r.RecreateWindowStart)
	d.RestartCount = r.RestartCount
	d.RestartWindowStart = FromNullTimePtr(r.RestartWindowStart)
	d.BugreportCapturedAt = FromNullTimePtr(r.BugreportCapturedAt)
	d.BatchID = FromNullString(r.BatchID)
	d.Reconciling = d.IsReconciling() // derived output-only, computed on read

	return d
}

// FromDeviceDomain converts a domain Device to a DeviceRow.
func FromDeviceDomain(d *device.Device) *DeviceRow {
	r := &DeviceRow{
		DeviceID:  d.ID,
		HostID:    d.HostID,
		Name:      d.Name,
		Status:    string(d.Status),
		GPUMode:   string(d.GPUMode),
		CreatedAt: d.CreatedAt,
		UpdatedAt: d.UpdatedAt,
	}
	r.CVDID = ToNullString(d.CVDID)
	r.BuildID = ToNullString(d.BuildID)
	r.BuildTarget = ToNullString(d.BuildTarget)
	r.ADBPort = ToNullInt64(d.ADBPort)
	r.WebRTCPort = ToNullInt64(d.WebRTCPort)
	r.CreatedBy = ToNullStringPtr(d.CreatedBy)
	r.ErrorMessage = ToNullString(d.ErrorMessage)
	r.StartedAt = ToNullTime(d.StartedAt)
	r.StoppedAt = ToNullTime(d.StoppedAt)
	r.Tags = d.Tags
	r.HealthStatus = ToNullString(string(d.HealthStatus))
	r.LastHealthCheck = ToNullTime(d.LastHealthCheck)
	r.ConsecutiveFails = ToNullInt64(int64(d.ConsecutiveFails))
	r.LastFailureReason = ToNullString(d.LastFailureReason)
	r.LastHealthyAt = ToNullTime(d.LastHealthyAt)
	r.CVDStatus = ToNullString(d.CVDStatus)
	// The observation always re-marshals (plain counters and timestamps); on
	// the unreachable error the column is left NULL rather than failing the
	// write, the same lenient contract as desired_spec.
	if d.BootObservation != nil {
		if b, err := json.Marshal(d.BootObservation); err == nil {
			r.BootObservation = ToNullString(string(b))
		}
	}
	// Plain strings and integers, so this always re-marshals; on the
	// unreachable error the column is left NULL and the read synthesizes from
	// cvd_id, which is the same contract as the observations above.
	if len(d.Instances) > 0 {
		if b, err := json.Marshal(d.Instances); err == nil {
			r.Instances = ToNullString(string(b))
		}
	}
	r.LifecycleMode = ToNullString(string(d.Lifecycle.Mode))
	r.TTLSeconds = ToNullInt64(d.Lifecycle.TTLSeconds)
	r.ExpiresAt = ToNullTime(d.Lifecycle.ExpiresAt)
	r.PinnedAt = ToNullTime(d.Lifecycle.PinnedAt)
	r.PinnedBy = ToNullStringPtr(d.Lifecycle.PinnedBy)
	if d.Lifecycle.PinnedReason != nil {
		r.PinnedReason = ToNullString(string(*d.Lifecycle.PinnedReason))
	}
	r.PinnedReasonDetail = ToNullStringPtr(d.Lifecycle.PinnedReasonDetail)
	r.PinnedUntil = ToNullTime(d.Lifecycle.PinnedUntil)
	r.ExtendCount = ToNullInt64(int64(d.Lifecycle.ExtendCount))

	// Persist the create-time spec as opaque JSON. The values originate from a
	// JSON request (map[string]any), so they always re-marshal; on the
	// unreachable error the column is left NULL rather than failing the write.
	if d.EnvConfig != nil || d.Mode != "" || d.InstanceCount != 0 {
		if b, err := json.Marshal(deviceSpec{EnvConfig: d.EnvConfig, Mode: d.Mode, InstanceCount: d.InstanceCount}); err == nil {
			r.DesiredSpec = ToNullString(string(b))
		}
	}
	r.RecreateCount = d.RecreateCount
	r.RecreateWindowStart = ToNullTime(d.RecreateWindowStart)
	r.RestartCount = d.RestartCount
	r.RestartWindowStart = ToNullTime(d.RestartWindowStart)
	r.BugreportCapturedAt = ToNullTime(d.BugreportCapturedAt)
	r.BatchID = ToNullString(d.BatchID)
	return r
}
