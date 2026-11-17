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
	"context"
	"errors"
	"io"
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

// fakeUploader records what was streamed into artifact storage.
type fakeUploader struct {
	object, contentType string
	content             []byte
	err                 error
}

func (f *fakeUploader) UploadObject(_ context.Context, _, name string, content io.Reader, contentType string) error {
	if f.err != nil {
		return f.err
	}
	f.object, f.contentType = name, contentType
	b, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	f.content = b
	return nil
}

// emptyZIP is a real, valid ZIP archive with no entries: the 22-byte
// end-of-central-directory record and nothing else. Used as the fixture body so
// the fakes serve something the production framing check would actually accept.
// A fixture that could not survive its own validation teaches nothing.
func emptyZIP() []byte {
	return append([]byte("PK\x05\x06"), make([]byte, 18)...)
}

// bugreportHO fakes the Host Orchestrator's bugreport surface end to end.
// (The fakePolicy system-config fake is shared with the declarative tests.)
func bugreportHO(t *testing.T, zip []byte) (*httptest.Server, *bool) {
	t.Helper()
	deleted := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/:bugreport"):
			w.Write([]byte(`{"name": "op-br"}`))
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/operations/"):
			w.Write([]byte(`"br-1"`))
		case r.Method == http.MethodGet && r.URL.Path == "/cvdbugreports/br-1":
			w.Write(zip)
		case r.Method == http.MethodDelete && r.URL.Path == "/cvdbugreports/br-1":
			deleted = true
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected HO request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return ts, &deleted
}

func bugreportService(t *testing.T, upload *fakeUploader, policy *fakePolicy, defaultBucket string, hoPort int64) *DeviceService {
	t.Helper()
	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h1"] = &host.Host{
		ID: "h1", Status: host.StatusRunning, MaxDevices: 4,
		IPAddress: "127.0.0.1", HostOrchestratorPort: hoPort,
	}
	deviceRepo := newDeviceRepoFake()
	deviceRepo.Devices["d1"] = &device.Device{
		ID: "d1", Name: "triage", HostID: "h1", Status: device.StatusError,
		CVDID: "cvdd1/triage",
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

// TestBugreportCapturesToConfiguredBucket proves the full pipeline (HO
// capture, stream to storage, host-side cleanup) with the policy override
// winning over the deployment default. The subject is an errored device on
// purpose, because bugreports must not require running.
func TestBugreportCapturesToConfiguredBucket(t *testing.T) {
	t.Parallel()
	zip := emptyZIP()
	ho, deleted := bugreportHO(t, zip)
	defer ho.Close()
	hoURL, _ := url.Parse(ho.URL)
	port, _ := strconv.ParseInt(hoURL.Port(), 10, 64)

	upload := &fakeUploader{}
	policy := &fakePolicy{values: map[string]string{models.ConfigKeyBugreportBucket: "override-bucket"}}
	svc := bugreportService(t, upload, policy, "default-bucket", port)

	artifact, err := svc.Bugreport(t.Context(), "d1", false)
	if err != nil {
		t.Fatalf("Bugreport: %v", err)
	}
	if artifact.Bucket != "override-bucket" {
		t.Errorf("bucket = %q, want the policy override", artifact.Bucket)
	}
	if !strings.HasPrefix(artifact.Object, "artifacts/triage-d1/bugreports/") || !strings.HasSuffix(artifact.Object, ".zip") {
		t.Errorf("object = %q, want artifacts/triage-d1/bugreports/<ts>.zip", artifact.Object)
	}
	if string(upload.content) != string(zip) {
		t.Errorf("uploaded %d bytes, want the HO zip verbatim", len(upload.content))
	}
	if upload.contentType != "application/zip" {
		t.Errorf("content type = %q, want application/zip", upload.contentType)
	}
	if !*deleted {
		t.Error("host-side bugreport was not deleted after upload")
	}
}

// TestBugreportBucketFallbackAndErrors covers the bucket resolution ladder
// and the precondition failures.
func TestBugreportBucketFallbackAndErrors(t *testing.T) {
	t.Parallel()
	ho, _ := bugreportHO(t, emptyZIP())
	// Cleanup rather than defer, because parallel subtests run after this
	// function returns and the fake HO must outlive them.
	t.Cleanup(ho.Close)
	hoURL, _ := url.Parse(ho.URL)
	port, _ := strconv.ParseInt(hoURL.Port(), 10, 64)

	t.Run("default bucket applies when no override is set", func(t *testing.T) {
		t.Parallel()
		upload := &fakeUploader{}
		svc := bugreportService(t, upload, &fakePolicy{values: map[string]string{}}, "default-bucket", port)
		artifact, err := svc.Bugreport(t.Context(), "d1", false)
		if err != nil {
			t.Fatalf("Bugreport: %v", err)
		}
		if artifact.Bucket != "default-bucket" {
			t.Errorf("bucket = %q, want the deployment default", artifact.Bucket)
		}
	})

	t.Run("no bucket anywhere is a validation error", func(t *testing.T) {
		t.Parallel()
		svc := bugreportService(t, &fakeUploader{}, &fakePolicy{values: map[string]string{}}, "", port)
		_, err := svc.Bugreport(t.Context(), "d1", false)
		if _, ok := errors.AsType[*device.ValidationError](err); !ok {
			t.Fatalf("Bugreport error = %v, want ValidationError", err)
		}
	})

	t.Run("device without a CVD is invalid state", func(t *testing.T) {
		t.Parallel()
		upload := &fakeUploader{}
		svc := bugreportService(t, upload, &fakePolicy{values: map[string]string{}}, "default-bucket", port)
		svc.deviceRepo.(*atomicDeviceRepo).Devices["d2"] = &device.Device{ID: "d2", HostID: "h1", Status: device.StatusPending}
		if _, err := svc.Bugreport(t.Context(), "d2", false); !errors.Is(err, device.ErrInvalidState) {
			t.Fatalf("Bugreport error = %v, want ErrInvalidState", err)
		}
	})

	t.Run("artifacts not wired is a validation error", func(t *testing.T) {
		t.Parallel()
		deviceRepo := newDeviceRepoFake()
		svc, _ := NewDeviceService(deviceRepo, hosttest.NewFakeRepo(), newMockBuildRepo(),
			hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
			WithTaskEnqueuer(&fakeEnqueuer{}))
		_, err := svc.Bugreport(t.Context(), "d1", false)
		if _, ok := errors.AsType[*device.ValidationError](err); !ok {
			t.Fatalf("Bugreport error = %v, want ValidationError", err)
		}
	})
}

// A 200 carrying something that is not an archive must never reach the bucket.
//
// The motivating case is real and was observed in production, just on a
// different client. A request that loses its credentials to an auth proxy gets a
// redirect to a sign-in page, which resolves to 200 and an HTML body. Every
// status check passes and the "bugreport" is a web page. The transport cannot
// catch it, and only the framing can.
func TestBugreportRefusesABodyThatIsNotAnArchive(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"an auth proxy's sign-in page", []byte("<!doctype html><html lang=\"en-US\"><title>Sign in</title>")},
		{"an empty body", nil},
		{"a body shorter than the magic", []byte("P")},
		{"a plausible-looking near miss", []byte("PJ\x03\x04rest-of-a-nearly-right-header")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ho, _ := bugreportHO(t, tc.body)
			t.Cleanup(ho.Close)
			hoURL, _ := url.Parse(ho.URL)
			port, _ := strconv.ParseInt(hoURL.Port(), 10, 64)

			upload := &fakeUploader{}
			svc := bugreportService(t, upload, &fakePolicy{values: map[string]string{}}, "default-bucket", port)

			_, err := svc.Bugreport(t.Context(), "d1", false)
			if !errors.Is(err, errArtifactMalformed) {
				t.Fatalf("Bugreport error = %v, want errArtifactMalformed", err)
			}
			// Nothing was stored, which is what the exercise is for. An error
			// alongside a written object would still leave the bad artifact
			// behind for someone to find later and trust.
			if upload.object != "" {
				t.Errorf("uploaded %q despite the body not being an archive", upload.object)
			}
		})
	}
}

// A capture that produced no usable artifact must NOT stamp the dedup marker.
//
// CaptureBugreportOnError marks a device handled so the failed-capture detector
// stops revisiting it. Marking on a corrupt capture would retire the row
// permanently with no artifact and nobody watching. This path runs unattended
// by construction, so it is the one place a silent bad capture is never noticed.
func TestCaptureOnErrorLeavesTheRowUnmarkedWhenTheArtifactIsBad(t *testing.T) {
	t.Parallel()
	ho, _ := bugreportHO(t, []byte("<!doctype html>not an archive"))
	t.Cleanup(ho.Close)
	hoURL, _ := url.Parse(ho.URL)
	port, _ := strconv.ParseInt(hoURL.Port(), 10, 64)

	upload := &fakeUploader{}
	svc := bugreportService(t, upload, &fakePolicy{values: map[string]string{}}, "default-bucket", port)

	err := svc.CaptureBugreportOnError(t.Context(), "d1")
	if !errors.Is(err, errArtifactMalformed) {
		t.Fatalf("CaptureBugreportOnError = %v, want errArtifactMalformed so the worker retries", err)
	}

	d, err := svc.deviceRepo.Get(t.Context(), "d1")
	if err != nil {
		t.Fatalf("reloading device: %v", err)
	}
	if d.BugreportCapturedAt != nil {
		t.Error("device was marked captured despite no usable artifact; the detector will never revisit it")
	}
}

// The happy path of the same method still marks the row, so the test above is
// discriminating rather than asserting a method that never marks anything.
func TestCaptureOnErrorMarksTheRowOnAGoodArtifact(t *testing.T) {
	t.Parallel()
	ho, _ := bugreportHO(t, emptyZIP())
	t.Cleanup(ho.Close)
	hoURL, _ := url.Parse(ho.URL)
	port, _ := strconv.ParseInt(hoURL.Port(), 10, 64)

	upload := &fakeUploader{}
	svc := bugreportService(t, upload, &fakePolicy{values: map[string]string{}}, "default-bucket", port)

	if err := svc.CaptureBugreportOnError(t.Context(), "d1"); err != nil {
		t.Fatalf("CaptureBugreportOnError: %v", err)
	}
	d, err := svc.deviceRepo.Get(t.Context(), "d1")
	if err != nil {
		t.Fatalf("reloading device: %v", err)
	}
	if d.BugreportCapturedAt == nil {
		t.Error("a successful capture must mark the row so the detector stops revisiting it")
	}
	if upload.object == "" {
		t.Error("nothing was uploaded")
	}
}
