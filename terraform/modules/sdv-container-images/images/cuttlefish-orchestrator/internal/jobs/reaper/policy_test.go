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

package reaper

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
)

func policyService(t *testing.T, compiled bool, policy func(context.Context) (bool, error)) *Service {
	t.Helper()
	svc := &Service{config: Config{DryRun: compiled}, logger: slog.New(slog.DiscardHandler)}
	svc.dryRunPolicy = policy
	return svc
}

// TestResolveDryRunSuspendsOnAFailedRead is the safety direction of this
// whole feature. An operator who paused reaping during an incident must not
// have it resumed by a transient Spanner error: a cycle that deletes nothing
// is recoverable, a cycle that deletes is not.
func TestResolveDryRunSuspendsOnAFailedRead(t *testing.T) {
	t.Parallel()
	svc := policyService(t, false, func(context.Context) (bool, error) {
		return false, errors.New("spanner unavailable")
	})

	if !svc.resolveDryRun(t.Context()) {
		t.Error("a failed policy read resumed deletions; it must suspend them")
	}
}

func TestResolveDryRunTakesTheRuntimeAnswer(t *testing.T) {
	t.Parallel()
	// Compiled config says delete; the operator has since suspended it.
	svc := policyService(t, false, func(context.Context) (bool, error) { return true, nil })

	if !svc.resolveDryRun(t.Context()) {
		t.Error("the runtime answer was ignored in favor of the compiled default")
	}
}

func TestResolveDryRunFallsBackToTheCompiledValue(t *testing.T) {
	t.Parallel()
	svc := &Service{config: Config{DryRun: true}, logger: slog.New(slog.DiscardHandler)}

	if !svc.resolveDryRun(t.Context()) {
		t.Error("with no policy wired the compiled Config value must stand")
	}
}

// TestReapRereadsThePolicyEveryCycle is what makes the switch a runtime
// control rather than a startup flag: a value captured once at construction
// would need a redeploy to take back.
func TestReapRereadsThePolicyEveryCycle(t *testing.T) {
	t.Parallel()
	deviceRepo := newReaperDeviceRepo(&device.Device{ID: "device-1", Status: device.StatusRunning})
	hostRepo := newReaperHostRepo()
	deviceSvc := &mockDeviceService{}

	suspended := true
	reads := 0
	svc := New(Config{Enabled: true, Interval: time.Minute, BatchSize: 10},
		Deps{DeviceService: deviceSvc, HostService: &mockHostService{}, DeviceRepo: deviceRepo, HostRepo: hostRepo},
		slog.New(slog.DiscardHandler),
		WithDryRunPolicy(func(context.Context) (bool, error) {
			reads++
			return suspended, nil
		}))

	if err := svc.reap(t.Context()); err != nil {
		t.Fatalf("first cycle: %v", err)
	}
	if len(deviceSvc.deletedIDs) != 0 {
		t.Fatalf("suspended cycle deleted %d devices", len(deviceSvc.deletedIDs))
	}

	// The operator lifts the suspension between cycles.
	suspended = false
	if err := svc.reap(t.Context()); err != nil {
		t.Fatalf("second cycle: %v", err)
	}
	if len(deviceSvc.deletedIDs) == 0 {
		t.Error("the second cycle still honored the first cycle's answer")
	}
	if reads < 2 {
		t.Errorf("policy read %d times across two cycles, want one per cycle", reads)
	}
}
