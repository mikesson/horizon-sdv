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

package hoclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/tests/harness/fakeho"
)

// These are the tests about what the Host Orchestrator SAYS, and they all run
// against tests/harness/fakeho, one fake whose shapes are copied from the
// upstream controller. The hand-rolled servers they replaced each encoded one
// author's belief about a response, and one of them encoded a shape upstream
// never sends.
//
// Tests about what the CONNECTION does (port failover, retries, breakers,
// deadlines, size caps) stay on ad-hoc servers in client_test.go, because
// they need a server that misbehaves in ways a real host cannot: hijacking a
// connection mid-exchange, never answering, or counting TCP dials.

const (
	contractGroup = "g"
	contractName  = "1"
)

// newContractHost starts the fake with one running CVD and a client aimed at
// it. Every test here starts from a device that exists, because that is the
// only state in which these verbs are defined.
func newContractHost(t *testing.T, tune ...func(*ClientConfig)) (*fakeho.Server, *HostClient) {
	t.Helper()
	server, err := fakeho.New()
	if err != nil {
		t.Fatalf("starting fake host: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	server.SeedGroup(fakeho.SingleHostIP, contractGroup, contractName, "Running")

	config := DefaultClientConfig()
	config.FallbackPorts = nil
	config.RetryDelay = time.Millisecond
	for _, apply := range tune {
		apply(&config)
	}
	return server, NewHostClient(fmt.Sprintf("http://%s:%d", fakeho.SingleHostIP, server.Port()), config)
}

func TestListCVDs(t *testing.T) {
	t.Parallel()
	_, client := newContractHost(t)

	cvds, err := client.ListCVDs(t.Context())
	if err != nil {
		t.Fatalf("ListCVDs: %v", err)
	}
	if len(cvds) != 1 || cvds[0].Group != contractGroup {
		t.Errorf("ListCVDs = %+v, want the seeded group", cvds)
	}
}

// The HO's :start handler decodes a request body unconditionally and 400s on
// EOF, while every other instance verb reads none. The old test recorded the
// bytes the client sent and compared them; this one runs against a host that
// enforces the rule, so a client that stopped sending the body fails here for
// the reason it would fail in production.
func TestLifecycleVerbsSatisfyTheHostsBodyRule(t *testing.T) {
	t.Parallel()
	_, client := newContractHost(t)
	ctx := t.Context()

	for name, call := range map[string]func() error{
		"StartCVD":      func() error { return client.StartCVD(ctx, contractGroup, contractName) },
		"StartCVDGroup": func() error { return client.StartCVDGroup(ctx, contractGroup) },
		"StopCVD":       func() error { return client.StopCVD(ctx, contractGroup, contractName) },
		"StopCVDGroup":  func() error { return client.StopCVDGroup(ctx, contractGroup) },
		"RestartCVD":    func() error { return client.RestartCVD(ctx, contractGroup, contractName) },
		"PowerwashCVD":  func() error { return client.PowerwashCVD(ctx, contractGroup, contractName) },
		"PowerbtnCVD":   func() error { return client.PowerbtnCVD(ctx, contractGroup, contractName) },
	} {
		if err := call(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The group and instance :start are separate host handlers that happen to
// share a verb spelling, and reaching one while meaning the other is silent:
// starting instance 0 of a stopped group leaves every sibling stopped while
// the caller believes the whole device came up. Assert the route actually
// served, since both calls succeed either way.
func TestStartCVDGroupAddressesTheGroupNotItsFirstInstance(t *testing.T) {
	t.Parallel()
	server, client := newContractHost(t)

	if err := client.StartCVDGroup(t.Context(), contractGroup); err != nil {
		t.Fatalf("StartCVDGroup: %v", err)
	}

	served := server.ServedRoutes()
	if !slices.Contains(served, "POST /cvds/{group}/:start") {
		t.Errorf("served %v, want the group :start route", served)
	}
	if slices.Contains(served, "POST /cvds/{group}/{name}/:start") {
		t.Errorf("StartCVDGroup reached the per-instance route: %v", served)
	}
}

// A host that answers is a host, however badly it answers: a 500 is the
// application's verdict, so the client must not treat it as this port being
// wrong and sweep to the next one.
func TestNoFailoverOnAppError(t *testing.T) {
	t.Parallel()
	failing, _ := newContractHost(t)
	failing.SetErrorAll(true)

	spare, err := fakeho.New()
	if err != nil {
		t.Fatalf("starting spare host: %v", err)
	}
	defer func() { _ = spare.Close() }()

	config := DefaultClientConfig()
	config.FallbackPorts = []int{spare.Port()}
	config.MaxRetries = 0
	client := NewHostClient(fmt.Sprintf("http://%s:%d", fakeho.SingleHostIP, failing.Port()), config)

	_, err = client.ListCVDs(t.Context())
	hostErr, ok := errors.AsType[*HostError](err)
	if !ok || hostErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("ListCVDs error = %v, want a 500 HostError", err)
	}
	if served := spare.ServedRoutes(); len(served) != 0 {
		t.Errorf("the fallback port was swept on an application error: %v", served)
	}
}

// The capture is an async operation, so the image is only ever in the
// operation's result. The :screenshot response carries only an operation
// name. Reading that response as the image is how this endpoint served empty
// captures as HTTP 200 successes.
func TestGetDisplayScreenshot(t *testing.T) {
	t.Parallel()
	_, client := newContractHost(t)

	shot, err := client.GetDisplayScreenshot(t.Context(), contractGroup, contractName, 0)
	if err != nil {
		t.Fatalf("GetDisplayScreenshot: %v", err)
	}
	if shot.ScreenshotBytesBase64 == "" || shot.ScreenshotMimeType != "image/png" {
		t.Errorf("screenshot = %+v, want PNG bytes from the operation result", shot)
	}
}

// A capture that completes carrying no image is a broken host contract, not a
// successful blank screenshot: clients that trusted it wrote 0-byte PNGs.
func TestGetDisplayScreenshotEmptyCaptureIsError(t *testing.T) {
	t.Parallel()
	server, client := newContractHost(t)
	server.SetEmptyScreenshot(fakeho.SingleHostIP, true)

	_, err := client.GetDisplayScreenshot(t.Context(), contractGroup, contractName, 0)
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("empty capture error = %v, want ErrInvalidResponse", err)
	}
	hostErr, ok := errors.AsType[*HostError](err)
	if !ok || hostErr.GetStatus() != http.StatusBadGateway {
		t.Errorf("empty capture must reach the API as 502, got %v", err)
	}
}

// The host reports a failed capture as the operation's stored error (500),
// which is terminal: it must surface as an error instead of an empty image.
func TestGetDisplayScreenshotOperationFailure(t *testing.T) {
	t.Parallel()
	server, client := newContractHost(t)
	server.SetOperationFailure(fakeho.SingleHostIP, "cvd display screenshot failed")

	_, err := client.GetDisplayScreenshot(t.Context(), contractGroup, contractName, 0)
	if err == nil {
		t.Fatal("a failed capture operation must not report success")
	}
	if !strings.Contains(err.Error(), "cvd display screenshot failed") {
		t.Errorf("host's own reason lost from the chain: %v", err)
	}
}

// `cvd display screenshot` blocks on the launcher socket with no timeout of
// its own, so a hung CVD never completes the operation. The wait is bounded
// (never past the client's operation ceiling) and ends as a 504 rather than
// holding the caller until an impatient client disconnects.
func TestGetDisplayScreenshotBoundsAWedgedCapture(t *testing.T) {
	t.Parallel()
	server, client := newContractHost(t, func(c *ClientConfig) {
		c.MaxWait = 50 * time.Millisecond
	})
	server.SetOperationsWedged(fakeho.SingleHostIP, true)

	_, err := client.GetDisplayScreenshot(t.Context(), contractGroup, contractName, 0)
	if !errors.Is(err, ErrOperationTimeout) {
		t.Fatalf("wedged capture error = %v, want ErrOperationTimeout", err)
	}
	hostErr, ok := errors.AsType[*HostError](err)
	if !ok || hostErr.GetStatus() != http.StatusGatewayTimeout {
		t.Errorf("wedged capture must reach the API as 504, got %v", err)
	}
}

// A caller's own cancellation is theirs, not a verdict on the host: it must
// not be relabeled as the host failing to produce the capture in time.
func TestGetDisplayScreenshotCallerCancelIsNotAHostTimeout(t *testing.T) {
	t.Parallel()
	server, client := newContractHost(t)
	server.SetOperationsWedged(fakeho.SingleHostIP, true)

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	_, err := client.GetDisplayScreenshot(ctx, contractGroup, contractName, 0)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller-cancel error = %v, want context.DeadlineExceeded", err)
	}
	if errors.Is(err, ErrOperationTimeout) {
		t.Error("caller cancellation was relabeled as a host operation timeout")
	}
}

// An exhausted operation wait carries 504 without becoming a terminal
// operation outcome (a retry resumes the same operation).
func TestExhaustedWaitCarries504AndStaysResumable(t *testing.T) {
	t.Parallel()
	server, client := newContractHost(t, func(c *ClientConfig) {
		c.MaxWait = 10 * time.Millisecond
	})
	server.SetOperationsWedged(fakeho.SingleHostIP, true)

	err := client.PowerwashCVD(t.Context(), contractGroup, contractName)
	hostErr, ok := errors.AsType[*HostError](err)
	if !ok || hostErr.GetStatus() != http.StatusGatewayTimeout {
		t.Fatalf("MaxWait exhaustion = %v, want 504 HostError", err)
	}
	if !errors.Is(err, ErrOperationTimeout) {
		t.Error("errors.Is(ErrOperationTimeout) must keep matching through the wrap")
	}
	if IsTerminalOperationError(err) {
		t.Error("an exhausted wait must stay non-terminal (retry resumes the operation)")
	}
}

// The full capture contract: :bugreport starts an operation whose stored
// result is the ID, the ZIP downloads by that ID, and delete removes the host
// copy. The adb flag is observable because the fake folds it into the ID it
// mints, the same way the host's own capture differs.
func TestBugreportFlow(t *testing.T) {
	t.Parallel()
	_, client := newContractHost(t)
	ctx := t.Context()

	id, err := client.CreateBugreport(ctx, contractGroup, true)
	if err != nil {
		t.Fatalf("CreateBugreport: %v", err)
	}
	if !strings.HasSuffix(id, "-adb") {
		t.Errorf("bugreport ID = %q, want the include_adb_bugreport request reflected in it", id)
	}

	body, err := client.DownloadBugreport(ctx, id)
	if err != nil {
		t.Fatalf("DownloadBugreport: %v", err)
	}
	content, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		t.Fatalf("reading bugreport stream: %v", err)
	}
	if !strings.HasPrefix(string(content), "PK") {
		t.Errorf("bugreport is not ZIP-framed: %q", content)
	}

	if err := client.DeleteBugreport(ctx, id); err != nil {
		t.Fatalf("DeleteBugreport: %v", err)
	}
	if _, err := client.DownloadBugreport(ctx, id); err == nil {
		t.Error("a deleted bugreport still downloads")
	}
}

// A host-side 404 maps to a HostError rather than a dangling reader.
func TestDownloadBugreportError(t *testing.T) {
	t.Parallel()
	_, client := newContractHost(t)

	_, err := client.DownloadBugreport(t.Context(), "missing")
	hostErr, ok := errors.AsType[*HostError](err)
	if !ok || hostErr.StatusCode != http.StatusNotFound {
		t.Fatalf("DownloadBugreport error = %v, want 404 HostError", err)
	}
}

// Start (an operation), stop (which publishes the file), list, download.
func TestScreenRecordingFlow(t *testing.T) {
	t.Parallel()
	_, client := newContractHost(t)
	ctx := t.Context()

	if err := client.StartScreenRecording(ctx, contractGroup, contractName); err != nil {
		t.Fatalf("StartScreenRecording: %v", err)
	}
	if err := client.StopScreenRecording(ctx, contractGroup, contractName); err != nil {
		t.Fatalf("StopScreenRecording: %v", err)
	}

	names, err := client.ListScreenRecordings(ctx, contractGroup, contractName)
	if err != nil {
		t.Fatalf("ListScreenRecordings: %v", err)
	}
	if len(names) != 1 {
		t.Fatalf("recordings = %v, want the one just stopped", names)
	}

	body, err := client.DownloadScreenRecording(ctx, contractGroup, contractName, names[0])
	if err != nil {
		t.Fatalf("DownloadScreenRecording: %v", err)
	}
	content, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		t.Fatalf("reading recording stream: %v", err)
	}
	if len(content) == 0 {
		t.Error("recording downloaded as zero bytes")
	}
}

// Displays answer directly while the screenshot on the same resource does not,
// and the listing arrives as a map keyed by number with its dimensions inside
// mode.windowed, the shape the client's custom unmarshaller exists for.
func TestDisplayLifecycle(t *testing.T) {
	t.Parallel()
	_, client := newContractHost(t)
	ctx := t.Context()

	added, err := client.AddDisplay(ctx, contractGroup, contractName, &DisplayAddRequest{
		Width: 720, Height: 1280, DPI: 320, RefreshRateHz: 60,
	})
	if err != nil {
		t.Fatalf("AddDisplay: %v", err)
	}

	listed, err := client.GetDisplays(ctx, contractGroup+"/"+contractName)
	if err != nil {
		t.Fatalf("GetDisplays: %v", err)
	}
	want := Display{DisplayNumber: added.DisplayNumber, Width: 720, Height: 1280, DPI: 320, RefreshRateHz: 60}
	if len(listed.Displays) != 1 || listed.Displays[0] != want {
		t.Errorf("displays = %+v, want [%+v]", listed.Displays, want)
	}

	if err := client.RemoveDisplay(ctx, contractGroup, contractName, added.DisplayNumber); err != nil {
		t.Fatalf("RemoveDisplay: %v", err)
	}
	after, err := client.GetDisplays(ctx, contractGroup+"/"+contractName)
	if err != nil {
		t.Fatalf("GetDisplays after remove: %v", err)
	}
	if len(after.Displays) != 0 {
		t.Errorf("displays = %+v, want none after removal", after.Displays)
	}
}
