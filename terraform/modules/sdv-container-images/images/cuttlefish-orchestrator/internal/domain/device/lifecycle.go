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
	"time"
)

// LifecycleMode represents the lifecycle management mode for a device.
//
// Devices can either be ephemeral (automatically deleted after TTL expires)
// or pinned (protected from automatic deletion until explicitly unpinned).
type LifecycleMode string

const (
	// LifecycleModeEphemeral indicates the device has a time-to-live (TTL)
	// and will be automatically deleted when the TTL expires. This is the
	// default mode for new devices.
	LifecycleModeEphemeral LifecycleMode = "ephemeral"

	// LifecycleModePinned indicates the device is protected from automatic
	// deletion. Pinned devices remain until explicitly unpinned or manually
	// deleted. A reason must be provided when pinning.
	LifecycleModePinned LifecycleMode = "pinned"
)

// PinReason represents the justification for pinning a device.
//
// Users must provide a reason when pinning a device to help with resource
// tracking and auditing. The "other" reason requires additional detail text.
type PinReason string

const (
	// PinReasonOvernightTest indicates the device is running an overnight test suite.
	PinReasonOvernightTest PinReason = "overnight_test"

	// PinReasonMemoryLeakDebug indicates the device is being used to debug memory leaks.
	PinReasonMemoryLeakDebug PinReason = "memory_leak_debug"

	// PinReasonLongRunningCI indicates the device is part of a long-running CI pipeline.
	PinReasonLongRunningCI PinReason = "long_running_ci"

	// PinReasonDemoPreparation indicates the device is being prepared for a demo.
	PinReasonDemoPreparation PinReason = "demo_preparation"

	// PinReasonCustomerSupport indicates the device is being used for customer support.
	PinReasonCustomerSupport PinReason = "customer_support"

	// PinReasonOther indicates a custom reason not covered by other options.
	// When using this reason, the ReasonDetail field must be provided.
	PinReasonOther PinReason = "other"
)

// PinReasonLabels maps pin reason codes to human-readable display labels.
// These labels are used in the UI and API responses.
var PinReasonLabels = map[PinReason]string{
	PinReasonOvernightTest:   "Overnight Stability Test",
	PinReasonMemoryLeakDebug: "Memory Leak Debugging",
	PinReasonLongRunningCI:   "Long-Running CI Pipeline",
	PinReasonDemoPreparation: "Demo Preparation",
	PinReasonCustomerSupport: "Customer Support Session",
	PinReasonOther:           "Other",
}

// IsValid checks if the pin reason is a recognized valid value.
func (r PinReason) IsValid() bool {
	_, ok := PinReasonLabels[r]
	return ok
}

// DefaultDeviceTTLSeconds is the compiled time-to-live for ephemeral devices
// in seconds (4 hours), taken when neither the request nor the stored
// lifetimes policy names one.
const DefaultDeviceTTLSeconds int64 = 14400

// DefaultExtendSeconds is the compiled TTL extension increment in seconds
// (2 hours), taken when neither the request nor the stored lifetimes policy
// names one.
const DefaultExtendSeconds int64 = 7200

// DefaultExtendCount is the compiled extension allowance per device, taken
// when the stored lifetimes policy does not name one. After the allowance is
// spent the device must be pinned or allowed to expire.
const DefaultExtendCount int = 3

// MaxTTLHours bounds the create-time session TTL. It is the compiled cap the
// ttl_hours maximum tag mirrors. It is not a setting, because the tag is
// enforced before any handler runs, so a runtime cap could only disagree
// with it.
const MaxTTLHours int64 = 8

// MaxExtendHours bounds a single TTL extension. The extend endpoint's
// seconds maximum tag mirrors it (in seconds).
const MaxExtendHours int64 = 4

// MaxExtendCount bounds the settable extension allowance. Total device life at
// this bound is 8h + 10 x 4h, under two days, against a host VM failsafe of
// 120 days of uptime; each extend is also checked against that VM's observed
// stop time.
const MaxExtendCount int = 10

// DeviceLifecycle holds the TTL and pinning state for a device.
//
// This struct tracks whether a device is ephemeral or pinned, when it
// expires, who pinned it and why, and how many times it has been extended.
type DeviceLifecycle struct { //nolint:revive // The stutter is the published OpenAPI schema name; renaming it renames the schema and the generated cfctl client with it.
	// Mode indicates whether the device is ephemeral or pinned.
	Mode LifecycleMode `json:"mode"`

	// TTLSeconds is the configured time-to-live in seconds.
	TTLSeconds int64 `json:"ttl_seconds,omitzero"`

	// ExpiresAt is when the device will expire (ephemeral mode only).
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	// PinnedAt is when the device was pinned (pinned mode only).
	PinnedAt *time.Time `json:"pinned_at,omitempty"`

	// PinnedBy is the user ID of who pinned the device.
	PinnedBy *string `json:"pinned_by,omitempty"`

	// PinnedReason is the reason code for why the device was pinned.
	PinnedReason *PinReason `json:"pinned_reason,omitempty"`

	// PinnedReasonDetail provides additional context for the pin reason.
	// Required when PinnedReason is PinReasonOther.
	PinnedReasonDetail *string `json:"pinned_reason_detail,omitempty"`

	// PinnedUntil is an optional expiration time for the pin.
	// If set, the device automatically unpins after this time.
	PinnedUntil *time.Time `json:"pinned_until,omitempty"`

	// ExtendCount tracks how many times the TTL has been extended.
	ExtendCount int `json:"extend_count,omitzero"`
}

// NewEphemeralLifecycle creates a new ephemeral lifecycle configuration.
// If ttlSeconds is zero or negative, DefaultDeviceTTLSeconds is used.
// The ExpiresAt timestamp is set to the current time plus the TTL.
func NewEphemeralLifecycle(ttlSeconds int64) DeviceLifecycle {
	if ttlSeconds <= 0 {
		ttlSeconds = DefaultDeviceTTLSeconds
	}
	expiresAt := time.Now().Add(time.Duration(ttlSeconds) * time.Second)
	return DeviceLifecycle{
		Mode:        LifecycleModeEphemeral,
		TTLSeconds:  ttlSeconds,
		ExpiresAt:   &expiresAt,
		ExtendCount: 0,
	}
}

// IsPinned returns true if the device is in pinned mode.
func (l *DeviceLifecycle) IsPinned() bool {
	return l.Mode == LifecycleModePinned
}

// IsEphemeral returns true if the device is in ephemeral mode with a TTL.
func (l *DeviceLifecycle) IsEphemeral() bool {
	return l.Mode == LifecycleModeEphemeral
}

// IsExpired returns true if the device's TTL has passed.
// Pinned devices never expire. Returns false if ExpiresAt is not set.
func (l *DeviceLifecycle) IsExpired() bool {
	if l.IsPinned() {
		return false
	}
	if l.ExpiresAt == nil {
		return false
	}
	return time.Now().After(*l.ExpiresAt)
}

// TimeRemaining returns the duration until the device expires.
// Returns zero for pinned devices or if the device has already expired.
func (l *DeviceLifecycle) TimeRemaining() time.Duration {
	if l.IsPinned() || l.ExpiresAt == nil {
		return 0
	}
	remaining := time.Until(*l.ExpiresAt)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// CanExtend returns true if the device's TTL can still be extended under the
// given allowance. Pinned devices cannot be extended; an allowance of zero
// means no extensions at all.
func (l *DeviceLifecycle) CanExtend(allowance int) bool {
	if l.IsPinned() {
		return false
	}
	return l.ExtendCount < allowance
}

// Pin transitions the device from ephemeral to pinned mode.
// The userID identifies who pinned the device, reason explains why,
// and pinnedUntil optionally sets when the pin should automatically expire.
//
// Pinning a pinned device is a re-pin: it replaces who, why and until, and
// keeps PinnedAt, so the pin's age survives moving pinned_until. A nil
// pinnedUntil on a re-pin makes the pin open-ended.
func (l *DeviceLifecycle) Pin(userID string, reason PinReason, reasonDetail string, pinnedUntil *time.Time) {
	if !l.IsPinned() || l.PinnedAt == nil {
		now := time.Now()
		l.PinnedAt = &now
	}
	l.Mode = LifecycleModePinned
	l.PinnedBy = &userID
	l.PinnedReason = &reason
	l.PinnedReasonDetail = nil
	if reasonDetail != "" {
		l.PinnedReasonDetail = &reasonDetail
	}
	l.PinnedUntil = pinnedUntil
	l.ExpiresAt = nil
}

// Unpin transitions the device from pinned back to ephemeral mode.
// A new expiration time is calculated from the current time plus the original TTL.
// All pinning-related fields are cleared.
func (l *DeviceLifecycle) Unpin() {
	expiresAt := time.Now().Add(time.Duration(l.TTLSeconds) * time.Second)
	l.Mode = LifecycleModeEphemeral
	l.ExpiresAt = &expiresAt
	l.PinnedAt = nil
	l.PinnedBy = nil
	l.PinnedReason = nil
	l.PinnedReasonDetail = nil
	l.PinnedUntil = nil
}

// Extend extends the device's TTL by the specified number of seconds, under
// the given extension allowance. If seconds is zero or negative,
// DefaultExtendSeconds is used. Returns an error if the device is pinned or
// has spent the allowance.
func (l *DeviceLifecycle) Extend(seconds int64, allowance int) error {
	if l.IsPinned() {
		return errors.New("cannot extend pinned device")
	}
	if !l.CanExtend(allowance) {
		return errors.New("maximum extensions reached")
	}
	if seconds <= 0 {
		seconds = DefaultExtendSeconds
	}

	// From the later of the current expiry and now, never from now alone:
	// extending from now shortens the lease of any device with more time left
	// than the increment. A device created with the default 4h TTL and
	// extended by the default 2h lost 2 hours and one of its three
	// extensions, and the reaper took it that much earlier. Starting from now
	// when the expiry has already passed keeps a lapsed device from being
	// handed an expiry that is still in the past.
	//
	// Total life stays bounded by the allowance, which is what caps it.
	base := time.Now()
	if l.ExpiresAt != nil && l.ExpiresAt.After(base) {
		base = *l.ExpiresAt
	}
	newExpiry := base.Add(time.Duration(seconds) * time.Second)

	l.ExpiresAt = &newExpiry
	l.ExtendCount++
	return nil
}

// PinRequest represents a request to pin a device.
//
// Users must provide a reason for pinning. If the reason is PinReasonOther,
// a ReasonDetail is required. PinnedUntil is optional and sets an auto-unpin time.
type PinRequest struct {
	// Reason is the justification for pinning (required).
	Reason PinReason `json:"reason"`

	// ReasonDetail provides additional context for the pin reason.
	// Required when Reason is PinReasonOther.
	ReasonDetail string `json:"reason_detail,omitempty"`

	// PinnedUntil optionally specifies when the pin should automatically expire.
	PinnedUntil *time.Time `json:"pinned_until,omitempty"`
}

// Validate checks that the pin request has valid values.
// Returns an error describing the first validation failure found.
func (r *PinRequest) Validate() error {
	if r.Reason == "" {
		return errors.New("pin reason is required")
	}
	if !r.Reason.IsValid() {
		return errors.New("invalid pin reason")
	}
	if r.Reason == PinReasonOther && r.ReasonDetail == "" {
		return errors.New("reason detail is required for 'other' reason")
	}
	if r.PinnedUntil != nil && r.PinnedUntil.Before(time.Now()) {
		return errors.New("pinned_until must be in the future")
	}
	return nil
}

// ExtendRequest represents a request to extend a device's TTL.
type ExtendRequest struct {
	// Seconds is the number of seconds to extend the TTL.
	// If zero, DefaultExtendSeconds (2 hours) is used.
	Seconds int64 `json:"seconds,omitempty"`
}
