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

package devicefeature

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	"cloud-cuttlefish-orchestrator/internal/features/device/validation"
	"cloud-cuttlefish-orchestrator/internal/platform/ice"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// tagDevices groups the device operations in the OpenAPI document.
const tagDevices = "Devices"

// DeviceHandler manages the HTTP transport layer for device operations.
//
// It is responsible for parsing requests, validating input formats, and
// invoking the device service. It does not contain business logic.
type DeviceHandler struct {
	service          *DeviceService
	ice              *ice.Composer
	signalingHandler *SignalingHandler
	wsHandler        *WebSocketHandler
}

// NewDeviceHandler creates a new device handler.
func NewDeviceHandler(
	service *DeviceService,
	iceComposer *ice.Composer,
	signalingHandler *SignalingHandler,
	wsHandler *WebSocketHandler,
) *DeviceHandler {
	return &DeviceHandler{
		service:          service,
		ice:              iceComposer,
		signalingHandler: signalingHandler,
		wsHandler:        wsHandler,
	}
}

// RegisterRaw binds the routes Huma cannot express as raw chi handlers. The
// signaling long-poll pair holds requests open, and the two WebSocket routes
// hijack the connection.
//
// The ADB route carries a byte stream instead of JSON, which is why it is here
// and not a Huma operation. It gives a client the ADB wire protocol without
// any route to the host. That is the only way to reach a device when media is
// relayed, and the only one that needs no VPN when hosts are private.
// Opening a stream or an ADB bridge names an INSTANCE. Every Android VM in a
// group registers its own operator device id, and the device id alone reaches
// none of them. The two message routes stay device-scoped because the operator
// mints connId against a device it has already been told about. The instance
// is settled by then, and repeating it in the path would decorate a segment
// nothing reads.
func (h *DeviceHandler) RegisterRaw(r chi.Router) {
	r.Post("/v2/devices/{id}/instances/{name}/signaling/connect", h.signalingHandler.CreateConnection)
	r.Post("/v2/devices/{id}/signaling/{connId}", h.signalingHandler.ForwardMessage)
	r.Get("/v2/devices/{id}/signaling/{connId}", h.signalingHandler.GetMessages)
	r.Get("/v2/devices/{id}/instances/{name}/signaling/ws", h.wsHandler.HandleSignaling)
	r.Get("/v2/devices/{id}/instances/{name}/adb", h.wsHandler.HandleADB)
}

// Register binds the HTTP routes to the Huma OpenAPI router.
//
// It defines the API contract, including paths, methods, summary documentation,
// and security requirements for all device-related endpoints.
func (h *DeviceHandler) Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-devices",
		Method:      http.MethodGet,
		Path:        "/v2/devices",
		Summary:     "List devices",
		Description: "Returns a list of Cuttlefish virtual devices",
		Tags:        []string{tagDevices},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusBadRequest, http.StatusUnauthorized},
	}, h.ListDevices)

	huma.Register(api, huma.Operation{
		OperationID: "create-device",
		Method:      http.MethodPost,
		Path:        "/v2/devices",
		Summary:     "Create a device",
		Description: "Creates a new Cuttlefish virtual device on a host",
		Tags:        []string{tagDevices},
		Security:    web.BearerAuth(),
		// 422: an Idempotency-Key reused with a different request.
		// 429: declarative auto-provisioning exhausted the host quota.
		// 503: the chosen host has no device capacity left.
		Errors: []int{
			http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound,
			http.StatusConflict, http.StatusUnprocessableEntity,
			http.StatusTooManyRequests, http.StatusServiceUnavailable,
		},
	}, h.CreateDevice)

	huma.Register(api, huma.Operation{
		OperationID: "get-device",
		Method:      http.MethodGet,
		Path:        "/v2/devices/{id}",
		Summary:     "Get a device",
		Description: "Returns details of a specific device",
		Tags:        []string{tagDevices},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound},
	}, h.GetDevice)

	huma.Register(api, huma.Operation{
		OperationID: "delete-device",
		Method:      http.MethodDelete,
		Path:        "/v2/devices/{id}",
		Summary:     "Delete a device",
		Description: "Stops and removes a device",
		Tags:        []string{tagDevices},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound, http.StatusConflict},
	}, h.DeleteDevice)

	huma.Register(api, huma.Operation{
		OperationID: "stop-device",
		Method:      http.MethodPost,
		Path:        "/v2/devices/{id}/stop",
		Summary:     "Stop a device",
		Description: "Stops a running device",
		Tags:        []string{tagDevices},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound, http.StatusConflict, http.StatusBadGateway, http.StatusGatewayTimeout},
	}, h.StopDevice)

	huma.Register(api, huma.Operation{
		OperationID: "start-device",
		Method:      http.MethodPost,
		Path:        "/v2/devices/{id}/start",
		Summary:     "Start a device",
		Description: "Starts a stopped device",
		Tags:        []string{tagDevices},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound, http.StatusConflict, http.StatusBadGateway, http.StatusGatewayTimeout},
	}, h.StartDevice)

	// Restart, powerwash and powerbtn address one Android VM: the Host
	// Orchestrator serves no group-level counterpart for any of them, so there is
	// no device-wide spelling that could mean what it says. Exclusivity is still
	// per device, so a second one is refused while the first is live even on a
	// different instance.
	huma.Register(api, huma.Operation{
		OperationID: "restart-instance",
		Method:      http.MethodPost,
		Path:        "/v2/devices/{id}/instances/{name}/restart",
		Summary:     "Restart an instance",
		Description: "Starts relaunching one running Android VM in the device's CVD group in place (no artifact " +
			"re-fetch) and returns an operation handle to poll. 409 if another exclusive operation is already " +
			"running on the device, including one on a different instance.",
		Tags:     []string{tagDevices},
		Security: web.BearerAuth(),
		Errors:   []int{http.StatusNotFound, http.StatusConflict, http.StatusBadGateway, http.StatusGatewayTimeout},
	}, h.RestartDevice)

	huma.Register(api, huma.Operation{
		OperationID: "powerwash-instance",
		Method:      http.MethodPost,
		Path:        "/v2/devices/{id}/instances/{name}/powerwash",
		Summary:     "Powerwash an instance",
		Description: "Starts resetting one running Android VM in the device's CVD group to first-boot state " +
			"(userdata wiped, same build) and returns an operation handle to poll. 409 if another exclusive " +
			"operation is already running on the device, including one on a different instance.",
		Tags:     []string{tagDevices},
		Security: web.BearerAuth(),
		Errors:   []int{http.StatusNotFound, http.StatusConflict, http.StatusBadGateway, http.StatusGatewayTimeout},
	}, h.PowerwashDevice)

	huma.Register(api, huma.Operation{
		OperationID: "take-device-snapshot",
		Method:      http.MethodPost,
		Path:        "/v2/devices/{id}/snapshots",
		Summary:     "Snapshot a device",
		Description: "Starts capturing a running device's guest state (RAM plus disk overlays) onto its host's " +
			"disk and returns an operation handle to poll; the response carries the snapshot id. The device is " +
			"briefly suspended during the capture and keeps running afterwards. Snapshots are HOST-PINNED: they " +
			"do not survive host re-provisioning, spot preemption or host expiry, and restore onto the same " +
			"stopped device only. Requires software rendering (gpu_mode swiftshader or auto); 409 if the device " +
			"already holds its snapshot budget or another exclusive operation is running.",
		Tags:     []string{tagDevices},
		Security: web.BearerAuth(),
		Errors:   []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict},
	}, h.TakeDeviceSnapshot)

	huma.Register(api, huma.Operation{
		OperationID: "list-device-snapshots",
		Method:      http.MethodGet,
		Path:        "/v2/devices/{id}/snapshots",
		Summary:     "List a device's snapshots",
		Description: "Lists the device's snapshot inventory, newest first. State `lost` means the host's disk " +
			"was recreated after the snapshot was taken and the bytes are gone.",
		Tags:     []string{tagDevices},
		Security: web.BearerAuth(),
		Errors:   []int{http.StatusNotFound},
	}, h.ListDeviceSnapshots)

	huma.Register(api, huma.Operation{
		OperationID: "delete-device-snapshot",
		Method:      http.MethodDelete,
		Path:        "/v2/devices/{id}/snapshots/{snapshotId}",
		Summary:     "Delete a device snapshot",
		Description: "Starts removing a snapshot from the device's host (freeing its disk space) and deletes it " +
			"from the inventory, returning an operation handle to poll.",
		Tags:     []string{tagDevices},
		Security: web.BearerAuth(),
		Errors:   []int{http.StatusNotFound, http.StatusConflict},
	}, h.DeleteDeviceSnapshot)

	huma.Register(api, huma.Operation{
		OperationID: "restore-device-snapshot",
		Method:      http.MethodPost,
		Path:        "/v2/devices/{id}/restore",
		Summary:     "Restore a device from a snapshot",
		Description: "Starts a STOPPED device from one of its snapshots (the guest resumes exactly where the " +
			"snapshot was taken) and returns an operation handle to poll. 409 if the device is not stopped or " +
			"the snapshot is no longer restorable (still creating, lost, or outlived by a host re-provision).",
		Tags:     []string{tagDevices},
		Security: web.BearerAuth(),
		Errors:   []int{http.StatusNotFound, http.StatusConflict},
	}, h.RestoreDeviceSnapshot)

	huma.Register(api, huma.Operation{
		OperationID: "powerbtn-instance",
		Method:      http.MethodPost,
		Path:        "/v2/devices/{id}/instances/{name}/powerbtn",
		Summary:     "Press the power button",
		Description: "Presses the power button on one running Android VM in the device's CVD group",
		Tags:        []string{tagDevices},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound, http.StatusConflict, http.StatusBadGateway, http.StatusGatewayTimeout},
	}, h.PowerbtnDevice)

	huma.Register(api, huma.Operation{
		OperationID: "bugreport-device",
		Method:      http.MethodPost,
		Path:        "/v2/devices/{id}/bugreport",
		Summary:     "Capture a bugreport",
		Description: "Captures a bugreport on the device's host and stores it as a durable artifact in the configured GCS bucket. Works on errored and stopped devices too, because the host-side capture only needs the CVD to exist.",
		Tags:        []string{tagDevices},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusBadGateway, http.StatusGatewayTimeout},
	}, h.BugreportDevice)

	// A recording captures one Android VM's screen, so these address an instance.
	huma.Register(api, huma.Operation{
		OperationID: "start-instance-recording",
		Method:      http.MethodPost,
		Path:        "/v2/devices/{id}/instances/{name}/recordings/start",
		Summary:     "Start a screen recording",
		Description: "Starts a screen recording on one running Android VM in the device's CVD group. Recordings stay on the host until fetched.",
		Tags:        []string{tagDevices},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound, http.StatusConflict, http.StatusBadGateway, http.StatusGatewayTimeout},
	}, h.StartRecording)

	huma.Register(api, huma.Operation{
		OperationID: "stop-instance-recording",
		Method:      http.MethodPost,
		Path:        "/v2/devices/{id}/instances/{name}/recordings/stop",
		Summary:     "Stop a screen recording",
		Description: "Stops the screen recording on one running Android VM in the device's CVD group.",
		Tags:        []string{tagDevices},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound, http.StatusConflict, http.StatusBadGateway, http.StatusGatewayTimeout},
	}, h.StopRecording)

	huma.Register(api, huma.Operation{
		OperationID: "list-instance-recordings",
		Method:      http.MethodGet,
		Path:        "/v2/devices/{id}/instances/{name}/recordings",
		Summary:     "List screen recordings",
		Description: "Returns the names of one instance's screen recordings held on the device's host (host-local until fetched).",
		Tags:        []string{tagDevices},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound, http.StatusConflict, http.StatusBadGateway},
	}, h.ListRecordings)

	huma.Register(api, huma.Operation{
		OperationID: "fetch-instance-recording",
		Method:      http.MethodPost,
		Path:        "/v2/devices/{id}/instances/{name}/recordings/{recording}/fetch",
		Summary:     "Archive a screen recording",
		Description: "Streams a host-local screen recording into the configured GCS bucket and returns its location.",
		Tags:        []string{tagDevices},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusBadGateway},
	}, h.FetchRecording)

	// Lifecycle management endpoints
	huma.Register(api, huma.Operation{
		OperationID: "get-device-lifecycle",
		Method:      http.MethodGet,
		Path:        "/v2/devices/{id}/lifecycle",
		Summary:     "Get device lifecycle",
		Description: "Returns the lifecycle state (ephemeral/pinned, TTL, expiration) of a device",
		Tags:        []string{tagDevices},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound},
	}, h.GetLifecycle)

	huma.Register(api, huma.Operation{
		OperationID: "pin-device",
		Method:      http.MethodPost,
		Path:        "/v2/devices/{id}/pin",
		Summary:     "Pin a device",
		Description: "Marks a device as pinned to prevent automatic deletion by the reaper",
		Tags:        []string{tagDevices},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict},
	}, h.PinDevice)

	huma.Register(api, huma.Operation{
		OperationID: "unpin-device",
		Method:      http.MethodPost,
		Path:        "/v2/devices/{id}/unpin",
		Summary:     "Unpin a device",
		Description: "Removes the pinned status and restores ephemeral TTL-based expiration",
		Tags:        []string{tagDevices},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict},
	}, h.UnpinDevice)

	huma.Register(api, huma.Operation{
		OperationID: "extend-device",
		Method:      http.MethodPost,
		Path:        "/v2/devices/{id}/extend",
		Summary:     "Extend device TTL",
		Description: "Extends the time-to-live of an ephemeral device (max 3 extensions)",
		Tags:        []string{tagDevices},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound, http.StatusConflict},
	}, h.ExtendDevice)

	// Display management endpoints (proxied to the device's Host Orchestrator).
	// Displays belong to one Android VM, so these address an instance by name:
	// a device-scoped display list has no single answer once a group has more
	// than one member.
	huma.Register(api, huma.Operation{
		OperationID: "list-displays",
		Method:      http.MethodGet,
		Path:        "/v2/devices/{id}/instances/{name}/displays",
		Summary:     "List an instance's displays",
		Description: "Returns the active displays on one Android VM in the device's CVD group, plus the available display presets",
		Tags:        []string{tagDevices},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound, http.StatusBadGateway},
	}, h.ListDisplays)

	huma.Register(api, huma.Operation{
		OperationID:   "add-display",
		Method:        http.MethodPost,
		Path:          "/v2/devices/{id}/instances/{name}/displays",
		Summary:       "Add a display",
		Description:   "Adds a virtual display to one Android VM in the device's CVD group, either from explicit dimensions or a preset",
		Tags:          []string{tagDevices},
		Security:      web.BearerAuth(),
		DefaultStatus: http.StatusCreated,
		Errors:        []int{http.StatusBadRequest, http.StatusNotFound, http.StatusBadGateway},
	}, h.AddDisplay)

	huma.Register(api, huma.Operation{
		OperationID: "remove-display",
		Method:      http.MethodDelete,
		Path:        "/v2/devices/{id}/instances/{name}/displays/{displayNumber}",
		Summary:     "Remove a display",
		Description: "Removes a virtual display from one Android VM in the device's CVD group",
		Tags:        []string{tagDevices},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound, http.StatusBadGateway},
	}, h.RemoveDisplay)

	huma.Register(api, huma.Operation{
		OperationID: "get-display-screenshot",
		Method:      http.MethodGet,
		Path:        "/v2/devices/{id}/instances/{name}/displays/{displayNumber}/screenshot",
		Summary:     "Capture a display screenshot",
		Description: "Captures the current content of one display of one Android VM in the device's CVD group as an image",
		Tags:        []string{tagDevices},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound, http.StatusBadGateway, http.StatusGatewayTimeout},
	}, h.GetScreenshot)

	huma.Register(api, huma.Operation{
		OperationID: "get-ice-config",
		Method:      http.MethodGet,
		Path:        "/v2/devices/{id}/ice-config",
		Summary:     "Get the ICE configuration",
		Description: "Returns the ICE servers for each end of a WebRTC connection to this device, with any TURN credential scoped to the caller and device, plus the transport policy the client is held to",
		Tags:        []string{tagDevices},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusUnauthorized, http.StatusNotFound},
	}, h.GetICEConfig)
}

// ListDevices returns a list of devices (Huma handler).
func (h *DeviceHandler) ListDevices(ctx context.Context, input *ListDevicesInput) (*ListDevicesOutput, error) {
	cursor, err := web.DecodePageToken(input.PageToken, input.Offset)
	if err != nil {
		return nil, web.MapError(err)
	}
	// One row past the page says whether another page exists.
	filter := device.ListFilter{
		Limit:  input.Limit + 1,
		Offset: input.Offset,
	}
	if cursor != nil {
		filter.AfterCreatedAt, filter.AfterID = cursor.CreatedAt, cursor.ID
	}

	if input.HostID != "" {
		filter.HostID = &input.HostID
	}
	if input.Name != "" {
		filter.Name = &input.Name
	}
	if input.Status != "" {
		s := device.Status(input.Status)
		filter.Status = &s
	}
	if input.BuildID != "" {
		filter.BuildID = &input.BuildID
	}
	creator, err := auth.CreatorScope(ctx, input.CreatedBy, input.Mine)
	if err != nil {
		return nil, web.MapError(err)
	}
	if creator != "" {
		filter.CreatedBy = &creator
	}

	devices, err := h.service.List(ctx, filter)
	if err != nil {
		return nil, web.MapError(err)
	}

	total, err := h.service.Count(ctx, filter)
	if err != nil {
		return nil, web.MapError(err)
	}

	output := &ListDevicesOutput{}
	if len(devices) > input.Limit {
		devices = devices[:input.Limit]
		last := devices[len(devices)-1]
		output.Body.NextPageToken = web.EncodePageToken(web.PageCursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	output.Body.Items = devices
	output.Body.Limit = input.Limit
	output.Body.Offset = input.Offset
	output.Body.Total = int(total)
	return output, nil
}

// GetDevice returns a device by ID (Huma handler).
func (h *DeviceHandler) GetDevice(ctx context.Context, input *GetDeviceInput) (*GetDeviceOutput, error) {
	d, err := h.service.Get(ctx, input.ID)
	if err != nil {
		return nil, web.MapError(err)
	}

	return &GetDeviceOutput{Body: d}, nil
}

// modeAdvanced is the advanced creation mode (full CVD env config).
const modeAdvanced = "advanced"

// statusRunning is the response status of in-place device ops (restart,
// powerwash, powerbtn). The device row stays running throughout.
const statusRunning = "running"

// CreateDevice creates a new device (Huma handler).
func (h *DeviceHandler) CreateDevice(ctx context.Context, input *CreateDeviceInput) (*CreateDeviceOutput, error) {
	principal, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, huma.Error401Unauthorized("Authentication required")
	}

	req := buildCreateRequest(input)
	if err := validateAdvancedMode(input, &req); err != nil {
		return nil, err
	}

	// Simple mode has no other build source. With an empty build_id the CVD
	// config carries no default_build and `cvd load` fails opaquely (exit 255)
	// after retrying to exhaustion. Declarative creates resolve their own
	// build; advanced mode has its own guard above. Placed before the
	// validate_only branch so a plan fails the same way the real call would.
	if input.Body.Mode != modeAdvanced && input.Body.Requirements == nil && input.Body.BuildID == "" {
		return nil, huma.Error400BadRequest(
			"a build is required: set build_id, or use requirements to have the platform resolve one")
	}

	return h.dispatchCreate(ctx, input, req, principal.Email)
}

// buildCreateRequest maps the transport body onto the domain create request.
func buildCreateRequest(input *CreateDeviceInput) device.CreateRequest {
	req := device.CreateRequest{
		HostID:             input.Body.HostID,
		Name:               input.Body.Name,
		BuildID:            input.Body.BuildID,
		BuildTarget:        input.Body.BuildTarget,
		GPUMode:            device.GPUMode(input.Body.GPUMode),
		Mode:               input.Body.Mode,
		TTLHours:           input.Body.TTLHours,
		Pinned:             input.Body.Pinned,
		PinnedReason:       device.PinReason(input.Body.PinnedReason),
		PinnedReasonDetail: input.Body.PinnedReasonDetail,
		PinnedUntil:        input.Body.PinnedUntil,
		Tags:               input.Body.Tags,
		IdempotencyKey:     input.IdempotencyKey,
	}
	if r := input.Body.Requirements; r != nil {
		req.Requirements = &device.Requirements{
			Interaction:  device.Interaction(r.Interaction),
			GPURequired:  r.GPURequired,
			Pool:         r.Pool,
			Capacity:     device.CapacityModel(r.Capacity),
			Architecture: device.Architecture(r.Architecture),
		}
		if r.Build != nil {
			req.Requirements.Build.Registered = r.Build.Registered
			if r.Build.Track != nil {
				req.Requirements.Build.TrackBranch = r.Build.Track.Branch
				req.Requirements.Build.TrackTarget = r.Build.Track.Target
			}
		}
		if r.Location != nil {
			req.Requirements.Location = &device.LocationRequirement{
				PreferredRegion: r.Location.PreferredRegion,
			}
		}
	}
	return req
}

// validateAdvancedMode enforces the advanced-mode rules and fills
// req.EnvConfig with the filtered configuration. No-op for simple mode.
func validateAdvancedMode(input *CreateDeviceInput, req *device.CreateRequest) error {
	if input.Body.Mode != modeAdvanced {
		return nil
	}
	if input.Body.AdvancedConfig == nil {
		return huma.Error400BadRequest("advanced_config is required when mode is 'advanced'")
	}

	if err := validation.ValidateEnvConfig(input.Body.AdvancedConfig.EnvConfig); err != nil {
		return huma.Error400BadRequest("env_config validation failed: " + err.Error())
	}

	// Filter blocked fields (defense in depth).
	filteredConfig := validation.FilterBlockedFields(input.Body.AdvancedConfig.EnvConfig)
	req.EnvConfig = filteredConfig

	// Advanced mode strips build disk fields on the client (the backend
	// merges the selected build), so a resolvable build is required.
	// Otherwise `cvd load` fails opaquely on a missing default_build.
	// A declarative create resolves its own build (resolveBuild fills
	// req.BuildID before the commit), so requirements satisfies this.
	// Without a device-level build each instance must name its own: one
	// instance's build does not fill another's disk.
	if input.Body.Requirements == nil && input.Body.BuildID == "" &&
		!validation.EveryInstanceHasBuild(filteredConfig) {
		return huma.Error400BadRequest(
			"a build is required for advanced mode: select a build, or give every instance " +
				"disk.default_build or " + validation.BuildKey)
	}

	slog.Info("CreateDevice advanced mode request",
		slog.String("host_id", input.Body.HostID),
		slog.String("name", input.Body.Name),
		slog.Int("instance_count", validation.CountInstances(filteredConfig)),
	)
	return nil
}

// dispatchCreate routes the admitted request to the service: a validate_only
// plan, a declarative create (requirements set), or an imperative create.
func (h *DeviceHandler) dispatchCreate(ctx context.Context, input *CreateDeviceInput, req device.CreateRequest, createdBy string) (*CreateDeviceOutput, error) {
	if input.Body.ValidateOnly {
		d, res, err := h.service.PlanCreate(ctx, req)
		if err != nil {
			return nil, web.MapError(err)
		}
		return createOutput(d, res), nil
	}

	if req.Requirements != nil {
		d, res, err := h.service.CreateDeclarative(ctx, req, createdBy)
		if err != nil {
			return nil, web.MapError(err)
		}
		return createOutput(d, res), nil
	}

	d, err := h.service.Create(ctx, req, createdBy)
	if err != nil {
		return nil, web.MapError(err)
	}
	return createOutput(d, nil), nil
}

// createOutput assembles the create response body: the (would-be) device with
// the resolution when one exists.
func createOutput(d *device.Device, res *device.Resolution) *CreateDeviceOutput {
	out := &CreateDeviceOutput{}
	out.Body.Device = *d
	out.Body.Resolution = res
	return out
}

// DeleteDevice removes a device (Huma handler).
func (h *DeviceHandler) DeleteDevice(ctx context.Context, input *DeleteDeviceInput) (*DeleteDeviceOutput, error) {
	if err := h.service.Delete(ctx, input.ID); err != nil {
		return nil, web.MapError(err)
	}
	return &DeleteDeviceOutput{}, nil
}

// StopDevice stops a running device (Huma handler).
func (h *DeviceHandler) StopDevice(ctx context.Context, input *StopDeviceInput) (*StopDeviceOutput, error) {
	if err := h.service.Stop(ctx, input.ID); err != nil {
		return nil, web.MapError(err)
	}
	output := &StopDeviceOutput{}
	output.Body.Status = "stopping"
	return output, nil
}

// StartDevice starts a stopped device (Huma handler).
func (h *DeviceHandler) StartDevice(ctx context.Context, input *StartDeviceInput) (*StartDeviceOutput, error) {
	if err := h.service.Start(ctx, input.ID); err != nil {
		return nil, web.MapError(err)
	}
	output := &StartDeviceOutput{}
	output.Body.Status = "starting"
	return output, nil
}

// RestartDevice relaunches a running device's CVD in place (Huma handler).
//
// It returns an operation handle. The old response was {"status":"running"},
// a constant, since the row is identical before and after, so it said nothing
// about whether the restart happened. The status is 200 and not 202 because
// the Operation is itself the response, which is what every
// long-running-operation REST method returns.
func (h *DeviceHandler) RestartDevice(ctx context.Context, input *RestartInstanceInput) (*VerbOperationOutput, error) {
	return h.admitVerb(ctx, func(ctx context.Context, id string) (*operation.Record, error) {
		return h.service.Restart(ctx, id, input.Instance)
	}, input.ID)
}

// PowerwashDevice resets one instance to first-boot state (Huma handler).
func (h *DeviceHandler) PowerwashDevice(ctx context.Context, input *PowerwashInstanceInput) (*VerbOperationOutput, error) {
	return h.admitVerb(ctx, func(ctx context.Context, id string) (*operation.Record, error) {
		return h.service.Powerwash(ctx, id, input.Instance)
	}, input.ID)
}

// admitVerb renders an admitted verb's handle, or maps the refusal.
func (h *DeviceHandler) admitVerb(ctx context.Context,
	admit func(context.Context, string) (*operation.Record, error), id string,
) (*VerbOperationOutput, error) {
	rec, err := admit(ctx, id)
	if err != nil {
		return nil, web.MapError(err)
	}
	return &VerbOperationOutput{Body: operation.FromRecord(rec, operation.Progress{})}, nil
}

// TakeDeviceSnapshot starts capturing a snapshot of a running device (Huma
// handler). The operation's response carries the snapshot id.
func (h *DeviceHandler) TakeDeviceSnapshot(ctx context.Context, input *TakeSnapshotInput) (*VerbOperationOutput, error) {
	return h.admitVerb(ctx, h.service.TakeSnapshot, input.ID)
}

// ListDeviceSnapshots lists a device's snapshot inventory (Huma handler).
func (h *DeviceHandler) ListDeviceSnapshots(ctx context.Context, input *ListSnapshotsInput) (*ListSnapshotsOutput, error) {
	snaps, err := h.service.ListSnapshots(ctx, input.ID)
	if err != nil {
		return nil, web.MapError(err)
	}
	output := &ListSnapshotsOutput{}
	// An empty inventory is [] and never null. The console's schema reads an array.
	output.Body.Snapshots = snaps
	if output.Body.Snapshots == nil {
		output.Body.Snapshots = []*device.Snapshot{}
	}
	return output, nil
}

// DeleteDeviceSnapshot starts removing a device's snapshot (Huma handler).
func (h *DeviceHandler) DeleteDeviceSnapshot(ctx context.Context, input *DeleteSnapshotInput) (*VerbOperationOutput, error) {
	return h.admitVerb(ctx, func(ctx context.Context, id string) (*operation.Record, error) {
		return h.service.DeleteSnapshot(ctx, id, input.SnapshotID)
	}, input.ID)
}

// RestoreDeviceSnapshot starts a stopped device from one of its snapshots
// (Huma handler).
func (h *DeviceHandler) RestoreDeviceSnapshot(ctx context.Context, input *RestoreSnapshotInput) (*VerbOperationOutput, error) {
	return h.admitVerb(ctx, func(ctx context.Context, id string) (*operation.Record, error) {
		return h.service.RestoreSnapshot(ctx, id, input.Body.SnapshotID)
	}, input.ID)
}

// PowerbtnDevice presses the power button on one running instance (Huma handler).
func (h *DeviceHandler) PowerbtnDevice(ctx context.Context, input *PowerbtnInstanceInput) (*PowerbtnDeviceOutput, error) {
	if err := h.service.Powerbtn(ctx, input.ID, input.Instance); err != nil {
		return nil, web.MapError(err)
	}
	output := &PowerbtnDeviceOutput{}
	output.Body.Status = statusRunning
	return output, nil
}

// BugreportDevice captures a bugreport to artifact storage (Huma handler).
func (h *DeviceHandler) BugreportDevice(ctx context.Context, input *BugreportDeviceInput) (*BugreportDeviceOutput, error) {
	artifact, err := h.service.Bugreport(ctx, input.ID, input.Body.IncludeADB)
	if err != nil {
		return nil, web.MapError(err)
	}
	output := &BugreportDeviceOutput{}
	output.Body.Bucket = artifact.Bucket
	output.Body.Object = artifact.Object
	output.Body.GSURI = artifact.GSURI()
	output.Body.DownloadPath = "/v2/storage/buckets/" + artifact.Bucket + "/objects/" + artifact.Object
	return output, nil
}

// StartRecording starts a screen recording on one instance (Huma handler).
func (h *DeviceHandler) StartRecording(ctx context.Context, input *StartRecordingInput) (*StartRecordingOutput, error) {
	if err := h.service.StartRecording(ctx, input.ID, input.Instance); err != nil {
		return nil, web.MapError(err)
	}
	output := &StartRecordingOutput{}
	output.Body.Status = statusRunning
	return output, nil
}

// StopRecording stops a screen recording on one instance (Huma handler).
func (h *DeviceHandler) StopRecording(ctx context.Context, input *StopRecordingInput) (*StopRecordingOutput, error) {
	if err := h.service.StopRecording(ctx, input.ID, input.Instance); err != nil {
		return nil, web.MapError(err)
	}
	output := &StopRecordingOutput{}
	output.Body.Status = statusRunning
	return output, nil
}

// ListRecordings lists one instance's host-local screen recordings (Huma handler).
func (h *DeviceHandler) ListRecordings(ctx context.Context, input *ListRecordingsInput) (*ListRecordingsOutput, error) {
	recordings, err := h.service.ListRecordings(ctx, input.ID, input.Instance)
	if err != nil {
		return nil, web.MapError(err)
	}
	output := &ListRecordingsOutput{}
	output.Body.Recordings = recordings
	return output, nil
}

// FetchRecording archives a screen recording to artifact storage (Huma handler).
func (h *DeviceHandler) FetchRecording(ctx context.Context, input *FetchRecordingInput) (*FetchRecordingOutput, error) {
	artifact, err := h.service.FetchRecording(ctx, input.ID, input.Instance, input.Name)
	if err != nil {
		return nil, web.MapError(err)
	}
	output := &FetchRecordingOutput{}
	output.Body.Bucket = artifact.Bucket
	output.Body.Object = artifact.Object
	output.Body.GSURI = artifact.GSURI()
	output.Body.DownloadPath = "/v2/storage/buckets/" + artifact.Bucket + "/objects/" + artifact.Object
	return output, nil
}

// GetLifecycle returns the lifecycle state of a device (Huma handler).
func (h *DeviceHandler) GetLifecycle(ctx context.Context, input *GetLifecycleInput) (*GetLifecycleOutput, error) {
	lifecycle, err := h.service.GetLifecycle(ctx, input.ID)
	if err != nil {
		return nil, web.MapError(err)
	}

	return &GetLifecycleOutput{Body: lifecycle}, nil
}

// PinDevice marks a device as pinned to prevent automatic deletion (Huma handler).
func (h *DeviceHandler) PinDevice(ctx context.Context, input *PinDeviceInput) (*PinDeviceOutput, error) {
	principal, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, huma.Error401Unauthorized("Authentication required")
	}

	req := device.PinRequest{
		Reason:       device.PinReason(input.Body.Reason),
		ReasonDetail: input.Body.ReasonDetail,
		PinnedUntil:  input.Body.PinnedUntil,
	}

	if err := req.Validate(); err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}

	d, err := h.service.Pin(ctx, input.ID, req, principal.Email)
	if err != nil {
		return nil, web.MapError(err)
	}

	return &PinDeviceOutput{Body: d}, nil
}

// UnpinDevice removes the pinned status from a device (Huma handler).
func (h *DeviceHandler) UnpinDevice(ctx context.Context, input *UnpinDeviceInput) (*UnpinDeviceOutput, error) {
	principal, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, huma.Error401Unauthorized("Authentication required")
	}

	d, err := h.service.Unpin(ctx, input.ID, principal.Email)
	if err != nil {
		return nil, web.MapError(err)
	}

	return &UnpinDeviceOutput{Body: d}, nil
}

// ExtendDevice extends the TTL of an ephemeral device (Huma handler).
func (h *DeviceHandler) ExtendDevice(ctx context.Context, input *ExtendDeviceInput) (*ExtendDeviceOutput, error) {
	req := device.ExtendRequest{
		Seconds: input.Body.Seconds,
	}

	d, err := h.service.Extend(ctx, input.ID, req)
	if err != nil {
		return nil, web.MapError(err)
	}

	return &ExtendDeviceOutput{Body: d}, nil
}
