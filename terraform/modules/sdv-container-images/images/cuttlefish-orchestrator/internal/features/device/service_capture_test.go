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

package devicefeature

import (
	"log/slog"
	"net/url"
	"strconv"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// captureSetup wires a capture-on-error service against a fake Host
// Orchestrator, returning the service and the shared upload sink so tests can
// assert whether a capture actually streamed.
func captureSetup(t *testing.T) (*DeviceService, *fakeUploader) {
	t.Helper()
	ho, _ := bugreportHO(t, []byte("PK-fake-zip"))
	t.Cleanup(ho.Close)
	hoURL, _ := url.Parse(ho.URL)
	port, _ := strconv.ParseInt(hoURL.Port(), 10, 64)

	upload := &fakeUploader{}
	policy := &fakePolicy{values: map[string]string{}}
	svc := bugreportService(t, upload, policy, "default-bucket", port)
	return svc, upload
}

func captured(t *testing.T, svc *DeviceService, id string) bool {
	t.Helper()
	d, err := svc.deviceRepo.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return d.BugreportCapturedAt != nil
}

// TestCaptureOnErrorCapturesAndMarks proves the happy path: an error device
// with a recorded CVD is captured (host-only flavor), the durable copy lands,
// and the row is marked so a redelivery no-ops.
func TestCaptureOnErrorCapturesAndMarks(t *testing.T) {
	t.Parallel()
	svc, upload := captureSetup(t)
	// d1 is seeded by bugreportService: error, CVDID cvdd1/triage, host h1 running.

	if err := svc.CaptureBugreportOnError(t.Context(), "d1"); err != nil {
		t.Fatalf("CaptureBugreportOnError: %v", err)
	}
	if upload.object == "" {
		t.Fatal("expected a bugreport upload, got none")
	}
	if upload.contentType != "application/zip" {
		t.Errorf("content type = %q, want application/zip", upload.contentType)
	}
	if !captured(t, svc, "d1") {
		t.Error("device was not marked captured")
	}

	// Redelivery: already marked, so no second upload.
	upload.object = ""
	if err := svc.CaptureBugreportOnError(t.Context(), "d1"); err != nil {
		t.Fatalf("second CaptureBugreportOnError: %v", err)
	}
	if upload.object != "" {
		t.Error("a captured device was re-captured on redelivery")
	}
}

// TestCaptureOnErrorMarksUncapturable covers the skip-not-fail cases: a device
// with nothing to capture is marked (so the detector stops revisiting it) and
// never uploads.
func TestCaptureOnErrorMarksUncapturable(t *testing.T) {
	t.Parallel()

	t.Run("error device without a CVD", func(t *testing.T) {
		t.Parallel()
		svc, upload := captureSetup(t)
		repo := svc.deviceRepo.(*atomicDeviceRepo)
		repo.Devices["d-nocvd"] = &device.Device{
			ID: "d-nocvd", Name: "nocvd", HostID: "h1", Status: device.StatusError,
		}
		if err := svc.CaptureBugreportOnError(t.Context(), "d-nocvd"); err != nil {
			t.Fatalf("CaptureBugreportOnError: %v", err)
		}
		if upload.object != "" {
			t.Error("a device with no CVD should not upload")
		}
		if !captured(t, svc, "d-nocvd") {
			t.Error("un-capturable device was not marked")
		}
	})

	t.Run("host gone", func(t *testing.T) {
		t.Parallel()
		svc, upload := captureSetup(t)
		repo := svc.deviceRepo.(*atomicDeviceRepo)
		repo.Devices["d-nohost"] = &device.Device{
			ID: "d-nohost", Name: "nohost", HostID: "missing", Status: device.StatusError,
			CVDID: "cvdd-nohost/nohost",
		}
		if err := svc.CaptureBugreportOnError(t.Context(), "d-nohost"); err != nil {
			t.Fatalf("CaptureBugreportOnError: %v", err)
		}
		if upload.object != "" {
			t.Error("a device whose host is gone should not upload")
		}
		if !captured(t, svc, "d-nohost") {
			t.Error("host-gone device was not marked")
		}
	})

	t.Run("host not serving", func(t *testing.T) {
		t.Parallel()
		svc, upload := captureSetup(t)
		svc.hostRepo.(*hosttest.FakeRepo).Hosts["h-stopped"] = &host.Host{
			ID: "h-stopped", Status: host.StatusStopped, MaxDevices: 4,
			IPAddress: "127.0.0.1", HostOrchestratorPort: 2080,
		}
		repo := svc.deviceRepo.(*atomicDeviceRepo)
		repo.Devices["d-stopped"] = &device.Device{
			ID: "d-stopped", Name: "stopped", HostID: "h-stopped", Status: device.StatusError,
			CVDID: "cvdd-stopped/stopped",
		}
		if err := svc.CaptureBugreportOnError(t.Context(), "d-stopped"); err != nil {
			t.Fatalf("CaptureBugreportOnError: %v", err)
		}
		if upload.object != "" {
			t.Error("a device on a non-serving host should not upload")
		}
		if !captured(t, svc, "d-stopped") {
			t.Error("non-serving-host device was not marked")
		}
	})
}

// TestCaptureOnErrorSkipsNonError proves a device that is not in the error
// state (recovered between scan and handling) is left entirely alone.
func TestCaptureOnErrorSkipsNonError(t *testing.T) {
	t.Parallel()
	svc, upload := captureSetup(t)
	repo := svc.deviceRepo.(*atomicDeviceRepo)
	repo.Devices["d-running"] = &device.Device{
		ID: "d-running", Name: "running", HostID: "h1", Status: device.StatusRunning,
		CVDID: "cvdd-running/running",
	}
	if err := svc.CaptureBugreportOnError(t.Context(), "d-running"); err != nil {
		t.Fatalf("CaptureBugreportOnError: %v", err)
	}
	if upload.object != "" {
		t.Error("a running device should not be captured")
	}
	if captured(t, svc, "d-running") {
		t.Error("a running device must not be marked captured")
	}
}

// TestCaptureOnErrorNoBucketMarks proves that capture enabled without a bucket
// (operator misconfiguration) skips the upload and still marks the device, so
// the detector does not re-enqueue it every cycle.
func TestCaptureOnErrorNoBucketMarks(t *testing.T) {
	t.Parallel()
	ho, _ := bugreportHO(t, []byte("zip"))
	t.Cleanup(ho.Close)
	hoURL, _ := url.Parse(ho.URL)
	port, _ := strconv.ParseInt(hoURL.Port(), 10, 64)

	upload := &fakeUploader{}
	// No override policy and no deployment default → no bucket resolves.
	svc := bugreportService(t, upload, &fakePolicy{values: map[string]string{}}, "", port)

	if err := svc.CaptureBugreportOnError(t.Context(), "d1"); err != nil {
		t.Fatalf("CaptureBugreportOnError: %v", err)
	}
	if upload.object != "" {
		t.Error("no bucket configured should not upload")
	}
	if !captured(t, svc, "d1") {
		t.Error("misconfigured capture should still mark to avoid a re-enqueue storm")
	}
}

// TestCaptureOnErrorUnwiredNoOp proves that without the artifacts option the
// handler is an inert no-op that never marks.
func TestCaptureOnErrorUnwiredNoOp(t *testing.T) {
	t.Parallel()
	deviceRepo := newDeviceRepoFake()
	deviceRepo.Devices["d1"] = &device.Device{
		ID: "d1", Name: "triage", HostID: "h1", Status: device.StatusError, CVDID: "cvdd1/triage",
	}
	svc, err := NewDeviceService(deviceRepo, hosttest.NewFakeRepo(), newMockBuildRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(), WithTaskEnqueuer(&fakeEnqueuer{}))
	if err != nil {
		t.Fatalf("NewDeviceService: %v", err)
	}
	if err := svc.CaptureBugreportOnError(t.Context(), "d1"); err != nil {
		t.Fatalf("CaptureBugreportOnError: %v", err)
	}
	if deviceRepo.Devices["d1"].BugreportCapturedAt != nil {
		t.Error("unwired capture must not mark the device")
	}
}
