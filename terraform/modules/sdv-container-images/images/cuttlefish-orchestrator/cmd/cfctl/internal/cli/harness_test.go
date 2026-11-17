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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alecthomas/kong"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/auth/authtest"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
)

// runCLI runs cfctl through the production wiring (NewParser, AfterApply and
// the real client with a locally signed SA JWT) against backend and returns
// captured stdout. It is not parallel-safe because it swaps os.Stdout.
func runCLI(t *testing.T, backend http.Handler, args ...string) (string, error) {
	t.Helper()

	srv := httptest.NewServer(backend)
	t.Cleanup(srv.Close)
	return runCLIAgainst(t, srv, args...)
}

// runCLIAgainst is runCLI for a server the caller started, for tests that
// need its URL before the command runs.
func runCLIAgainst(t *testing.T, srv *httptest.Server, args ...string) (string, error) {
	t.Helper()

	keyPath, _ := authtest.WriteKeyFile(t)
	cfg := testFile(config.Config{Server: srv.URL, CredentialsPath: keyPath})

	var c CLI
	parser, err := NewParser(&c, cfg, "test")
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	parser.Exit = func(int) { t.Fatal("command tried to exit") }

	kctx, err := parser.Parse(args)
	if err != nil {
		return "", err
	}
	ctx, cancel := c.CommandContext(t.Context())
	t.Cleanup(cancel)
	kctx.BindTo(ctx, (*context.Context)(nil))

	return captureStdout(t, func() error { return kctx.Run() })
}

// captureStdout swaps os.Stdout for a pipe around fn.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()

	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	runErr := fn()
	_ = w.Close()
	os.Stdout = old
	return <-done, runErr
}

// requireJWT fails the request if it does not carry a locally signed SA JWT,
// which is proof the production auth stack ran.
func requireJWT(t *testing.T, r *http.Request) {
	t.Helper()
	authz := r.Header.Get("Authorization")
	if !strings.HasPrefix(authz, "Bearer eyJ") || strings.Count(authz, ".") != 2 {
		t.Errorf("request %s %s lacks a signed JWT (Authorization: %.20q...)", r.Method, r.URL.Path, authz)
	}
}

// Host list follows next_page_token. The fake ignores any offset.
func TestE2E_HostListPaginatesThroughProductionStack(t *testing.T) {
	const total = 150
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.URL.Path != "/v2/hosts" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		limit, start := 0, 0
		fmt.Sscan(r.URL.Query().Get("limit"), &limit)
		if token := r.URL.Query().Get("page_token"); token != "" {
			fmt.Sscanf(token, "after-%d", &start)
		}
		end := min(start+limit, total)
		items := make([]string, 0, limit)
		for i := start; i < end; i++ {
			items = append(items, fmt.Sprintf(`{"id":"h%03d","name":"n%03d","zone":"z","status":"running"}`, i, i))
		}
		next := ""
		if end < total {
			next = fmt.Sprintf("after-%d", end)
		}
		fmt.Fprintf(w, `{"items":[%s],"next_page_token":%q}`, strings.Join(items, ","), next)
	})

	out, err := runCLI(t, backend, "host", "list", "--json")
	if err != nil {
		t.Fatalf("host list --json: %v", err)
	}
	var hosts []map[string]any
	if err := json.Unmarshal([]byte(out), &hosts); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\n%s", err, out)
	}
	if len(hosts) != total {
		t.Fatalf("got %d hosts, want %d (pagination through the full command path)", len(hosts), total)
	}

	out, err = runCLI(t, backend, "host", "list")
	if err != nil {
		t.Fatalf("host list: %v", err)
	}
	if got := strings.Count(out, "\n"); got != total+1 { // header + rows
		t.Errorf("table has %d lines, want %d", got, total+1)
	}
}

func TestE2E_StatusRetries429AndReadsInfo(t *testing.T) {
	var healthCalls atomic.Int64
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			if healthCalls.Add(1) == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			fmt.Fprint(w, `{"status":"ok"}`)
		case "/info":
			requireJWT(t, r)
			fmt.Fprint(w, `{"version":"9.9.9"}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})

	out, err := runCLI(t, backend, "status", "--json")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if healthCalls.Load() != 2 {
		t.Errorf("health calls = %d, want 2 (the production retry transport must be wired)", healthCalls.Load())
	}
	var got struct{ Status, Version string }
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding status JSON: %v\n%s", err, out)
	}
	if got.Status != "ok" || got.Version != "9.9.9" {
		t.Errorf("status = %+v, want ok/9.9.9", got)
	}
}

// assertDeleteForceJSON runs `<resource> delete <id> --force --json` against
// a backend that accepts exactly one DELETE on path, asserting the emitted
// ActionResult, the shared contract of every delete command.
func assertDeleteForceJSON(t *testing.T, resource, id, path string) {
	t.Helper()
	var deleted atomic.Bool
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == path {
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	})

	out, err := runCLI(t, backend, resource, "delete", id, "--force", "--json")
	if err != nil {
		t.Fatalf("%s delete: %v", resource, err)
	}
	if !deleted.Load() {
		t.Fatal("backend never saw the DELETE")
	}
	var result struct{ Resource, ID, Action, Status string }
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("stdout is not an ActionResult: %v\n%s", err, out)
	}
	if result.Resource != resource || result.ID != id || result.Action != "delete" || result.Status != "ok" {
		t.Errorf("ActionResult = %+v", result)
	}
}

func TestE2E_DeviceDeleteForceJSONEmitsActionResult(t *testing.T) {
	assertDeleteForceJSON(t, "device", testDeviceID, "/v2/devices/"+testDeviceID)
}

func TestE2E_DeviceCreateWaitJSONPrintsFinalEntity(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/devices":
			fmt.Fprint(w, `{"id":"d9","name":"d9","host_id":"h1","status":"creating"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/devices/d9":
			fmt.Fprint(w, `{"id":"d9","name":"d9","host_id":"h1","status":"running","adb_port":6520}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})

	out, err := runCLI(t, backend, "device", "create", "--host", testHostID, "--name", "d9", "--wait", "--json")
	if err != nil {
		t.Fatalf("device create --wait: %v", err)
	}
	var device struct {
		Status  string `json:"status"`
		ADBPort int    `json:"adb_port"`
	}
	if err := json.Unmarshal([]byte(out), &device); err != nil {
		t.Fatalf("decoding device JSON: %v\n%s", err, out)
	}
	if device.Status != "running" || device.ADBPort != 6520 {
		t.Errorf("waited entity = %+v, want the FINAL running state, not the create response", device)
	}
}

func TestE2E_ProblemDetailsSurfaceInErrors(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"title":"Not Found","status":404,"detail":"host not found"}`)
	})

	_, err := runCLI(t, backend, "host", "get", testHostID)
	if err == nil || !strings.Contains(err.Error(), "host not found") {
		t.Fatalf("err = %v, want the backend's problem detail", err)
	}
}

func TestE2E_EmptyListJSONIsArray(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[]}`)
	})

	out, err := runCLI(t, backend, "device", "list", "--json")
	if err != nil {
		t.Fatalf("device list --json: %v", err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("stdout = %q, want [] (never null)", out)
	}
}

func TestE2E_ConfirmWithoutForceFailsOnNonInteractiveStdin(t *testing.T) {
	// Stdin is at EOF under go test, so the prompt must fail. The prompt's
	// creator annotation may issue GETs, but the DELETE must never happen.
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("only prompt-annotation GETs are allowed, got %s %s", r.Method, r.URL.Path)
		}
	})

	_, err := runCLI(t, backend, "host", "delete", testHostID, "--json")
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v, want a pass---force guidance error", err)
	}
}

// The --mine flag must send mine=true on every list command. Without the flag
// the CLI must not send the parameter.
func TestE2E_ListMineSendsParam(t *testing.T) {
	var lastQuery string
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastQuery = r.URL.RawQuery
		fmt.Fprint(w, `{"items":[]}`)
	})

	for _, tc := range []struct {
		args     []string
		wantMine string
	}{
		{[]string{"device", "list", "--mine", "--json"}, "true"},
		{[]string{"device", "list", "--json"}, ""},
		{[]string{"host", "list", "--mine", "--json"}, "true"},
		{[]string{"batch", "list", "--created-by", "other@example.com", "--json"}, ""},
	} {
		if _, err := runCLI(t, backend, tc.args...); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		q, err := url.ParseQuery(lastQuery)
		if err != nil {
			t.Fatalf("%v: parsing query %q: %v", tc.args, lastQuery, err)
		}
		if got := q.Get("mine"); got != tc.wantMine {
			t.Errorf("%v sent mine=%q, want %q", tc.args, got, tc.wantMine)
		}
	}
	if q, _ := url.ParseQuery(lastQuery); q.Get("created_by") != "other@example.com" {
		t.Errorf("batch list --created-by sent created_by=%q, want other@example.com", q.Get("created_by"))
	}
}

// A 200 with an empty capture must fail the command and write no file. A CVD
// broken by a reboot returns this.
func TestE2E_ScreenshotEmptyBodyFails(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"screenshot_bytes":"","screenshot_mime_type":""}`)
	})

	out := t.TempDir() + "/shot.png"
	_, err := runCLI(t, backend, "device", "screenshot", testDeviceID, "--instance", "ivi", "--output", out)
	if err == nil || !strings.Contains(err.Error(), "empty screenshot") {
		t.Fatalf("err = %v, want the empty-screenshot failure", err)
	}
	if _, statErr := os.Stat(out); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("a file was written for an empty screenshot (stat: %v)", statErr)
	}
}

// The --timeout flag must bound an in-flight API request through the whole
// production stack.
func TestE2E_TimeoutBoundsInFlightRequest(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done(): // The canceled client lets teardown drain fast.
		case <-time.After(10 * time.Second):
			fmt.Fprint(w, `{"status":"ok"}`)
		}
	})

	start := time.Now()
	_, err := runCLI(t, backend, "status", "--timeout", "300ms")
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("err = %v, want the --timeout deadline", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("command took %v, want ~the 300ms --timeout bound", elapsed)
	}
}

func TestForeignCreatorDetail(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/me" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"email":"me@example.com"}`)
	})
	srv := httptest.NewServer(backend)
	t.Cleanup(srv.Close)
	keyPath, _ := authtest.WriteKeyFile(t)
	client, err := cco.NewClient(t.Context(), srv.URL, &config.Config{Server: srv.URL, CredentialsPath: keyPath}, 0)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if got := foreignCreatorDetail(t.Context(), client, ""); got != "" {
		t.Errorf("unknown creator = %q, want empty", got)
	}
	if got := foreignCreatorDetail(t.Context(), client, "ME@example.com"); got != "" {
		t.Errorf("own resource (case-insensitive) = %q, want empty", got)
	}
	want := " (created by bob@example.com, not you)"
	if got := foreignCreatorDetail(t.Context(), client, "bob@example.com"); got != want {
		t.Errorf("foreign resource = %q, want %q", got, want)
	}
}

func TestDeleteFlow_InteractiveDeclineJSONEmitsCanceled(t *testing.T) {
	// A decline exits 0, and a --json consumer must still get a parseable
	// canceled outcome.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })
	fmt.Fprintln(w, "n")
	_ = w.Close()

	flow := deleteFlow{
		resource: "host", id: "h1",
		del: func(context.Context, string) error {
			t.Error("decline must not delete")
			return nil
		},
	}
	out, err := captureStdout(t, func() error {
		return flow.run(t.Context(), &CLI{JSON: true})
	})
	if err != nil {
		t.Fatalf("run: %v (a decline is not an error)", err)
	}
	var result struct{ Resource, ID, Action, Status string }
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("stdout is not an ActionResult: %v\n%s", err, out)
	}
	if result.Status != "canceled" || result.ID != "h1" || result.Action != "delete" {
		t.Errorf("ActionResult = %+v, want canceled delete of h1", result)
	}
}

func TestParse_CorruptConfigFailsCommandsButNotCompletion(t *testing.T) {
	newParser := func(t *testing.T) (*CLI, *kong.Kong) {
		t.Helper()
		var c CLI
		c.ConfigErr = errors.New("yaml exploded")
		parser, err := NewParser(&c, nil, "test")
		if err != nil {
			t.Fatalf("NewParser: %v", err)
		}
		parser.Exit = func(int) {}
		return &c, parser
	}

	t.Run("config-dependent command fails with the real cause", func(t *testing.T) {
		_, parser := newParser(t)
		_, err := parser.Parse([]string{"host", "list"})
		if err == nil || !strings.Contains(err.Error(), "yaml exploded") {
			t.Fatalf("err = %v, want the load error, not a masked symptom", err)
		}
		if !strings.Contains(err.Error(), "by hand or delete it") {
			t.Errorf("err = %v, want remediation guidance", err)
		}
	})

	t.Run("completion still works", func(t *testing.T) {
		_, parser := newParser(t)
		if _, err := parser.Parse([]string{"completion", "bash"}); err != nil {
			t.Fatalf("completion must tolerate a corrupt config: %v", err)
		}
	})
}

// A declarative create must carry requirements (and no host_id) on the wire,
// and surface the resolution in --json output.
func TestE2E_DeviceCreateDeclarative(t *testing.T) {
	var sawBody map[string]any
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v2/devices" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&sawBody); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		fmt.Fprint(w, `{"id":"d1","name":"auto-dev","host_id":"h-placed","status":"pending",
			"resolution":{"build_id":"b1","build_name":"seeded","build_action":"use",
			              "host_id":"h-placed","host_action":"place"}}`)
	})

	out, err := runCLI(t, backend, "device", "create", "--name", "auto-dev", "--interaction", "headless", "--json")
	if err != nil {
		t.Fatalf("declarative create: %v", err)
	}

	if _, hasHost := sawBody["host_id"]; hasHost {
		t.Error("declarative request must not carry host_id")
	}
	reqs, _ := sawBody["requirements"].(map[string]any)
	if reqs == nil || reqs["interaction"] != "headless" {
		t.Errorf("requirements = %v, want interaction=headless", sawBody["requirements"])
	}

	var outcome struct {
		ID         string `json:"id"`
		Resolution *struct {
			HostAction string `json:"host_action"`
		} `json:"resolution"`
	}
	if err := json.Unmarshal([]byte(out), &outcome); err != nil {
		t.Fatalf("stdout is not a create outcome: %v\n%s", err, out)
	}
	if outcome.ID != "d1" || outcome.Resolution == nil || outcome.Resolution.HostAction != "place" {
		t.Errorf("outcome = %+v, want device d1 with a place resolution", outcome)
	}
}

// --dry-run sends validate_only and never waits; the wire carries the GPU
// hard requirement.
func TestE2E_DeviceCreateDryRun(t *testing.T) {
	var sawBody map[string]any
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sawBody); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		fmt.Fprint(w, `{"name":"plan-dev","status":"pending",
			"resolution":{"build_name":"aosp-android-latest-release (latest)","build_action":"register",
			              "host_action":"provision","machine_type":"g2-standard-8","zone":"europe-west4-a"}}`)
	})

	out, err := runCLI(t, backend, "device", "create", "--name", "plan-dev",
		"--interaction", "interactive", "--gpu-required", "--dry-run", "--json")
	if err != nil {
		t.Fatalf("dry-run create: %v", err)
	}

	if sawBody["validate_only"] != true {
		t.Errorf("validate_only = %v, want true", sawBody["validate_only"])
	}
	reqs, _ := sawBody["requirements"].(map[string]any)
	if reqs == nil || reqs["gpu_required"] != true {
		t.Errorf("requirements = %v, want gpu_required=true", sawBody["requirements"])
	}

	var outcome struct {
		ID         string `json:"id"`
		Resolution *struct {
			HostAction  string `json:"host_action"`
			MachineType string `json:"machine_type"`
		} `json:"resolution"`
	}
	if err := json.Unmarshal([]byte(out), &outcome); err != nil {
		t.Fatalf("stdout is not a plan outcome: %v\n%s", err, out)
	}
	if outcome.ID != "" {
		t.Error("a dry run must not carry a device ID")
	}
	if outcome.Resolution == nil || outcome.Resolution.HostAction != "provision" || outcome.Resolution.MachineType != "g2-standard-8" {
		t.Errorf("resolution = %+v, want a provision plan", outcome.Resolution)
	}
}

// The /v2/batches endpoint ignores limit and offset, so a paging client would
// never terminate. The client must issue one unpaged GET.
func TestE2E_BatchListIsUnpaged(t *testing.T) {
	const total = 130
	var calls atomic.Int64
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/batches" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		calls.Add(1)
		if q := r.URL.Query(); q.Has("limit") || q.Has("offset") {
			t.Errorf("batch list sent pagination params (%q); the server ignores them", r.URL.RawQuery)
		}
		items := make([]string, 0, total)
		for i := range total {
			items = append(items, fmt.Sprintf(`{"batch_id":"b%03d","status":"ready","total":2,"running":2}`, i))
		}
		fmt.Fprintf(w, `{"items":[%s],"total":%d}`, strings.Join(items, ","), total)
	})

	out, err := runCLI(t, backend, "batch", "list", "--json")
	if err != nil {
		t.Fatalf("batch list --json: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("batch list made %d requests, want exactly 1", calls.Load())
	}
	var batches []map[string]any
	if err := json.Unmarshal([]byte(out), &batches); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\n%s", err, out)
	}
	if len(batches) != total {
		t.Fatalf("got %d batches, want all %d from the single response", len(batches), total)
	}
}

func TestE2E_BatchDeleteForceJSONEmitsActionResult(t *testing.T) {
	assertDeleteForceJSON(t, "batch", "b1", "/v2/batches/b1")
}

func TestE2E_BatchDeletePromptsWithoutForce(t *testing.T) {
	// Stdin is at EOF under go test, so the prompt must fail with --force
	// guidance. Only the prompt annotation GETs may reach the server.
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/batches/b1":
			fmt.Fprint(w, `{"batch_id":"b1","status":"ready","total":12,"created_by":"bob@example.com"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/me":
			fmt.Fprint(w, `{"email":"me@example.com"}`)
		default:
			t.Errorf("only prompt-annotation GETs are allowed, got %s %s", r.Method, r.URL.Path)
		}
	})

	_, err := runCLI(t, backend, "batch", "delete", "b1")
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v, want a pass---force guidance error", err)
	}
}

func TestE2E_BatchDeleteWaitPollsUntilGone(t *testing.T) {
	// The server keeps a batch visible only while it has live members, so
	// --wait polls until the batch returns 404.
	var deleted atomic.Bool
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v2/batches/b1":
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/batches/b1":
			if !deleted.Load() {
				t.Error("GetBatch polled before the DELETE was issued")
			}
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"title":"Not Found","status":404,"detail":"batch not found"}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})

	out, err := runCLI(t, backend, "batch", "delete", "b1", "--force", "--wait", "--json")
	if err != nil {
		t.Fatalf("batch delete --wait: %v", err)
	}
	var result struct{ Status string }
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("stdout is not an ActionResult: %v\n%s", err, out)
	}
	if result.Status != "ok" {
		t.Errorf("ActionResult status = %q, want ok", result.Status)
	}
}

func TestE2E_BatchCreateRejectsLoneTrackFlag(t *testing.T) {
	// Batch create shares device create's requirement validation.
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("invalid flags must fail client-side, got %s %s", r.Method, r.URL.Path)
	})

	_, err := runCLI(t, backend, "batch", "create", "--prefix", "ci", "--count", "2",
		"--track-target", "aosp_cf_x86_64_only_phone-userdebug")
	if err == nil || !strings.Contains(err.Error(), "--track-branch and --track-target must be set together") {
		t.Fatalf("err = %v, want the track-pairing rule", err)
	}
}

// An over-cap --limit fails client-side with the valid range.
func TestE2E_LimitBeyondServerCapFailsClientSide(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("limit validation must fail client-side, got %s %s", r.Method, r.URL.Path)
	})
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"operation", "list", "--limit", "500"}, "between 1 and 100"},
		{[]string{"admin", "tasks", "list", "--limit", "1000"}, "between 1 and 500"},
		{[]string{"operation", "list", "--limit", "0"}, "between 1 and 100"},
	} {
		_, err := runCLI(t, backend, tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
}

// The server reads --gpu-type and --gpu-count as one setting and ignores a
// lone flag, so either flag alone must fail client-side.
func TestE2E_HostCreateRequiresGPUPair(t *testing.T) {
	rejecting := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("GPU pairing must fail client-side, got %s %s", r.Method, r.URL.Path)
	})
	for _, args := range [][]string{
		{"host", "create", "--name", "g1", "--gpu-type", "nvidia-tesla-t4"},
		{"host", "create", "--name", "g1", "--gpu-count", "2"},
		{"host", "create", "--name", "g1", "--gpu-type", "nvidia-tesla-t4", "--gpu-count", "0"},
	} {
		_, err := runCLI(t, rejecting, args...)
		if err == nil || !strings.Contains(err.Error(), "--gpu-type and --gpu-count must be set together") {
			t.Errorf("%v: err = %v, want the pairing rule", args[2:], err)
		}
	}

	accepting := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"h1","name":"g1","zone":"z","status":"pending"}`)
	})
	if _, err := runCLI(t, accepting, "host", "create", "--name", "g1",
		"--gpu-type", "nvidia-tesla-t4", "--gpu-count", "2", "--json"); err != nil {
		t.Errorf("the full pair must be accepted: %v", err)
	}
	if _, err := runCLI(t, accepting, "host", "create", "--name", "c1", "--json"); err != nil {
		t.Errorf("neither flag (CPU-only) must be accepted: %v", err)
	}
}

// Device create prints next-step hints on stderr so stdout carries only the
// result.
func TestE2E_DeviceCreateHintsNextSteps(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"d1","name":"dev1","host_id":"h1","status":"pending"}`)
	})

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	oldStderr := os.Stderr
	os.Stderr = w
	captured := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		captured <- string(b)
	}()

	stdout, runErr := runCLI(t, backend, "device", "create", "--name", "dev1", "--interaction", "headless")
	os.Stderr = oldStderr
	_ = w.Close()
	stderr := <-captured

	if runErr != nil {
		t.Fatalf("device create: %v", runErr)
	}
	for _, hint := range []string{"cfctl device connect d1", "cfctl device webrtc d1"} {
		if !strings.Contains(stderr, hint) {
			t.Errorf("stderr is missing the next-step hint %q:\n%s", hint, stderr)
		}
		if strings.Contains(stdout, hint) {
			t.Errorf("next-step hint %q leaked onto stdout:\n%s", hint, stdout)
		}
	}
}

// Admin tasks list renders a header row and one aligned line per task.
func TestE2E_AdminTasksListRendersTable(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/admin/tasks/failed" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"tasks":[
			{"id":"t1","type":"device.delete","resource_id":"d1","attempts":2,"max_attempts":5,"last_error":"host unreachable"},
			{"id":"t2","type":"host.provision","resource_id":"h9","attempts":5,"max_attempts":5,"last_error":"zone stockout"}]}`)
	})

	out, err := runCLI(t, backend, "admin", "tasks", "list")
	if err != nil {
		t.Fatalf("admin tasks list: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want header + 2 rows:\n%s", len(lines), out)
	}
	for col, want := range map[string]string{
		"header": "LAST ERROR", "row 1": "2/5", "row 2": "zone stockout",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table is missing %s content %q:\n%s", col, want, out)
		}
	}
	if !strings.Contains(lines[0], "ID") || !strings.Contains(lines[1], "t1") {
		t.Errorf("want header first then rows:\n%s", out)
	}
}

// A create with no flags is a declarative headless create under a generated
// name.
func TestE2E_DeviceCreateWithNoFlagsDefaultsToHeadless(t *testing.T) {
	var sawBody map[string]any
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v2/devices" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&sawBody); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		fmt.Fprint(w, `{"id":"d1","name":"cvd-x","host_id":"h1","status":"pending"}`)
	})

	if _, err := runCLI(t, backend, "device", "create"); err != nil {
		t.Fatalf("bare device create: %v", err)
	}
	if _, hasHost := sawBody["host_id"]; hasHost {
		t.Error("a bare create must be declarative and carry no host_id")
	}
	reqs, _ := sawBody["requirements"].(map[string]any)
	if reqs == nil || reqs["interaction"] != "headless" {
		t.Errorf("requirements = %v, want interaction=headless", sawBody["requirements"])
	}
	name, _ := sawBody["name"].(string)
	if !regexp.MustCompile(`^cvd-[a-z0-9]{8}$`).MatchString(name) {
		t.Errorf("name = %q, want cvd- plus 8 lowercase letters or digits", name)
	}
}

// A named host keeps the create imperative; the headless default applies only
// when neither --host nor --interaction is given.
func TestE2E_DeviceCreateWithHostStaysImperative(t *testing.T) {
	var sawBody map[string]any
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sawBody); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		fmt.Fprint(w, `{"id":"d1","name":"cvd-x","host_id":"h1","status":"pending"}`)
	})

	if _, err := runCLI(t, backend, "device", "create", "--host", testHostID); err != nil {
		t.Fatalf("device create --host: %v", err)
	}
	if sawBody["host_id"] != testHostID {
		t.Errorf("host_id = %v, want %s", sawBody["host_id"], testHostID)
	}
	if _, hasReqs := sawBody["requirements"]; hasReqs {
		t.Error("an imperative create must not carry requirements")
	}
}

// Batch create uses the same --build-id flag as device create, and rejects
// --build.
func TestE2E_BatchCreateBuildIDFlagMatchesDeviceCreate(t *testing.T) {
	var sawBody map[string]any
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sawBody); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		fmt.Fprint(w, `{"status":"planned","requested":2,"admitted":2}`)
	})

	if _, err := runCLI(t, backend, "batch", "create", "--prefix", "ci", "--count", "2",
		"--build-id", "b-123", "--dry-run", "--json"); err != nil {
		t.Fatalf("batch create --build-id: %v", err)
	}
	reqs, _ := sawBody["requirements"].(map[string]any)
	build, _ := reqs["build"].(map[string]any)
	if build == nil || build["registered"] != "b-123" {
		t.Errorf("requirements = %v, want build.registered=b-123", sawBody["requirements"])
	}

	_, err := runCLI(t, backend, "batch", "create", "--prefix", "ci", "--count", "2",
		"--build", "b-123", "--dry-run", "--json")
	if err == nil || !strings.Contains(err.Error(), "--build") {
		t.Fatalf("err = %v, want the old --build spelling rejected", err)
	}
}

func TestE2E_AuthStatusJSON(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("auth status is local-only, got %s %s", r.Method, r.URL.Path)
	})

	out, err := runCLI(t, backend, "auth", "status", "--json")
	if err != nil {
		t.Fatalf("auth status --json: %v", err)
	}
	var got struct{ Server, Method, Credential string }
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	if got.Server == "" || !strings.Contains(got.Method, "key file") || got.Credential == "" {
		t.Errorf("auth status = %+v, want the resolved key-file mode with server and credential", got)
	}
}

func TestE2E_ConfigShowJSON(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("config show is local-only, got %s %s", r.Method, r.URL.Path)
	})

	out, err := runCLI(t, backend, "config", "show", "--json")
	if err != nil {
		t.Fatalf("config show --json: %v", err)
	}
	type sourced struct {
		Value  string `json:"value"`
		Source string `json:"source"`
	}
	var got struct {
		Path            string  `json:"path"`
		Server          sourced `json:"server"`
		CredentialsPath sourced `json:"credentials_path"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	if got.Path == "" || got.Server.Value == "" || got.CredentialsPath.Value == "" {
		t.Errorf("config show = %+v, want path, server, and credentials populated", got)
	}
	if got.Server.Source != "profile" || got.CredentialsPath.Source != "profile" {
		t.Errorf("config show = %+v, want both values sourced from the profile", got)
	}
}

// A host in error state must show the backend's message, suggestion,
// alternative zones and retryability.
func TestE2E_HostGetShowsErrorGuidance(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{
			"id":"h1","name":"stock","zone":"europe-west4-a","status":"error",
			"machine_type":"n1-standard-8","max_devices":4,
			"error_message":"ZONE_RESOURCE_POOL_EXHAUSTED: no n1-standard-8 capacity",
			"error_suggestion":"Retry in another zone or later.",
			"error_alternative_zones":["europe-west4-b","europe-west1-b"],
			"error_retryable":true,
			"created_at":"2026-07-31T10:00:00Z"}`)
	})

	out, err := runCLI(t, backend, "host", "get", testHostID)
	if err != nil {
		t.Fatalf("host get: %v", err)
	}
	for _, want := range []string{
		"ZONE_RESOURCE_POOL_EXHAUSTED",
		"Retry in another zone or later.",
		"europe-west4-b, europe-west1-b",
		"Retryable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("host get output missing %q:\n%s", want, out)
		}
	}
}

// Build list must page through the whole collection. The endpoint is keyset
// paged, so the fake serves next_page_token and ignores any offset.
func TestE2E_BuildListPaginatesThroughProductionStack(t *testing.T) {
	const total = 250
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.URL.Path != "/v2/builds" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		limit, start := 0, 0
		fmt.Sscan(r.URL.Query().Get("limit"), &limit)
		if token := r.URL.Query().Get("page_token"); token != "" {
			fmt.Sscanf(token, "after-%d", &start)
		}
		end := min(start+limit, total)
		items := make([]string, 0, limit)
		for i := start; i < end; i++ {
			items = append(items, fmt.Sprintf(`{"id":"b%03d","name":"build-%03d","source":"url","type":"custom","target":"t"}`, i, i))
		}
		next := ""
		if end < total {
			next = fmt.Sprintf("after-%d", end)
		}
		fmt.Fprintf(w, `{"items":[%s],"total":%d,"next_page_token":%q}`, strings.Join(items, ","), total, next)
	})

	out, err := runCLI(t, backend, "build", "list", "--json")
	if err != nil {
		t.Fatalf("build list --json: %v", err)
	}
	var builds []map[string]any
	if err := json.Unmarshal([]byte(out), &builds); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\n%s", err, out)
	}
	if len(builds) != total {
		t.Fatalf("got %d builds, want %d (pagination through the full command path)", len(builds), total)
	}
}

// captureStderr swaps os.Stderr for a pipe around fn.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	os.Stderr = old
	_ = w.Close()
	return <-done
}

// A --json run must not print next-step hints. CLI.AfterRun owns this rule.
func TestE2E_JSONSuppressesNextStepHints(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"d1","name":"dev1","host_id":"h1","status":"pending"}`)
	})
	var runErr error
	stderr := captureStderr(t, func() {
		_, runErr = runCLI(t, backend, "--json", "device", "create", "--name", "dev1", "--interaction", "headless")
	})
	if runErr != nil {
		t.Fatalf("device create --json: %v", runErr)
	}
	if strings.Contains(stderr, "cfctl device connect") {
		t.Errorf("--json run still printed a next-step hint:\n%s", stderr)
	}
}
