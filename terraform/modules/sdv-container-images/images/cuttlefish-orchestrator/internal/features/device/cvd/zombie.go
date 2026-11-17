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
	"errors"
	"fmt"
	"log/slog"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
)

// Health-driven recreate budget: a zombie device is recreated from its
// persisted spec at most zombieRecreateBudget times per zombieRecreateWindow
// before it is failed explicitly. That bounds recreate thrash on a broken
// build while still auto-recovering a transient CVD death.
const (
	zombieRecreateBudget = 3
	zombieRecreateWindow = 24 * time.Hour
)

// errZombieNoop is an internal Mutate sentinel: the device recovered on its own
// between the suspector's scan and this reconcile, so there is nothing to do.
var errZombieNoop = errors.New("zombie recovery no-op")

// Zombie restart budget for the cheap first step. A device whose CVD is present
// but unresponsive (cvd_status Unreachable) is relaunched in place at most
// zombieRestartBudget times per zombieRestartWindow before falling through to
// the heavier recreate. A hung run_cvd launcher revives in seconds without
// re-fetching artifacts.
const (
	zombieRestartBudget = 2
	zombieRestartWindow = time.Hour
)

// errRestartSkip is an internal Mutate sentinel: the zombie is not eligible for
// a cheap restart (its CVD is gone rather than merely unresponsive, or the
// restart budget is spent), so recovery falls through to recreate.
var errRestartSkip = errors.New("zombie restart skipped")

// RecoverZombie handles a running device whose CVD died under it
// (health_status=zombie): it re-drives the CVD create from the persisted
// desired_spec, bounded by a per-device budget, or fails the device with a
// reason once the budget is spent or no spec is persisted. It reports whether a
// recreate was scheduled (the caller ensures the create task); a device that
// recovered on its own is a clean no-op. The status flip and budget arithmetic
// commit in one Mutate CAS, so a device that changed underneath is re-observed.
func (a *Actuator) RecoverZombie(ctx context.Context, deviceID string) (bool, error) {
	recreate := false
	_, err := a.devices.Mutate(ctx, deviceID, func(cur *device.Device) error {
		recreate = false
		if cur.Status != device.StatusRunning || cur.HealthStatus != device.HealthStatusZombie {
			return errZombieNoop // recovered on its own; leave it be
		}
		// We are acting on this zombie one way or the other: clear the now-stale
		// health so the row stops lying (whether it errors or recreates) and the
		// suspector does not re-fire mid-recreate.
		cur.HealthStatus = device.HealthStatusUnknown
		cur.ConsecutiveFails = 0
		// No persisted spec (a pre-desired_spec device) can't be recreated
		// correctly, so fail it instead of rebuilding from defaults.
		if cur.Mode == "" {
			cur.SetError("CVD is a zombie and the device has no persisted spec to recreate from")
			return nil
		}
		now := time.Now()
		switch {
		case cur.RecreateWindowStart == nil || now.Sub(*cur.RecreateWindowStart) > zombieRecreateWindow:
			cur.RecreateCount = 1
			cur.RecreateWindowStart = &now
		case cur.RecreateCount < zombieRecreateBudget:
			cur.RecreateCount++
		default:
			cur.SetError(fmt.Sprintf("CVD is a zombie; exhausted %d recreate attempts within %v",
				zombieRecreateBudget, zombieRecreateWindow))
			return nil
		}
		// Re-drive the create: back to creating. The fresh CVD gets a fresh
		// restart budget.
		cur.RestartCount = 0
		cur.RestartWindowStart = nil
		cur.Status = device.StatusCreating
		recreate = true
		return nil
	})
	if errors.Is(err, errZombieNoop) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("recovering zombie device %s: %w", deviceID, err)
	}
	return recreate, nil
}

// zombieRestartTarget addresses the CVD a reserved restart should relaunch.
type zombieRestartTarget struct {
	cvdID  string
	hostID string
}

// RestartZombieCVD is the cheap first step of zombie recovery. When a running,
// zombie device's CVD is present but unresponsive (cvd_status Unreachable), it
// relaunches the hung run_cvd in place (the host tool's :restart) instead of
// recreating the CVD from desired_spec, which takes seconds and needs no
// artifact re-fetch. The device is left running+zombie with no status
// transition, so the next health check confirms recovery, or the reconcile
// escalates once the restart budget is spent. It reports whether a restart was
// issued; false means ineligible, budget-spent, or a failed restart, and tells
// the caller to recreate. The budget arithmetic commits in one Mutate CAS
// before the best-effort restart call.
func (a *Actuator) RestartZombieCVD(ctx context.Context, deviceID string) (bool, error) {
	target, reserved, err := a.reserveZombieRestart(ctx, deviceID)
	if err != nil {
		return false, err
	}
	if !reserved {
		return false, nil // ineligible or budget spent, so recreate
	}
	group, name := device.ParseCVDID(target.cvdID)
	if group == "" || name == "" {
		return false, nil // no addressable CVD, so recreate
	}
	client, serving := a.servingHOClient(ctx, target.hostID)
	if !serving {
		return false, nil // host gone or not serving, so recreate
	}
	if err := client.RestartCVD(ctx, group, name); err != nil {
		a.logger.Warn("zombie CVD restart failed; falling through to recreate",
			slog.String("device_id", deviceID), slog.Any("error", err))
		return false, nil
	}
	return true, nil
}

// reserveZombieRestart CASes the restart budget for a running, zombie device
// whose CVD is present but unresponsive (Unreachable), returning the CVD to
// restart. reserved=false means the device is ineligible, having recovered on
// its own, or its CVD is gone instead of hung, or the restart budget is
// spent, and recovery should recreate instead.
func (a *Actuator) reserveZombieRestart(ctx context.Context, deviceID string) (zombieRestartTarget, bool, error) {
	var target zombieRestartTarget
	_, mErr := a.devices.Mutate(ctx, deviceID, func(cur *device.Device) error {
		if cur.Status != device.StatusRunning || cur.HealthStatus != device.HealthStatusZombie {
			return errZombieNoop
		}
		if cur.CVDStatus != device.CVDStateUnreachable {
			return errRestartSkip // the CVD is gone, so recreate
		}
		now := time.Now()
		switch {
		case cur.RestartWindowStart == nil || now.Sub(*cur.RestartWindowStart) > zombieRestartWindow:
			cur.RestartCount = 1
			cur.RestartWindowStart = &now
		case cur.RestartCount < zombieRestartBudget:
			cur.RestartCount++
		default:
			return errRestartSkip // budget spent, so recreate
		}
		target = zombieRestartTarget{cvdID: cur.CVDID, hostID: cur.HostID}
		return nil
	})
	if errors.Is(mErr, errZombieNoop) || errors.Is(mErr, errRestartSkip) {
		return zombieRestartTarget{}, false, nil
	}
	if mErr != nil {
		return zombieRestartTarget{}, false, fmt.Errorf("recording zombie restart for device %s: %w", deviceID, mErr)
	}
	return target, true, nil
}
