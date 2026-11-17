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
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/device/devicetest"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

const (
	testBootImage = "https://www.googleapis.com/compute/v1/projects/img-proj/global/images/cco-host-20260801"

	// launcherMissingStderr is the tail of a failed `cvd load` as the Host
	// Orchestrator stores it: the command line, then cvd's stderr.
	launcherMissingStderr = "execution of \"/usr/bin/cvd\" command with args [\"cvd\" \"load\"] failed: exit status 1\n" +
		" Stderr: \n'/var/lib/cuttlefish-common/artifacts/x/bin/' does not contain any of " +
		"'[cvd_internal_start, launch_cvd]'."
)

// failedLoadActuator is an actuator whose host answers every cvd load with
// the stored failure body and records boot image testBootImage.
func failedLoadActuator(t *testing.T, failure string, envConfig map[string]any) (*Actuator, *devicetest.FakeRepo) {
	t.Helper()
	body, err := json.Marshal(hoclient.ErrorResponse{Error: "cvd load failed", Details: failure})
	require.NoError(t, err)
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/cvds" && r.Method == http.MethodGet:
			w.Write([]byte(`{"cvds": []}`))
		case r.URL.Path == "/cvds" && r.Method == http.MethodPost:
			w.Write([]byte(`{"name": "op-load"}`))
		case r.URL.Path == "/operations/op-load/:wait":
			w.WriteHeader(http.StatusInternalServerError)
			w.Write(body)
		default:
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(ho.Close)
	hostURL, _ := url.Parse(ho.URL)
	port, _ := strconv.ParseInt(hostURL.Port(), 10, 64)

	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{
		ID: "h", Status: host.StatusRunning, MaxDevices: 4,
		IPAddress: "127.0.0.1", HostOrchestratorPort: port,
		BootImage: testBootImage,
	}
	deviceRepo := devicetest.NewFakeRepo()
	deviceRepo.Devices["deadbeef-dev"] = &device.Device{
		ID: "deadbeef-dev", HostID: "h", Name: "dev-1", Status: device.StatusPending,
		Mode: "advanced", InstanceCount: 1, EnvConfig: envConfig,
	}
	act := NewActuator(deviceRepo, hostRepo, nopBuilds{},
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())
	return act, deviceRepo
}

func urlEnvConfig(build string) map[string]any {
	return map[string]any{"instances": []any{
		map[string]any{"disk": map[string]any{"default_build": build}},
	}}
}

// A load that found no launcher for a URL build names the image the host
// booted, on the error the worker retries and on the device once the task is
// exhausted.
func TestCreateCVDLauncherMissingForURLSourceNamesBootImage(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"gs://bucket/build/", "https://example.com/cf-img.zip"} {
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			act, deviceRepo := failedLoadActuator(t, launcherMissingStderr, urlEnvConfig(source))

			h := NewCreateHandler(act, nil)
			exhausted := &task.Task{ResourceID: "deadbeef-dev", Attempts: task.DefaultMaxAttempts, MaxAttempts: task.DefaultMaxAttempts}
			err := h.Handle(t.Context(), exhausted)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "host image "+testBootImage+" may predate URL build sources")

			d := deviceRepo.Devices["deadbeef-dev"]
			assert.Equal(t, device.StatusError, d.Status)
			assert.Contains(t, d.ErrorMessage, "host image "+testBootImage+" may predate URL build sources "+
				"(cuttlefish 1.58, or nightly 1.58.0~git202610160155 or later)")
			assert.Contains(t, d.ErrorMessage, "launch_cvd", "the hint is added to the cvd error, never in place of it")

			_, isHostErr := errors.AsType[*hoclient.HostError](err)
			assert.True(t, isHostErr, "the hint keeps the host error in the chain")
		})
	}
}

// The same failure for an Android Build source says nothing about URL
// fetching, and so does any other failure for a URL source.
func TestCreateCVDFailureWithoutURLFetchCauseHasNoHint(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		failure   string
		envConfig map[string]any
	}{
		"android build source": {launcherMissingStderr, urlEnvConfig("@ab/aosp-main/aosp_cf_x86_64_phone-trunk_staging-userdebug")},
		"other failure":        {"validate tap devices: tap device in use", urlEnvConfig("gs://bucket/build/")},
		"api base url only": {launcherMissingStderr, map[string]any{
			"instances": []any{map[string]any{}},
			"fetch":     map[string]any{"api_base_url": "https://example.com/custom"},
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			act, _ := failedLoadActuator(t, tc.failure, tc.envConfig)
			err := act.RunCreateCVD(t.Context(), "deadbeef-dev", nil)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "may predate URL build sources")
		})
	}
}

// A host provisioned before its boot image was recorded still gets the hint.
func TestWithURLFetchHintUnrecordedBootImage(t *testing.T) {
	t.Parallel()
	err := withURLFetchHint(errors.New(launcherMissingStderr), urlEnvConfig("gs://bucket/build/"), &host.Host{ID: "h"})
	assert.True(t, strings.HasPrefix(err.Error(), "the host image may predate URL build sources"), err.Error())
}

// cvd prints one stack-trace line per failed check, so a real stderr runs past
// the 1000 runes devices.error_message keeps. The hint has to survive that cut.
func TestCreateCVDLauncherMissingHintSurvivesErrorMessageTruncation(t *testing.T) {
	t.Parallel()
	trace := strings.Repeat("  at host_tool_target.cpp:116 CF_EXPECT(FindLauncher(dir))\n", 40)
	act, deviceRepo := failedLoadActuator(t, launcherMissingStderr+"\n"+trace, urlEnvConfig("gs://bucket/build/"))

	h := NewCreateHandler(act, nil)
	exhausted := &task.Task{ResourceID: "deadbeef-dev", Attempts: task.DefaultMaxAttempts, MaxAttempts: task.DefaultMaxAttempts}
	require.Error(t, h.Handle(t.Context(), exhausted))

	stored := deviceRepo.Devices["deadbeef-dev"].ErrorMessage
	require.Greater(t, utf8.RuneCountInString(stored), 1000, "the fixture must overflow the column to test the cut")
	assert.Contains(t, spannerplatform.TruncateRunes(stored, 1000),
		"host image "+testBootImage+" may predate URL build sources")
}
