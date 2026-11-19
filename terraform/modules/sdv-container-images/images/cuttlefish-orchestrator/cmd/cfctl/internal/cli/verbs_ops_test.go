// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
)

func TestE2E_DeviceSnapshotCommands(t *testing.T) {
	var restoreCalled bool
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/devices/"+testDeviceID+"/snapshots":
			fmt.Fprint(w, `{"name":"operations/op-snap","done":false,"metadata":{"verb":"snapshot","resource_type":"device","resource_id":"`+testDeviceID+`","create_time":"2026-05-01T12:00:00Z"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/operations/op-snap":
			fmt.Fprint(w, `{"name":"operations/op-snap","done":true,"metadata":{"verb":"snapshot","resource_type":"device","resource_id":"`+testDeviceID+`","create_time":"2026-05-01T12:00:00Z"},"response":{"snapshot_id":"snap-42"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/operations/op-fail":
			fmt.Fprint(w, `{"name":"operations/op-fail","done":true,"metadata":{"verb":"snapshot","resource_type":"device","resource_id":"`+testDeviceID+`","create_time":"2026-05-01T12:00:00Z"},"error":{"title":"Conflict","status":409,"detail":"snapshot failed"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/devices/"+testDeviceID+"/snapshots":
			fmt.Fprint(w, `{"snapshots":[{"id":"snap-42","device_id":"`+testDeviceID+`","host_id":"`+testHostID+`","state":"active","created_at":"2026-05-01T12:00:00Z","updated_at":"2026-05-01T12:00:00Z","created_by":"dev@example.com"}]}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/v2/devices/"+testDeviceID+"/snapshots/snap-42":
			fmt.Fprint(w, `{"name":"operations/op-del","done":false,"metadata":{"verb":"snapshot-delete","resource_type":"device","resource_id":"`+testDeviceID+`","create_time":"2026-05-01T12:00:00Z"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/operations/op-del":
			fmt.Fprint(w, `{"name":"operations/op-del","done":true,"metadata":{"verb":"snapshot-delete","resource_type":"device","resource_id":"`+testDeviceID+`","create_time":"2026-05-01T12:00:00Z"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v2/devices/"+testDeviceID+"/restore":
			restoreCalled = true
			fmt.Fprint(w, `{"name":"operations/op-res","done":false,"metadata":{"verb":"restore","resource_type":"device","resource_id":"`+testDeviceID+`","create_time":"2026-05-01T12:00:00Z"}}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	// Take async (--json and human) + --wait (human and --json).
	out, err := runCLI(t, backend, "--json", "device", "snapshot", "take", testDeviceID)
	if err != nil || !strings.Contains(out, `"operations/op-snap"`) {
		t.Fatalf("snapshot take --json: out=%q err=%v", out, err)
	}
	if _, err := runCLI(t, backend, "device", "snapshot", "take", testDeviceID); err != nil {
		t.Fatalf("snapshot take human: %v", err)
	}
	outWait, err := runCLI(t, backend, "--json", "device", "snapshot", "take", testDeviceID, "--wait")
	if err != nil || !strings.Contains(outWait, `"done": true`) {
		t.Fatalf("snapshot take --wait --json: out=%q err=%v", outWait, err)
	}
	if _, err := runCLI(t, backend, "device", "snapshot", "take", testDeviceID, "--wait"); err != nil {
		t.Fatalf("snapshot take --wait human: %v", err)
	}

	// List table and --json.
	outList, err := runCLI(t, backend, "device", "snapshot", "list", testDeviceID)
	if err != nil || !strings.Contains(outList, "snap-42") || !strings.Contains(outList, "active") {
		t.Fatalf("snapshot list: out=%q err=%v", outList, err)
	}
	outListJSON, err := runCLI(t, backend, "--json", "device", "snapshot", "list", testDeviceID)
	if err != nil || !strings.Contains(outListJSON, `"snap-42"`) {
		t.Fatalf("snapshot list --json: out=%q err=%v", outListJSON, err)
	}

	// Delete --force --wait --json exercises runVerbOperation settle + JSON output.
	outDel, err := runCLI(t, backend, "--json", "device", "snapshot", "delete", testDeviceID, "snap-42", "--force", "--wait")
	if err != nil || !strings.Contains(outDel, `"operations/op-del"`) {
		t.Fatalf("snapshot delete --force --wait --json: out=%q err=%v", outDel, err)
	}

	// Restore declined interactively must NOT hit backend.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })
	fmt.Fprintln(w, "n")
	_ = w.Close()

	outDecline, err := runCLI(t, backend, "--json", "device", "snapshot", "restore", testDeviceID, "snap-42")
	if err != nil || restoreCalled || !strings.Contains(outDecline, `"canceled"`) {
		t.Fatalf("snapshot restore decline: restoreCalled=%v out=%q err=%v", restoreCalled, outDecline, err)
	}

	// Restore with --force succeeds.
	if _, err := runCLI(t, backend, "device", "snapshot", "restore", testDeviceID, "snap-42", "--force"); err != nil || !restoreCalled {
		t.Fatalf("snapshot restore --force: restoreCalled=%v err=%v", restoreCalled, err)
	}

	// runVerbOperation settled failure returns *cco.WaitFailedError.
	failErr := runVerbOperation(t.Context(), &CLI{}, verbFlow{
		id: testDeviceID, action: "snapshot-delete", gerund: "Deleting", past: "deleted", wait: true,
		act: func(ctx context.Context, id string) (*cco.Operation, error) {
			return &cco.Operation{Name: "operations/op-fail"}, nil
		},
		settle: func(ctx context.Context, opID string) (*cco.Operation, error) {
			return nil, &cco.WaitFailedError{State: cco.StatusOpFailed, Target: cco.StatusOpDone, Detail: "snapshot failed"}
		},
	})
	var waitErr *cco.WaitFailedError
	if !errors.As(failErr, &waitErr) {
		t.Fatalf("expected WaitFailedError from failed verbFlow, got %v", failErr)
	}
}

func TestE2E_DeviceRecordCommands(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/devices/"+testDeviceID+"/instances/1/recordings/start":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v2/devices/"+testDeviceID+"/instances/1/recordings/stop":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/devices/"+testDeviceID+"/instances/1/recordings":
			fmt.Fprint(w, `{"recordings":["clip-a.webm","clip-b.webm"]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v2/devices/"+testDeviceID+"/instances/1/recordings/clip-a.webm/fetch":
			fmt.Fprint(w, `{"bucket":"bkt","object":"rec/clip-a.webm","gs_uri":"gs://bkt/rec/clip-a.webm"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	if _, err := runCLI(t, backend, "device", "record", "start", testDeviceID, "--instance", "1"); err != nil {
		t.Fatalf("record start: %v", err)
	}
	if out, err := runCLI(t, backend, "--json", "device", "record", "stop", testDeviceID, "--instance", "1"); err != nil || !strings.Contains(out, `"record_stop"`) {
		t.Fatalf("record stop --json: out=%q err=%v", out, err)
	}
	if out, err := runCLI(t, backend, "--json", "device", "record", "list", testDeviceID, "--instance", "1"); err != nil || !strings.Contains(out, `"clip-a.webm"`) {
		t.Fatalf("record list --json: out=%q err=%v", out, err)
	}
	if _, err := runCLI(t, backend, "device", "record", "list", testDeviceID, "--instance", "1"); err != nil {
		t.Fatalf("record list human: %v", err)
	}
	if out, err := runCLI(t, backend, "--json", "device", "record", "fetch", testDeviceID, "clip-a.webm", "--instance", "1", "--no-download"); err != nil || !strings.Contains(out, `"gs://bkt/rec/clip-a.webm"`) {
		t.Fatalf("record fetch --no-download --json: out=%q err=%v", out, err)
	}
}

func TestE2E_OperationCommands(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/operations/op-run":
			fmt.Fprint(w, `{"name":"operations/op-run","done":false,"metadata":{"verb":"powerwash","resource_type":"device","resource_id":"`+testDeviceID+`","create_time":"2026-05-01T12:00:00Z","attempt":2,"last_error":"transient timeout","next_attempt_at":"2026-05-01T12:01:00Z"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/operations/op-err":
			fmt.Fprint(w, `{"name":"operations/op-err","done":true,"metadata":{"verb":"restore","resource_type":"device","resource_id":"`+testDeviceID+`","create_time":"2026-05-01T12:00:00Z"},"error":{"title":"Internal Error","status":500,"detail":"disk corrupt"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/operations":
			if got := r.URL.Query().Get("resource_id"); got != testDeviceID {
				t.Errorf("resource_id = %q, want %s", got, testDeviceID)
			}
			if got := r.URL.Query().Get("limit"); got != "10" {
				t.Errorf("limit = %q, want 10", got)
			}
			fmt.Fprint(w, `{"operations":[{"name":"operations/op-1","done":true,"metadata":{"verb":"restart","resource_type":"device","resource_id":"`+testDeviceID+`","create_time":"2026-05-01T12:00:00Z"}}],"count":1}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	outRun, err := runCLI(t, backend, "operation", "get", "op-run")
	if err != nil || !strings.Contains(outRun, "running") || !strings.Contains(outRun, "transient timeout") {
		t.Fatalf("operation get running: out=%q err=%v", outRun, err)
	}
	outErr, err := runCLI(t, backend, "operation", "get", "op-err")
	if err != nil || !strings.Contains(outErr, "failed") || !strings.Contains(outErr, "disk corrupt") {
		t.Fatalf("operation get failed: out=%q err=%v", outErr, err)
	}
	outJSON, err := runCLI(t, backend, "--json", "operation", "get", "op-run")
	if err != nil || !strings.Contains(outJSON, `"operations/op-run"`) {
		t.Fatalf("operation get --json: out=%q err=%v", outJSON, err)
	}

	outList, err := runCLI(t, backend, "operation", "list", "--resource-id", testDeviceID, "--limit", "10")
	if err != nil || !strings.Contains(outList, "op-1") || !strings.Contains(outList, "done") {
		t.Fatalf("operation list: out=%q err=%v", outList, err)
	}
	if _, err := runCLI(t, backend, "operation", "list", "--limit", "0"); err == nil {
		t.Fatal("expected error for --limit 0")
	}
	if _, err := runCLI(t, backend, "operation", "list", "--limit", "101"); err == nil {
		t.Fatal("expected error for --limit 101")
	}
}

func TestE2E_StartStopBatchListAuthAndExitError(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/batches":
			fmt.Fprint(w, `{"items":[{"batch_id":"b-1","name_prefix":"ci","status":"ready","total":2,"running":2,"errored":0}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v2/devices/"+testDeviceID+"/start":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/devices/"+testDeviceID:
			fmt.Fprintf(w, `{"id":%q,"name":"d-1","host_id":%q,"status":"running"}`, testDeviceID, testHostID)
		case r.Method == http.MethodPost && r.URL.Path == "/v2/hosts/"+testHostID+"/stop":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/hosts/"+testHostID:
			fmt.Fprintf(w, `{"id":%q,"name":"h-1","zone":"europe-west4-a","status":"stopped"}`, testHostID)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/me":
			fmt.Fprint(w, `{"email":"dev@example.com","display_name":"Dev User"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	outBatches, err := runCLI(t, backend, "batch", "list")
	if err != nil || !strings.Contains(outBatches, "b-1") {
		t.Fatalf("batch list: out=%q err=%v", outBatches, err)
	}

	if _, err := runCLI(t, backend, "device", "start", testDeviceID); err != nil {
		t.Fatalf("device start: %v", err)
	}
	outWaitDev, err := runCLI(t, backend, "--json", "device", "start", testDeviceID, "--wait")
	if err != nil || !strings.Contains(outWaitDev, `"running"`) {
		t.Fatalf("device start --wait --json: out=%q err=%v", outWaitDev, err)
	}
	outWaitHost, err := runCLI(t, backend, "host", "stop", testHostID, "--wait")
	_ = outWaitHost
	if err != nil {
		t.Fatalf("host stop --wait: %v", err)
	}

	outWho, err := runCLI(t, backend, "auth", "whoami")
	if err != nil || !strings.Contains(outWho, "dev@example.com") || !strings.Contains(outWho, "Dev User") {
		t.Fatalf("auth whoami: out=%q err=%v", outWho, err)
	}
	outWhoJSON, err := runCLI(t, backend, "--json", "auth", "whoami")
	if err != nil || !strings.Contains(outWhoJSON, `"dev@example.com"`) {
		t.Fatalf("auth whoami --json: out=%q err=%v", outWhoJSON, err)
	}

	// Test AuthStatusCmd unconfigured human output + --json with valid config.
	statusCmd := AuthStatusCmd{}
	outUnconf, err := captureStdout(t, func() error {
		return statusCmd.Run(t.Context(), &CLI{}, &config.Config{})
	})
	if err != nil || !strings.Contains(outUnconf, "unconfigured") {
		t.Fatalf("AuthStatusCmd unconfigured: out=%q err=%v", outUnconf, err)
	}
	outStatusJSON, err := runCLI(t, backend, "--json", "auth", "status")
	if err != nil {
		t.Fatalf("auth status --json: %v", err)
	}
	var statusPayload map[string]any
	if err := json.Unmarshal([]byte(outStatusJSON), &statusPayload); err != nil {
		t.Fatalf("invalid auth status JSON: %v\n%s", err, outStatusJSON)
	}

	// Verify exitError.Unwrap preserves underlying sentinel errors.
	sentinel := errors.New("root failure")
	wrapped := WithExitCode(sentinel, false)
	if !errors.Is(wrapped, sentinel) {
		t.Errorf("WithExitCode unwrap failed: %v", wrapped)
	}
}
