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
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"
)

// CVD represents a Cuttlefish Virtual Device on a host
type CVD struct {
	Group          string   `json:"group"`
	Name           string   `json:"name"`
	BuildID        string   `json:"build_id,omitempty"`
	Status         string   `json:"status"`
	Displays       []string `json:"displays,omitempty"`
	WebRTCDeviceID string   `json:"webrtc_device_id,omitempty"`
	ADBSerial      string   `json:"adb_serial,omitempty"`
}

// CVDHealthStatus is one CVD as the fleet listing reported it. Status is a raw
// CVD wire-state string (one of device.CVDState*); the classification lives in
// the device domain, the single source of truth for these strings.
type CVDHealthStatus struct {
	CVDID  string `json:"cvd_id"`
	Group  string `json:"group"` // host-side CVD group the instance belongs to
	Status string `json:"status"`
}

// CreateCVDRequest is the request to create a new CVD.
type CreateCVDRequest struct {
	// EnvConfig is the canonical `cvd load` environment configuration, passed
	// through to the host verbatim. Build references (common.host_package,
	// instances[*].disk.default_build, instances[*].boot.*) resolve as
	// @ab/<build>/<target> fetches or host-local paths; the parser rejects
	// unknown fields.
	// See: https://android.googlesource.com/device/google/cuttlefish/+/refs/heads/main/host/commands/cvd/parser/README.md
	EnvConfig map[string]any `json:"env_config,omitempty"`
}

// CreateCVDResponse is the response from creating a CVD
type CreateCVDResponse struct {
	CVDs []*CVD `json:"cvds"`
}

// ListCVDsResponse is the response from listing CVDs
type ListCVDsResponse struct {
	CVDs []*CVD `json:"cvds"`
}

// Operation represents an async operation
type Operation struct {
	Name string `json:"name"`
}

// ErrorResponse represents an error response from the host orchestrator
type ErrorResponse struct {
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
}

// DisplayAddRequest is the request to add a display to a CVD
type DisplayAddRequest struct {
	Width         int `json:"width"`
	Height        int `json:"height"`
	DPI           int `json:"dpi,omitempty"`
	RefreshRateHz int `json:"refresh_rate_hz,omitempty"`
}

// DisplayAddResponse is the response from adding a display
type DisplayAddResponse struct {
	DisplayNumber int `json:"display_number"`
}

// Display represents a display on a CVD (our normalized format)
type Display struct {
	DisplayNumber int `json:"display_number"`
	Width         int `json:"width"`
	Height        int `json:"height"`
	DPI           int `json:"dpi"`
	RefreshRateHz int `json:"refresh_rate_hz"`
}

// hoDisplayMode matches upstream: {"windowed": [720, 1280]}
type hoDisplayMode struct {
	Windowed []int `json:"windowed"`
}

// hoDisplay matches upstream Host Orchestrator format exactly:
// {"dpi":[320,320],"mode":{"windowed":[720,1280]},"refresh_rate_hz":60}
type hoDisplay struct {
	DPI           []int         `json:"dpi"`
	Mode          hoDisplayMode `json:"mode"`
	RefreshRateHz int           `json:"refresh_rate_hz"`
}

// DisplayScreenshotResponse is a captured display image, matching the Host
// Orchestrator wire shape (bytes arrive base64-encoded in JSON).
type DisplayScreenshotResponse struct {
	ScreenshotBytesBase64 string `json:"screenshot_bytes"`
	ScreenshotMimeType    string `json:"screenshot_mime_type"`
}

// ListDisplaysResponse is the response from listing displays
// Note: HO returns displays as map[int]hoDisplay per upstream API
type ListDisplaysResponse struct {
	Displays []Display `json:"displays"`
}

// UnmarshalJSON handles the HO response format where displays is a map with int keys
func (r *ListDisplaysResponse) UnmarshalJSON(data []byte) error {
	// Try to unmarshal the HO format: {"displays": {0: {...}, 1: {...}}}
	// Note: JSON keys are strings, so we use map[string] even though upstream uses map[int]
	var hoFormat struct {
		Displays map[string]hoDisplay `json:"displays"`
	}
	if err := json.Unmarshal(data, &hoFormat); err == nil && hoFormat.Displays != nil {
		r.Displays = make([]Display, 0, len(hoFormat.Displays))
		for numStr, info := range hoFormat.Displays {
			displayNum, err := strconv.Atoi(numStr)
			if err != nil {
				return fmt.Errorf("%w: display number %q: %w", ErrInvalidResponse, numStr, err)
			}

			// Extract dimensions from mode.windowed
			var width, height int
			if len(info.Mode.Windowed) >= 2 {
				width, height = info.Mode.Windowed[0], info.Mode.Windowed[1]
			}

			// Extract DPI (use first value if array)
			var dpi int
			if len(info.DPI) > 0 {
				dpi = info.DPI[0]
			}

			r.Displays = append(r.Displays, Display{
				DisplayNumber: displayNum,
				Width:         width,
				Height:        height,
				DPI:           dpi,
				RefreshRateHz: info.RefreshRateHz,
			})
		}
		return nil
	}

	// Fallback: try standard array format
	var arrayFormat struct {
		Displays []Display `json:"displays"`
	}
	if err := json.Unmarshal(data, &arrayFormat); err != nil {
		return err
	}
	r.Displays = arrayFormat.Displays
	return nil
}

// Client is the interface for communicating with a Host Orchestrator
type Client interface {
	// ListCVDs returns all CVDs on the host
	ListCVDs(ctx context.Context) ([]*CVD, error)

	// CreateCVD creates a new CVD
	CreateCVD(ctx context.Context, req *CreateCVDRequest) (*CreateCVDResponse, error)

	// CreateCVDOperation starts a CVD creation and returns the HO operation
	// name WITHOUT waiting. The caller journals it so a redelivery can
	// resume the same operation (journaled in task_steps).
	CreateCVDOperation(ctx context.Context, req *CreateCVDRequest) (string, error)

	// WaitCVDOperation waits for a creation operation by name, with the wait
	// ceiling sized by the group's desired instance count.
	WaitCVDOperation(ctx context.Context, operation string, instances int) (*CreateCVDResponse, error)

	// DeleteCVD deletes a CVD
	DeleteCVD(ctx context.Context, cvdID string) error

	// Reset performs a host-wide `cvd reset`: it stops and removes every CVD
	// group and kills orphaned run_cvd/crosvm processes, freeing leaked tap
	// devices and stale device-ID registrations. DESTRUCTIVE host-wide: every
	// CVD on the host is lost. Remediation uses it to un-poison a host in
	// place without recreating the VM.
	Reset(ctx context.Context) error

	// StartCVD starts ONE stopped instance of a group.
	StartCVD(ctx context.Context, group, name string) error

	// StartCVDGroup starts every instance of a group. Not the same verb as
	// StartCVD against the first instance: the host serves a group route and an
	// instance route separately, and the instance one leaves that group's other
	// instances stopped. Bringing a whole device up is this one.
	StartCVDGroup(ctx context.Context, group string) error

	// StopCVD stops ONE running instance of a group.
	StopCVD(ctx context.Context, group, name string) error

	// StopCVDGroup stops every instance of a group. Stopping a device is a
	// group verb for the same reason starting one is, and the failure is worse
	// in this direction: the instances left running are billed, hold their taps
	// and operator registrations, and sit behind a row that says stopped.
	StopCVDGroup(ctx context.Context, group string) error

	// RestartCVD relaunches a CVD in place (the cheap first step of zombie
	// recovery, before a full recreate).
	RestartCVD(ctx context.Context, group, name string) error

	// PowerwashCVD resets a CVD to first-boot state without re-fetching
	// artifacts (userdata wiped, same build).
	PowerwashCVD(ctx context.Context, group, name string) error

	// PowerbtnCVD presses the device's power button.
	PowerbtnCVD(ctx context.Context, group, name string) error

	// CreateSnapshot captures a running CVD's guest state into a snapshot
	// directory on the host (suspend → snapshot → resume) and returns the
	// snapshot ID once the capture operation completes. A failed capture can
	// leave the CVD suspended. The host never compensates, so the caller must.
	CreateSnapshot(ctx context.Context, group, name, snapshotID string) (string, error)

	// DeleteSnapshot removes a stored snapshot from the host's disk.
	// Idempotent: the host's remove succeeds for an absent snapshot.
	DeleteSnapshot(ctx context.Context, snapshotID string) error

	// StartCVDFromSnapshot starts a STOPPED CVD from a snapshot stored on the
	// host. The host 404s if the snapshot directory is gone.
	StartCVDFromSnapshot(ctx context.Context, group, name, snapshotID string) error

	// GetDisplayScreenshot captures one display of a CVD, waiting for the
	// host's asynchronous capture operation. A returned response always
	// carries image bytes; an empty capture is reported as an error.
	GetDisplayScreenshot(ctx context.Context, group, name string, displayNumber int) (*DisplayScreenshotResponse, error)

	// CreateBugreport captures a bugreport for a CVD group on the host and
	// returns its host-side ID once the capture operation completes.
	CreateBugreport(ctx context.Context, group string, includeADB bool) (string, error)

	// DownloadBugreport streams a captured bugreport ZIP. The caller must
	// close the reader; the body is NOT size-capped (bugreports routinely
	// exceed MaxResponseSize) and is bounded only by ctx.
	DownloadBugreport(ctx context.Context, bugreportID string) (io.ReadCloser, error)

	// DeleteBugreport removes a captured bugreport from the host's disk.
	DeleteBugreport(ctx context.Context, bugreportID string) error

	// StartScreenRecording starts a screen recording on a CVD instance.
	StartScreenRecording(ctx context.Context, group, name string) error

	// StopScreenRecording stops a screen recording on a CVD instance.
	StopScreenRecording(ctx context.Context, group, name string) error

	// ListScreenRecordings returns the names of screen recordings on the host
	// for a CVD instance.
	ListScreenRecordings(ctx context.Context, group, name string) ([]string, error)

	// DownloadScreenRecording streams a screen recording from the host. The
	// caller must close the reader; the body is NOT size-capped and is bounded
	// only by ctx.
	DownloadScreenRecording(ctx context.Context, group, name, recording string) (io.ReadCloser, error)

	// GetAllCVDStatus returns health status for all CVDs on the host
	GetAllCVDStatus(ctx context.Context) ([]*CVDHealthStatus, error)

	// ProbeCVDs observes the Host Orchestrator's CVD listing WITHOUT
	// consulting or teaching the host's circuit breaker. For boot-window
	// ready-probes (an answer, regardless of content, is the ready signal)
	// and best-effort mid-operation sampling only. See the HostClient
	// implementation for why ordinary calls must not be replaced with it.
	ProbeCVDs(ctx context.Context) ([]*CVD, error)

	// GetCVDLogTail fetches up to tailBytes from the end of one file in a
	// CVD's logs directory plus the file's total size, in one Range GET. The
	// boot-progress observation's raw input. Breaker-bypassing like ProbeCVDs
	// (a diagnostic read must never be what takes a host out), and not cheap
	// host-side, because the logs route spawns `cvd fleet` per request, so
	// callers budget their cadence.
	GetCVDLogTail(ctx context.Context, group, name, file string, tailBytes int64) (tail []byte, totalSize int64, err error)

	// FetchCVDLogFile reads one file from a CVD's logs directory whole, for
	// the questions a tail cannot answer, since assemble_cvd's launch decisions
	// sit at the HEAD of launcher.log. Capped at MaxResponseSize: a larger
	// file is an error rather than a truncated read. Shares the tail fetch's
	// host-side cost (`cvd fleet` per request).
	FetchCVDLogFile(ctx context.Context, group, name, file string) ([]byte, error)

	// GetDisplays returns all displays for a CVD by cvdID ("group/name")
	GetDisplays(ctx context.Context, cvdID string) (*ListDisplaysResponse, error)

	// AddDisplay adds a display to a CVD
	AddDisplay(ctx context.Context, group, name string, req *DisplayAddRequest) (*DisplayAddResponse, error)

	// RemoveDisplay removes a display from a CVD
	RemoveDisplay(ctx context.Context, group, name string, displayNumber int) error

	// CreatePolledConnection creates a new signaling connection
	CreatePolledConnection(ctx context.Context, deviceID string) (*PolledConnectionResponse, error)

	// ForwardSignalingMessage forwards a message to the device
	ForwardSignalingMessage(ctx context.Context, connID string, msg any) error

	// GetSignalingMessages retrieves messages from the device
	GetSignalingMessages(ctx context.Context, connID string, start int) ([]any, error)
}

// PolledConnectionResponse is the response from creating a polled connection
type PolledConnectionResponse struct {
	ConnID     string `json:"connection_id"`
	DeviceInfo any    `json:"device_info"`
}

// ClientConfig holds configuration for the client
type ClientConfig struct {
	// AllowInsecure allows self-signed certificates
	AllowInsecure bool

	// Timeout bounds a single normal (non-:wait) request, applied as a context
	// deadline covering headers AND body read. The http.Client carries no
	// timeout of its own so :wait long-polls can outlive this bound. 0 uses
	// defaultRequestTimeout; without that fallback a hand-built config would
	// leave every normal call with no deadline at all.
	Timeout time.Duration

	// RetryDelay is the initial delay for retries (with exponential backoff)
	RetryDelay time.Duration

	// MaxRetries is the maximum number of retry attempts for transient errors
	MaxRetries int

	// CallBudget bounds one complete retrying call: every attempt, port
	// sweep, and backoff in doRequestWithRetry. Without it a dead host holds
	// callers for the full retry ladder (~87s worst case with the defaults),
	// longer than any client waits, so impatient clients disconnect first and
	// the classified 502 never reaches them. 0 uses the 20s default; < 0
	// disables the bound. Streaming downloads and :wait long-polls are not
	// governed by it.
	CallBudget time.Duration

	// MaxWait is the outer ceiling on one operation wait, checked between polls.
	// 0 uses defaultMaxWait: left at zero the poll loop's deadline is `now`, so
	// it exits before its first poll and reports every operation as timed out
	// having never checked one.
	MaxWait time.Duration

	// WaitPollTimeout bounds a single :wait long-poll request. It MUST exceed
	// the Host Orchestrator's wait window (WaitOperationDuration, 2 min
	// upstream): the HO signals "still in progress" only by answering 503 at
	// the END of that window, so a shorter per-request timeout means the
	// signal can never arrive and every poll on a busy operation dies as a
	// fake connection failure, abandoning healthy in-flight operations
	// (the adopt-race trigger). 0 uses defaultWaitPollTimeout.
	WaitPollTimeout time.Duration

	// MaxResponseSize limits a decoded response body to prevent OOM. 0 uses
	// defaultMaxResponseSize: left at zero the limit is zero, so every response
	// with a body is rejected as too large.
	MaxResponseSize int64

	// MaxIdleConns is the PER-HOST idle connection pool size. The fleet-wide
	// total is not settable from here: it is a compiled bound
	// (maxIdleConnsTotal) that has to stay above the reachable-host working
	// set, which no caller of this config knows.
	MaxIdleConns int

	// IdleConnTimeout controls how long idle connections stay in the pool
	IdleConnTimeout time.Duration

	// FallbackPorts are alternative ports to try if the primary port fails.
	// This handles cases where Host Orchestrator might run on different ports
	// (e.g., 2080 via nginx proxy or 2081 directly).
	FallbackPorts []int

	// CircuitBreakerMaxFailures is the number of consecutive failed calls to a
	// host that trips its breaker open. 0 uses a default of 5.
	CircuitBreakerMaxFailures int

	// CircuitBreakerOpenTimeout is how long a tripped breaker stays open before
	// allowing a probe. 0 uses a default of 30s.
	CircuitBreakerOpenTimeout time.Duration
}

// DefaultClientConfig returns default client configuration
func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		AllowInsecure:   true,
		Timeout:         defaultRequestTimeout,
		RetryDelay:      1 * time.Second,
		MaxRetries:      3,
		CallBudget:      defaultCallBudget,
		MaxWait:         defaultMaxWait,
		WaitPollTimeout: defaultWaitPollTimeout,
		MaxResponseSize: defaultMaxResponseSize,
		MaxIdleConns:    10,
		IdleConnTimeout: 90 * time.Second,
		FallbackPorts:   []int{2080, 2081}, // Try nginx proxy (2080) and direct (2081)

		CircuitBreakerMaxFailures: 5,
		CircuitBreakerOpenTimeout: 30 * time.Second,
	}
}

// ScreenRecordingsResponse matches the Host Orchestrator's list shape.
type ScreenRecordingsResponse struct {
	ScreenRecordings []string `json:"screen_recordings"`
}

// CreateSnapshotRequest names the snapshot to take. The host accepts an empty
// id and picks one itself; CCO always supplies its own so the inventory row
// and the host-side directory agree by construction.
type CreateSnapshotRequest struct {
	SnapshotID string `json:"snapshot_id,omitempty"`
}

// CreateSnapshotResponse is the take operation's stored result.
type CreateSnapshotResponse struct {
	SnapshotID string `json:"snapshot_id"`
}

// StartCVDRequest is the :start verb's body. SnapshotID, when set, starts the
// CVD from that stored snapshot instead of a cold boot.
type StartCVDRequest struct {
	SnapshotID string `json:"snapshot_id,omitempty"`
}
