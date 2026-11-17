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

package batchfeature

import (
	"fmt"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/batch"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
)

// buildRequirement mirrors the device create's build requirement.
type buildRequirement struct {
	Registered string `json:"registered,omitempty" doc:"ID of an already-registered build"`
	Track      *struct {
		Branch string `json:"branch"`
		Target string `json:"target"`
	} `json:"track,omitempty" doc:"Track the latest build on a branch/target"`
}

// batchRequirements is the declarative intent shared by every member device.
type batchRequirements struct {
	Interaction string            `json:"interaction" enum:"interactive,headless" doc:"How the devices will be used (interactive prefers GPU)"`
	GPURequired bool              `json:"gpu_required,omitempty" doc:"Fail instead of degrading to software rendering when no GPU capacity exists"`
	Build       *buildRequirement `json:"build,omitempty"`
	Location    *struct {
		PreferredRegion string `json:"preferred_region" minLength:"1" maxLength:"63" pattern:"^[a-z]+-[a-z]+[0-9]+$" doc:"GCE region (e.g. us-central1)"`
	} `json:"location,omitempty" doc:"Placement preference; omitted = capacity-ranked placement"`
	Capacity string `json:"capacity,omitempty" enum:"auto,spot,standard" doc:"Provisioning economics for the batch's hosts: spot (discounted, preemptible), standard (on-demand), or auto, where operator policy decides by interaction (default)"`
	// Untyped on the wire for the reason the device create's is: which pools
	// exist is compiled knowledge, which of them this deployment offers is
	// operator state, and the refusal that knows both is the server's.
	Pool string `json:"pool,omitempty" doc:"Subset of the fleet this batch's hosts must belong to; omitted routes the batch from what it declared it needs"`
	// The empty value belongs in the enum: a client that sends the field
	// cleared means the same as one that omits it, and the domain reads both
	// as x86.
	Architecture string `json:"architecture,omitempty" enum:"x86,arm," doc:"CPU architecture of the guests; omitted is x86. An arm64 guest is routed to the arm pool's bare-metal machines and refused where this deployment cannot build one"`
}

// CreateBatchInput is the POST /v2/batches request.
type CreateBatchInput struct {
	IdempotencyKey string `header:"Idempotency-Key" doc:"Makes the create retry-safe: a repeat with the same key resumes the same batch."`
	Body           struct {
		// Members are named "<prefix>-%03d", so the prefix must leave room for
		// the suffix inside a device name's own 63-character limit and must
		// already satisfy its pattern. Without both, an over-long or
		// upper-case prefix passes admission and then fails per member, after
		// the batch row and its hosts exist.
		NamePrefix   string            `json:"name_prefix" minLength:"1" maxLength:"59" pattern:"^[a-z][a-z0-9-]*$" doc:"Members are named <prefix>-000, <prefix>-001, …"`
		Count        int               `json:"count" minimum:"1" maximum:"100" doc:"Number of devices to create"`
		MinReady     int               `json:"min_ready,omitempty" minimum:"1" doc:"Reject unless at least this many can be admitted (default: count; atomic requires the full count)"`
		Requirements batchRequirements `json:"requirements"`
		FillPolicy   string            `json:"fill_policy,omitempty" enum:"best_effort,atomic" default:"best_effort" doc:"atomic fails and tears down the whole batch if a member permanently fails or it misses ready_timeout_seconds"`
		Placement    string            `json:"placement,omitempty" enum:"isolated,pack" default:"isolated" doc:"isolated provisions dedicated exclusive hosts; pack fills existing shareable capacity first"`
		ReadyTimeout int               `json:"ready_timeout_seconds,omitempty" minimum:"0" doc:"atomic only: fail the batch unless every member is running within this many seconds of creation (0 = no deadline)"`
		TTLHours     int               `json:"ttl_hours,omitempty" minimum:"0" doc:"Per-member TTL; the batch is reaped with its members"`
		ValidateOnly bool              `json:"validate_only,omitempty" doc:"Plan the batch without provisioning or creating anything"`
	}
}

// BatchSummary is the derived view of a batch: the member rollup plus the
// persisted policy (fill/placement/timeout) and its settle markers.
type BatchSummary struct {
	BatchID       string             `json:"batch_id,omitempty"`
	NamePrefix    string             `json:"name_prefix,omitempty"`
	Status        string             `json:"status,omitempty"`
	Requested     int                `json:"requested,omitempty"`
	Admitted      int                `json:"admitted,omitempty"`
	NewHosts      int                `json:"new_hosts,omitempty"`
	Total         int                `json:"total,omitempty"`
	Running       int                `json:"running,omitempty"`
	Errored       int                `json:"errored,omitempty"`
	Reconciling   bool               `json:"reconciling,omitempty"`
	FillPolicy    string             `json:"fill_policy,omitempty"`
	Placement     string             `json:"placement,omitempty"`
	ReadyTimeout  int                `json:"ready_timeout_seconds,omitempty"`
	CreatedBy     string             `json:"created_by,omitempty"`
	CreatedAt     *time.Time         `json:"created_at,omitempty"`
	ReadyAt       *time.Time         `json:"ready_at,omitempty"`
	FailedAt      *time.Time         `json:"failed_at,omitempty"`
	FailureReason string             `json:"failure_reason,omitempty"`
	Resolution    *device.Resolution `json:"resolution,omitempty"`
	Warnings      []string           `json:"warnings,omitempty"`
}

// applyBatch folds the persisted policy row into the summary.
func (s *BatchSummary) applyBatch(b *batch.Batch) {
	if b == nil {
		return
	}
	s.NamePrefix = b.NamePrefix
	s.Requested = b.RequestedCount
	s.FillPolicy = string(b.FillPolicy)
	s.Placement = string(b.Placement)
	s.ReadyTimeout = int(b.ReadyTimeout / time.Second)
	s.CreatedBy = b.CreatedBy
	s.CreatedAt = &b.CreatedAt
	s.ReadyAt = b.ReadyAt
	s.FailedAt = b.FailedAt
	s.FailureReason = b.FailureReason
}

// CreateBatchOutput is the POST /v2/batches response.
type CreateBatchOutput struct {
	Body BatchSummary
}

// GetBatchInput is the path-only input for single-batch reads.
type GetBatchInput struct {
	ID string `path:"id" doc:"Batch ID"`
}

// GetBatchOutput is the GET /v2/batches/{id} response.
type GetBatchOutput struct {
	Body BatchSummary
}

// ListBatchesInput is the GET /v2/batches input.
type ListBatchesInput struct {
	CreatedBy string `query:"created_by" doc:"Filter by creator email"`
	Mine      bool   `query:"mine" doc:"Only batches created by the caller (mutually exclusive with created_by)"`
}

// ListBatchesOutput is the GET /v2/batches response. Same items/total shape as
// the device and host lists; batches are derived views over a small set, so
// there is no limit/offset pagination.
type ListBatchesOutput struct {
	Body struct {
		Items []BatchSummary `json:"items"`
		Total int            `json:"total"`
	}
}

// BatchMember is one member device's connection info.
type BatchMember struct {
	DeviceID       string `json:"device_id"`
	Name           string `json:"name"`
	Status         string `json:"status"`
	HostID         string `json:"host_id"`
	ADBSerial      string `json:"adb_serial,omitempty"`
	WebRTCDeviceID string `json:"webrtc_device_id,omitempty"`
}

// ListBatchDevicesOutput is the GET /v2/batches/{id}/devices response.
type ListBatchDevicesOutput struct {
	Body struct {
		Devices []BatchMember `json:"devices"`
	}
}

// summaryOf maps a service View to the wire summary.
func summaryOf(v *View) BatchSummary {
	s := BatchSummary{
		BatchID: v.BatchID, Status: string(v.Status),
		Total: v.Rollup.Total, Running: v.Rollup.Running, Errored: v.Rollup.Errored,
		Reconciling: v.Rollup.Reconciling > 0,
	}
	s.applyBatch(v.Batch)
	return s
}

// adbSerial builds host:port from the device's (enriched) host IP and ADB port,
// preferring the external IP. Empty when either is unknown.
//
// This is a DIALABLE address, and it stays correct only because every batch
// member is single-instance: the firewall opens 6520 alone, so a group's second
// instance on 6521 would be printed here and refuse the connection. Giving
// batches an env_config means replacing this with the adb-over-WebSocket
// address, not widening the firewall.
func adbSerial(d *device.Device) string {
	ip := d.HostExternalIP
	if ip == "" {
		ip = d.HostInternalIP
	}
	if ip == "" || d.ADBPort == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", ip, d.ADBPort)
}
