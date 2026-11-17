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

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
)

// healthyImages is a host-image listing whose every enabled family boots.
const healthyImages = `{"items":[{"machine_family":"n1","image_family":"cco-host","enabled":true,` +
	`"created_at":"2026-10-18T00:00:00Z","updated_at":"2026-10-18T00:00:00Z",` +
	`"resolved":{"ok":true,"project":"images","image_name":"cco-host-20261018"}}],"limit":200,"offset":0,"total":1}`

// doctorBackend serves every endpoint doctor probes. readyCode sets the
// /ready status.
func doctorBackend(t *testing.T, readyCode int) http.Handler {
	t.Helper()
	return doctorBackendImages(t, readyCode, healthyImages)
}

// doctorBackendImages is doctorBackend with the host-image listing given.
func doctorBackendImages(t *testing.T, readyCode int, images string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		switch r.URL.Path {
		case "/health":
			fmt.Fprint(w, `{"status":"ok"}`)
		case "/ready":
			if readyCode != http.StatusOK {
				http.Error(w, `{"status":"degraded"}`, readyCode)
				return
			}
			fmt.Fprint(w, `{"status":"ok"}`)
		case "/info":
			fmt.Fprint(w, `{"version":"v1.2.3"}`)
		case "/v2/me":
			fmt.Fprint(w, `{"email":"me@example.com"}`)
		case "/v2/quota":
			fmt.Fprint(w, `{"max_hosts":20,"used_hosts":7,"available":13,"percentage":35}`)
		case "/v2/admin/host-image-configs":
			if r.URL.Query().Get("enabled_only") != "true" {
				t.Errorf("images probe asked for disabled rows too: %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, images)
		case "/v2/storage/verify-access":
			fmt.Fprint(w, `{"gs_uri":"gs://bkt","can_proceed":true,"orchestrator_access":{"service_account":"sa@x","has_access":true,"checked_at":"now"}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
}

func TestE2E_DoctorAllChecksPass(t *testing.T) {
	out, err := runCLI(t, doctorBackend(t, http.StatusOK), "doctor", "--bucket", "bkt")
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	for _, want := range []string{
		"OK   health", "OK   ready", "server version v1.2.3", "authenticated as me@example.com",
		"7/20 hosts used", "n1=cco-host-20261018", "gs://bkt accessible",
		"OK   signer   signing as the key file's service account ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q:\n%s", want, out)
		}
	}
}

func TestE2E_DoctorFailingCheckFailsTheCommand(t *testing.T) {
	out, err := runCLI(t, doctorBackend(t, http.StatusServiceUnavailable), "doctor")
	if err == nil {
		t.Fatal("doctor must fail when a check fails")
	}
	if !strings.Contains(err.Error(), "1 of 8 checks failed") {
		t.Errorf("unexpected failure summary: %v", err)
	}
	if !strings.Contains(out, "FAIL ready") || !strings.Contains(out, "skipped (pass --bucket") {
		t.Errorf("doctor output incomplete:\n%s", out)
	}
}

// An enabled family whose image does not resolve fails the images row with
// the server's refusal, which names the setting to fix.
func TestE2E_DoctorFailsOnAFamilyThatCannotBoot(t *testing.T) {
	broken := `{"items":[{"machine_family":"c4a","image_family":"cco-host-arm64","enabled":true,` +
		`"created_at":"2026-10-18T00:00:00Z","updated_at":"2026-10-18T00:00:00Z",` +
		`"resolved":{"ok":false,"project":"images","reason":"not_found",` +
		`"message":"machine family c4a cannot boot a host: image family \"cco-host-arm64\" has no image in project \"images\""}}],` +
		`"limit":200,"offset":0,"total":1}`
	out, err := runCLI(t, doctorBackendImages(t, http.StatusOK, broken), "doctor")
	if err == nil || !strings.Contains(err.Error(), "1 of 8 checks failed") {
		t.Fatalf("err = %v, want exactly the images check failed", err)
	}
	if !strings.Contains(out, "FAIL images") || !strings.Contains(out, "cco-host-arm64") {
		t.Errorf("images row does not carry the refusal:\n%s", out)
	}
}

// doctorNamedHostBackend is doctorBackend that also answers the --host name
// lookup with hosts and records the host ID the storage probe sends.
func doctorNamedHostBackend(t *testing.T, hosts string, probed *string) http.Handler {
	t.Helper()
	doctor := doctorBackend(t, http.StatusOK)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/hosts":
			requireJWT(t, r)
			if got := r.URL.Query().Get("name"); got != "cf-host-3" {
				t.Errorf("host lookup name = %q, want cf-host-3", got)
			}
			fmt.Fprintf(w, `{"items":[%s],"next_page_token":""}`, hosts)
		case "/v2/storage/verify-access":
			var body struct {
				HostID string `json:"host_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding verify-access body: %v", err)
			}
			*probed = body.HostID
			doctor.ServeHTTP(w, r)
		default:
			doctor.ServeHTTP(w, r)
		}
	})
}

func TestE2E_DoctorHostNameIsResolved(t *testing.T) {
	var probed string
	b := doctorNamedHostBackend(t, hostItem(testHostID, "z-a", "running"), &probed)
	if _, err := runCLI(t, b, "doctor", "--bucket", "bkt", "--host", "cf-host-3"); err != nil {
		t.Fatalf("doctor --host cf-host-3: %v", err)
	}
	if probed != testHostID {
		t.Errorf("storage probe sent host_id %q, want the resolved %s", probed, testHostID)
	}
}

func TestE2E_DoctorAmbiguousHostNameIsAConflict(t *testing.T) {
	var probed string
	b := doctorNamedHostBackend(t, hostItem(testHostID, "z-a", "running")+","+hostItem(otherHostID, "z-b", "stopped"), &probed)
	out, err := runCLI(t, b, "doctor", "--bucket", "bkt", "--host", "cf-host-3")
	if got := exitCodeFor(err, false); err == nil || got != ExitConflict {
		t.Fatalf("err = %v (exit %d), want a conflict (exit %d)", err, got, ExitConflict)
	}
	for _, want := range []string{"ZONE", testHostID, otherHostID} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%s", want, err)
		}
	}
	if out != "" {
		t.Errorf("an unsettled --host must stop before any check row:\n%s", out)
	}
	if probed != "" {
		t.Errorf("storage probe ran with host_id %q", probed)
	}
}

func TestE2E_DoctorUnknownHostNameIsNotFound(t *testing.T) {
	var probed string
	_, err := runCLI(t, doctorNamedHostBackend(t, "", &probed), "doctor", "--bucket", "bkt", "--host", "cf-host-3")
	if got := exitCodeFor(err, false); err == nil || got != ExitNotFound {
		t.Fatalf("err = %v (exit %d), want not found (exit %d)", err, got, ExitNotFound)
	}
}

// Without a usable credential, doctor must still run and report the auth row
// as failed instead of dying in client construction.
func TestDoctorReportsClientConstructionFailureAsAuthRow(t *testing.T) {
	var c CLI
	// No service account and no audience: the self-signed JWT path fails.
	parser, err := NewParser(&c, testFile(config.Config{Server: "https://cco.example.com"}), "test")
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	parser.Exit = func(int) { t.Fatal("command tried to exit") }
	kctx, err := parser.Parse([]string{"doctor"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	kctx.BindTo(t.Context(), (*context.Context)(nil))
	out, err := captureStdout(t, func() error { return kctx.Run() })
	if err == nil || !strings.Contains(err.Error(), "1 of 8 checks failed") {
		t.Fatalf("err = %v, want exactly the auth check failed", err)
	}
	if !strings.Contains(out, "FAIL auth") || !strings.Contains(out, "no service account configured") {
		t.Errorf("auth row missing the construction failure:\n%s", out)
	}
	if !strings.Contains(out, "SKIP health") || !strings.Contains(out, "SKIP quota") {
		t.Errorf("client-dependent rows not skipped:\n%s", out)
	}
	if !strings.Contains(out, "OK   signer   none (unconfigured)") {
		t.Errorf("signer row must run without a client:\n%s", out)
	}
}
