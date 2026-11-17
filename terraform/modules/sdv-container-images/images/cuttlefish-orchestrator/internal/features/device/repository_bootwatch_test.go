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

package devicefeature_test

import (
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	devicefeature "cloud-cuttlefish-orchestrator/internal/features/device"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

func seedBootwatchDevice(t *testing.T, db *spannertest.TestDB, repo *devicefeature.DeviceRepository, hostID, deviceID string) {
	t.Helper()
	if err := db.SeedHost(t.Context(), hostID, "bw-"+deviceID, "us-central1-a"); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	d := &device.Device{
		ID: deviceID, HostID: hostID, Name: "bw-" + deviceID,
		Status: device.StatusCreating, GPUMode: device.GPUModeAuto, BuildID: "P100",
	}
	if err := repo.Create(t.Context(), d); err != nil {
		t.Fatalf("create device: %v", err)
	}
}

func sampleObservation(now time.Time) *device.BootObservation {
	since := now.Add(-12 * time.Minute)
	return &device.BootObservation{
		KernelPanics: 4, GuestReboots: 7, UBootStarts: 9,
		KernelLogBytes: 2 << 20,
		LastPanic:      "Kernel panic - not syncing: bad XCR0",
		Verdict:        device.BootVerdictLooping,
		TransientSince: &since,
		ObservedAt:     now,
	}
}

// TestBootObservation_SurvivesUnrelatedMutate pins the row round-trip in both
// converter directions. Mutate reads the full row into the domain and writes
// it back, so an observation missing from either conversion is silently
// blanked by every unrelated write.
func TestBootObservation_SurvivesUnrelatedMutate(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := devicefeature.NewDeviceRepository(db.Client)
	seedBootwatchDevice(t, db, repo, "host-bw-mutate", "dev-bw-mutate")

	now := time.Now().UTC().Truncate(time.Microsecond)
	obs := sampleObservation(now)
	if err := repo.RecordBootObservation(ctx, "host-bw-mutate", "dev-bw-mutate", device.CVDStateStarting, obs); err != nil {
		t.Fatalf("RecordBootObservation: %v", err)
	}

	if _, err := repo.Mutate(ctx, "dev-bw-mutate", func(cur *device.Device) error {
		cur.Tags = []string{"unrelated"}
		return nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	got, err := repo.Get(ctx, "dev-bw-mutate")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.BootObservation == nil {
		t.Fatal("boot observation blanked by an unrelated Mutate")
	}
	if got.BootObservation.GuestReboots != obs.GuestReboots ||
		got.BootObservation.Verdict != obs.Verdict ||
		got.BootObservation.LastPanic != obs.LastPanic {
		t.Errorf("observation mangled by Mutate round-trip: got %+v", got.BootObservation)
	}
	if got.BootObservation.TransientSince == nil ||
		!got.BootObservation.TransientSince.Equal(*obs.TransientSince) {
		t.Errorf("TransientSince did not survive: got %v, want %v",
			got.BootObservation.TransientSince, obs.TransientSince)
	}
}

// TestRecordBootObservation_OneSample verifies the observation and the enum
// land together. The derivation reads both, so a write path that splits them
// can settle a fresh enum against a stale count.
func TestRecordBootObservation_OneSample(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := devicefeature.NewDeviceRepository(db.Client)
	seedBootwatchDevice(t, db, repo, "host-bw-sample", "dev-bw-sample")

	obs := sampleObservation(time.Now().UTC().Truncate(time.Microsecond))
	if err := repo.RecordBootObservation(ctx, "host-bw-sample", "dev-bw-sample", device.CVDStateStarting, obs); err != nil {
		t.Fatalf("RecordBootObservation: %v", err)
	}
	got, err := repo.Get(ctx, "dev-bw-sample")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.CVDStatus != device.CVDStateStarting {
		t.Errorf("cvd_status = %q, want %q (must ride the same write)", got.CVDStatus, device.CVDStateStarting)
	}
	if got.BootObservation == nil || got.BootObservation.KernelPanics != obs.KernelPanics {
		t.Errorf("observation not stored: %+v", got.BootObservation)
	}

	// A device deleted underneath the sampler is not an error.
	if err := repo.RecordBootObservation(ctx, "host-bw-sample", "no-such-device", device.CVDStateStarting, obs); err != nil {
		t.Errorf("RecordBootObservation on a gone device = %v, want nil", err)
	}
}

// TestBatchUpdateHealth_BootObservation verifies the two riding rules: a
// non-nil observation is written (and forces the full path past the
// TimestampOnly dedup), and a nil one leaves the stored observation intact
// rather than blanking it, since most cycles do not sample.
func TestBatchUpdateHealth_BootObservation(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := devicefeature.NewDeviceRepository(db.Client)
	seedBootwatchDevice(t, db, repo, "host-bw-batch", "dev-bw-batch")

	now := time.Now().UTC().Truncate(time.Microsecond)
	obs := sampleObservation(now)
	err := repo.BatchUpdateHealth(ctx, []device.HealthUpdate{{
		DeviceID: "dev-bw-batch", HostID: "host-bw-batch",
		Status: device.HealthStatusHealthy, CVDStatus: device.CVDStateRunning,
		BootObs:       obs,
		TimestampOnly: true, // must not drop the sample
	}})
	if err != nil {
		t.Fatalf("BatchUpdateHealth: %v", err)
	}
	got, err := repo.Get(ctx, "dev-bw-batch")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.BootObservation == nil || got.BootObservation.GuestReboots != obs.GuestReboots {
		t.Fatalf("TimestampOnly dropped the enriched observation: %+v", got.BootObservation)
	}
	if got.CVDStatus != device.CVDStateRunning {
		t.Errorf("cvd_status = %q, want %q", got.CVDStatus, device.CVDStateRunning)
	}

	// An unenriched cycle (nil BootObs) must leave the observation untouched.
	err = repo.BatchUpdateHealth(ctx, []device.HealthUpdate{{
		DeviceID: "dev-bw-batch", HostID: "host-bw-batch",
		Status: device.HealthStatusHealthy, CVDStatus: device.CVDStateRunning,
	}})
	if err != nil {
		t.Fatalf("BatchUpdateHealth (unenriched): %v", err)
	}
	got, err = repo.Get(ctx, "dev-bw-batch")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.BootObservation == nil {
		t.Fatal("an unenriched health cycle blanked the stored observation")
	}
}
