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
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/domain/idempotency"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/idempotencytest"
)

func newServiceForCreate(t *testing.T, deviceRepo *atomicDeviceRepo, hostRepo *hosttest.FakeRepo, enq TaskEnqueuer) *DeviceService {
	t.Helper()
	opts := []DeviceServiceOption{}
	if enq != nil {
		opts = append(opts, WithTaskEnqueuer(enq))
	}
	svc, err := NewDeviceService(deviceRepo, hostRepo, newMockBuildRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(), opts...)
	require.NoError(t, err)
	return svc
}

func runningHostRepo() *hosttest.FakeRepo {
	hr := hosttest.NewFakeRepo()
	hr.Hosts["test-host"] = &host.Host{
		ID:                   "test-host",
		Status:               host.StatusRunning,
		MaxDevices:           4,
		IPAddress:            "10.0.0.1",
		HostOrchestratorPort: 2080,
	}
	return hr
}

func TestCreateEnqueuesCreateTask(t *testing.T) {
	t.Parallel()
	hostRepo := runningHostRepo()
	deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
	enq := &fakeEnqueuer{}
	svc := newServiceForCreate(t, deviceRepo, hostRepo, enq)

	d, err := svc.Create(t.Context(), device.CreateRequest{
		HostID:    "test-host",
		Name:      "dev-1",
		Mode:      "advanced",
		EnvConfig: map[string]any{"instances": []any{map[string]any{}}},
	}, "user@example.com")
	require.NoError(t, err)

	require.Len(t, enq.enqueued, 1, "Create enqueues exactly one task")
	tk := enq.enqueued[0]
	assert.Equal(t, TaskTypeCreateCVD, tk.Type)
	assert.Equal(t, d.ID, tk.ResourceID)

	// The task carries only the device ID; the create-time spec rides the
	// device row (desired_spec) and the actuator re-derives it from there.
	assert.Equal(t, "advanced", d.Mode)
	assert.Equal(t, 1, d.InstanceCount)
	assert.NotNil(t, d.EnvConfig, "env_config persists on the row, not the task")
}

func TestCreateRequiresTaskEnqueuer(t *testing.T) {
	t.Parallel()
	hostRepo := runningHostRepo()
	deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
	svc := newServiceForCreate(t, deviceRepo, hostRepo, nil) // no enqueuer

	_, err := svc.Create(t.Context(), device.CreateRequest{
		HostID: "test-host", Name: "dev-1",
	}, "user@example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "enqueuer")
}

func TestDeleteEnqueuesCVDCleanup(t *testing.T) {
	t.Parallel()
	hostRepo := runningHostRepo()
	deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
	enq := &fakeEnqueuer{}
	svc := newServiceForCreate(t, deviceRepo, hostRepo, enq)

	deviceRepo.Devices["d1"] = &device.Device{ID: "d1", HostID: "test-host", Status: device.StatusRunning, CVDID: "cvd/1"}
	require.NoError(t, svc.Delete(t.Context(), "d1"))

	require.Len(t, enq.enqueued, 1, "Delete enqueues one durable CVD-cleanup task")
	assert.Equal(t, TaskTypeCleanupCVD, enq.enqueued[0].Type)
	assert.Equal(t, "d1", enq.enqueued[0].ResourceID)

	// A device without a recorded CVDID still gets a cleanup task. CVDID is
	// only written on full creation success, so an interrupted create can
	// leave an orphaned group that only the deterministic name can find.
	deviceRepo.Devices["d2"] = &device.Device{ID: "d2", HostID: "test-host", Status: device.StatusError}
	require.NoError(t, svc.Delete(t.Context(), "d2"))
	require.Len(t, enq.enqueued, 2, "cleanup enqueued even without a CVDID")
	assert.Equal(t, "d2", enq.enqueued[1].ResourceID)
}

func newIdempotentService(t *testing.T, deviceRepo *atomicDeviceRepo, hostRepo *hosttest.FakeRepo, enq TaskEnqueuer, idem IdempotencyStore) *DeviceService {
	t.Helper()
	svc, err := NewDeviceService(deviceRepo, hostRepo, newMockBuildRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
		WithTaskEnqueuer(enq), WithIdempotencyStore(idem))
	require.NoError(t, err)
	return svc
}

func TestCreateIdempotentRetryReturnsSameDevice(t *testing.T) {
	t.Parallel()
	hostRepo := runningHostRepo()
	deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
	enq := &fakeEnqueuer{}
	svc := newIdempotentService(t, deviceRepo, hostRepo, enq, idempotencytest.NewFakeStore())

	req := device.CreateRequest{HostID: "test-host", Name: "dev-1", IdempotencyKey: "k1"}
	d1, err := svc.Create(t.Context(), req, "user@example.com")
	require.NoError(t, err)
	d2, err := svc.Create(t.Context(), req, "user@example.com")
	require.NoError(t, err)

	assert.Equal(t, d1.ID, d2.ID, "retry with the same key returns the original device")
	assert.Len(t, deviceRepo.Devices, 1, "no second device created")
	assert.Len(t, enq.enqueued, 1, "no second task enqueued")
}

func TestCreateIdempotencyKeyConflict(t *testing.T) {
	t.Parallel()
	hostRepo := runningHostRepo()
	deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
	svc := newIdempotentService(t, deviceRepo, hostRepo, &fakeEnqueuer{}, idempotencytest.NewFakeStore())

	_, err := svc.Create(t.Context(),
		device.CreateRequest{HostID: "test-host", Name: "dev-a", IdempotencyKey: "k1"}, "u")
	require.NoError(t, err)

	// Same key, different request -> conflict.
	_, err = svc.Create(t.Context(),
		device.CreateRequest{HostID: "test-host", Name: "dev-b", IdempotencyKey: "k1"}, "u")
	require.ErrorIs(t, err, idempotency.ErrConflict)
}

// TestCreateRaceRecovery covers the lost-commit-race path. The key lookup
// misses, the create commit fails because a concurrent request with the same
// key won the insert, and the recovery lookup resolves the key.
func TestCreateRaceRecovery(t *testing.T) {
	t.Parallel()
	winnerReq := device.CreateRequest{HostID: "test-host", Name: "dev-1", IdempotencyKey: "k1"}
	fp, err := idempotency.Fingerprint(winnerReq)
	require.NoError(t, err)

	seed := func(t *testing.T) *atomicDeviceRepo {
		t.Helper()
		deviceRepo := newDeviceRepoFakeWithHosts(runningHostRepo())
		deviceRepo.Devices["winner"] = &device.Device{
			ID: "winner", HostID: "test-host", Name: "dev-1", Status: device.StatusRunning,
		}
		deviceRepo.CreateErr = errors.New("spanner: row already exists")
		return deviceRepo
	}

	// An equivalent request that lost the race gets the winner's device.
	deviceRepo := seed(t)
	svc := newIdempotentService(t, deviceRepo, runningHostRepo(), &fakeEnqueuer{},
		idempotencytest.NewRaceStore("winner", fp))
	got, err := svc.Create(t.Context(), winnerReq, "u")
	require.NoError(t, err)
	assert.Equal(t, "winner", got.ID, "lost race returns the winner's device")

	// A different request reusing the key gets the sequential path's
	// conflict, never the winner's device.
	svc = newIdempotentService(t, seed(t), runningHostRepo(), &fakeEnqueuer{},
		idempotencytest.NewRaceStore("winner", fp))
	_, err = svc.Create(t.Context(),
		device.CreateRequest{HostID: "test-host", Name: "dev-other", IdempotencyKey: "k1"}, "u")
	require.ErrorIs(t, err, idempotency.ErrConflict)
}

func TestCreateIgnoresEmptyIdempotencyKey(t *testing.T) {
	t.Parallel()
	hostRepo := runningHostRepo()
	deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
	svc := newIdempotentService(t, deviceRepo, hostRepo, &fakeEnqueuer{}, idempotencytest.NewFakeStore())

	req := device.CreateRequest{HostID: "test-host", Name: "dev-1"} // no key
	d1, err := svc.Create(t.Context(), req, "u")
	require.NoError(t, err)
	d2, err := svc.Create(t.Context(), req, "u")
	require.NoError(t, err)
	assert.NotEqual(t, d1.ID, d2.ID, "without a key, each create is independent")
	assert.Len(t, deviceRepo.Devices, 2)
}

// The transport pattern admits a name that parses as a UUID, so each service
// entry point must refuse it before anything is placed or stored. Batch members
// reach the store through Create, so the imperative and declarative cases cover
// them.
func TestCreateRejectsIDShapedName(t *testing.T) {
	t.Parallel()
	const idShaped = "abcdef01-2345-6789-abcd-ef0123456789"

	t.Run("imperative", func(t *testing.T) {
		t.Parallel()
		hostRepo := runningHostRepo()
		deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
		enq := &fakeEnqueuer{}
		svc := newServiceForCreate(t, deviceRepo, hostRepo, enq)

		_, err := svc.Create(t.Context(), device.CreateRequest{HostID: "test-host", Name: idShaped}, "u@example.com")
		var verr *device.ValidationError
		require.ErrorAs(t, err, &verr)
		assert.Contains(t, verr.Message, "UUID")
		assert.Empty(t, deviceRepo.Devices, "no device row is stored")
		assert.Empty(t, enq.enqueued, "no create task is enqueued")
	})

	t.Run("hex form", func(t *testing.T) {
		t.Parallel()
		hostRepo := runningHostRepo()
		deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
		svc := newServiceForCreate(t, deviceRepo, hostRepo, &fakeEnqueuer{})

		_, err := svc.Create(t.Context(), device.CreateRequest{HostID: "test-host", Name: "abcdef0123456789abcdef0123456789"}, "u@example.com")
		var verr *device.ValidationError
		require.ErrorAs(t, err, &verr)
		assert.Empty(t, deviceRepo.Devices)
	})

	t.Run("declarative", func(t *testing.T) {
		t.Parallel()
		f := newDeclarativeFixture(t)
		req := declarativeReq(device.InteractionHeadless)
		req.Name = idShaped
		var verr *device.ValidationError

		_, _, err := f.svc.CreateDeclarative(t.Context(), req, "u@example.com")
		require.ErrorAs(t, err, &verr)
		_, err = f.svc.Create(t.Context(), req, "u@example.com")
		require.ErrorAs(t, err, &verr)
		assert.Empty(t, f.provisioner.specs, "no host is provisioned for a refused name")
		assert.Empty(t, f.deviceRepo.Devices)
	})

	t.Run("validate_only", func(t *testing.T) {
		t.Parallel()
		f := newDeclarativeFixture(t)
		req := declarativeReq(device.InteractionHeadless)
		req.Name = idShaped
		var verr *device.ValidationError

		_, _, err := f.svc.PlanCreate(t.Context(), req)
		require.ErrorAs(t, err, &verr, "a declarative plan refuses the name the real call would")

		hostRepo := runningHostRepo()
		svc := newServiceForCreate(t, newDeviceRepoFakeWithHosts(hostRepo), hostRepo, &fakeEnqueuer{})
		_, _, err = svc.PlanCreate(t.Context(), device.CreateRequest{HostID: "test-host", Name: idShaped})
		require.ErrorAs(t, err, &verr, "an imperative plan refuses it too")
	})
}
