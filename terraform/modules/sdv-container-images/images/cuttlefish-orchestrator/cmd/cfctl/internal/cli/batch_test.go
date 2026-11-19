// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/alecthomas/kong"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
)

func TestE2E_BatchCreate_WaitJSONReady(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/batches":
			fmt.Fprint(w, `{"batch_id":"b-ready","name_prefix":"ci","fill_policy":"atomic","status":"provisioning","total":2,"admitted":2}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/batches/b-ready":
			fmt.Fprint(w, `{"batch_id":"b-ready","name_prefix":"ci","fill_policy":"atomic","status":"ready","total":2,"running":2,"ready":2,"admitted":2}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/batches/b-ready/devices":
			fmt.Fprint(w, `{"devices":[{"device_id":"d-1","name":"ci-000","status":"running","host_id":"h-1"},{"device_id":"d-2","name":"ci-001","status":"running","host_id":"h-1"}]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	out, err := runCLI(t, backend, "--json", "batch", "create", "--prefix", "ci", "--count", "2", "--fill-policy", "atomic", "--wait")
	if err != nil {
		t.Fatalf("batch create --wait --json failed: %v", err)
	}

	var decoded []struct {
		DeviceID string `json:"device_id"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("stdout must be pure valid JSON, got %v:\n%s", err, out)
	}
	if len(decoded) != 2 || decoded[0].DeviceID != "d-1" {
		t.Errorf("unexpected decoded members: %+v", decoded)
	}
}

func TestE2E_BatchCreate_WaitJSONFailedReturnsWaitFailedExitCode(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/batches":
			fmt.Fprint(w, `{"batch_id":"b-fail","name_prefix":"ci","fill_policy":"atomic","status":"provisioning","total":2,"admitted":2}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/batches/b-fail":
			fmt.Fprint(w, `{"batch_id":"b-fail","name_prefix":"ci","fill_policy":"atomic","status":"failed","failure_reason":"atomic teardown: member d-2 failed","total":2,"running":0,"failed":1}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/batches/b-fail/devices":
			fmt.Fprint(w, `{"devices":[{"device_id":"d-1","name":"ci-000","status":"deleted","host_id":"h-1"},{"device_id":"d-2","name":"ci-001","status":"error","host_id":"h-1"}]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	out, err := runCLI(t, backend, "--json", "batch", "create", "--prefix", "ci", "--count", "2", "--fill-policy", "atomic", "--wait")
	if err == nil {
		t.Fatalf("expected non-zero error for failed batch with --wait --json, got nil (stdout:\n%s)", out)
	}

	var waitErr *cco.WaitFailedError
	if !errors.As(err, &waitErr) {
		t.Fatalf("expected *cco.WaitFailedError in chain, got %T: %v", err, err)
	}
	if waitErr.State != cco.BatchStatusFailed || waitErr.Target != cco.BatchStatusReady {
		t.Errorf("unexpected WaitFailedError: %+v", waitErr)
	}

	wrapped := WithExitCode(err, false)
	var coder kong.ExitCoder
	if !errors.As(wrapped, &coder) || coder.ExitCode() != ExitOperationFailed {
		t.Errorf("exit code = %d, want %d (err=%v)", coder.ExitCode(), ExitOperationFailed, err)
	}

	var decoded []struct {
		DeviceID string `json:"device_id"`
		Status   string `json:"status"`
	}
	if jsonErr := json.Unmarshal([]byte(out), &decoded); jsonErr != nil {
		t.Fatalf("stdout must still be valid JSON on failed --wait --json, got %v:\n%s", jsonErr, out)
	}
	if len(decoded) != 2 || decoded[1].Status != "error" {
		t.Errorf("unexpected decoded members: %+v", decoded)
	}
}

func TestE2E_BatchCreate_WaitHumanReadyFailedAndDegraded(t *testing.T) {
	t.Run("human ready", func(t *testing.T) {
		backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requireJWT(t, r)
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/v2/batches":
				fmt.Fprint(w, `{"batch_id":"b-ok","name_prefix":"ci","fill_policy":"best_effort","status":"provisioning","total":1,"admitted":1}`)
			case r.Method == http.MethodGet && r.URL.Path == "/v2/batches/b-ok":
				fmt.Fprint(w, `{"batch_id":"b-ok","name_prefix":"ci","fill_policy":"best_effort","status":"ready","total":1,"running":1,"ready":1,"admitted":1}`)
			case r.Method == http.MethodGet && r.URL.Path == "/v2/batches/b-ok/devices":
				fmt.Fprint(w, `{"devices":[{"device_id":"d-1","name":"ci-000","status":"running","host_id":"h-1"}]}`)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})

		out, err := runCLI(t, backend, "batch", "create", "--prefix", "ci", "--count", "1", "--wait")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "Batch ready: 1/1 running") || !strings.Contains(out, "ci-000") {
			t.Errorf("unexpected human output:\n%s", out)
		}
	})

	t.Run("human degraded warns and exits zero", func(t *testing.T) {
		backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requireJWT(t, r)
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/v2/batches":
				fmt.Fprint(w, `{"batch_id":"b-deg","name_prefix":"ci","fill_policy":"best_effort","status":"provisioning","total":2,"admitted":2}`)
			case r.Method == http.MethodGet && r.URL.Path == "/v2/batches/b-deg":
				fmt.Fprint(w, `{"batch_id":"b-deg","name_prefix":"ci","fill_policy":"best_effort","status":"degraded","total":2,"running":1,"failed":1}`)
			case r.Method == http.MethodGet && r.URL.Path == "/v2/batches/b-deg/devices":
				fmt.Fprint(w, `{"devices":[{"device_id":"d-1","name":"ci-000","status":"running","host_id":"h-1"}]}`)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})

		out, err := runCLI(t, backend, "batch", "create", "--prefix", "ci", "--count", "2", "--wait")
		if err != nil {
			t.Fatalf("degraded best_effort batch should warn without error, got: %v", err)
		}
		if !strings.Contains(out, "ci-000") {
			t.Errorf("unexpected output:\n%s", out)
		}
	})

	t.Run("human failed returns WaitFailedError", func(t *testing.T) {
		backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requireJWT(t, r)
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/v2/batches":
				fmt.Fprint(w, `{"batch_id":"b-hf","name_prefix":"ci","fill_policy":"atomic","status":"provisioning","total":1,"admitted":1}`)
			case r.Method == http.MethodGet && r.URL.Path == "/v2/batches/b-hf":
				fmt.Fprint(w, `{"batch_id":"b-hf","name_prefix":"ci","fill_policy":"atomic","status":"failed","failure_reason":"boot failed","total":1}`)
			case r.Method == http.MethodGet && r.URL.Path == "/v2/batches/b-hf/devices":
				fmt.Fprint(w, `{"devices":[{"device_id":"d-1","name":"ci-000","status":"error","host_id":"h-1"}]}`)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})

		_, err := runCLI(t, backend, "batch", "create", "--prefix", "ci", "--count", "1", "--fill-policy", "atomic", "--wait")
		var waitErr *cco.WaitFailedError
		if !errors.As(err, &waitErr) {
			t.Fatalf("expected *cco.WaitFailedError, got %v", err)
		}
	})
}

func TestE2E_BatchCreateDryRunImmediateListAndDelete(t *testing.T) {
	var sawDryRun, sawDelete bool
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/batches":
			sawDryRun = true
			fmt.Fprint(w, `{"batch_id":"plan-1","name_prefix":"ci","fill_policy":"best_effort","status":"planned","total":2,"admitted":2,"new_hosts":1}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/batches":
			fmt.Fprint(w, `{"items":[{"batch_id":"b-1","name_prefix":"ci","fill_policy":"best_effort","status":"ready","total":2,"running":2}],"total":1}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/batches/b-1":
			if sawDelete {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"title":"Not Found","status":404}`)
				return
			}
			fmt.Fprint(w, `{"batch_id":"b-1","name_prefix":"ci","fill_policy":"best_effort","status":"ready","total":2,"running":2}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/v2/batches/b-1":
			sawDelete = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/batches/b-1/devices":
			fmt.Fprint(w, `{"devices":[{"device_id":"d-1","name":"ci-000","status":"running"}]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	outDry, err := runCLI(t, backend, "--json", "batch", "create", "--prefix", "ci", "--count", "2", "--dry-run")
	if err != nil || !sawDryRun || !strings.Contains(outDry, `"batch_id": "plan-1"`) {
		t.Fatalf("dry-run JSON failed: err=%v out=%s", err, outDry)
	}

	outImm, err := runCLI(t, backend, "--json", "batch", "create", "--prefix", "ci", "--count", "2")
	if err != nil || !strings.Contains(outImm, `"batch_id": "plan-1"`) {
		t.Fatalf("immediate JSON create failed: err=%v out=%s", err, outImm)
	}

	outList, err := runCLI(t, backend, "--json", "batch", "list", "--mine")
	if err != nil || !strings.Contains(outList, `"b-1"`) {
		t.Fatalf("batch list JSON failed: err=%v out=%s", err, outList)
	}

	outDevs, err := runCLI(t, backend, "--json", "batch", "devices", "b-1")
	if err != nil || !strings.Contains(outDevs, `"d-1"`) {
		t.Fatalf("batch devices JSON failed: err=%v out=%s", err, outDevs)
	}

	outDel, err := runCLI(t, backend, "--json", "batch", "delete", "b-1", "--force", "--wait")
	if err != nil || !sawDelete || !strings.Contains(outDel, `"status": "ok"`) {
		t.Fatalf("batch delete --force --wait --json failed: err=%v out=%s", err, outDel)
	}
}
