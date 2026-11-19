// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package output

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func captureStdoutStderr(t *testing.T, fn func()) (string, string) {
	t.Helper()

	origOut, origErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	os.Stdout, os.Stderr = outW, errW
	t.Cleanup(func() {
		os.Stdout, os.Stderr = origOut, origErr
	})

	fn()

	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = origOut, origErr

	outBytes, _ := io.ReadAll(outR)
	errBytes, _ := io.ReadAll(errR)
	_ = outR.Close()
	_ = errR.Close()
	return string(outBytes), string(errBytes)
}

func TestOutputFormatHelpers(t *testing.T) {
	t.Cleanup(func() { SetVerbose(false) })

	stdout, stderr := captureStdoutStderr(t, func() {
		if err := PrintActionResult("device", "dev-1", "pin"); err != nil {
			t.Errorf("PrintActionResult failed: %v", err)
		}
		Success("created %s", "dev-1")
		Info("waiting on %s", "host-1")
		Warn("deprecated flag %s", "--old")

		SetVerbose(false)
		Debug("hidden %d", 1)
		SetVerbose(true)
		Debug("visible %d", 2)
	})

	// First JSON block in stdout must decode cleanly as ActionResult.
	dec := json.NewDecoder(strings.NewReader(stdout))
	var res ActionResult
	if err := dec.Decode(&res); err != nil {
		t.Fatalf("decode ActionResult from stdout %q: %v", stdout, err)
	}
	if res.Resource != "device" || res.ID != "dev-1" || res.Action != "pin" || res.Status != "ok" {
		t.Errorf("unexpected ActionResult: %+v", res)
	}
	if !strings.Contains(stdout, "created dev-1") {
		t.Errorf("stdout missing Success output: %q", stdout)
	}

	if !strings.Contains(stderr, "waiting on host-1") {
		t.Errorf("stderr missing Info output: %q", stderr)
	}
	if !strings.Contains(stderr, "Warning: deprecated flag --old") {
		t.Errorf("stderr missing Warn output: %q", stderr)
	}
	if strings.Contains(stderr, "hidden 1") {
		t.Errorf("stderr leaked verbose=false Debug line: %q", stderr)
	}
	if !strings.Contains(stderr, "[DEBUG] visible 2") {
		t.Errorf("stderr missing verbose=true Debug line: %q", stderr)
	}

	// PrintJSON rejects unmarshalable values (channels).
	_, _ = captureStdoutStderr(t, func() {
		if err := PrintJSON(make(chan int)); err == nil {
			t.Error("expected PrintJSON(chan int) to return error")
		}
	})
}
