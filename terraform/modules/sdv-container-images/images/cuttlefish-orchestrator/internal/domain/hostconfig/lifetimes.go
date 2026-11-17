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

package hostconfig

import (
	"strconv"
	"strings"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// Lifetimes is the operator lifetime policy: what a create or extend resolves
// to when the caller does not say. Domain vocabulary because three consumers
// must agree on it: the settings surface that stores it, the device
// service's create and extend paths, and the host service's lease grant. The
// caps are absent on purpose: the API maximums live in struct tags mirrored
// by bound tests, so raising a ceiling is a deploy, never a setting.
type Lifetimes struct {
	// DeviceTTLHours is the session TTL a device create takes when the
	// request names none.
	DeviceTTLHours int64

	// ExtendHours is the grant a TTL extension takes when the request names
	// no amount.
	ExtendHours int64

	// ExtendCount is the extension allowance per device; zero means no
	// extensions, so the device must be pinned or allowed to expire.
	ExtendCount int64

	// HostTTLHours is the lease an idle host starts with. A device whose
	// expiry outlasts it raises the host's lease to the device's promise plus
	// host.GracePeriodSeconds, so this bounds only idle and standby hosts.
	HostTTLHours int64

	// IsSet reports whether an operator stored the whole policy; false means
	// at least one field carries its compiled default.
	IsSet bool
}

// DefaultLifetimes is the compiled policy: the answer every consumer lands on
// when no operator stored one, or a stored value fails to parse.
func DefaultLifetimes() Lifetimes {
	return Lifetimes{
		DeviceTTLHours: device.DefaultDeviceTTLSeconds / 3600,
		ExtendHours:    device.DefaultExtendSeconds / 3600,
		ExtendCount:    int64(device.DefaultExtendCount),
		HostTTLHours:   host.DefaultHostTTLSeconds / 3600,
	}
}

// PolicyInt64 parses raw as an integer in [lo, hi] and reports whether raw
// supplied the value. Absent, malformed or out-of-range reads as fallback,
// the compiled default, so a bad row can never grant more than the code
// would.
func PolicyInt64(raw string, lo, hi, fallback int64) (int64, bool) {
	if v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil && v >= lo && v <= hi {
		return v, true
	}
	return fallback, false
}
