// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestE2E_BatchGetAndBatchDevices(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/batches/b-1":
			fmt.Fprint(w, `{"batch_id":"b-1","name_prefix":"ci","fill_policy":"best_effort","status":"ready","total":2,"running":2,"ready":2,"admitted":2}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/batches/b-1/devices":
			fmt.Fprint(w, `{"devices":[{"device_id":"d-1","name":"ci-000","status":"running","host_id":"h-1"}]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	outGet, err := runCLI(t, backend, "batch", "get", "b-1")
	if err != nil {
		t.Fatalf("batch get failed: %v", err)
	}
	if !strings.Contains(outGet, "b-1") || !strings.Contains(outGet, "ready") {
		t.Errorf("unexpected batch get output:\n%s", outGet)
	}

	outJSON, err := runCLI(t, backend, "--json", "batch", "get", "b-1")
	if err != nil {
		t.Fatalf("--json batch get failed: %v", err)
	}
	if !strings.Contains(outJSON, `"batch_id": "b-1"`) {
		t.Errorf("unexpected --json batch get output:\n%s", outJSON)
	}

	outDevs, err := runCLI(t, backend, "batch", "devices", "b-1")
	if err != nil {
		t.Fatalf("batch devices failed: %v", err)
	}
	if !strings.Contains(outDevs, "ci-000") {
		t.Errorf("unexpected batch devices output:\n%s", outDevs)
	}
}

func TestE2E_DeviceLifecycleAndBugreport(t *testing.T) {
	var gotIncludeADB bool
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/devices/"+testDeviceID+"/lifecycle":
			fmt.Fprint(w, `{"mode":"ephemeral","ttl_seconds":14400,"extend_count":1}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v2/devices/"+testDeviceID+"/bugreport":
			gotIncludeADB = true
			fmt.Fprint(w, `{"bucket":"bkt","object":"bugreports/`+testDeviceID+`.zip","gs_uri":"gs://bkt/bugreports/`+testDeviceID+`.zip","download_path":"/v2/devices/`+testDeviceID+`/bugreport"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	outLife, err := runCLI(t, backend, "device", "lifecycle", testDeviceID)
	if err != nil {
		t.Fatalf("device lifecycle failed: %v", err)
	}
	if !strings.Contains(outLife, "ephemeral") {
		t.Errorf("unexpected lifecycle output:\n%s", outLife)
	}

	outBR, err := runCLI(t, backend, "--json", "device", "bugreport", testDeviceID, "--include-adb", "--no-download")
	if err != nil {
		t.Fatalf("device bugreport failed: %v", err)
	}
	if !gotIncludeADB {
		t.Error("expected bugreport endpoint call")
	}
	if !strings.Contains(outBR, `"gs_uri": "gs://bkt/bugreports/`+testDeviceID+`.zip"`) {
		t.Errorf("unexpected bugreport JSON output:\n%s", outBR)
	}
}
