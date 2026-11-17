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

package audit

// Action represents an auditable action
type Action string

// Common audit actions
const (
	// Host actions
	ActionHostCreate Action = "host.create"
	ActionHostStart  Action = "host.start"
	ActionHostStop   Action = "host.stop"
	ActionHostDelete Action = "host.delete"
	ActionHostRetry  Action = "host.retry"
	ActionHostUpdate Action = "host.update"
	// ActionHostZoneFailover is a provisioning host moved to another ranked
	// zone after a classified capacity failure.
	ActionHostZoneFailover Action = "host.zone_failover"
	// ActionHostShapeFallback is a provisioning host moved onto the next
	// machine shape on the fleet's ladder because every ranked zone was out of
	// capacity for the one above it. The entry names both shapes: the host row
	// keeps only the one it ended on, so nothing else can tell this apart from
	// an ordinary create on that shape.
	ActionHostShapeFallback Action = "host.shape_fallback"
	// ActionHostPreempted is a Spot host reclaimed by GCE, recorded on
	// operation evidence when the preemption is marked. Host rows are torn
	// down with the fleet, so this trail is what the operations view's
	// preemption KPIs count.
	ActionHostPreempted Action = "host.preempted"
	// ActionHostPreemptionRecovered is a preempted host re-provisioned and
	// serving again; its metadata carries the recovery duration.
	ActionHostPreemptionRecovered Action = "host.preemption_recovered"
	// ActionHostLeaseExtend is a draining fleet host whose lease the autoscaler
	// renewed instead of letting it go. Host rows hard-delete, so this is the
	// only durable record that a host outlived the TTL it was created with.
	// Read beside host.create, it shows what the autoscaler actually did.
	ActionHostLeaseExtend Action = "host.lease_extend"
	// ActionHostLeaseShorten is a surplus fleet host whose lease the autoscaler
	// cut so the reaper would collect it early. It is the counterpart of the
	// renewal above, and it carries more weight. A renewal explains why a host
	// lived. This explains why one died, and the row it describes is gone by
	// the time anyone asks.
	ActionHostLeaseShorten Action = "host.lease_shorten"

	// Device actions
	ActionDeviceCreate Action = "device.create"
	ActionDeviceStart  Action = "device.start"
	ActionDeviceStop   Action = "device.stop"
	// ActionDeviceRestart is an in-place relaunch of the CVD.
	ActionDeviceRestart Action = "device.restart"
	// ActionDevicePowerwash is a reset to first-boot state (userdata wiped).
	ActionDevicePowerwash Action = "device.powerwash"
	// ActionDevicePowerbtn is a power-button press on the device.
	ActionDevicePowerbtn Action = "device.powerbtn"
	// ActionDeviceBugreport is a bugreport captured to artifact storage.
	ActionDeviceBugreport Action = "device.bugreport"
	// ActionDeviceRecordStart starts a device screen recording.
	ActionDeviceRecordStart Action = "device.record_start"
	// ActionDeviceRecordStop stops a device screen recording.
	ActionDeviceRecordStop Action = "device.record_stop"
	// ActionDeviceRecordFetch archives a device screen recording to storage.
	ActionDeviceRecordFetch Action = "device.record_fetch"
	// ActionDeviceSnapshot captures a device snapshot onto its host.
	ActionDeviceSnapshot Action = "device.snapshot"
	// ActionDeviceSnapshotDelete removes a stored device snapshot.
	ActionDeviceSnapshotDelete Action = "device.snapshot_delete"
	// ActionDeviceSnapshotRestore starts a stopped device from a snapshot.
	ActionDeviceSnapshotRestore Action = "device.snapshot_restore"
	ActionDeviceDelete          Action = "device.delete"
	ActionDevicePin             Action = "device.pin"
	ActionDeviceUnpin           Action = "device.unpin"
	ActionDeviceExtend          Action = "device.extend"

	// Quota actions
	ActionQuotaUpdate Action = "quota.update"

	// Admin actions
	ActionConfigChange Action = "admin.config"
	// ActionZoneCooldownClear is an operator lifting a zone_health cooldown by
	// hand. It discards the cell's consecutive-failure count along with the
	// countdown, so the trail is the only record that the ranking penalty was
	// dropped rather than served out.
	ActionZoneCooldownClear Action = "zone.cooldown_clear"

	// Task queue actions
	ActionTaskRetry  Action = "task.retry"
	ActionTaskDelete Action = "task.delete"
)

// MetaReclaim is the metadata key a lease shortening stamps to say which
// population the host came from. Two populations reach one action, and the row
// they describe is gone by the time anyone reads the entry, so without the key
// the trail cannot tell a host the fleet had too many of from one fenced for a
// group that has ended.
//
// Homed here rather than in the slice that writes it, because a second reader
// arrived: the console's activity figures count these entries, and platform may
// not import a feature. Both sides now spell it once.
const MetaReclaim = "reclaim"

// The values MetaReclaim takes. Both paths stamp one. If either value meant
// "the key is absent", the trail's meaning would depend on which revision wrote
// the entry, and entries outlive revisions by design.
const (
	// ReclaimFleetSurplus: standing inventory the target no longer wants.
	ReclaimFleetSurplus = "fleet_surplus"
	// ReclaimDedicatedEmpty: a host fenced for a group that has ended, which was
	// never inventory and which nothing else reclaims.
	ReclaimDedicatedEmpty = "dedicated_empty"
)

// ResourceType represents the type of resource being audited
type ResourceType string

// Audited resource types.
const (
	ResourceHost   ResourceType = "host"
	ResourceDevice ResourceType = "device"
	ResourceQuota  ResourceType = "quota"
	ResourceSystem ResourceType = "system"
	ResourceTask   ResourceType = "task"
)
