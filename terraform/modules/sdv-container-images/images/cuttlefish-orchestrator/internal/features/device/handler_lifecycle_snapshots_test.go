// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package devicefeature

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

func newLifecycleAndSnapshotHandlerAPI(t *testing.T) (*testAPI, *atomicDeviceRepo, *fakeAdmitter) {
	t.Helper()
	hostRepo := hosttest.NewFakeRepo()
	deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
	admitter := &fakeAdmitter{}
	svc, err := NewDeviceService(deviceRepo, hostRepo, newMockBuildRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
		WithTaskEnqueuer(&fakeEnqueuer{}),
		WithOperations(admitter))
	require.NoError(t, err)

	api := newTestAPI(t)
	NewDeviceHandler(svc, nil, nil, nil).Register(api.API)
	return api, deviceRepo, admitter
}

func TestDeviceHandler_PinUnpinExtendLifecycleAndSnapshots(t *testing.T) {
	t.Parallel()

	const (
		devID  = "123e4567-e89b-12d3-a456-426614174000"
		snapID = "223e4567-e89b-12d3-a456-426614174001"
		hostID = "323e4567-e89b-12d3-a456-426614174002"
	)

	api, repo, admitter := newLifecycleAndSnapshotHandlerAPI(t)
	repo.Devices[devID] = &device.Device{
		ID:        devID,
		Name:      "lifecycle-dev",
		HostID:    hostID,
		CVDID:     "cvd-1/lifecycle-dev",
		Status:    device.StatusStopped,
		Lifecycle: device.NewEphemeralLifecycle(14400),
	}
	repo.snapshots[snapID] = &device.Snapshot{
		ID:              snapID,
		DeviceID:        devID,
		HostID:          hostID,
		CVDGroup:        "cvd-1",
		CVDName:         "lifecycle-dev",
		HostIncarnation: 1,
		State:           device.SnapshotStateReady,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}

	// PinDevice (200) + persisted fields
	pinUntil := time.Now().UTC().Add(12 * time.Hour).Truncate(time.Second)
	pinResp := api.Post("/v2/devices/"+devID+"/pin", map[string]any{
		"reason":       "long_running_ci",
		"pinned_until": pinUntil.Format(time.RFC3339),
	})
	require.Equal(t, http.StatusOK, pinResp.Code, "pin body: %s", pinResp.Body.String())
	assert.True(t, repo.Devices[devID].Lifecycle.IsPinned())
	require.NotNil(t, repo.Devices[devID].Lifecycle.PinnedReason)
	assert.Equal(t, device.PinReason("long_running_ci"), *repo.Devices[devID].Lifecycle.PinnedReason)

	// GetLifecycle while pinned (200)
	lifeResp := api.Get("/v2/devices/" + devID + "/lifecycle")
	require.Equal(t, http.StatusOK, lifeResp.Code, "lifecycle body: %s", lifeResp.Body.String())
	var lc map[string]any
	require.NoError(t, json.Unmarshal(lifeResp.Body.Bytes(), &lc))
	assert.Equal(t, string(device.LifecycleModePinned), lc["mode"])

	// UnpinDevice (200)
	unpinResp := api.Post("/v2/devices/"+devID+"/unpin", map[string]any{})
	require.Equal(t, http.StatusOK, unpinResp.Code, "unpin body: %s", unpinResp.Body.String())
	assert.False(t, repo.Devices[devID].Lifecycle.IsPinned())
	require.NotNil(t, repo.Devices[devID].Lifecycle.ExpiresAt)

	// ExtendDevice (200)
	prevExp := *repo.Devices[devID].Lifecycle.ExpiresAt
	extendResp := api.Post("/v2/devices/"+devID+"/extend", map[string]any{
		"seconds": 3600,
	})
	require.Equal(t, http.StatusOK, extendResp.Code, "extend body: %s", extendResp.Body.String())
	require.NotNil(t, repo.Devices[devID].Lifecycle.ExpiresAt)
	assert.True(t, repo.Devices[devID].Lifecycle.ExpiresAt.After(prevExp))

	// ListDeviceSnapshots (200)
	listResp := api.Get("/v2/devices/" + devID + "/snapshots")
	require.Equal(t, http.StatusOK, listResp.Code, "list snapshots body: %s", listResp.Body.String())
	assert.Contains(t, listResp.Body.String(), snapID)

	// DeleteDeviceSnapshot (200 / 202 with admitted operation)
	delResp := api.Delete("/v2/devices/" + devID + "/snapshots/" + snapID)
	require.True(t, delResp.Code == http.StatusOK || delResp.Code == http.StatusAccepted, "delete status=%d body=%s", delResp.Code, delResp.Body.String())
	require.NotEmpty(t, admitter.admitted)

	// Missing device returns 404 across lifecycle and snapshots routes.
	missingID := "999e4567-e89b-12d3-a456-426614174999"
	assert.Equal(t, http.StatusNotFound, api.Get("/v2/devices/"+missingID+"/lifecycle").Code)
	assert.Equal(t, http.StatusNotFound, api.Post("/v2/devices/"+missingID+"/pin", map[string]any{"reason": "other", "reason_detail": "missing"}).Code)
	assert.Equal(t, http.StatusNotFound, api.Post("/v2/devices/"+missingID+"/unpin", map[string]any{}).Code)
	assert.Equal(t, http.StatusNotFound, api.Post("/v2/devices/"+missingID+"/extend", map[string]any{"seconds": 60}).Code)
	assert.Equal(t, http.StatusNotFound, api.Get("/v2/devices/"+missingID+"/snapshots").Code)
}

func TestCaptureBugreportHandler(t *testing.T) {
	t.Parallel()

	svc, upload := captureSetup(t)
	h := NewCaptureBugreportHandler(svc)
	assert.Equal(t, TaskTypeCaptureBugreport, h.TaskType())

	err := h.Handle(t.Context(), &task.Task{
		ID:         "task-cap-1",
		Type:       TaskTypeCaptureBugreport,
		ResourceID: "d1",
	})
	require.NoError(t, err)
	assert.True(t, captured(t, svc, "d1"), "device row must be marked captured")
	assert.NotEmpty(t, upload.object, "expected bugreport artifact uploaded")
	assert.Equal(t, "application/zip", upload.contentType)

	// Unknown/deleted device is a safe idempotent no-op for the task handler (no upload).
	upload.object = ""
	err = h.Handle(t.Context(), &task.Task{
		ID:         "task-cap-missing",
		Type:       TaskTypeCaptureBugreport,
		ResourceID: "does-not-exist",
	})
	assert.NoError(t, err)
	assert.Empty(t, upload.object)
}

func TestExtractMessageType(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  []byte
		want string
	}{
		{name: "json_type_offer", raw: []byte(`{"type":"offer","sdp":"v=0"}`), want: "offer"},
		{name: "missing_type", raw: []byte(`{"sdp":"v=0"}`), want: "unknown"},
		{name: "empty_json", raw: []byte(`{}`), want: "unknown"},
		{name: "invalid_json", raw: []byte(`not json`), want: "unknown"},
		{name: "empty_bytes", raw: nil, want: "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, extractMessageType(tc.raw))
		})
	}
}
