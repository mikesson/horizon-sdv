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
	"fmt"
	"time"
)

// PlacementEligibleStatuses are host states automatic placement may land a
// device on, as the strings the hosts table stores. Hosts still coming up are
// included on purpose: concurrent declarative creates pack onto the host
// the first one started provisioning instead of each provisioning another.
// Domain vocabulary because three consumers must agree on it: the placement
// query, the device repository's create admission, and the fleet keeper's
// standby inventory count.
//
// The two statuses before ProvisionedStatuses are eligible on one further
// condition, applied by the placement query and the standby count: the host's
// provision has not been refused for capacity (Host.ProvisionBlockedAt). A
// host GCE keeps turning away is not capacity a create can count on.
var PlacementEligibleStatuses = []string{
	string(StatusPending),
	string(StatusProvisioning),
	string(StatusStarting),
	string(StatusRunning),
}

// ProvisionedStatuses are the placement-eligible states whose VM already
// exists: the provision has succeeded and only the Host Orchestrator's boot
// remains. A host here serves creates whatever its provisioning history says,
// which is what lets a provision-blocked marker that was never cleared (an
// insert that succeeded while its read-back failed) stop mattering the moment
// the reconciler observes the VM.
var ProvisionedStatuses = []string{
	string(StatusStarting),
	string(StatusRunning),
}

// ProvisionLead is how much lease an idle host must retain to count as standby
// inventory: one ending sooner is reaped before a provisioned replacement could
// land. One constant because two consumers must agree on the definition: the
// keeper's deficit count and the control-plane snapshot's panel.
//
// It measures nothing else. How long a host create takes, and how young a host
// is too young to reclaim, are separate quantities that merely sit near ten
// minutes too; fusing any of them here would let one measurement move all three.
const ProvisionLead = 10 * time.Minute

// ProvisioningLeg is how long a host create actually takes, end to end. It is a
// measurement, which is why it is not a round number and why it will become
// a per-shape measurement.
//
// It is the width of the demand signal's window: two creates that both missed
// placement inside one leg could not have shared a host, because the first
// one's host did not exist yet when the second arrived. Widen it and unrelated
// misses read as concurrent; narrow it and truly concurrent ones do not.
//
// Explicitly NOT ProvisionLead above, whatever their values do: that one is a
// lease margin the reaper's timing sets, this one is a measurement of GCE and
// the host image. They answer to different evidence and must be free to move
// apart.
const ProvisioningLeg = 155 * time.Second

// MinShortenAge is how old a fleet host must be before its emptiness may be
// read as "drained" rather than "provisioned moments ago". It is a correctness
// guard and is not operator-settable. Below it the answer is wrong.
//
// A host row exists from the moment the create is admitted and holds no device
// until one commits onto it, which is a whole provisioning leg later at best.
// The drain grace an operator may set bottoms out at two minutes, so without
// this guard a host could be shortened to a lease shorter than the leg that is
// still filling it, and the create that provisioned it would land on a host
// already being reaped.
//
// batchsettle carries exactly this guard for exactly this reason
// (batchHostGrace), and generalizing reclamation to the fleet must not drop it.
// Ten minutes is well above the leg because the leg is a median and the tail
// is what strands a create.
const MinShortenAge = 10 * time.Minute

// LifecycleMode represents the lifecycle state of a host
type LifecycleMode string

const (
	// LifecycleModeEphemeral indicates the host is subject to TTL-based termination
	LifecycleModeEphemeral LifecycleMode = "ephemeral"
	// LifecycleModeProtected indicates the host has pinned devices and cannot be auto-terminated
	LifecycleModeProtected LifecycleMode = "protected"
)

// DefaultHostTTLSeconds is the compiled TTL for ephemeral hosts (10 hours),
// taken when neither the request nor the stored lifetimes policy names one.
const DefaultHostTTLSeconds int64 = 36000

// MaxHostTTLHours bounds the create-time TTL and the hours one host
// extend request may add (one week). It bounds a single request only: repeated
// extends and device-driven raises have no cumulative cap, and the one limit on
// a lease is the VM horizon (CheckVMHorizon). The ttl_hours and extend hours
// maximum tags mirror it.
const MaxHostTTLHours int64 = 168

// MaxFleetRescueAge bounds how long renewal may keep a fleet host alive. It is
// the POLICY bound. The VM's own max_run_duration stops it at 120 days of
// uptime with no orchestrator involvement, and a renewal that ran into that
// would lose the fleet's warmest host abruptly rather than by choice. Nothing
// here reaches it; the policy bound binds first.
//
// It exists because renewal prefers warmth and warmth only grows within an
// incarnation: nothing evicts the artifact ledger but a host delete or an
// incarnation bump, so the oldest fleet hosts are the warmest (last to be
// shortened, first to be renewed), and a fleet under sustained demand would
// converge onto hosts that outlive every image respin. TTL attrition is the only
// mechanism rolling host images through the fleet today and renewal removes it.
// A day spans a CI shift and every interactive session while still turning the
// fleet over daily.
//
// It bounds the resulting LEASE and not merely eligibility, which is not the
// same rule. Refusing only hosts already past the bound still grants a fresh
// full TTL to one a minute short of it, so a host would live for the bound plus
// its whole TTL, 34 hours on today's defaults, which is not a fleet that rolls
// over daily.
const MaxFleetRescueAge = 24 * time.Hour

// DefaultMaxDevices is the capacity a host takes when the create names none.
// It is also the density auto-provisioned hosts are packed to unless the
// host.auto_max_devices policy says otherwise.
//
// Capacity is counted in CVD INSTANCES, not device rows: a device is a group
// of N Android VMs and is charged N against it, so a four-instance device
// fills this host on its own.
const DefaultMaxDevices int64 = 4

// HostImageCVDAccounts is how many instance slots the host image preallocates
// TAP sets for, one set per slot, fixed at boot by num_cvd_accounts. Raising
// it is an image respin, not a deploy.
//
// cvd knows nothing about this number. Its instance-id allocator is unbounded
// and hands out slot 11 as readily as slot 1, so a host admitted above this
// does not fail at admission: it fails much later, when run_cvd goes looking
// for a TAP device that was never created and the launch dies with the group
// half-built.
const HostImageCVDAccounts int64 = 10

// MaxDevicesPerHost bounds the create-time capacity, in instances like
// DefaultMaxDevices. The max_devices maximum tag mirrors it.
//
// It is the image's slot count. Admitting capacity the image cannot back only
// moves the failure from a rejected create to a broken launch. To raise it,
// raise num_cvd_accounts in the image first, then this.
const MaxDevicesPerHost int64 = HostImageCVDAccounts

// GracePeriodSeconds is the teardown margin added when a device's expiry
// raises its host's: the host must outlive every promise made to its devices
// by at least this long, so a device's final minutes are never on a host that
// is already being reaped.
const GracePeriodSeconds int64 = 900 // 15 minutes

// HostLifecycle holds lifecycle-related state for a host. Hosts are never
// pinned directly. Protection is derived from pinned devices via
// ProtectedDeviceCount.
type HostLifecycle struct { //nolint:revive // The stutter is the published OpenAPI schema name; renaming it renames the schema and the generated cfctl client with it.
	Mode                 LifecycleMode `json:"mode"`
	TTLSeconds           int64         `json:"ttl_seconds"`
	ExpiresAt            *time.Time    `json:"expires_at,omitempty"`
	ProtectedDeviceCount int64         `json:"protected_device_count"`
}

// NewEphemeralLifecycle creates a new ephemeral lifecycle with the given TTL
func NewEphemeralLifecycle(ttlSeconds int64) HostLifecycle {
	if ttlSeconds <= 0 {
		ttlSeconds = DefaultHostTTLSeconds
	}
	expiresAt := time.Now().Add(time.Duration(ttlSeconds) * time.Second)
	return HostLifecycle{
		Mode:                 LifecycleModeEphemeral,
		TTLSeconds:           ttlSeconds,
		ExpiresAt:            &expiresAt,
		ProtectedDeviceCount: 0,
	}
}

// IsProtected returns true if the host has pinned devices
func (l *HostLifecycle) IsProtected() bool {
	return l.ProtectedDeviceCount > 0
}

// IsEphemeral returns true if the host is ephemeral (no pinned devices)
func (l *HostLifecycle) IsEphemeral() bool {
	return l.ProtectedDeviceCount == 0
}

// IsExpired returns true if the ephemeral host has expired
func (l *HostLifecycle) IsExpired() bool {
	if l.IsProtected() {
		return false
	}
	if l.ExpiresAt == nil {
		return false
	}
	return time.Now().After(*l.ExpiresAt)
}

// TimeRemaining returns the time until expiration for ephemeral hosts
func (l *HostLifecycle) TimeRemaining() time.Duration {
	if l.IsProtected() || l.ExpiresAt == nil {
		return 0
	}
	remaining := time.Until(*l.ExpiresAt)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// IncrementProtectedCount increments the protected device count
// and updates the lifecycle mode accordingly
func (l *HostLifecycle) IncrementProtectedCount(delta int64) {
	l.ProtectedDeviceCount += delta
	if l.ProtectedDeviceCount < 0 {
		l.ProtectedDeviceCount = 0
	}

	// Update mode based on protected count
	if l.ProtectedDeviceCount > 0 {
		l.Mode = LifecycleModeProtected
		l.ExpiresAt = nil // Protected hosts don't expire
	} else {
		l.Mode = LifecycleModeEphemeral
		// Reset expiry when becoming unprotected
		expiresAt := time.Now().Add(time.Duration(l.TTLSeconds) * time.Second)
		l.ExpiresAt = &expiresAt
	}
}

// CheckVMHorizon refuses a lease raise that GCE would cut short: expiry plus
// GracePeriodSeconds must not pass terminatesAt, the time the VM's
// max_run_duration stops it. A nil terminatesAt (the VM not yet observed, or
// not running) passes. Device create, device extend and host extend call it.
// The heal back to live and unpin do not, because they restore time the user
// was already granted.
func CheckVMHorizon(hostID string, expiry time.Time, terminatesAt *time.Time) error {
	if terminatesAt == nil {
		return nil
	}
	if expiry.Add(time.Duration(GracePeriodSeconds) * time.Second).After(*terminatesAt) {
		return &VMHorizonError{HostID: hostID, Expiry: expiry, TerminatesAt: *terminatesAt}
	}
	return nil
}

// Extend raises the host's lease by d, from the later of now and the current
// expiry, so an extend never lowers a lease that device raises already pushed
// out. It refuses a deleting or deleted host; any class but reserved, because the fleet
// keeper, collapse and batch settlement own those leases and would overwrite
// it; a protected host, whose nil expiry is the protected encoding that a
// timestamp would corrupt; and a lease the VM horizon would cut short. Every
// refusal matches ErrInvalidState. Repeated extends have no cumulative cap.
func (h *Host) Extend(d time.Duration, now time.Time) error {
	if h.Status.IsTerminal() || h.Status == StatusDeleting {
		return &InvalidStateError{Message: fmt.Sprintf("host %s is %s: its lease can no longer be extended", h.ID, h.Status)}
	}
	if class := DeriveClass(h.Origin, h.Exclusive, h.BatchID); class != ClassReserved {
		return &InvalidStateError{Message: fmt.Sprintf(
			"host %s is a %s host: the orchestrator manages its lease and would overwrite an extend. "+
				"To keep a device, extend or pin the device instead: "+
				"cfctl device extend DEVICE --by 2h, or cfctl device pin DEVICE --reason overnight_test --for 24h",
			h.ID, class)}
	}
	if h.Lifecycle.IsProtected() || h.Lifecycle.ExpiresAt == nil {
		return &InvalidStateError{Message: fmt.Sprintf(
			"host %s is protected by %d pinned devices; it has no expiry", h.ID, h.Lifecycle.ProtectedDeviceCount)}
	}
	base := now
	if h.Lifecycle.ExpiresAt.After(base) {
		base = *h.Lifecycle.ExpiresAt
	}
	expiry := base.Add(d)
	if err := CheckVMHorizon(h.ID, expiry, h.VMTerminatesAt); err != nil {
		return err
	}
	h.Lifecycle.ExpiresAt = &expiry
	return nil
}
