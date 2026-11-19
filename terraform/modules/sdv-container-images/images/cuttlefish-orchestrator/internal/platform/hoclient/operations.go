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
	"net/url"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// tracer is the OpenTelemetry tracer for this package.
var tracer = otel.Tracer(telemetry.ScopeName("hoclient"))

// traced runs fn inside a client span named name, recording fn's error on the
// span. Every span carries the host IP; fn can attach success attributes via
// trace.SpanFromContext.
func (c *HostClient) traced(ctx context.Context, name string, attrs []attribute.KeyValue, fn func(ctx context.Context) error) error {
	attrs = append([]attribute.KeyValue{attribute.String("host.ip", c.hostIP)}, attrs...)
	ctx, span := tracer.Start(ctx, name, trace.WithAttributes(attrs...))
	defer span.End()

	if err := fn(ctx); err != nil {
		telemetry.RecordError(span, err)
		return err
	}
	return nil
}

// cvdAttrs returns the span attributes identifying a CVD.
func cvdAttrs(group, name string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("cvd.group", group),
		attribute.String("cvd.name", name),
	}
}

// ListCVDs returns all CVDs on the host
func (c *HostClient) ListCVDs(ctx context.Context) ([]*CVD, error) {
	var cvds []*CVD
	err := c.traced(ctx, "hoclient.ListCVDs", nil, func(ctx context.Context) error {
		var err error
		cvds, err = c.listCVDs(ctx)
		if err != nil {
			return err
		}
		trace.SpanFromContext(ctx).SetAttributes(attribute.Int("cvd.count", len(cvds)))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return cvds, nil
}

// ProbeCVDs observes the Host Orchestrator's CVD listing (/cvds), the
// condition MarkHOReady is keyed on, WITHOUT consulting or teaching the
// host's circuit breaker: one pass over the ports, no retries, no recording.
//
// It exists for boot-window ready-probes and best-effort mid-operation
// sampling. A freshly-provisioned VM refuses connections for the tens of
// seconds its HO takes to boot; those failures are expected, not host-health
// evidence, and a probe cadence that recorded them would trip the shared
// breaker (default: 5 failures, open 30s) exactly as the host comes up,
// refusing the first real create the probe was meant to accelerate. The same
// bypass lets a probe see a recovered HO while the breaker is still open,
// and keeps a sampler running beside a recording :wait poll from
// double-counting the same outage. Ordinary calls must keep the recording
// path. This one learns nothing in either direction.
func (c *HostClient) ProbeCVDs(ctx context.Context) ([]*CVD, error) {
	var errs []error
	for _, base := range c.baseURLs {
		var resp ListCVDsResponse
		err := c.doSingleRequest(ctx, http.MethodGet, base+"/cvds", nil, &resp, c.requestTimeout())
		if err == nil {
			return resp.CVDs, nil
		}
		errs = append(errs, err)
	}
	return nil, fmt.Errorf("host orchestrator not answering: %w", errors.Join(errs...))
}

// GetCVDLogTail fetches up to tailBytes from the end of one file in a CVD's
// logs directory (kernel.log, launcher.log), plus the file's total size. It is
// the boot-progress observation's raw input. Like ProbeCVDs it neither consults
// nor teaches the circuit breaker: it is a diagnostic read of a host whose
// launcher may be hung, and a read that recorded failures would trip the
// shared breaker against the very host being diagnosed.
//
// One Range GET carries both answers: the host's route is GET-only (a HEAD
// 405s before its file server), and a suffix range answers 206 with
// `Content-Range: bytes start-end/total`, so the size probe is free with the
// tail. It is not cheap host-side, though: the logs route runs `cvd fleet`
// before serving a byte, so callers budget their cadence explicitly.
func (c *HostClient) GetCVDLogTail(ctx context.Context, group, name, file string, tailBytes int64) ([]byte, int64, error) {
	path := fmt.Sprintf("/cvds/%s/%s/logs/%s",
		url.PathEscape(group), url.PathEscape(name), url.PathEscape(file))
	var errs []error
	for _, base := range c.baseURLs {
		tail, total, err := c.fetchLogTail(ctx, base+path, tailBytes)
		if err == nil {
			return tail, total, nil
		}
		// An application error (404 group not registered, 500) reads the same
		// on every port; only connection failures justify the next one.
		if !isConnectionError(err) && !errors.Is(err, ErrHostUnreachable) {
			return nil, 0, err
		}
		errs = append(errs, err)
	}
	return nil, 0, fmt.Errorf("host orchestrator not answering: %w", errors.Join(errs...))
}

// fetchLogTail performs the single suffix-range GET against one base URL.
func (c *HostClient) fetchLogTail(ctx context.Context, reqURL string, tailBytes int64) ([]byte, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, c.requestTimeout())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, http.NoBody)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=-%d", tailBytes))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if isConnectionError(err) {
			return nil, 0, fmt.Errorf("%w: %w", ErrHostUnreachable, err)
		}
		return nil, 0, &HostError{StatusCode: 0, Message: msgRequestFailed, Retryable: IsRetryable(err), Cause: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, 0, c.parseErrorResponse(resp.StatusCode, body)
	}

	// A 200 means the server ignored the range and sent the whole file, so
	// the read must run to the end to keep the TAIL, where the markers live.
	// Bounded by the response cap; a file too large to buffer whole without
	// range support fails the fetch, which degrades to an un-enriched sample.
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.maxResponseSize()+1))
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read log tail: %w", err)
	}
	if int64(len(data)) > c.maxResponseSize() {
		return nil, 0, &HostError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("log response exceeds %d bytes and the server ignored the range request", c.maxResponseSize()),
		}
	}
	total := int64(len(data))
	if resp.StatusCode == http.StatusPartialContent {
		if t, ok := contentRangeTotal(resp.Header.Get("Content-Range")); ok {
			total = t
		}
	}
	if int64(len(data)) > tailBytes {
		data = data[int64(len(data))-tailBytes:]
	}
	return data, total, nil
}

// FetchCVDLogFile reads one file from a CVD's logs directory whole. The tail
// fetch above cannot answer a question about how the CURRENT launch was
// assembled: assemble_cvd writes its decisions at the HEAD of launcher.log and
// a long-lived device pushes them arbitrarily far from the end. It is buffered
// because every caller parses the whole file, and capped at the response
// limit. A log too large to hold is reported as an error, so no verdict is
// drawn from a truncated read.
//
// Not cheap host-side: like the tail fetch it goes through the logs route,
// which runs `cvd fleet` before serving a byte. Unlike the tail fetch it takes
// the ordinary breaker-recording path, because this read backs a user-requested
// verb and is no diagnostic sampler, so a host whose breaker is open should
// refuse it fast instead of being probed by it.
func (c *HostClient) FetchCVDLogFile(ctx context.Context, group, name, file string) ([]byte, error) {
	path := fmt.Sprintf("/cvds/%s/%s/logs/%s",
		url.PathEscape(group), url.PathEscape(name), url.PathEscape(file))
	attrs := append(cvdAttrs(group, name), attribute.String("log.file", file))

	var content []byte
	err := c.traced(ctx, "hoclient.FetchCVDLogFile", attrs, func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, c.requestTimeout())
		defer cancel()

		body, err := c.streamFile(ctx, path)
		if err != nil {
			return err
		}
		defer body.Close()

		data, err := io.ReadAll(io.LimitReader(body, c.maxResponseSize()+1))
		if err != nil {
			return fmt.Errorf("failed to read log %s: %w", file, err)
		}
		if int64(len(data)) > c.maxResponseSize() {
			return &HostError{Message: fmt.Sprintf("log %s exceeds %d bytes", file, c.maxResponseSize())}
		}
		content = data
		trace.SpanFromContext(ctx).SetAttributes(attribute.Int("log.bytes", len(data)))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return content, nil
}

// contentRangeTotal parses the complete length from a Content-Range header
// ("bytes 0-99/1234"). An unknown length ("*") reports false.
func contentRangeTotal(h string) (int64, bool) {
	rest, ok := strings.CutPrefix(h, "bytes ")
	if !ok {
		return 0, false
	}
	_, totalStr, ok := strings.Cut(rest, "/")
	if !ok || totalStr == "*" {
		return 0, false
	}
	t, err := strconv.ParseInt(totalStr, 10, 64)
	if err != nil {
		return 0, false
	}
	return t, true
}

// listCVDs queries the Host Orchestrator API (/cvds), falling back to the
// standard Cuttlefish Operator API (/devices) on 404.
func (c *HostClient) listCVDs(ctx context.Context) ([]*CVD, error) {
	var resp ListCVDsResponse
	err := c.doRequestWithRetry(ctx, http.MethodGet, "/cvds", nil, &resp)
	if err == nil {
		return resp.CVDs, nil
	}

	hostErr, ok := errors.AsType[*HostError](err)
	if !ok || hostErr.StatusCode != http.StatusNotFound {
		return nil, err
	}

	var devices []struct {
		DeviceID  string `json:"device_id"`
		GroupName string `json:"group_name"`
		Name      string `json:"name"`
		ADBPort   int    `json:"adb_port"`
	}
	if err := c.doRequestWithRetry(ctx, http.MethodGet, "/devices", nil, &devices); err != nil {
		return nil, err
	}

	cvds := make([]*CVD, len(devices))
	for i, d := range devices {
		cvds[i] = &CVD{
			Group:          d.GroupName,
			Name:           d.Name,
			Status:         device.CVDStateUnknown, // Operator doesn't return status in list
			WebRTCDeviceID: d.DeviceID,
			ADBSerial:      fmt.Sprintf("0.0.0.0:%d", d.ADBPort),
		}
	}
	return cvds, nil
}

// CreateCVD creates a new CVD, starting the operation and waiting for it. CCO's
// own create path drives the two halves separately so it can journal the
// operation name between them; this is the undivided form the client surface
// still owes its callers.
func (c *HostClient) CreateCVD(ctx context.Context, req *CreateCVDRequest) (*CreateCVDResponse, error) {
	op, err := c.CreateCVDOperation(ctx, req)
	if err != nil {
		return nil, err
	}
	return c.WaitCVDOperation(ctx, op, requestInstances(req))
}

// requestInstances is how many instances a create request declares, which sizes
// its wait. A config naming none is the one-instance shape a simple create
// sends, so it counts as one rather than as nothing.
func requestInstances(req *CreateCVDRequest) int {
	instances, _ := req.EnvConfig["instances"].([]any)
	return max(1, len(instances))
}

// CreateCVDOperation starts a CVD creation, returning the operation name
// without waiting.
func (c *HostClient) CreateCVDOperation(ctx context.Context, req *CreateCVDRequest) (string, error) {
	var op Operation
	err := c.traced(ctx, "hoclient.CreateCVDOperation", nil, func(ctx context.Context) error {
		if err := c.doRequestWithRetry(ctx, http.MethodPost, "/cvds", req, &op); err != nil {
			return err
		}
		trace.SpanFromContext(ctx).SetAttributes(attribute.String("operation.name", op.Name))
		return nil
	})
	if err != nil {
		return "", err
	}
	return op.Name, nil
}

// WaitCVDOperation waits for a creation operation by name. instances is the
// group's DESIRED instance count, which sizes the wait: the operation covers
// one serial assembly plus that many guests booting in parallel, so a ceiling
// that ignored it would be tightest for the largest groups.
func (c *HostClient) WaitCVDOperation(ctx context.Context, operation string, instances int) (*CreateCVDResponse, error) {
	attrs := []attribute.KeyValue{
		attribute.String("operation.name", operation),
		attribute.Int("cvd.instances", instances),
	}
	var resp CreateCVDResponse
	err := c.traced(ctx, "hoclient.WaitCVDOperation", attrs, func(ctx context.Context) error {
		if err := c.waitForOperation(ctx, operation, &resp, c.createBudget(instances)); err != nil {
			return err
		}
		if len(resp.CVDs) > 0 {
			trace.SpanFromContext(ctx).SetAttributes(
				attribute.String("cvd.id", resp.CVDs[0].Group+"/"+resp.CVDs[0].Name),
				attribute.String("cvd.status", resp.CVDs[0].Status),
			)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// DeleteCVD deletes a CVD. Teardown is a queued task (device.cleanup_cvd), so
// its wait answers to the worker's handle timeout rather than to any request
// deadline.
func (c *HostClient) DeleteCVD(ctx context.Context, cvdID string) error {
	attrs := []attribute.KeyValue{attribute.String("cvd.id", cvdID)}
	return c.traced(ctx, "hoclient.DeleteCVD", attrs, func(ctx context.Context) error {
		var op Operation
		if err := c.doRequestWithRetry(ctx, http.MethodDelete, "/cvds/"+url.PathEscape(cvdID), nil, &op); err != nil {
			return err
		}
		trace.SpanFromContext(ctx).SetAttributes(attribute.String("operation.name", op.Name))
		return c.waitForOperation(ctx, op.Name, nil, queuedBudget)
	})
}

// Reset issues a host-wide `cvd reset` (POST /reset), waiting for the operation
// to complete. It frees leaked taps / stale registrations. Host remediation is
// reconciler-driven, so like DeleteCVD its wait has no request envelope.
func (c *HostClient) Reset(ctx context.Context) error {
	return c.traced(ctx, "hoclient.Reset", nil, func(ctx context.Context) error {
		var op Operation
		if err := c.doRequestWithRetry(ctx, http.MethodPost, "/reset", nil, &op); err != nil {
			return err
		}
		trace.SpanFromContext(ctx).SetAttributes(attribute.String("operation.name", op.Name))
		return c.waitForOperation(ctx, op.Name, nil, queuedBudget)
	})
}

// StartCVD starts a stopped CVD
func (c *HostClient) StartCVD(ctx context.Context, group, name string) error {
	return c.lifecycleCVD(ctx, group, name, verbStart)
}

// StartCVDGroup starts every instance of a group.
func (c *HostClient) StartCVDGroup(ctx context.Context, group string) error {
	return c.lifecycleCVDGroup(ctx, group, verbStartGroup)
}

// StopCVD stops a running CVD
func (c *HostClient) StopCVD(ctx context.Context, group, name string) error {
	return c.lifecycleCVD(ctx, group, name, verbStop)
}

// StopCVDGroup stops every instance of a group.
func (c *HostClient) StopCVDGroup(ctx context.Context, group string) error {
	return c.lifecycleCVDGroup(ctx, group, verbStopGroup)
}

// RestartCVD relaunches a CVD in place (the host tool's :restart) and waits for
// the operation. It revives a hung run_cvd launcher without re-fetching
// artifacts, the cheap first step of zombie recovery, before a full recreate.
func (c *HostClient) RestartCVD(ctx context.Context, group, name string) error {
	return c.lifecycleCVD(ctx, group, name, verbRestart)
}

// PowerwashCVD resets a CVD to first-boot state in place (the host tool's
// :powerwash) and waits for the operation, giving a clean device on the same
// build without re-fetching artifacts.
func (c *HostClient) PowerwashCVD(ctx context.Context, group, name string) error {
	return c.lifecycleCVD(ctx, group, name, verbPowerwash)
}

// PowerbtnCVD presses the device's power button (the host tool's :powerbtn)
// and waits for the operation.
func (c *HostClient) PowerbtnCVD(ctx context.Context, group, name string) error {
	return c.lifecycleCVD(ctx, group, name, verbPowerbtn)
}

// CreateSnapshot captures a CVD's guest state into a snapshot directory on
// the host and waits for the operation, returning the snapshot ID. The host
// runs suspend → snapshot → resume itself; the capture copies disk overlays
// plus the full guest RAM image, which is why the wait rides the queued
// budget (MaxWait, ten minutes by default) rather than a request-path tier.
//
// A failed capture can leave the CVD SUSPENDED: the host's failure path never
// resumes, and it exposes no resume-only endpoint. The caller owns
// compensation (a restart is the only HTTP remedy).
func (c *HostClient) CreateSnapshot(ctx context.Context, group, name, snapshotID string) (string, error) {
	attrs := append(cvdAttrs(group, name), attribute.String("snapshot.id", snapshotID))
	var resp CreateSnapshotResponse
	err := c.traced(ctx, "hoclient.CreateSnapshot", attrs, func(ctx context.Context) error {
		path := fmt.Sprintf("/cvds/%s/%s/snapshots", url.PathEscape(group), url.PathEscape(name))
		var op Operation
		if err := c.doRequestWithRetry(ctx, http.MethodPost, path, CreateSnapshotRequest{SnapshotID: snapshotID}, &op); err != nil {
			return err
		}
		if err := c.waitForOperation(ctx, op.Name, &resp, queuedBudget); err != nil {
			return err
		}
		// A completed take that names no snapshot leaves the caller with an
		// inventory row addressing nothing.
		if resp.SnapshotID == "" {
			return &HostError{Message: "host returned an empty snapshot id", Cause: ErrInvalidResponse}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return resp.SnapshotID, nil
}

// DeleteSnapshot removes a stored snapshot from the host's disk and waits for
// the operation. Idempotent: the host's remove succeeds for an absent
// snapshot, so redelivery is safe.
func (c *HostClient) DeleteSnapshot(ctx context.Context, snapshotID string) error {
	attrs := []attribute.KeyValue{attribute.String("snapshot.id", snapshotID)}
	return c.traced(ctx, "hoclient.DeleteSnapshot", attrs, func(ctx context.Context) error {
		var op Operation
		if err := c.doRequestWithRetry(ctx, http.MethodDelete, "/snapshots/"+url.PathEscape(snapshotID), nil, &op); err != nil {
			return err
		}
		return c.waitForOperation(ctx, op.Name, nil, queuedBudget)
	})
}

// StartCVDFromSnapshot starts a stopped CVD from a snapshot stored on the
// host (the :start verb with a snapshot id) and waits for the operation.
// Restore replays the guest RAM image and is driven only by the task queue,
// so it rides the queued budget; the host 404s a snapshot id whose directory
// is gone.
func (c *HostClient) StartCVDFromSnapshot(ctx context.Context, group, name, snapshotID string) error {
	v := lifecycleVerb{"hoclient.StartCVDFromSnapshot", verbStartName, StartCVDRequest{SnapshotID: snapshotID}, queuedBudget}
	return c.lifecycleCVD(ctx, group, name, v)
}

// CreateBugreport captures a bugreport for a CVD group: it starts the host's
// :bugreport operation and waits for it, returning the host-side bugreport ID
// the ZIP can then be downloaded and deleted by. includeADB additionally runs
// a full on-device `adb bugreport`, which is much richer but needs a
// responsive Android and can take minutes.
func (c *HostClient) CreateBugreport(ctx context.Context, group string, includeADB bool) (string, error) {
	attrs := []attribute.KeyValue{
		attribute.String("cvd.group", group),
		attribute.Bool("bugreport.include_adb", includeADB),
	}
	var bugreportID string
	err := c.traced(ctx, "hoclient.CreateBugreport", attrs, func(ctx context.Context) error {
		path := fmt.Sprintf("/cvds/%s/:bugreport", url.PathEscape(group))
		if includeADB {
			path += "?include_adb_bugreport=true"
		}
		var op Operation
		if err := c.doRequestWithRetry(ctx, http.MethodPost, path, nil, &op); err != nil {
			return err
		}
		// The operation's stored result is the bugreport ID (a bare JSON
		// string), not a CVD response.
		if err := c.waitForOperation(ctx, op.Name, &bugreportID, extendedBudget); err != nil {
			return err
		}
		trace.SpanFromContext(ctx).SetAttributes(attribute.String("bugreport.id", bugreportID))
		return nil
	})
	if err != nil {
		return "", err
	}
	return bugreportID, nil
}

// DownloadBugreport streams a captured bugreport ZIP from the host.
func (c *HostClient) DownloadBugreport(ctx context.Context, bugreportID string) (io.ReadCloser, error) {
	return c.streamFile(ctx, "/cvdbugreports/"+url.PathEscape(bugreportID))
}

// DeleteBugreport removes a captured bugreport from the host's disk.
func (c *HostClient) DeleteBugreport(ctx context.Context, bugreportID string) error {
	attrs := []attribute.KeyValue{attribute.String("bugreport.id", bugreportID)}
	return c.traced(ctx, "hoclient.DeleteBugreport", attrs, func(ctx context.Context) error {
		return c.doRequestWithRetry(ctx, http.MethodDelete, "/cvdbugreports/"+url.PathEscape(bugreportID), nil, nil)
	})
}

// StartScreenRecording starts a screen recording on a CVD instance (the host
// tool's :start_screen_recording) and waits for the operation.
func (c *HostClient) StartScreenRecording(ctx context.Context, group, name string) error {
	return c.lifecycleCVD(ctx, group, name, verbStartRecording)
}

// StopScreenRecording stops a screen recording on a CVD instance (the host
// tool's :stop_screen_recording) and waits for the operation.
func (c *HostClient) StopScreenRecording(ctx context.Context, group, name string) error {
	return c.lifecycleCVD(ctx, group, name, verbStopRecording)
}

// ListScreenRecordings returns the names of screen recordings on the host.
func (c *HostClient) ListScreenRecordings(ctx context.Context, group, name string) ([]string, error) {
	var resp ScreenRecordingsResponse
	err := c.traced(ctx, "hoclient.ListScreenRecordings", cvdAttrs(group, name), func(ctx context.Context) error {
		path := fmt.Sprintf("/cvds/%s/%s/screen_recordings", url.PathEscape(group), url.PathEscape(name))
		if err := c.doRequestWithRetry(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return err
		}
		trace.SpanFromContext(ctx).SetAttributes(attribute.Int("recording.count", len(resp.ScreenRecordings)))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return resp.ScreenRecordings, nil
}

// DownloadScreenRecording streams a screen recording from the host.
func (c *HostClient) DownloadScreenRecording(ctx context.Context, group, name, recording string) (io.ReadCloser, error) {
	path := fmt.Sprintf("/cvds/%s/%s/screen_recordings/%s", url.PathEscape(group), url.PathEscape(name), url.PathEscape(recording))
	return c.streamFile(ctx, path)
}

// GetDisplayScreenshot captures one display of a CVD. The capture is an
// ASYNC host operation: the :screenshot request answers with an operation
// name and the image only exists once that operation completes, so the
// response body of the request itself carries no image at all. Decoding it as
// one yields an empty capture served as success: every field blank, HTTP 200.
//
// The wait needs its budget more than any other verb here: the host's
// screenshot path blocks on the run_cvd launcher socket with no timeout of its
// own, so a hung CVD never completes the operation and an unbounded wait would
// hold until the client disconnected first, the raced disconnect the call
// budget exists to avoid. A capture is one frame grab, so the interactive tier
// is generous for it.
func (c *HostClient) GetDisplayScreenshot(ctx context.Context, group, name string, displayNumber int) (*DisplayScreenshotResponse, error) {
	attrs := append(cvdAttrs(group, name), attribute.Int("display.number", displayNumber))
	var resp DisplayScreenshotResponse
	err := c.traced(ctx, "hoclient.GetDisplayScreenshot", attrs, func(ctx context.Context) error {
		path := fmt.Sprintf("/cvds/%s/%s/displays/%d/:screenshot", url.PathEscape(group), url.PathEscape(name), displayNumber)
		var op Operation
		if err := c.doRequestWithRetry(ctx, http.MethodGet, path, nil, &op); err != nil {
			return err
		}
		trace.SpanFromContext(ctx).SetAttributes(attribute.String("operation.name", op.Name))
		if err := c.waitForOperation(ctx, op.Name, &resp, interactiveBudget); err != nil {
			return err
		}
		// A completed capture with no image is a broken host contract, and
		// passing it on as success makes every client write a 0-byte file and
		// call it a screenshot.
		if resp.ScreenshotBytesBase64 == "" {
			return &HostError{Message: "host returned an empty screenshot", Cause: ErrInvalidResponse}
		}
		trace.SpanFromContext(ctx).SetAttributes(attribute.Int("screenshot.encoded_bytes", len(resp.ScreenshotBytesBase64)))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// verbStartName is the host's start verb. Three call sites spell it: the
// instance verb, the group verb, and the snapshot restore, which is the
// instance verb carrying a snapshot id.
const verbStartName = ":start"

// lifecycleVerb is one CVD lifecycle POST: the host tool's verb, the span it
// records under, the body the HO's handler expects, and the wait budget its
// latency class earns. Each verb declares its own budget because the budget is
// a property of the verb, and lifecycleCVD serves verbs from both latency
// tiers, so one shared value could not cover them.
type lifecycleVerb struct {
	spanName string
	verb     string
	body     any
	budget   waitBudget
}

var (
	// The HO's INSTANCE :start handler unconditionally JSON-decodes its request
	// body, so unlike every other verb here an empty body is rejected as 400
	// "Malformed JSON in request". Its GROUP counterpart is a different handler
	// that reads the route variables only and decodes nothing, which is why the
	// two carry different bodies rather than sharing one declaration.
	verbStart      = lifecycleVerb{"hoclient.StartCVD", verbStartName, struct{}{}, extendedBudget}
	verbStartGroup = lifecycleVerb{"hoclient.StartCVDGroup", verbStartName, nil, extendedBudget}
	verbRestart    = lifecycleVerb{"hoclient.RestartCVD", ":restart", nil, extendedBudget}
	verbPowerwash  = lifecycleVerb{"hoclient.PowerwashCVD", ":powerwash", nil, extendedBudget}

	verbStop           = lifecycleVerb{"hoclient.StopCVD", ":stop", nil, interactiveBudget}
	verbStopGroup      = lifecycleVerb{"hoclient.StopCVDGroup", ":stop", nil, interactiveBudget}
	verbPowerbtn       = lifecycleVerb{"hoclient.PowerbtnCVD", ":powerbtn", nil, interactiveBudget}
	verbStartRecording = lifecycleVerb{"hoclient.StartScreenRecording", ":start_screen_recording", nil, interactiveBudget}
	verbStopRecording  = lifecycleVerb{"hoclient.StopScreenRecording", ":stop_screen_recording", nil, interactiveBudget}
)

// lifecycleCVD posts a lifecycle verb for a CVD and waits for the resulting
// operation under that verb's budget.
func (c *HostClient) lifecycleCVD(ctx context.Context, group, name string, v lifecycleVerb) error {
	return c.traced(ctx, v.spanName, cvdAttrs(group, name), func(ctx context.Context) error {
		path := fmt.Sprintf("/cvds/%s/%s/%s", url.PathEscape(group), url.PathEscape(name), v.verb)
		var op Operation
		if err := c.doRequestWithRetry(ctx, http.MethodPost, path, v.body, &op); err != nil {
			return err
		}
		return c.waitForOperation(ctx, op.Name, nil, v.budget)
	})
}

// lifecycleCVDGroup posts a group-scoped lifecycle verb and waits for the
// resulting operation under that verb's budget. The path omits the instance
// segment, which is what selects the host's group handler over its instance
// one. The same verb spelling addresses two different actions.
func (c *HostClient) lifecycleCVDGroup(ctx context.Context, group string, v lifecycleVerb) error {
	return c.traced(ctx, v.spanName, cvdAttrs(group, ""), func(ctx context.Context) error {
		path := fmt.Sprintf("/cvds/%s/%s", url.PathEscape(group), v.verb)
		var op Operation
		if err := c.doRequestWithRetry(ctx, http.MethodPost, path, v.body, &op); err != nil {
			return err
		}
		return c.waitForOperation(ctx, op.Name, nil, v.budget)
	})
}

// GetDisplays returns all displays for a CVD. cvdID is the device's recorded
// "group/name"; a value naming no group addresses no instance and is refused
// here rather than defaulted, because instance names are the host's to choose
// (a caller supplies them in advanced mode) and nothing in a bare id says
// which one was meant.
func (c *HostClient) GetDisplays(ctx context.Context, cvdID string) (*ListDisplaysResponse, error) {
	group, name := device.ParseCVDID(cvdID)
	if group == "" || name == "" {
		return nil, &HostError{
			StatusCode: http.StatusNotFound,
			Message:    fmt.Sprintf("cvd id %q does not name a CVD instance", cvdID),
			Cause:      ErrCVDNotFound,
		}
	}
	var resp ListDisplaysResponse
	err := c.traced(ctx, "hoclient.GetDisplays", cvdAttrs(group, name), func(ctx context.Context) error {
		path := fmt.Sprintf("/cvds/%s/%s/displays", url.PathEscape(group), url.PathEscape(name))
		if err := c.doRequestWithRetry(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return err
		}
		trace.SpanFromContext(ctx).SetAttributes(attribute.Int("display.count", len(resp.Displays)))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// AddDisplay adds a display to a CVD
func (c *HostClient) AddDisplay(ctx context.Context, group, name string, req *DisplayAddRequest) (*DisplayAddResponse, error) {
	attrs := append(cvdAttrs(group, name),
		attribute.Int("display.width", req.Width),
		attribute.Int("display.height", req.Height),
	)
	var resp DisplayAddResponse
	err := c.traced(ctx, "hoclient.AddDisplay", attrs, func(ctx context.Context) error {
		path := fmt.Sprintf("/cvds/%s/%s/displays", url.PathEscape(group), url.PathEscape(name))
		if err := c.doRequestWithRetry(ctx, http.MethodPost, path, req, &resp); err != nil {
			return err
		}
		trace.SpanFromContext(ctx).SetAttributes(attribute.Int("display.number", resp.DisplayNumber))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// RemoveDisplay removes a display from a CVD
func (c *HostClient) RemoveDisplay(ctx context.Context, group, name string, displayNumber int) error {
	attrs := append(cvdAttrs(group, name), attribute.Int("display.number", displayNumber))
	return c.traced(ctx, "hoclient.RemoveDisplay", attrs, func(ctx context.Context) error {
		path := fmt.Sprintf("/cvds/%s/%s/displays/%d", url.PathEscape(group), url.PathEscape(name), displayNumber)
		return c.doRequestWithRetry(ctx, http.MethodDelete, path, nil, nil)
	})
}

// CreatePolledConnection creates a new signaling connection
func (c *HostClient) CreatePolledConnection(ctx context.Context, deviceID string) (*PolledConnectionResponse, error) {
	attrs := []attribute.KeyValue{attribute.String("device.id", deviceID)}
	var resp PolledConnectionResponse
	err := c.traced(ctx, "hoclient.CreatePolledConnection", attrs, func(ctx context.Context) error {
		req := map[string]string{"device_id": deviceID}
		return c.doRequestWithRetry(ctx, http.MethodPost, "/polled_connections", req, &resp)
	})
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// ForwardSignalingMessage forwards a message to the device
func (c *HostClient) ForwardSignalingMessage(ctx context.Context, connID string, msg any) error {
	attrs := []attribute.KeyValue{attribute.String("conn.id", connID)}
	return c.traced(ctx, "hoclient.ForwardSignalingMessage", attrs, func(ctx context.Context) error {
		// The operator expects {payload: msg}
		wrapper := map[string]any{"payload": msg}
		return c.doRequestWithRetry(ctx, http.MethodPost, "/polled_connections/"+url.PathEscape(connID)+"/:forward", wrapper, nil)
	})
}

// GetSignalingMessages retrieves messages from the device
func (c *HostClient) GetSignalingMessages(ctx context.Context, connID string, start int) ([]any, error) {
	attrs := []attribute.KeyValue{
		attribute.String("conn.id", connID),
		attribute.Int("start", start),
	}
	var msgs []any
	err := c.traced(ctx, "hoclient.GetSignalingMessages", attrs, func(ctx context.Context) error {
		path := fmt.Sprintf("/polled_connections/%s/messages?start=%d", url.PathEscape(connID), start)
		if err := c.doRequestWithRetry(ctx, http.MethodGet, path, nil, &msgs); err != nil {
			return err
		}
		trace.SpanFromContext(ctx).SetAttributes(attribute.Int("message.count", len(msgs)))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return msgs, nil
}

// GetAllCVDStatus returns health status for all CVDs on the host
func (c *HostClient) GetAllCVDStatus(ctx context.Context) ([]*CVDHealthStatus, error) {
	cvds, err := c.ListCVDs(ctx)
	if err != nil {
		return nil, err
	}

	results := make([]*CVDHealthStatus, 0, len(cvds))
	for _, cvd := range cvds {
		results = append(results, &CVDHealthStatus{
			CVDID:  fmt.Sprintf("%s/%s", cvd.Group, cvd.Name),
			Group:  cvd.Group,
			Status: cvd.Status,
		})
	}

	return results, nil
}
