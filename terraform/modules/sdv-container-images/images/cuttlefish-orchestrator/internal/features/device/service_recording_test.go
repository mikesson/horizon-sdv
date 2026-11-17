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
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// fakeWebM is an EBML-framed body, the leading bytes a real Matroska/WebM
// recording carries, confirmed against a stored artifact. The fixture must be
// able to survive the production framing check, or it proves nothing about it.
func fakeWebM() []byte {
	return append([]byte{0x1A, 0x45, 0xDF, 0xA3}, []byte("fake-recording-payload")...)
}

// recordingHO fakes the HO screen-recording surface end to end.
func recordingHO(t *testing.T, webm []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/:start_screen_recording"),
			r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/:stop_screen_recording"):
			w.Write([]byte(`{"name": "op-rec"}`))
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/operations/"):
			w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/screen_recordings"):
			w.Write([]byte(`{"screen_recordings":["rec-1.webm"]}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/screen_recordings/"):
			w.Write(webm)
		default:
			t.Errorf("unexpected HO request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func recordingService(t *testing.T, upload *fakeUploader, policy *fakePolicy, defaultBucket string, hoPort int64, status device.Status) *DeviceService {
	t.Helper()
	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h1"] = &host.Host{
		ID: "h1", Status: host.StatusRunning, MaxDevices: 4,
		IPAddress: "127.0.0.1", HostOrchestratorPort: hoPort,
	}
	deviceRepo := newDeviceRepoFake()
	deviceRepo.Devices["d1"] = &device.Device{
		ID: "d1", Name: "rec-dev", HostID: "h1", Status: status, CVDID: "cvdd1/rec-dev",
		Instances: []device.Instance{{Name: "rec-dev"}},
	}
	svc, err := NewDeviceService(deviceRepo, hostRepo, newMockBuildRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
		WithTaskEnqueuer(&fakeEnqueuer{}),
		WithArtifacts(upload, policy, defaultBucket))
	if err != nil {
		t.Fatalf("NewDeviceService: %v", err)
	}
	return svc
}

// TestRecordingStartStopRequireRunning mirrors the other instance-op
// preconditions. You can only record a running device.
func TestRecordingStartStopRequireRunning(t *testing.T) {
	t.Parallel()
	ho := recordingHO(t, fakeWebM())
	t.Cleanup(ho.Close)
	hoURL, _ := url.Parse(ho.URL)
	port, _ := strconv.ParseInt(hoURL.Port(), 10, 64)

	svc := recordingService(t, &fakeUploader{}, &fakePolicy{values: map[string]string{}}, "b", port, device.StatusStopped)
	if err := svc.StartRecording(t.Context(), "d1", "rec-dev"); !errors.Is(err, device.ErrInvalidState) {
		t.Errorf("StartRecording on stopped device = %v, want ErrInvalidState", err)
	}
	if err := svc.StopRecording(t.Context(), "d1", "rec-dev"); !errors.Is(err, device.ErrInvalidState) {
		t.Errorf("StopRecording on stopped device = %v, want ErrInvalidState", err)
	}
}

// TestRecordingLifecycle covers start/stop on a running device and list on a
// stopped one (list does not require running, since recordings outlive the stop).
func TestRecordingLifecycle(t *testing.T) {
	t.Parallel()
	ho := recordingHO(t, fakeWebM())
	t.Cleanup(ho.Close)
	hoURL, _ := url.Parse(ho.URL)
	port, _ := strconv.ParseInt(hoURL.Port(), 10, 64)

	running := recordingService(t, &fakeUploader{}, &fakePolicy{values: map[string]string{}}, "b", port, device.StatusRunning)
	if err := running.StartRecording(t.Context(), "d1", "rec-dev"); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	if err := running.StopRecording(t.Context(), "d1", "rec-dev"); err != nil {
		t.Fatalf("StopRecording: %v", err)
	}

	stopped := recordingService(t, &fakeUploader{}, &fakePolicy{values: map[string]string{}}, "b", port, device.StatusStopped)
	names, err := stopped.ListRecordings(t.Context(), "d1", "rec-dev")
	if err != nil {
		t.Fatalf("ListRecordings on stopped device: %v", err)
	}
	if len(names) != 1 || names[0] != "rec-1.webm" {
		t.Errorf("recordings = %v, want [rec-1.webm]", names)
	}
}

// TestFetchRecordingToConfiguredBucket proves the HO→GCS archive path with the
// recording-bucket policy override, and the not-wired guard.
func TestFetchRecordingToConfiguredBucket(t *testing.T) {
	t.Parallel()
	webm := fakeWebM()
	ho := recordingHO(t, webm)
	t.Cleanup(ho.Close)
	hoURL, _ := url.Parse(ho.URL)
	port, _ := strconv.ParseInt(hoURL.Port(), 10, 64)

	upload := &fakeUploader{}
	policy := &fakePolicy{values: map[string]string{models.ConfigKeyRecordingBucket: "rec-bucket"}}
	svc := recordingService(t, upload, policy, "default-bucket", port, device.StatusStopped)

	artifact, err := svc.FetchRecording(t.Context(), "d1", "rec-dev", "rec-1.webm")
	if err != nil {
		t.Fatalf("FetchRecording: %v", err)
	}
	if artifact.Bucket != "rec-bucket" {
		t.Errorf("bucket = %q, want the policy override", artifact.Bucket)
	}
	if artifact.Object != "artifacts/rec-dev-d1/recordings/rec-1.webm" {
		t.Errorf("object = %q, want artifacts/rec-dev-d1/recordings/rec-1.webm", artifact.Object)
	}
	if string(upload.content) != string(webm) || upload.contentType != "video/webm" {
		t.Errorf("uploaded %q (%s), want the webm verbatim as video/webm", upload.content, upload.contentType)
	}

	t.Run("fetch without artifacts wired is a validation error", func(t *testing.T) {
		t.Parallel()
		bare, _ := NewDeviceService(newDeviceRepoFake(), hosttest.NewFakeRepo(), newMockBuildRepo(),
			hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
			WithTaskEnqueuer(&fakeEnqueuer{}))
		if _, err := bare.FetchRecording(t.Context(), "d1", "rec-dev", "x.webm"); err == nil {
			t.Fatal("FetchRecording without artifacts should fail")
		}
	})
}

// A recording body that is not EBML-framed must never reach the bucket. Same
// class as the bugreport guard. The transport only rejects HTTP failures, so
// without this any 200 body is archived as video/webm and the failure surfaces
// when someone tries to play back evidence of a bug they can no longer
// reproduce.
func TestFetchRecordingRefusesABodyThatIsNotWebM(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"an auth proxy's sign-in page", []byte("<!doctype html><title>Sign in</title>")},
		{"an empty body", nil},
		{"a body shorter than the magic", []byte{0x1A, 0x45}},
		{"the suffix mistaken for the framing", []byte("webm-bytes")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ho := recordingHO(t, tc.body)
			t.Cleanup(ho.Close)
			hoURL, _ := url.Parse(ho.URL)
			port, _ := strconv.ParseInt(hoURL.Port(), 10, 64)

			upload := &fakeUploader{}
			svc := recordingService(t, upload, &fakePolicy{values: map[string]string{}},
				"default-bucket", port, device.StatusStopped)

			_, err := svc.FetchRecording(t.Context(), "d1", "rec-dev", "rec-1.webm")
			if !errors.Is(err, errArtifactMalformed) {
				t.Fatalf("FetchRecording error = %v, want errArtifactMalformed", err)
			}
			if upload.object != "" {
				t.Errorf("archived %q despite the body not being a recording", upload.object)
			}
		})
	}
}
