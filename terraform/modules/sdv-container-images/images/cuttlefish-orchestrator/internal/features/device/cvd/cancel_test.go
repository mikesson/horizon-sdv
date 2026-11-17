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

package cvd

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/device/devicetest"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// syncDeviceRepo makes the in-memory fake safe for the cooperative-cancel
// watcher, which reads the device row from a goroutine while a test mutates it.
// The shared fake hands out live pointers and is documented as unsynchronized,
// so Get returns a snapshot COPY under a lock and the mutators lock too. That
// is also closer to the real Spanner repo, which deserializes a fresh struct
// per read.
type syncDeviceRepo struct {
	*devicetest.FakeRepo
	mu sync.Mutex
}

func newSyncDeviceRepo() *syncDeviceRepo {
	return &syncDeviceRepo{FakeRepo: devicetest.NewFakeRepo()}
}

func (r *syncDeviceRepo) Get(_ context.Context, id string) (*device.Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.Devices[id]
	if !ok {
		return nil, device.ErrNotFound
	}
	cp := *d
	return &cp, nil
}

// The boot-watch sampler mutates the row from its own goroutine (observation
// writes, the verdict settle, loop attribution) while the row-poll reads it,
// so every repo path the sampler drives takes the same lock Get does.
func (r *syncDeviceRepo) Mutate(ctx context.Context, id string, fn func(*device.Device) error) (*device.Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.FakeRepo.Mutate(ctx, id, fn)
}

func (r *syncDeviceRepo) RecordBootObservation(ctx context.Context, hostID, deviceID, cvdStatus string, obs *device.BootObservation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.FakeRepo.RecordBootObservation(ctx, hostID, deviceID, cvdStatus, obs)
}

func (r *syncDeviceRepo) UpdateStatus(ctx context.Context, id string, status device.Status, errorMsg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.FakeRepo.UpdateStatus(ctx, id, status, errorMsg)
}

func (r *syncDeviceRepo) ListByHost(ctx context.Context, hostID string) ([]*device.Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.FakeRepo.ListByHost(ctx, hostID)
}

func (r *syncDeviceRepo) put(d *device.Device) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Devices[d.ID] = d
}

func (r *syncDeviceRepo) setStatus(id string, status device.Status) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Devices[id].Status = status
}

func (r *syncDeviceRepo) getStatus(id string) device.Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Devices[id].Status
}

func (r *syncDeviceRepo) remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.Devices, id)
}

func TestWatchCreateRow_DetectsStandDowns(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		flip func(r *syncDeviceRepo)
		want standDown
	}{
		{"deleting", func(r *syncDeviceRepo) { r.setStatus("d", device.StatusDeleting) }, standDownDeleted},
		{"deleted", func(r *syncDeviceRepo) { r.setStatus("d", device.StatusDeleted) }, standDownDeleted},
		{"row gone", func(r *syncDeviceRepo) { r.remove("d") }, standDownDeleted},
		// The boot verdict's settle, or any other terminal error writer,
		// ends the create's ownership the same way.
		{"error settle", func(r *syncDeviceRepo) { r.setStatus("d", device.StatusError) }, standDownSettled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := newSyncDeviceRepo()
			repo.put(&device.Device{ID: "d", Status: device.StatusCreating})
			act := &Actuator{devices: repo, logger: slog.Default(), cancelPoll: time.Millisecond}

			done := make(chan standDown, 1)
			go func() { done <- act.watchCreateRow(t.Context(), "d") }()

			tc.flip(repo)
			select {
			case got := <-done:
				assert.Equal(t, tc.want, got, "watcher must report the stand-down reason")
			case <-time.After(2 * time.Second):
				t.Fatal("watcher did not detect the row settle")
			}
		})
	}
}

func TestWatchCreateRow_StopsOnContextCancel(t *testing.T) {
	t.Parallel()
	repo := newSyncDeviceRepo()
	repo.put(&device.Device{ID: "d", Status: device.StatusCreating})
	act := &Actuator{devices: repo, logger: slog.Default(), cancelPoll: 50 * time.Millisecond}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan standDown, 1)
	go func() { done <- act.watchCreateRow(ctx, "d") }()

	cancel() // a live create, not a settle: the watcher must exit reporting none
	select {
	case got := <-done:
		assert.Equal(t, standDownNone, got, "a context cancel is not a row settle")
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not stop on context cancel")
	}
}

// A device deleted while its CVD launch is still running must abandon the wait
// and complete the create task cleanly (return nil): the device's cleanup task,
// enqueued in the delete's transaction, owns teardown. Deleting the in-flight
// group here would leak the launcher's processes, so the create
// must NOT actuate on it.
func TestRunCreateCVD_DeletedMidCreate_BailsCleanly(t *testing.T) {
	t.Parallel()

	waitStarted := make(chan struct{})
	var once sync.Once
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/cvds" && r.Method == http.MethodGet:
			w.Write([]byte(`{"cvds": []}`)) // no prior group: adopt guard passes
		case r.URL.Path == "/cvds" && r.Method == http.MethodPost:
			w.Write([]byte(`{"name": "op-create"}`)) // launch started
		case strings.HasSuffix(r.URL.Path, "/:wait"):
			once.Do(func() { close(waitStarted) })
			<-r.Context().Done() // hold the long-poll until the watcher cancels it
		case r.Method == http.MethodDelete:
			t.Error("DeleteCVD actuated on a group whose launch is still in flight")
			w.Write([]byte(`{}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer ho.Close()
	hostURL, _ := url.Parse(ho.URL)
	port, _ := strconv.ParseInt(hostURL.Port(), 10, 64)

	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{
		ID: "h", Status: host.StatusRunning, MaxDevices: 4,
		IPAddress: "127.0.0.1", HostOrchestratorPort: port,
	}
	repo := newSyncDeviceRepo()
	repo.put(&device.Device{ID: "deadbeef-dev", HostID: "h", Name: "dev-1", Status: device.StatusPending})

	act := NewActuator(repo, hostRepo, nopBuilds{},
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
		WithCreateCancelPoll(5*time.Millisecond))

	errCh := make(chan error, 1)
	go func() { errCh <- act.RunCreateCVD(t.Context(), "deadbeef-dev", nil) }()

	<-waitStarted // the launch is in flight and the create is parked on :wait
	repo.setStatus("deadbeef-dev", device.StatusDeleting)

	select {
	case err := <-errCh:
		require.NoError(t, err, "a delete mid-create must complete the task, not fail it")
	case <-time.After(5 * time.Second):
		t.Fatal("RunCreateCVD did not abandon the wait after the delete")
	}
	assert.Equal(t, device.StatusDeleting, repo.getStatus("deadbeef-dev"),
		"the create must not overwrite the delete (no error, no running)")
}
