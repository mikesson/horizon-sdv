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

package cvd

import (
	"context"
	"log/slog"
	"strings"
)

// hostPoisonSignatures are the upstream HO launch-failure strings that mean a
// host holds a leaked tap device or a stale operator device-ID registration.
// No drift check can see that state.
var hostPoisonSignatures = []string{
	"validate tap devices",
	"tap interface",
	"tap device in use",
	"Device Id already registered",
	"Device Id already pre-registered",
	"Device id already taken",
}

// isHostPoisonError reports whether a create failure carries the poison
// signature. The match is on substrings because HO surfaces launch failures
// as opaque 500s with no structured error code.
func isHostPoisonError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, sig := range hostPoisonSignatures {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}

// recordHostPoison attributes a poison-signature create failure to the host.
// The repository bumps the counter and, at the threshold, degrades the host to
// fence it from placement in one transaction. Remediation itself is driven by
// the device controller's degraded-host sweep, not enqueued here: a fenced host is
// already safe, and a single best-effort enqueue would strand the host if it
// failed. It is best-effort, so a failure only delays the fence to the next
// attempt.
func (a *Actuator) recordHostPoison(ctx context.Context, hostID, reason string) {
	degraded, err := a.hosts.RecordCreateFailure(ctx, hostID, reason)
	if err != nil {
		a.logger.Error("failed to record host create failure",
			slog.String("host_id", hostID), slog.Any("error", err))
		return
	}
	if degraded {
		a.logger.Warn("host degraded (poisoned by repeated create failures); fencing from placement",
			slog.String("host_id", hostID), slog.String("reason", reason))
	}
}

// clearHostPoison resets a host's create-failure suspicion after a clean
// create. The counter is consecutive, so a success proves the host is serving.
func (a *Actuator) clearHostPoison(ctx context.Context, hostID string) {
	if err := a.hosts.ClearCreateFailures(ctx, hostID); err != nil {
		a.logger.Warn("failed to clear host poison suspicion",
			slog.String("host_id", hostID), slog.Any("error", err))
	}
}
