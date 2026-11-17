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

package fakeho_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/tests/harness/fakeho"
)

// A fake nobody drives is a guess. This file drives the REAL client against it,
// so every shape below is one the shipping code parsed rather than one this
// package asserts about itself.

const (
	testGroup = "cvd-conformance"
	testName  = "1"
)

func newFake(t *testing.T) (*fakeho.Server, hoclient.Client) {
	t.Helper()
	server, err := fakeho.New()
	if err != nil {
		t.Fatalf("starting fake: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	cfg := hoclient.DefaultClientConfig()
	cfg.MaxRetries = 0
	cfg.FallbackPorts = nil
	cfg.MaxWait = 10 * time.Second
	cfg.WaitPollTimeout = 5 * time.Second
	factory := hoclient.NewFactory(cfg)
	return server, factory.ClientForHost(fmt.Sprintf("http://%s:%d", fakeho.SingleHostIP, server.Port()))
}

// The whole client surface against the fake, in one pass. The assertion that
// matters is at the end: every route the client called was one the fake models.
func TestClientSurfaceIsServed(t *testing.T) {
	server, client := newFake(t)
	ctx := t.Context()

	assertCreateAndList(t, ctx, client)
	assertJournaledCreate(t, ctx, client)

	for name, call := range map[string]func() error{
		"StartCVD":             func() error { return client.StartCVD(ctx, testGroup, testName) },
		"StopCVD":              func() error { return client.StopCVD(ctx, testGroup, testName) },
		"StartCVDGroup":        func() error { return client.StartCVDGroup(ctx, testGroup) },
		"StopCVDGroup":         func() error { return client.StopCVDGroup(ctx, testGroup) },
		"RestartCVD":           func() error { return client.RestartCVD(ctx, testGroup, testName) },
		"PowerwashCVD":         func() error { return client.PowerwashCVD(ctx, testGroup, testName) },
		"PowerbtnCVD":          func() error { return client.PowerbtnCVD(ctx, testGroup, testName) },
		"StartScreenRecording": func() error { return client.StartScreenRecording(ctx, testGroup, testName) },
		"StopScreenRecording":  func() error { return client.StopScreenRecording(ctx, testGroup, testName) },
		"Reset":                func() error { return client.Reset(ctx) },
	} {
		if err := call(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	// Reset wiped the host, so re-seed for the resource calls below.
	server.SeedGroup(fakeho.SingleHostIP, testGroup, testName, "Running")

	assertDisplays(t, ctx, client)
	assertScreenshot(t, ctx, client)
	assertRecordings(t, ctx, client)
	assertBugreport(t, ctx, client)
	assertSnapshots(t, ctx, server, client)
	assertSignaling(t, ctx, client)

	if _, err := client.GetAllCVDStatus(ctx); err != nil {
		t.Errorf("GetAllCVDStatus: %v", err)
	}
	if _, err := client.ProbeCVDs(ctx); err != nil {
		t.Errorf("ProbeCVDs: %v", err)
	}
	assertLogReads(t, ctx, server, client)

	assertServedRoutes(t, server)
	assertClientSurfaceDriven(t, driven)
}

func assertCreateAndList(t *testing.T, ctx context.Context, client hoclient.Client) {
	t.Helper()

	created, err := client.CreateCVD(ctx, &hoclient.CreateCVDRequest{
		EnvConfig: map[string]any{"common": map[string]any{"group_name": testGroup}},
	})
	if err != nil {
		t.Fatalf("CreateCVD: %v", err)
	}
	if len(created.CVDs) != 1 || created.CVDs[0].Group != testGroup {
		t.Fatalf("CreateCVD returned %+v, want the group from env_config", created.CVDs)
	}

	cvds, err := client.ListCVDs(ctx)
	if err != nil {
		t.Fatalf("ListCVDs: %v", err)
	}
	if len(cvds) != 1 || cvds[0].Status != "Running" {
		t.Fatalf("ListCVDs = %+v, want one Running CVD", cvds)
	}
}

// assertJournaledCreate drives the journaled path: start the create, keep the
// operation name, wait on it separately. This is what a redelivered task
// resumes, so it has to work without the create call that produced it.
func assertJournaledCreate(t *testing.T, ctx context.Context, client hoclient.Client) {
	t.Helper()

	opName, err := client.CreateCVDOperation(ctx, &hoclient.CreateCVDRequest{
		EnvConfig: map[string]any{"common": map[string]any{"group_name": "cvd-journaled"}},
	})
	if err != nil {
		t.Fatalf("CreateCVDOperation: %v", err)
	}
	resumed, err := client.WaitCVDOperation(ctx, opName, 1)
	if err != nil {
		t.Fatalf("WaitCVDOperation: %v", err)
	}
	if len(resumed.CVDs) != 1 || resumed.CVDs[0].Group != "cvd-journaled" {
		t.Fatalf("WaitCVDOperation = %+v, want the journaled group", resumed.CVDs)
	}
	if err := client.DeleteCVD(ctx, "cvd-journaled"); err != nil {
		t.Fatalf("DeleteCVD: %v", err)
	}
}

// assertLogReads drives both reads of the logs route. The boot-watch evidence
// read is a suffix range and must return the LAST bytes plus the true total;
// the snapshot precheck reads the same file WHOLE, because the launch markers
// it decides on sit at the head. Both go through the same ServeContent path
// the real logs route ends in.
func assertLogReads(t *testing.T, ctx context.Context, server *fakeho.Server, client hoclient.Client) {
	t.Helper()
	content := bytes.Repeat([]byte("boot line for the tail fetch\n"), 40)
	server.SeedLogFile(fakeho.SingleHostIP, testGroup, testName, "kernel.log", content)

	tail, total, err := client.GetCVDLogTail(ctx, testGroup, testName, "kernel.log", 100)
	if err != nil {
		t.Fatalf("GetCVDLogTail: %v", err)
	}
	if total != int64(len(content)) {
		t.Errorf("GetCVDLogTail total = %d, want %d (Content-Range must carry the full size)", total, len(content))
	}
	if !bytes.Equal(tail, content[len(content)-100:]) {
		t.Errorf("GetCVDLogTail returned %d bytes that are not the file's tail", len(tail))
	}
	if _, _, err := client.GetCVDLogTail(ctx, "no-such-group", testName, "kernel.log", 100); err == nil {
		t.Error("GetCVDLogTail must 404 for a group not in the fleet, like upstream's cvd-fleet resolution")
	}

	whole, err := client.FetchCVDLogFile(ctx, testGroup, testName, "kernel.log")
	if err != nil {
		t.Fatalf("FetchCVDLogFile: %v", err)
	}
	if !bytes.Equal(whole, content) {
		t.Errorf("FetchCVDLogFile returned %d of %d bytes; the precheck reads the head, so a tail is not an answer",
			len(whole), len(content))
	}
	// The take's fail-closed refusal is built on this error, so a missing file
	// has to be one rather than an empty read.
	if _, err := client.FetchCVDLogFile(ctx, testGroup, testName, "no-such.log"); err == nil {
		t.Error("FetchCVDLogFile must fail for a log the host does not have")
	}
}

// driven is every hoclient.Client method this test calls. It is written out
// rather than derived so that the check below can compare it against the
// interface: a method added to the client fails here until it has been driven
// against the fake, which is what forces the fake to model it instead of a new
// hand-rolled server appearing somewhere.
var driven = []string{
	"ListCVDs", "CreateCVD", "CreateCVDOperation", "WaitCVDOperation", "DeleteCVD",
	"Reset", "StartCVD", "StopCVD", "StartCVDGroup", "StopCVDGroup",
	"RestartCVD", "PowerwashCVD", "PowerbtnCVD",
	"GetDisplayScreenshot", "CreateBugreport", "DownloadBugreport", "DeleteBugreport",
	"StartScreenRecording", "StopScreenRecording", "ListScreenRecordings",
	"DownloadScreenRecording", "GetAllCVDStatus", "GetDisplays", "AddDisplay",
	"RemoveDisplay", "CreatePolledConnection", "ForwardSignalingMessage",
	"GetSignalingMessages", "ProbeCVDs", "GetCVDLogTail", "FetchCVDLogFile",
	"CreateSnapshot", "DeleteSnapshot", "StartCVDFromSnapshot",
}

// assertClientSurfaceDriven checks that every method on the client interface
// has been exercised against this fake. Without it, a new client method can be
// added, faked ad hoc in a test file, and never noticed here.
func assertClientSurfaceDriven(t *testing.T, driven []string) {
	t.Helper()

	iface := reflect.TypeFor[hoclient.Client]()
	for method := range iface.Methods() {
		if !slices.Contains(driven, method.Name) {
			t.Errorf("hoclient.Client.%s is never driven against the fake", method.Name)
		}
	}
	for _, name := range driven {
		if _, ok := iface.MethodByName(name); !ok {
			t.Errorf("%q is listed as driven but is not a client method", name)
		}
	}
}

func assertSnapshots(t *testing.T, ctx context.Context, server *fakeho.Server, client hoclient.Client) {
	t.Helper()

	// CCO always names its snapshots, so the id must round-trip verbatim.
	id, err := client.CreateSnapshot(ctx, testGroup, testName, "snap-conformance")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if id != "snap-conformance" {
		t.Errorf("CreateSnapshot returned id %q, want the requested snap-conformance", id)
	}

	// An empty request id falls back to the operation name, which is
	// upstream's default. It keeps working because /v1 passthrough callers
	// rely on it.
	serverPicked, err := client.CreateSnapshot(ctx, testGroup, testName, "")
	if err != nil {
		t.Fatalf("CreateSnapshot with server-chosen id: %v", err)
	}
	if serverPicked == "" {
		t.Error("server-chosen snapshot id is empty")
	}

	if err := client.StartCVDFromSnapshot(ctx, testGroup, testName, id); err != nil {
		t.Errorf("StartCVDFromSnapshot: %v", err)
	}

	if err := client.DeleteSnapshot(ctx, id); err != nil {
		t.Fatalf("DeleteSnapshot: %v", err)
	}
	if server.HasSnapshot(fakeho.SingleHostIP, id) {
		t.Error("snapshot still on the host after DeleteSnapshot")
	}
	// The delete verb's retry budget assumes deleting the deleted succeeds.
	if err := client.DeleteSnapshot(ctx, id); err != nil {
		t.Errorf("second DeleteSnapshot: %v", err)
	}
	// And the restore path must surface the upstream 404 for a gone snapshot,
	// not report a cold boot as a restore.
	if err := client.StartCVDFromSnapshot(ctx, testGroup, testName, id); err == nil {
		t.Error("StartCVDFromSnapshot of a deleted snapshot reported success")
	}
}

func assertDisplays(t *testing.T, ctx context.Context, client hoclient.Client) {
	t.Helper()

	added, err := client.AddDisplay(ctx, testGroup, testName, &hoclient.DisplayAddRequest{
		Width: 720, Height: 1280, DPI: 320, RefreshRateHz: 60,
	})
	if err != nil {
		t.Fatalf("AddDisplay: %v", err)
	}

	listed, err := client.GetDisplays(ctx, testGroup+"/"+testName)
	if err != nil {
		t.Fatalf("GetDisplays: %v", err)
	}
	// The listing is a map keyed by number, with dimensions inside
	// mode.windowed. The client's custom unmarshaller is what makes it a flat
	// Display, and this is what exercises it.
	idx := slices.IndexFunc(listed.Displays, func(d hoclient.Display) bool {
		return d.DisplayNumber == added.DisplayNumber
	})
	if idx < 0 {
		t.Fatalf("GetDisplays = %+v, want the display just added (%d)", listed.Displays, added.DisplayNumber)
	}
	if got := listed.Displays[idx]; got.Width != 720 || got.Height != 1280 || got.DPI != 320 || got.RefreshRateHz != 60 {
		t.Errorf("display = %+v, want the dimensions it was added with", got)
	}

	if err := client.RemoveDisplay(ctx, testGroup, testName, added.DisplayNumber); err != nil {
		t.Fatalf("RemoveDisplay: %v", err)
	}
	after, err := client.GetDisplays(ctx, testGroup+"/"+testName)
	if err != nil {
		t.Fatalf("GetDisplays after remove: %v", err)
	}
	if slices.ContainsFunc(after.Displays, func(d hoclient.Display) bool {
		return d.DisplayNumber == added.DisplayNumber
	}) {
		t.Errorf("display %d survived removal", added.DisplayNumber)
	}
}

// The capture is asynchronous. The :screenshot request answers with an
// operation and the image lives only in its result. A client that read the
// request's own body would get nothing, which is the defect this models.
func assertScreenshot(t *testing.T, ctx context.Context, client hoclient.Client) {
	t.Helper()

	shot, err := client.GetDisplayScreenshot(ctx, testGroup, testName, 0)
	if err != nil {
		t.Fatalf("GetDisplayScreenshot: %v", err)
	}
	if shot.ScreenshotMimeType != "image/png" {
		t.Errorf("mime type = %q, want image/png", shot.ScreenshotMimeType)
	}
	raw, err := base64.StdEncoding.DecodeString(shot.ScreenshotBytesBase64)
	if err != nil {
		t.Fatalf("screenshot bytes are not base64: %v", err)
	}
	if !bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")) {
		t.Errorf("screenshot is not PNG-framed: %x", raw[:min(8, len(raw))])
	}
}

func assertRecordings(t *testing.T, ctx context.Context, client hoclient.Client) {
	t.Helper()

	if err := client.StartScreenRecording(ctx, testGroup, testName); err != nil {
		t.Fatalf("StartScreenRecording: %v", err)
	}
	if err := client.StopScreenRecording(ctx, testGroup, testName); err != nil {
		t.Fatalf("StopScreenRecording: %v", err)
	}

	names, err := client.ListScreenRecordings(ctx, testGroup, testName)
	if err != nil {
		t.Fatalf("ListScreenRecordings: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("ListScreenRecordings returned nothing after a completed recording")
	}

	body, err := client.DownloadScreenRecording(ctx, testGroup, testName, names[0])
	if err != nil {
		t.Fatalf("DownloadScreenRecording: %v", err)
	}
	defer body.Close()
	content, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("reading recording: %v", err)
	}
	if len(content) == 0 {
		t.Error("recording downloaded as zero bytes")
	}
}

// The bugreport operation stores its ID as a bare JSON string rather than an
// object, which is why the client decodes the result into a string.
func assertBugreport(t *testing.T, ctx context.Context, client hoclient.Client) {
	t.Helper()

	id, err := client.CreateBugreport(ctx, testGroup, false)
	if err != nil {
		t.Fatalf("CreateBugreport: %v", err)
	}
	if id == "" {
		t.Fatal("CreateBugreport returned an empty ID")
	}

	body, err := client.DownloadBugreport(ctx, id)
	if err != nil {
		t.Fatalf("DownloadBugreport: %v", err)
	}
	content, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		t.Fatalf("reading bugreport: %v", err)
	}
	if !bytes.HasPrefix(content, []byte("PK")) {
		t.Errorf("bugreport is not ZIP-framed: %q", content[:min(4, len(content))])
	}

	if err := client.DeleteBugreport(ctx, id); err != nil {
		t.Fatalf("DeleteBugreport: %v", err)
	}
	if _, err := client.DownloadBugreport(ctx, id); err == nil {
		t.Error("a deleted bugreport still downloads")
	}
}

func assertSignaling(t *testing.T, ctx context.Context, client hoclient.Client) {
	t.Helper()

	conn, err := client.CreatePolledConnection(ctx, "device-1")
	if err != nil {
		t.Fatalf("CreatePolledConnection: %v", err)
	}
	if conn.ConnID == "" {
		t.Fatal("CreatePolledConnection returned no connection ID")
	}

	if err := client.ForwardSignalingMessage(ctx, conn.ConnID, map[string]any{"type": "request-offer"}); err != nil {
		t.Fatalf("ForwardSignalingMessage: %v", err)
	}

	msgs, err := client.GetSignalingMessages(ctx, conn.ConnID, 0)
	if err != nil {
		t.Fatalf("GetSignalingMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %+v, want the one forwarded", msgs)
	}
	// The start counter is what stops a client re-reading; past the end is
	// empty, not an error.
	rest, err := client.GetSignalingMessages(ctx, conn.ConnID, 1)
	if err != nil {
		t.Fatalf("GetSignalingMessages(start=1): %v", err)
	}
	if len(rest) != 0 {
		t.Errorf("messages from start=1 = %+v, want none", rest)
	}
}

// If the client grows an endpoint, this fails until the fake serves it, which
// stops a hand-rolled server being written in a test file instead.
func assertServedRoutes(t *testing.T, server *fakeho.Server) {
	t.Helper()

	want := []string{
		"GET /cvds",
		"POST /cvds",
		"DELETE /cvds/{group}",
		"POST /cvds/{group}/:bugreport",
		"POST /cvds/{group}/:start",
		"POST /cvds/{group}/:stop",
		"POST /cvds/{group}/{name}/:start",
		"POST /cvds/{group}/{name}/:stop",
		"POST /cvds/{group}/{name}/:restart",
		"POST /cvds/{group}/{name}/:powerwash",
		"POST /cvds/{group}/{name}/:powerbtn",
		"POST /cvds/{group}/{name}/:start_screen_recording",
		"POST /cvds/{group}/{name}/:stop_screen_recording",
		"GET /cvds/{group}/{name}/displays",
		"POST /cvds/{group}/{name}/displays",
		"DELETE /cvds/{group}/{name}/displays/{displayNumber}",
		"GET /cvds/{group}/{name}/displays/{displayNumber}/:screenshot",
		"GET /cvds/{group}/{name}/screen_recordings",
		"GET /cvds/{group}/{name}/screen_recordings/{recording}",
		"POST /cvds/{group}/{name}/snapshots",
		"DELETE /snapshots/{id}",
		"POST /operations/{name}/:wait",
		"GET /cvdbugreports/{id}",
		"DELETE /cvdbugreports/{id}",
		"POST /reset",
		"POST /polled_connections",
		"POST /polled_connections/{connID}/:forward",
		"GET /polled_connections/{connID}/messages",
	}

	served := server.ServedRoutes()
	for _, route := range want {
		if !slices.Contains(served, route) {
			t.Errorf("route %q was never served; the fake does not cover it", route)
		}
	}
}

// The operator listing is the fallback for a host with no Host Orchestrator
// API, and it reports no status, which is why a device found this way is
// unknown rather than running.
func TestListCVDsFallsBackToTheOperator(t *testing.T) {
	server, client := newFake(t)
	server.SeedGroup(fakeho.SingleHostIP, testGroup, testName, "Running")
	server.SetCVDsAPIUnsupported(fakeho.SingleHostIP, true)

	cvds, err := client.ListCVDs(t.Context())
	if err != nil {
		t.Fatalf("ListCVDs: %v", err)
	}
	if len(cvds) != 1 {
		t.Fatalf("ListCVDs = %+v, want the one device the operator lists", cvds)
	}
	// The operator listing carries no status, so the client degrades it rather
	// than inventing one.
	if cvds[0].Status != "Unknown" {
		t.Errorf("status = %q, want Unknown; the operator listing carries none", cvds[0].Status)
	}
	if !slices.Contains(server.ServedRoutes(), "GET /devices") {
		t.Error("the fallback never reached /devices")
	}
}

// An async verb can be accepted and still fail: the request answers 200 with
// an operation, and only :wait carries the outcome. A client that treats the
// first 200 as success reports a failure as done.
func TestOperationFailureSurfacesOnWait(t *testing.T) {
	server, client := newFake(t)
	server.SeedGroup(fakeho.SingleHostIP, testGroup, testName, "Running")
	server.SetOperationFailure(fakeho.SingleHostIP, "cvd powerwash failed")

	err := client.PowerwashCVD(t.Context(), testGroup, testName)
	if err == nil {
		t.Fatal("PowerwashCVD succeeded against a failing operation")
	}
	if !hoclient.IsTerminalOperationError(err) {
		t.Errorf("error = %v, want a terminal outcome the client stops polling on", err)
	}
}

// Upstream decodes a request body on :start alone, so an empty one is a 400.
// Every other instance verb takes none.
func TestStartRequiresARequestBody(t *testing.T) {
	server, _ := newFake(t)
	server.SeedGroup(fakeho.SingleHostIP, testGroup, testName, "Running")
	base := fmt.Sprintf("http://%s:%d", fakeho.SingleHostIP, server.Port())

	for verb, wantStatus := range map[string]int{
		":start": 400,
		":stop":  200,
	} {
		resp, err := postEmpty(t.Context(), base+"/cvds/"+testGroup+"/"+testName+"/"+verb)
		if err != nil {
			t.Fatalf("POST %s: %v", verb, err)
		}
		if resp != wantStatus {
			t.Errorf("POST %s with no body = %d, want %d", verb, resp, wantStatus)
		}
	}
}

// postEmpty sends a POST with no body at all, the shape hoclient's :start
// comment is about.
func postEmpty(ctx context.Context, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, http.NoBody)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}
