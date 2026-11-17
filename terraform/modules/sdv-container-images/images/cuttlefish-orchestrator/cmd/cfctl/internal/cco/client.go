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

package cco

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/auth"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// Version is the cfctl version set at build time, used for the User-Agent header.
var Version = "dev"

// Client is an HTTP client for the orchestrator API.
type Client struct {
	baseURL    string
	httpClient *http.Client
	// streamClient has the same authentication with no request bound and no
	// retries. A stream must outlive a per-request timeout, and an upgrade
	// handshake must never be replayed.
	streamClient *http.Client
	// pollInterval paces Wait* status polling. Zero means the 5s default.
	pollInterval time.Duration
	// requestBound and slowRequestBound cap one API call, retries included,
	// and match the server's two request-deadline classes. Zero leaves the
	// command context as the only bound.
	requestBound     time.Duration
	slowRequestBound time.Duration
}

const (
	// defaultRequestBound caps an ordinary API call.
	defaultRequestBound = 30 * time.Second
	// defaultSlowRequestBound caps a host-operation call. It is above the
	// server's 290s deadline for these routes, so a stuck verb ends with the
	// server's error.
	defaultSlowRequestBound = 300 * time.Second
)

// NewClient creates an API client for serverURL, which is also the JWT
// audience. A positive commandTimeout (the user's --timeout) is the only
// bound; otherwise each request is capped by its route class.
func NewClient(ctx context.Context, serverURL string, cfg *config.Config, commandTimeout time.Duration) (*Client, error) {
	serverURL = strings.TrimSuffix(serverURL, "/")

	httpClient, err := auth.NewHTTPClient(ctx, cfg, serverURL)
	if err != nil {
		return nil, fmt.Errorf("authenticating: %w", err)
	}
	if httpClient.Transport == nil {
		httpClient.Transport = http.DefaultTransport
	}

	// Streaming endpoints need the authentication without bound or retries.
	streamClient := &http.Client{Transport: httpClient.Transport}

	httpClient.Transport = &retryRoundTripper{
		next:       httpClient.Transport,
		maxRetries: 3,
	}

	c := &Client{
		baseURL:      serverURL,
		httpClient:   httpClient,
		streamClient: streamClient,
	}
	// An explicit --timeout replaces the per-route bounds so it can relax them.
	if commandTimeout <= 0 {
		c.requestBound = defaultRequestBound
		c.slowRequestBound = defaultSlowRequestBound
	}
	return c, nil
}

// slowOperationPath reports whether a path belongs to the server's
// host-operation deadline class (hostOperationRequest in internal/app). Those
// handlers wait on Host Orchestrator work and run under a 290s deadline.
func slowOperationPath(path string) bool {
	path, _, _ = strings.Cut(path, "?")
	if !strings.HasPrefix(path, "/v2/devices/") {
		return false
	}
	switch {
	case strings.HasSuffix(path, "/start"),
		strings.HasSuffix(path, "/stop"),
		strings.HasSuffix(path, "/restart"),
		strings.HasSuffix(path, "/powerwash"),
		strings.HasSuffix(path, "/powerbtn"),
		strings.HasSuffix(path, "/bugreport"):
		return true
	}
	return strings.Contains(path, "/recordings/")
}

// requestBoundFor picks the per-call bound for a path; zero means unbounded.
func (c *Client) requestBoundFor(path string) time.Duration {
	if slowOperationPath(path) {
		return c.slowRequestBound
	}
	return c.requestBound
}

// ProblemDetail represents an RFC 9457 error response from Huma.
type ProblemDetail struct {
	Title  string               `json:"title"`
	Detail string               `json:"detail,omitempty"`
	Errors []ProblemDetailError `json:"errors,omitempty"`
}

// ProblemDetailError is one entry of Huma's errors[] array (huma.ErrorDetail).
type ProblemDetailError struct {
	Message  string `json:"message,omitempty"`
	Location string `json:"location,omitempty"`
	Value    any    `json:"value,omitempty"`
}

// message renders the problem details with one errors[] entry per line.
func (pd *ProblemDetail) message() string {
	var b strings.Builder
	b.WriteString(pd.Title)
	if pd.Detail != "" {
		b.WriteString(": ")
		b.WriteString(pd.Detail)
	}
	for _, e := range pd.Errors {
		b.WriteString("\n  ")
		if e.Location != "" {
			b.WriteString(e.Location)
			b.WriteString(": ")
		}
		b.WriteString(e.Message)
	}
	return b.String()
}

// Error is a non-2xx API response. Callers branch on Status, for example
// WaitHost treats 404 as deleted.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// decodeAPIError turns a non-2xx response body into an *Error. It expects RFC
// 9457 problem details and falls back to the raw body (for example an IAP
// error page), truncated to a few kilobytes.
func decodeAPIError(statusCode int, body []byte) error {
	var pd ProblemDetail
	if err := json.Unmarshal(body, &pd); err == nil && pd.Title != "" {
		return &Error{Status: statusCode, Message: pd.message()}
	}
	const maxErrorBody = 2048
	if len(body) > maxErrorBody {
		body = fmt.Appendf(body[:maxErrorBody:maxErrorBody], "... (%d more bytes)", len(body)-maxErrorBody)
	}
	return &Error{Status: statusCode, Message: fmt.Sprintf("request failed with status %d: %s", statusCode, body)}
}

// userAgent has a %s placeholder for the build-time Version.
var userAgent = fmt.Sprintf("cfctl/%%s (%s/%s; %s)", runtime.GOOS, runtime.GOARCH, runtime.Version())

// do makes an HTTP request and decodes the JSON response.
func (c *Client) do(ctx context.Context, method, path string, body, result any) error {
	return c.doWithHeaders(ctx, method, path, body, result, nil)
}

// maxResponseBody caps how much of a response body is read.
const maxResponseBody = 10 << 20

// doWithHeaders is do plus caller-supplied request headers (e.g. Idempotency-Key).
func (c *Client) doWithHeaders(ctx context.Context, method, path string, body, result any, headers map[string]string) error {
	_, err := c.exchange(ctx, method, path, body, result, headers)
	return err
}

// exchange is doWithHeaders that also returns the response headers.
func (c *Client) exchange(ctx context.Context, method, path string, body, result any, headers map[string]string) (http.Header, error) {
	if bound := c.requestBoundFor(path); bound > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, bound)
		defer cancel()
	}

	u, err := url.Parse(c.baseURL + path)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	var bodyReader io.Reader
	if body != nil {
		bodyBytes, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to encode request body: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf(userAgent, Version))
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	output.Debug("--> %s %s", method, u)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	output.Debug("<-- %s", resp.Status)

	// One byte past the cap tells a full body from a cut one.
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, decodeAPIError(resp.StatusCode, respBody)
	}

	if len(respBody) > maxResponseBody {
		return nil, fmt.Errorf("%s %s: response body is larger than the %d MiB cfctl reads", method, path, maxResponseBody>>20)
	}

	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			return nil, fmt.Errorf("failed to decode response: %w", err)
		}
	}

	return resp.Header, nil
}

// idempotencyHeader returns the Idempotency-Key header, minting a key when the
// caller supplied none. retryRoundTripper only replays a POST that carries one.
func idempotencyHeader(key string) map[string]string {
	return map[string]string{"Idempotency-Key": cmp.Or(key, uuid.NewString())}
}

// listPageSize is the backend maximum per list request.
const listPageSize = 100

// listAll follows next_page_token on a keyset-paged list endpoint until the
// server returns an empty token. A page_token walk neither repeats nor skips
// rows when rows are created or deleted between requests.
func listAll[T any](ctx context.Context, c *Client, path string, params url.Values) ([]T, error) {
	items := []T{} // Non-nil so an empty --json result encodes as [].
	params.Set("limit", strconv.Itoa(listPageSize))
	for {
		var page ListResponse[T]
		if err := c.do(ctx, http.MethodGet, path+"?"+params.Encode(), nil, &page); err != nil {
			return nil, err
		}
		items = append(items, page.Items...)
		if page.NextPageToken == "" {
			return items, nil
		}
		params.Set("page_token", page.NextPageToken)
	}
}

// Health checks the server health.
func (c *Client) Health(ctx context.Context) (*HealthResponse, error) {
	var resp HealthResponse
	if err := c.do(ctx, http.MethodGet, "/health", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Info returns runtime information about the server (authenticated).
func (c *Client) Info(ctx context.Context) (*InfoResponse, error) {
	var resp InfoResponse
	if err := c.do(ctx, http.MethodGet, "/info", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Me returns the current user's information.
func (c *Client) Me(ctx context.Context) (*UserInfo, error) {
	var resp UserInfo
	if err := c.do(ctx, http.MethodGet, "/v2/me", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// setCreatorParams applies the created_by/mine listing scope to params.
func setCreatorParams(params url.Values, createdBy string, mine bool) {
	if createdBy != "" {
		params.Set("created_by", createdBy)
	}
	if mine {
		params.Set("mine", "true")
	}
}

// ListHosts returns all hosts matching the options.
func (c *Client) ListHosts(ctx context.Context, opts HostListOptions) ([]Host, error) {
	params := url.Values{}
	if opts.Name != "" {
		params.Set("name", opts.Name)
	}
	if opts.Zone != "" {
		params.Set("zone", opts.Zone)
	}
	if opts.Status != "" {
		params.Set("status", opts.Status)
	}
	setCreatorParams(params, opts.CreatedBy, opts.Mine)
	return listAll[Host](ctx, c, "/v2/hosts", params)
}

// GetHost returns a host by ID.
func (c *Client) GetHost(ctx context.Context, id string) (*Host, error) {
	var host Host
	if err := c.do(ctx, http.MethodGet, "/v2/hosts/"+url.PathEscape(id), nil, &host); err != nil {
		return nil, err
	}
	return &host, nil
}

// CreateHost creates a host. A repeat with the same idempotencyKey returns the
// original host. An empty key is minted per call.
func (c *Client) CreateHost(ctx context.Context, req CreateHostRequest, idempotencyKey string) (*Host, error) {
	var host Host
	headers := idempotencyHeader(idempotencyKey)
	if err := c.doWithHeaders(ctx, http.MethodPost, "/v2/hosts", req, &host, headers); err != nil {
		return nil, err
	}
	return &host, nil
}

// DeleteHost deletes a host.
func (c *Client) DeleteHost(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v2/hosts/"+url.PathEscape(id), nil, nil)
}

// StartHost starts a host.
func (c *Client) StartHost(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v2/hosts/"+url.PathEscape(id)+"/start", nil, nil)
}

// StopHost stops a host.
func (c *Client) StopHost(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v2/hosts/"+url.PathEscape(id)+"/stop", nil, nil)
}

// ListHostCVDs returns every CVD on a host, including ones CCO does not track
// as devices. The server queries the host live, so the call is not cheap.
func (c *Client) ListHostCVDs(ctx context.Context, hostID string) ([]HostCVD, error) {
	var resp ListHostCVDsResponse
	if err := c.do(ctx, http.MethodGet, "/v2/hosts/"+url.PathEscape(hostID)+"/cvds", nil, &resp); err != nil {
		return nil, err
	}
	if resp.Cvds == nil {
		return []HostCVD{}, nil
	}
	return resp.Cvds, nil
}

// ListDevices returns all devices matching the options.
func (c *Client) ListDevices(ctx context.Context, opts DeviceListOptions) ([]Device, error) {
	params := url.Values{}
	if opts.HostID != "" {
		params.Set("host_id", opts.HostID)
	}
	if opts.Name != "" {
		params.Set("name", opts.Name)
	}
	if opts.Status != "" {
		params.Set("status", opts.Status)
	}
	setCreatorParams(params, opts.CreatedBy, opts.Mine)
	return listAll[Device](ctx, c, "/v2/devices", params)
}

// GetDevice returns a device by ID.
func (c *Client) GetDevice(ctx context.Context, id string) (*Device, error) {
	var device Device
	if err := c.do(ctx, http.MethodGet, "/v2/devices/"+url.PathEscape(id), nil, &device); err != nil {
		return nil, err
	}
	return &device, nil
}

// CreateDevice creates a device, or only resolves the plan with ValidateOnly.
// A repeat with the same idempotencyKey returns the original device. An empty
// key is minted per call.
func (c *Client) CreateDevice(ctx context.Context, req CreateDeviceRequest, idempotencyKey string) (*CreateDeviceOutcome, error) {
	var outcome CreateDeviceOutcome
	headers := idempotencyHeader(idempotencyKey)
	if req.ValidateOnly {
		// A plan commits nothing, so there is no duplicate for a replay to avoid.
		headers = nil
	}
	if err := c.doWithHeaders(ctx, http.MethodPost, "/v2/devices", req, &outcome, headers); err != nil {
		return nil, err
	}
	return &outcome, nil
}

// DeleteDevice deletes a device.
func (c *Client) DeleteDevice(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v2/devices/"+url.PathEscape(id), nil, nil)
}

// CreateBatch creates a batch, or only plans it with ValidateOnly. A repeat
// with the same idempotencyKey resumes the same batch. An empty key is minted
// per call.
func (c *Client) CreateBatch(ctx context.Context, req CreateBatchRequest, idempotencyKey string) (*BatchSummary, error) {
	var summary BatchSummary
	headers := idempotencyHeader(idempotencyKey)
	if req.ValidateOnly {
		// A plan commits nothing, so there is no duplicate for a replay to avoid.
		headers = nil
	}
	if err := c.doWithHeaders(ctx, http.MethodPost, "/v2/batches", req, &summary, headers); err != nil {
		return nil, err
	}
	return &summary, nil
}

// GetBatch returns a batch's rolled-up status.
func (c *Client) GetBatch(ctx context.Context, id string) (*BatchSummary, error) {
	var summary BatchSummary
	if err := c.do(ctx, http.MethodGet, "/v2/batches/"+url.PathEscape(id), nil, &summary); err != nil {
		return nil, err
	}
	return &summary, nil
}

// ListBatches returns the active batches matching the options in one request.
// /v2/batches is unpaged, so it must not go through listAll.
func (c *Client) ListBatches(ctx context.Context, opts BatchListOptions) ([]BatchSummary, error) {
	params := url.Values{}
	setCreatorParams(params, opts.CreatedBy, opts.Mine)
	path := "/v2/batches"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	var resp ListResponse[BatchSummary]
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	if resp.Items == nil { // Non-nil so an empty --json result encodes as [].
		return []BatchSummary{}, nil
	}
	return resp.Items, nil
}

// ListBatchDevices returns the batch's member devices with connection info.
func (c *Client) ListBatchDevices(ctx context.Context, id string) ([]BatchMember, error) {
	var resp batchDevicesResponse
	if err := c.do(ctx, http.MethodGet, "/v2/batches/"+url.PathEscape(id)+"/devices", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Devices, nil
}

// DeleteBatch tears down every member device of the batch. Idempotent.
func (c *Client) DeleteBatch(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v2/batches/"+url.PathEscape(id), nil, nil)
}

// StartDevice starts a device.
func (c *Client) StartDevice(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v2/devices/"+url.PathEscape(id)+"/start", nil, nil)
}

// StopDevice stops a device.
func (c *Client) StopDevice(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v2/devices/"+url.PathEscape(id)+"/stop", nil, nil)
}

// instancePath is the route prefix for one Android VM in a device's CVD group.
// Interaction verbs are per instance; lifecycle verbs are per device.
func instancePath(id, instance string) string {
	return "/v2/devices/" + url.PathEscape(id) + "/instances/" + url.PathEscape(instance)
}

// RestartDevice starts relaunching one instance's CVD in place and returns the
// operation handle. The device row does not change, so only the operation shows
// the outcome.
func (c *Client) RestartDevice(ctx context.Context, id, instance string) (*Operation, error) {
	return c.postInstanceVerb(ctx, id, instance, "restart")
}

// PowerwashDevice starts resetting one instance to first-boot state and
// returns the operation handle.
func (c *Client) PowerwashDevice(ctx context.Context, id, instance string) (*Operation, error) {
	return c.postInstanceVerb(ctx, id, instance, "powerwash")
}

// postVerb admits an async device-scoped verb and decodes its handle.
func (c *Client) postVerb(ctx context.Context, id, verb string) (*Operation, error) {
	var out Operation
	if err := c.do(ctx, http.MethodPost, "/v2/devices/"+url.PathEscape(id)+"/"+verb, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// postInstanceVerb admits an async per-instance verb and decodes its handle.
func (c *Client) postInstanceVerb(ctx context.Context, id, instance, verb string) (*Operation, error) {
	var out Operation
	if err := c.do(ctx, http.MethodPost, instancePath(id, instance)+"/"+verb, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PowerbtnDevice presses one instance's power button.
func (c *Client) PowerbtnDevice(ctx context.Context, id, instance string) error {
	return c.do(ctx, http.MethodPost, instancePath(id, instance)+"/powerbtn", nil, nil)
}

// TakeDeviceSnapshot starts a snapshot of a running device and returns the
// operation handle. The settled operation carries the snapshot ID.
func (c *Client) TakeDeviceSnapshot(ctx context.Context, id string) (*Operation, error) {
	return c.postVerb(ctx, id, "snapshots")
}

// ListDeviceSnapshots lists a device's snapshot inventory, newest first.
func (c *Client) ListDeviceSnapshots(ctx context.Context, id string) ([]Snapshot, error) {
	var out ListSnapshotsResponse
	if err := c.do(ctx, http.MethodGet, "/v2/devices/"+url.PathEscape(id)+"/snapshots", nil, &out); err != nil {
		return nil, err
	}
	return out.Snapshots, nil
}

// DeleteDeviceSnapshot starts removing a device's snapshot and returns the
// operation handle.
func (c *Client) DeleteDeviceSnapshot(ctx context.Context, id, snapshotID string) (*Operation, error) {
	var out Operation
	path := "/v2/devices/" + url.PathEscape(id) + "/snapshots/" + url.PathEscape(snapshotID)
	if err := c.do(ctx, http.MethodDelete, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RestoreDeviceSnapshot starts a STOPPED device from one of its snapshots and
// returns the operation handle.
func (c *Client) RestoreDeviceSnapshot(ctx context.Context, id, snapshotID string) (*Operation, error) {
	var out Operation
	body := map[string]any{"snapshot_id": snapshotID}
	if err := c.do(ctx, http.MethodPost, "/v2/devices/"+url.PathEscape(id)+"/restore", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// BugreportDevice captures a bugreport for a device into artifact storage.
func (c *Client) BugreportDevice(ctx context.Context, id string, includeADB bool) (*Bugreport, error) {
	body := map[string]any{}
	if includeADB {
		body["include_adb"] = true
	}
	var resp Bugreport
	if err := c.do(ctx, http.MethodPost, "/v2/devices/"+url.PathEscape(id)+"/bugreport", body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// StartDeviceRecording starts a screen recording on one instance.
func (c *Client) StartDeviceRecording(ctx context.Context, id, instance string) error {
	return c.do(ctx, http.MethodPost, instancePath(id, instance)+"/recordings/start", nil, nil)
}

// StopDeviceRecording stops a screen recording on one instance.
func (c *Client) StopDeviceRecording(ctx context.Context, id, instance string) error {
	return c.do(ctx, http.MethodPost, instancePath(id, instance)+"/recordings/stop", nil, nil)
}

// ListDeviceRecordings returns one instance's host-local screen recording names.
func (c *Client) ListDeviceRecordings(ctx context.Context, id, instance string) ([]string, error) {
	var resp RecordingList
	if err := c.do(ctx, http.MethodGet, instancePath(id, instance)+"/recordings", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Recordings, nil
}

// FetchDeviceRecording archives a host-local recording to artifact storage.
func (c *Client) FetchDeviceRecording(ctx context.Context, id, instance, name string) (*FetchedRecording, error) {
	var resp FetchedRecording
	path := instancePath(id, instance) + "/recordings/" + url.PathEscape(name) + "/fetch"
	if err := c.do(ctx, http.MethodPost, path, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// DownloadStorageObject streams a storage object into w over the streamClient.
// do() caps bodies at 10 MB and bounds each request, which would cut off large
// artifact downloads.
func (c *Client) DownloadStorageObject(ctx context.Context, bucket, object string, w io.Writer) error {
	segments := strings.Split(object, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	path := "/v2/storage/buckets/" + url.PathEscape(bucket) + "/objects/" + strings.Join(segments, "/")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, http.NoBody)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", fmt.Sprintf(userAgent, Version))

	resp, err := c.streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return decodeAPIError(resp.StatusCode, body)
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("streaming gs://%s/%s: %w", bucket, object, err)
	}
	return nil
}

// GetDeviceScreenshot captures one display of one running instance.
func (c *Client) GetDeviceScreenshot(ctx context.Context, id, instance string, display int) (*Screenshot, error) {
	var shot Screenshot
	path := fmt.Sprintf("%s/displays/%d/screenshot", instancePath(id, instance), display)
	if err := c.do(ctx, http.MethodGet, path, nil, &shot); err != nil {
		return nil, err
	}
	return &shot, nil
}

// AdminListFailedTasks lists dead-lettered tasks.
func (c *Client) AdminListFailedTasks(ctx context.Context, limit int) (*AdminListFailedTasksResponse, error) {
	path := "/v2/admin/tasks/failed"
	if limit > 0 {
		path += fmt.Sprintf("?limit=%d", limit)
	}
	var resp AdminListFailedTasksResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// AdminRetryTask requeues a dead-lettered task for another attempt.
func (c *Client) AdminRetryTask(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v2/admin/tasks/"+url.PathEscape(id)+"/retry", nil, nil)
}

// AdminDeleteTask force-removes a task from the queue.
func (c *Client) AdminDeleteTask(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v2/admin/tasks/"+url.PathEscape(id), nil, nil)
}

// StartBuildUpload mints a staged-artifact upload session. An already-staged
// artifact (same content hash) returns AlreadyExists with no session.
func (c *Client) StartBuildUpload(ctx context.Context, req StartBuildUploadRequest) (*StartBuildUploadResponse, error) {
	var out StartBuildUploadResponse
	if err := c.do(ctx, http.MethodPost, "/v2/builds/uploads", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RegisterBuild registers a build; re-registering the same artifact set (or
// Android build ID) returns the existing record.
func (c *Client) RegisterBuild(ctx context.Context, req RegisterBuildRequest) (*Build, error) {
	var out Build
	if err := c.do(ctx, http.MethodPost, "/v2/builds", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListBuilds lists all registered builds.
func (c *Client) ListBuilds(ctx context.Context) ([]Build, error) {
	return listAll[Build](ctx, c, "/v2/builds", url.Values{})
}

// ListMachineTypes returns the machine-type catalog matching the options. The
// endpoint is unpaged and returns every match in one response.
func (c *Client) ListMachineTypes(ctx context.Context, opts MachineTypeListOptions) ([]MachineType, error) {
	params := url.Values{}
	if opts.Family != "" {
		params.Set("family", opts.Family)
	}
	if opts.Category != "" {
		params.Set("category", opts.Category)
	}
	if opts.Architecture != "" {
		params.Set("architecture", opts.Architecture)
	}
	if opts.Zone != "" {
		params.Set("zone", opts.Zone)
	}
	if opts.GPUSupport != "" {
		params.Set("gpu_support", opts.GPUSupport)
	}
	var page ListResponse[MachineType]
	if err := c.do(ctx, http.MethodGet, "/v2/machine-types?"+params.Encode(), nil, &page); err != nil {
		return nil, err
	}
	if page.Items == nil {
		page.Items = []MachineType{}
	}
	return page.Items, nil
}

// GetMachineType returns one machine-type catalog entry.
func (c *Client) GetMachineType(ctx context.Context, id string) (*MachineType, error) {
	var mt MachineType
	if err := c.do(ctx, http.MethodGet, "/v2/machine-types/"+url.PathEscape(id), nil, &mt); err != nil {
		return nil, err
	}
	return &mt, nil
}

// GetDefaultMachineType returns the machine type new hosts get by default.
func (c *Client) GetDefaultMachineType(ctx context.Context) (*MachineType, error) {
	var mt MachineType
	if err := c.do(ctx, http.MethodGet, "/v2/machine-types/default", nil, &mt); err != nil {
		return nil, err
	}
	return &mt, nil
}

// ListMachineFamilies returns the machine-family summaries.
func (c *Client) ListMachineFamilies(ctx context.Context) ([]FamilySummary, error) {
	var resp struct {
		Families []FamilySummary `json:"families"`
	}
	if err := c.do(ctx, http.MethodGet, "/v2/machine-families", nil, &resp); err != nil {
		return nil, err
	}
	if resp.Families == nil {
		resp.Families = []FamilySummary{} // Non-nil so an empty --json result encodes as [].
	}
	return resp.Families, nil
}

// ListZoneAvailability returns the zone catalog. machineType and gpuType
// narrow the availability answer to those resources; either may be empty.
func (c *Client) ListZoneAvailability(ctx context.Context, machineType, gpuType string) ([]ZoneAvailability, error) {
	params := url.Values{}
	if machineType != "" {
		params.Set("machine_type", machineType)
	}
	if gpuType != "" {
		params.Set("gpu_type", gpuType)
	}
	path := "/v2/zones/availability"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	var resp struct {
		Zones []ZoneAvailability `json:"zones"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	if resp.Zones == nil {
		resp.Zones = []ZoneAvailability{} // Non-nil so an empty --json result encodes as [].
	}
	return resp.Zones, nil
}

// GetQuota returns the platform quota.
func (c *Client) GetQuota(ctx context.Context) (*QuotaResponse, error) {
	var q QuotaResponse
	if err := c.do(ctx, http.MethodGet, "/v2/quota", nil, &q); err != nil {
		return nil, err
	}
	return &q, nil
}

// ExtendDevice extends a device's TTL by the given number of seconds
// (0 lets the server apply its default extension).
func (c *Client) ExtendDevice(ctx context.Context, id string, seconds int64) (*Device, error) {
	body := map[string]int64{}
	if seconds > 0 {
		body["seconds"] = seconds
	}
	var device Device
	if err := c.do(ctx, http.MethodPost, "/v2/devices/"+url.PathEscape(id)+"/extend", body, &device); err != nil {
		return nil, err
	}
	return &device, nil
}

// ExtendHost adds hours to a reserved host's lease, counted from the later of
// now and its current expiry.
func (c *Client) ExtendHost(ctx context.Context, id string, hours int64) (*Host, error) {
	body := map[string]int64{"hours": hours}
	var host Host
	if err := c.do(ctx, http.MethodPost, "/v2/hosts/"+url.PathEscape(id)+"/extend", body, &host); err != nil {
		return nil, err
	}
	return &host, nil
}

// RetryHost re-provisions a preempted or failed host in place.
func (c *Client) RetryHost(ctx context.Context, id string, fallbackToStandard bool) (*Host, error) {
	body := map[string]bool{}
	if fallbackToStandard {
		body["fallback_to_standard"] = true
	}
	var host Host
	if err := c.do(ctx, http.MethodPost, "/v2/hosts/"+url.PathEscape(id)+"/retry", body, &host); err != nil {
		return nil, err
	}
	return &host, nil
}

// UpdateHost changes how many devices a live host will accept.
func (c *Client) UpdateHost(ctx context.Context, id string, maxDevices int64) (*Host, error) {
	body := map[string]int64{"max_devices": maxDevices}
	var host Host
	if err := c.do(ctx, http.MethodPatch, "/v2/hosts/"+url.PathEscape(id), body, &host); err != nil {
		return nil, err
	}
	return &host, nil
}

// SyncHost asks the server to reconcile the host against GCE now.
func (c *Client) SyncHost(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v2/hosts/"+url.PathEscape(id)+"/sync", nil, nil)
}

// PinDevice fences a device from automatic reclamation.
func (c *Client) PinDevice(ctx context.Context, id string, req PinDeviceRequest) (*Device, error) {
	var device Device
	if err := c.do(ctx, http.MethodPost, "/v2/devices/"+url.PathEscape(id)+"/pin", req, &device); err != nil {
		return nil, err
	}
	return &device, nil
}

// UnpinDevice returns a device to normal reclamation.
func (c *Client) UnpinDevice(ctx context.Context, id string) (*Device, error) {
	var device Device
	if err := c.do(ctx, http.MethodPost, "/v2/devices/"+url.PathEscape(id)+"/unpin", nil, &device); err != nil {
		return nil, err
	}
	return &device, nil
}

// GetBuild returns one registered build.
func (c *Client) GetBuild(ctx context.Context, id string) (*Build, error) {
	var b Build
	if err := c.do(ctx, http.MethodGet, "/v2/builds/"+url.PathEscape(id), nil, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// DeleteBuild removes a build from the registry.
func (c *Client) DeleteBuild(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v2/builds/"+url.PathEscape(id), nil, nil)
}

// ListDisplays returns one instance's displays and the available presets.
func (c *Client) ListDisplays(ctx context.Context, id, instance string) (*ListDisplaysResponse, error) {
	var resp ListDisplaysResponse
	if err := c.do(ctx, http.MethodGet, instancePath(id, instance)+"/displays", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// AddDisplay adds a display to one instance and returns its assigned number.
func (c *Client) AddDisplay(ctx context.Context, id, instance string, req AddDisplayRequest) (int, error) {
	var resp struct {
		DisplayNumber int `json:"display_number"`
	}
	if err := c.do(ctx, http.MethodPost, instancePath(id, instance)+"/displays", req, &resp); err != nil {
		return 0, err
	}
	return resp.DisplayNumber, nil
}

// RemoveDisplay removes a display from one instance.
func (c *Client) RemoveDisplay(ctx context.Context, id, instance string, displayNumber int) error {
	return c.do(ctx, http.MethodDelete,
		instancePath(id, instance)+"/displays/"+strconv.Itoa(displayNumber), nil, nil)
}

// GetDeviceLifecycle returns a device's TTL and pin state.
func (c *Client) GetDeviceLifecycle(ctx context.Context, id string) (*DeviceLifecycle, error) {
	var lc DeviceLifecycle
	if err := c.do(ctx, http.MethodGet, "/v2/devices/"+url.PathEscape(id)+"/lifecycle", nil, &lc); err != nil {
		return nil, err
	}
	return &lc, nil
}

// GetReady checks server readiness: the process is alive and its dependencies
// are reachable.
func (c *Client) GetReady(ctx context.Context) (*HealthResponse, error) {
	var resp HealthResponse
	if err := c.do(ctx, http.MethodGet, "/ready", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// VerifyAccess asks the server to verify storage access.
func (c *Client) VerifyAccess(ctx context.Context, req VerifyAccessRequest) (*VerifyAccessResponse, error) {
	var resp VerifyAccessResponse
	if err := c.do(ctx, http.MethodPost, "/v2/storage/verify-access", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// configGet reads one system-config setting body and its entity tag.
func configGet[T any](ctx context.Context, c *Client, key string) (T, string, error) {
	var v T
	header, err := c.exchange(ctx, http.MethodGet, "/v2/admin/system-config/"+key, nil, &v, nil)
	if err != nil {
		return v, "", err
	}
	return v, header.Get("ETag"), nil
}

// configWrite sends a PUT or DELETE to one system-config setting and decodes
// the stored echo and the new entity tag. A non-empty ifMatch makes the write
// conditional on the setting being unchanged since the read that returned it.
func configWrite[T any](ctx context.Context, c *Client, method, key, ifMatch string, body any) (T, string, error) {
	var v T
	var headers map[string]string
	if ifMatch != "" {
		headers = map[string]string{"If-Match": ifMatch}
	}
	header, err := c.exchange(ctx, method, "/v2/admin/system-config/"+key, body, &v, headers)
	if apiErr, ok := errors.AsType[*Error](err); ok && apiErr.Status == http.StatusPreconditionFailed {
		return v, "", &Error{
			Status:  apiErr.Status,
			Message: "the setting changed since it was read; run the command again to apply the change to the current value",
		}
	}
	if err != nil {
		return v, "", err
	}
	return v, header.Get("ETag"), nil
}

// GetZonePin reads the single zone placement is restricted to; empty means
// ranked placement across the enabled catalog.
func (c *Client) GetZonePin(ctx context.Context) (string, error) {
	v, _, err := configGet[StringValue](ctx, c, "zone-pin")
	return v.Value, err
}

// UpdateZonePin restricts placement to one zone; empty clears the pin.
func (c *Client) UpdateZonePin(ctx context.Context, zone string) (string, error) {
	v, _, err := configWrite[StringValue](ctx, c, http.MethodPut, "zone-pin", "", StringValue{Value: zone})
	return v.Value, err
}

// GetQuotaMargin reads the percent of each regional quota limit zone ranking
// keeps in reserve.
func (c *Client) GetQuotaMargin(ctx context.Context) (int64, error) {
	v, _, err := configGet[Int64Value](ctx, c, "quota-margin")
	return v.Value, err
}

// UpdateQuotaMargin sets the quota reserve margin percent.
func (c *Client) UpdateQuotaMargin(ctx context.Context, percent int64) (int64, error) {
	v, _, err := configWrite[Int64Value](ctx, c, http.MethodPut, "quota-margin", "", Int64Value{Value: percent})
	return v.Value, err
}

// GetV1ZoneHint reads whether the legacy /v1 zone token is a placement hint
// (true) or a literal pin (false).
func (c *Client) GetV1ZoneHint(ctx context.Context) (bool, error) {
	v, _, err := configGet[BoolValue](ctx, c, "v1-zone-hint")
	return v.Value, err
}

// UpdateV1ZoneHint sets the legacy zone-token mode.
func (c *Client) UpdateV1ZoneHint(ctx context.Context, hint bool) (bool, error) {
	v, _, err := configWrite[BoolValue](ctx, c, http.MethodPut, "v1-zone-hint", "", BoolValue{Value: hint})
	return v.Value, err
}

// GetSpotAutoDefault reads whether auto-capacity headless intent resolves to
// Spot hosts.
func (c *Client) GetSpotAutoDefault(ctx context.Context) (bool, error) {
	v, _, err := configGet[BoolValue](ctx, c, "spot-auto-default")
	return v.Value, err
}

// UpdateSpotAutoDefault sets the Spot auto-default.
func (c *Client) UpdateSpotAutoDefault(ctx context.Context, on bool) (bool, error) {
	v, _, err := configWrite[BoolValue](ctx, c, http.MethodPut, "spot-auto-default", "", BoolValue{Value: on})
	return v.Value, err
}

// GetDefaultGPUMachineType reads the machine-type override for
// auto-provisioned GPU hosts; empty means the catalog fallback.
func (c *Client) GetDefaultGPUMachineType(ctx context.Context) (string, error) {
	v, _, err := configGet[StringValue](ctx, c, "default-gpu-machine-type")
	return v.Value, err
}

// UpdateDefaultGPUMachineType sets the GPU host machine-type override; empty
// clears it.
func (c *Client) UpdateDefaultGPUMachineType(ctx context.Context, machineType string) (string, error) {
	v, _, err := configWrite[StringValue](ctx, c, http.MethodPut, "default-gpu-machine-type", "", StringValue{Value: machineType})
	return v.Value, err
}

// GetDefaultUpstreamBuild reads the AOSP branch/target used when a
// declarative create finds no registered builds.
func (c *Client) GetDefaultUpstreamBuild(ctx context.Context) (*UpstreamBuild, string, error) {
	v, etag, err := configGet[UpstreamBuild](ctx, c, "default-upstream-build")
	if err != nil {
		return nil, "", err
	}
	return &v, etag, nil
}

// UpdateDefaultUpstreamBuild replaces the upstream default build pair.
func (c *Client) UpdateDefaultUpstreamBuild(ctx context.Context, ifMatch string, build UpstreamBuild) (*UpstreamBuild, string, error) {
	v, etag, err := configWrite[UpstreamBuild](ctx, c, http.MethodPut, "default-upstream-build", ifMatch, build)
	if err != nil {
		return nil, "", err
	}
	return &v, etag, nil
}

// GetSpotMinUptime reads the minimum estimated-uptime tiers Spot placement
// demands from capacity advice.
func (c *Client) GetSpotMinUptime(ctx context.Context) (*SpotMinUptime, string, error) {
	v, etag, err := configGet[SpotMinUptime](ctx, c, "spot-min-uptime")
	if err != nil {
		return nil, "", err
	}
	return &v, etag, nil
}

// UpdateSpotMinUptime replaces the Spot min-uptime tier pair.
func (c *Client) UpdateSpotMinUptime(ctx context.Context, ifMatch string, tiers SpotMinUptime) (*SpotMinUptime, string, error) {
	v, etag, err := configWrite[SpotMinUptime](ctx, c, http.MethodPut, "spot-min-uptime", ifMatch, tiers)
	if err != nil {
		return nil, "", err
	}
	return &v, etag, nil
}

// GetSweepPolicy reads what the background loops may do on their own.
func (c *Client) GetSweepPolicy(ctx context.Context) (*SweepPolicy, string, error) {
	v, etag, err := configGet[SweepPolicy](ctx, c, "sweeps")
	if err != nil {
		return nil, "", err
	}
	return &v, etag, nil
}

// UpdateSweepPolicy changes only the switches the request names.
func (c *Client) UpdateSweepPolicy(ctx context.Context, ifMatch string, req UpdateSweepPolicyRequest) (*SweepPolicy, string, error) {
	v, etag, err := configWrite[SweepPolicy](ctx, c, http.MethodPut, "sweeps", ifMatch, req)
	if err != nil {
		return nil, "", err
	}
	return &v, etag, nil
}

// GetDockerHost reads the GCE shape a legacy docker-only host create
// compiles to, alongside its compiled TTL cap.
func (c *Client) GetDockerHost(ctx context.Context) (*DockerHost, string, error) {
	v, etag, err := configGet[DockerHost](ctx, c, "docker-host")
	if err != nil {
		return nil, "", err
	}
	return &v, etag, nil
}

// UpdateDockerHost replaces the docker-host shape.
func (c *Client) UpdateDockerHost(ctx context.Context, ifMatch string, req UpdateDockerHostRequest) (*DockerHost, string, error) {
	v, etag, err := configWrite[DockerHost](ctx, c, http.MethodPut, "docker-host", ifMatch, req)
	if err != nil {
		return nil, "", err
	}
	return &v, etag, nil
}

// GetLifetimes reads the lifetime policy alongside its compiled caps.
func (c *Client) GetLifetimes(ctx context.Context) (*Lifetimes, string, error) {
	v, etag, err := configGet[Lifetimes](ctx, c, "lifetimes")
	if err != nil {
		return nil, "", err
	}
	return &v, etag, nil
}

// UpdateLifetimes replaces the four settable lifetime defaults together.
func (c *Client) UpdateLifetimes(ctx context.Context, ifMatch string, req UpdateLifetimesRequest) (*Lifetimes, string, error) {
	v, etag, err := configWrite[Lifetimes](ctx, c, http.MethodPut, "lifetimes", ifMatch, req)
	if err != nil {
		return nil, "", err
	}
	return &v, etag, nil
}

// GetFleetPolicy reads the autoscaler policy alongside its compiled bounds.
func (c *Client) GetFleetPolicy(ctx context.Context) (*FleetPolicy, string, error) {
	v, etag, err := configGet[FleetPolicy](ctx, c, "fleet")
	if err != nil {
		return nil, "", err
	}
	return &v, etag, nil
}

// UpdateFleetAutoscaler sets the autoscaler mode and one pool's free-host and
// running-host lower bounds. An empty pool is the default one, and naming a
// pool switches it on.
func (c *Client) UpdateFleetAutoscaler(ctx context.Context, ifMatch string, req UpdateFleetAutoscalerRequest) (*FleetPolicy, string, error) {
	v, etag, err := configWrite[FleetPolicy](ctx, c, http.MethodPut, "fleet/autoscaler", ifMatch, req)
	if err != nil {
		return nil, "", err
	}
	return &v, etag, nil
}

// UpdateFleetTuning sets the demand horizon and the drain grace.
func (c *Client) UpdateFleetTuning(ctx context.Context, ifMatch string, req UpdateFleetTuningRequest) (*FleetPolicy, string, error) {
	v, etag, err := configWrite[FleetPolicy](ctx, c, http.MethodPut, "fleet/tuning", ifMatch, req)
	if err != nil {
		return nil, "", err
	}
	return &v, etag, nil
}

// UpdateFleetSchedules replaces one pool's schedule windows; an empty list
// clears them. An empty pool is the default one.
func (c *Client) UpdateFleetSchedules(ctx context.Context, ifMatch, pool string, schedules []FleetSchedule) (*FleetPolicy, string, error) {
	if schedules == nil {
		schedules = []FleetSchedule{}
	}
	v, etag, err := configWrite[FleetPolicy](ctx, c, http.MethodPut, "fleet/schedules", ifMatch,
		UpdateFleetSchedulesRequest{Pool: pool, Schedules: schedules})
	if err != nil {
		return nil, "", err
	}
	return &v, etag, nil
}

// UpdateFleetLadder replaces one pool's ordered machine-type preference. An
// empty list restores the catalog default, and an empty pool is the default
// one. The server validates each machine type against the pool.
func (c *Client) UpdateFleetLadder(ctx context.Context, ifMatch, pool string, rungs []string) (*FleetPolicy, string, error) {
	if rungs == nil {
		rungs = []string{}
	}
	v, etag, err := configWrite[FleetPolicy](ctx, c, http.MethodPut, "fleet/ladder", ifMatch,
		UpdateFleetLadderRequest{Pool: pool, ShapeLadder: rungs})
	if err != nil {
		return nil, "", err
	}
	return &v, etag, nil
}

// DisableFleetPool switches a pool off and removes its bounds, windows and
// ladder. Its hosts drain by normal reclamation. The default pool cannot be
// switched off.
func (c *Client) DisableFleetPool(ctx context.Context, ifMatch, pool string) (*FleetPolicy, string, error) {
	v, etag, err := configWrite[FleetPolicy](ctx, c, http.MethodDelete, "fleet/pools/"+url.PathEscape(pool), ifMatch, nil)
	if err != nil {
		return nil, "", err
	}
	return &v, etag, nil
}

// GetArtifactBuckets reads the artifact bucket settings, including the
// read-only deployment default.
func (c *Client) GetArtifactBuckets(ctx context.Context) (*ArtifactBuckets, string, error) {
	v, etag, err := configGet[ArtifactBuckets](ctx, c, "artifact-buckets")
	if err != nil {
		return nil, "", err
	}
	return &v, etag, nil
}

// UpdateArtifactBuckets replaces the artifact bucket overrides and the
// capture-on-error toggle; overlay onto GetArtifactBuckets first.
func (c *Client) UpdateArtifactBuckets(ctx context.Context, ifMatch string, req UpdateArtifactBucketsRequest) (*ArtifactBuckets, string, error) {
	v, etag, err := configWrite[ArtifactBuckets](ctx, c, http.MethodPut, "artifact-buckets", ifMatch, req)
	if err != nil {
		return nil, "", err
	}
	return &v, etag, nil
}

// AdminStats returns the platform-statistics snapshot.
func (c *Client) AdminStats(ctx context.Context) (*AdminStats, error) {
	var s AdminStats
	if err := c.do(ctx, http.MethodGet, "/v2/admin/stats", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// AdminProvisioningStats returns provisioning outcome counts over the
// server's window.
func (c *Client) AdminProvisioningStats(ctx context.Context) (*ProvisioningStats, error) {
	var s ProvisioningStats
	if err := c.do(ctx, http.MethodGet, "/v2/admin/provisioning-stats", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// AdminReclaimable returns the reclaimable-capacity worklist.
func (c *Client) AdminReclaimable(ctx context.Context) (*Reclaimable, error) {
	var r Reclaimable
	if err := c.do(ctx, http.MethodGet, "/v2/admin/reclaimable", nil, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// AdminTaskStats returns the task-queue depth snapshot.
func (c *Client) AdminTaskStats(ctx context.Context) (*TaskQueueStats, error) {
	var s TaskQueueStats
	if err := c.do(ctx, http.MethodGet, "/v2/admin/tasks/stats", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// AdminListAudit returns audit-log entries matching the filter, newest first.
func (c *Client) AdminListAudit(ctx context.Context, f AuditFilter) (*AuditListResponse, error) {
	params := url.Values{}
	for key, val := range map[string]string{
		"actor_email": f.ActorEmail, "action": f.Action,
		"resource_type": f.ResourceType, "resource_id": f.ResourceID,
		"kind": f.Kind, "start_time": f.StartTime, "end_time": f.EndTime,
	} {
		if val != "" {
			params.Set(key, val)
		}
	}
	if f.Limit > 0 {
		params.Set("limit", strconv.Itoa(f.Limit))
	}
	if f.Offset > 0 {
		params.Set("offset", strconv.Itoa(f.Offset))
	}
	path := "/v2/admin/audit"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	var resp AuditListResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// AdminControlPlane returns the control-plane snapshot.
func (c *Client) AdminControlPlane(ctx context.Context) (*ControlPlane, error) {
	var cp ControlPlane
	if err := c.do(ctx, http.MethodGet, "/v2/admin/control-plane", nil, &cp); err != nil {
		return nil, err
	}
	return &cp, nil
}

// AdminCapacity returns the capacity snapshot, which shows where the fleet can
// grow now and the regional quota headroom.
func (c *Client) AdminCapacity(ctx context.Context) (*Capacity, error) {
	var snapshot Capacity
	if err := c.do(ctx, http.MethodGet, "/v2/admin/capacity", nil, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// AdminListZones returns every zone Compute Engine offers this project, with
// the operator's enablement decision and the curated shapes each zone stocks.
func (c *Client) AdminListZones(ctx context.Context) (*AdminZoneList, error) {
	var resp AdminZoneList
	if err := c.do(ctx, http.MethodGet, "/v2/admin/zones", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// AdminSetEnabledZones replaces the set of zones enabled for host creation.
func (c *Client) AdminSetEnabledZones(ctx context.Context, zones []string) ([]string, error) {
	body := struct {
		Zones []string `json:"zones"`
	}{Zones: zones}
	var resp struct {
		Zones []string `json:"zones"`
	}
	if err := c.do(ctx, http.MethodPut, "/v2/admin/zones/enabled", body, &resp); err != nil {
		return nil, err
	}
	return resp.Zones, nil
}

// AdminClearZoneCooldown deletes a zone_health cell, which ends the cooldown and
// resets the consecutive-failure count. It is idempotent.
func (c *Client) AdminClearZoneCooldown(ctx context.Context, zone, machineFamily string) error {
	path := "/v2/admin/zone-health/" + url.PathEscape(zone) + "/" + url.PathEscape(machineFamily)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// AdminCatalogFreshness reports the per-region catalog sync state.
func (c *Client) AdminCatalogFreshness(ctx context.Context) (*CatalogFreshness, error) {
	var f CatalogFreshness
	if err := c.do(ctx, http.MethodGet, "/v2/admin/catalog/freshness", nil, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// AdminUpdateMachineType curates a machine type: enablement or the catalog
// default.
func (c *Client) AdminUpdateMachineType(ctx context.Context, id string, req UpdateMachineTypeRequest) (*MachineType, error) {
	var m MachineType
	if err := c.do(ctx, http.MethodPatch, "/v2/admin/machine-types/"+url.PathEscape(id), req, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// ListHostImageConfigs returns the machine-family to image-family mappings.
func (c *Client) ListHostImageConfigs(ctx context.Context, opts HostImageConfigListOptions) (*HostImageConfigList, error) {
	params := url.Values{}
	if opts.Limit > 0 {
		params.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Offset > 0 {
		params.Set("offset", strconv.Itoa(opts.Offset))
	}
	if opts.EnabledOnly {
		params.Set("enabled_only", "true")
	}
	path := "/v2/admin/host-image-configs"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	var list HostImageConfigList
	if err := c.do(ctx, http.MethodGet, path, nil, &list); err != nil {
		return nil, err
	}
	return &list, nil
}

// GetHostImageConfig returns one machine family's image mapping.
func (c *Client) GetHostImageConfig(ctx context.Context, family string) (*HostImageConfig, error) {
	var cfg HostImageConfig
	if err := c.do(ctx, http.MethodGet, "/v2/admin/host-image-configs/"+url.PathEscape(family), nil, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// CreateHostImageConfig registers a machine-family to image-family mapping.
func (c *Client) CreateHostImageConfig(ctx context.Context, req CreateHostImageConfigRequest) (*HostImageConfig, error) {
	var cfg HostImageConfig
	if err := c.do(ctx, http.MethodPost, "/v2/admin/host-image-configs", req, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// UpdateHostImageConfig replaces a mapping's mutable fields.
func (c *Client) UpdateHostImageConfig(ctx context.Context, family string, req UpdateHostImageConfigRequest) (*HostImageConfig, error) {
	var cfg HostImageConfig
	if err := c.do(ctx, http.MethodPut, "/v2/admin/host-image-configs/"+url.PathEscape(family), req, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// DeleteHostImageConfig removes a machine family's image mapping.
func (c *Client) DeleteHostImageConfig(ctx context.Context, family string) error {
	return c.do(ctx, http.MethodDelete, "/v2/admin/host-image-configs/"+url.PathEscape(family), nil, nil)
}

// ListStorageBuckets lists the buckets the deployment can reach.
func (c *Client) ListStorageBuckets(ctx context.Context) (*ListStorageBucketsResponse, error) {
	var resp ListStorageBucketsResponse
	if err := c.do(ctx, http.MethodGet, "/v2/storage/buckets", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// storagePageSize is the server's maximum page_size for an object listing.
const storagePageSize = 1000

// ListStorageObjects lists one folder level of a bucket, optionally under a
// prefix, following next_page_token until the last page.
func (c *Client) ListStorageObjects(ctx context.Context, bucket, prefix string) (*ListStorageObjectsResponse, error) {
	path := "/v2/storage/buckets/" + url.PathEscape(bucket) + "/objects"
	params := url.Values{}
	if prefix != "" {
		params.Set("prefix", prefix)
	}
	params.Set("page_size", strconv.Itoa(storagePageSize))
	all := &ListStorageObjectsResponse{Objects: []StorageObject{}}
	for {
		var page ListStorageObjectsResponse
		if err := c.do(ctx, http.MethodGet, path+"?"+params.Encode(), nil, &page); err != nil {
			return nil, err
		}
		all.Objects = append(all.Objects, page.Objects...)
		all.Prefixes = append(all.Prefixes, page.Prefixes...)
		if page.NextPageToken == "" {
			return all, nil
		}
		params.Set("page_token", page.NextPageToken)
	}
}

// DeleteStorageObject deletes one object. The name is escaped whole so a nested
// name fits the single {name} path segment.
func (c *Client) DeleteStorageObject(ctx context.Context, bucket, name string) error {
	return c.do(ctx, http.MethodDelete,
		"/v2/storage/buckets/"+url.PathEscape(bucket)+"/objects/"+url.PathEscape(name), nil, nil)
}

// UpdateBuild patches a build's mutable fields.
func (c *Client) UpdateBuild(ctx context.Context, id string, req UpdateBuildRequest) (*Build, error) {
	var b Build
	if err := c.do(ctx, http.MethodPatch, "/v2/builds/"+url.PathEscape(id), req, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// UpdatePlatformQuota sets the platform-wide host quota.
func (c *Client) UpdatePlatformQuota(ctx context.Context, maxHosts int64) (*QuotaResponse, error) {
	body := struct {
		MaxHosts int64 `json:"max_hosts"`
	}{MaxHosts: maxHosts}
	var q QuotaResponse
	if err := c.do(ctx, http.MethodPut, "/v2/quota/platform", body, &q); err != nil {
		return nil, err
	}
	return &q, nil
}
