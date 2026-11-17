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
	"strings"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// racedVerb is the verb handler over the REAL operations repository and task
// queue: the settle under test is the production CAS, the ack is the queue's
// own, and the dead-letter it must avoid is the one the tasks table records.
type racedVerb struct {
	handler *VerbOperationHandler
	ops     *cspanner.OperationsRepository
	queue   *cspanner.TaskQueue
	devices *atomicDeviceRepo
	calls   *int
}

const raceShards = 4

// newRacedVerb admits a restart against a running device and claims its task,
// leaving the fixture exactly where a worker stands before actuating.
func newRacedVerb(t *testing.T) (*racedVerb, *operation.Record, *task.Task) {
	t.Helper()

	db := spannertest.MustNewTestDB(t)
	queue := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(raceShards), cspanner.WithHolder("race-test"))
	ops := cspanner.NewOperationsRepository(db.Client, queue, slog.Default())

	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h1"] = &host.Host{
		ID: "h1", Status: host.StatusRunning, MaxDevices: 4,
		IPAddress: "127.0.0.1", HostOrchestratorPort: 1080,
	}
	devices := newDeviceRepoFake()
	devices.Devices["d1"] = &device.Device{
		ID: "d1", Name: "dev", HostID: "h1", Status: device.StatusRunning, CVDID: "cvdd1/dev",
		Instances: []device.Instance{{Name: "dev"}},
	}
	svc, err := NewDeviceService(devices, hostRepo, newMockBuildRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
		WithTaskEnqueuer(&fakeEnqueuer{}))
	if err != nil {
		t.Fatalf("building service: %v", err)
	}

	calls := new(int)
	h, err := NewVerbOperationHandler(VerbDefinition{
		Verb: operation.VerbDeviceRestart, Label: "restart", Action: audit.ActionDeviceRestart,
		Call: func(hoclient.Client, context.Context, string, string) error {
			*calls++
			return nil
		},
	}, svc, ops, slog.Default())
	if err != nil {
		t.Fatalf("building handler: %v", err)
	}

	rec, err := ops.Admit(t.Context(), cspanner.AdmitRequest{
		Verb: operation.VerbDeviceRestart, ResourceID: "d1", CreatedBy: "someone@example.com",
	})
	if err != nil {
		t.Fatalf("admitting the verb: %v", err)
	}
	claimed, err := queue.Claim(t.Context(), task.ShardFor(rec.TaskID, raceShards), 10, time.Hour)
	if err != nil {
		t.Fatalf("claiming the verb's task: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d tasks, want the one the admission enqueued", len(claimed))
	}
	return &racedVerb{handler: h, ops: ops, queue: queue, devices: devices, calls: calls}, rec, claimed[0]
}

// This makes the dominant robustness-harness flake deterministic. A verb is
// admitted against a running device whose user stops or deletes it before the
// task runs.
//
// The whole outcome is asserted end to end because each half is a different
// wrong answer. Retrying waits for a state that cannot come back; returning the
// error dead-letters a queue that did its job; leaving the operation open
// blocks the resource until a sweep heals it. One settle on the first attempt
// answers all three.
func TestVerbHandlerSettlesARaceWithALaterLifecycleChange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     device.Status
		rowGone    bool
		wantStatus int
		wantDetail string
	}{
		{name: "deleted row", rowGone: true, wantStatus: http.StatusNotFound, wantDetail: "device not found"},
		{name: "stopped", status: device.StatusStopped, wantStatus: http.StatusConflict, wantDetail: "current status stopped"},
		{name: "deleting", status: device.StatusDeleting, wantStatus: http.StatusNotFound, wantDetail: "device d1 is deleting"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, rec, claimed := newRacedVerb(t)
			if tt.rowGone {
				delete(f.devices.Devices, "d1")
			} else {
				f.devices.Devices["d1"].Status = tt.status
			}

			if err := f.handler.Handle(t.Context(), claimed); err != nil {
				t.Fatalf("the task must succeed after settling its operation: %v", err)
			}
			if *f.calls != 0 {
				t.Errorf("the verb actuated %d times against a device that had moved on", *f.calls)
			}

			settled, err := f.ops.Get(t.Context(), rec.ID)
			if err != nil {
				t.Fatalf("reading the settled operation: %v", err)
			}
			if !settled.Done {
				t.Fatal("the operation is still open; nothing else will ever close it")
			}
			if settled.Error == nil {
				t.Fatal("a settled conflict recorded no problem document")
			}
			if settled.Error.Status != tt.wantStatus {
				t.Errorf("problem status = %d, want %d", settled.Error.Status, tt.wantStatus)
			}
			if !strings.Contains(settled.Error.Detail, tt.wantDetail) {
				t.Errorf("problem detail = %q, want it to name %q", settled.Error.Detail, tt.wantDetail)
			}

			// The worker's ack, plus the assertion the robustness harness makes
			// at quiescence. No task is left in the dead-letter state.
			if err := f.queue.Complete(t.Context(), claimed); err != nil {
				t.Fatalf("completing the acked task: %v", err)
			}
			failed, err := f.queue.ListFailed(t.Context(), 10, "")
			if err != nil {
				t.Fatalf("listing dead-lettered tasks: %v", err)
			}
			if len(failed) != 0 {
				t.Errorf("dead-lettered tasks = %d, want none: the handler had a definitive outcome", len(failed))
			}
		})
	}
}

// Settling on the first attempt releases the resource earlier than the old
// retry-to-exhaustion path did, so the two guards that make an at-least-once
// redelivery safe are asserted against the real writers.
//
// A redelivered task finds the operation done and returns without actuating,
// which is the guard that protects the DEVICE, and the terminal CAS refuses a
// second write, so a redelivery cannot overwrite the outcome. The earlier
// release does let through a NEW verb on the same device. That is the intended
// meaning of a settled operation, and it is safe for the same reason. The old
// task can no longer actuate anything.
func TestSettledRaceStaysSettledUnderRedelivery(t *testing.T) {
	t.Parallel()

	f, rec, claimed := newRacedVerb(t)
	f.devices.Devices["d1"].Status = device.StatusDeleted

	if err := f.handler.Handle(t.Context(), claimed); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	settled, err := f.ops.Get(t.Context(), rec.ID)
	if err != nil {
		t.Fatalf("reading the settled operation: %v", err)
	}
	firstOutcome := settled.Error

	// The device comes back before the lease lapses. Nothing about the
	// redelivery may reach it.
	f.devices.Devices["d1"].Status = device.StatusRunning
	if err := f.handler.Handle(t.Context(), claimed); err != nil {
		t.Fatalf("a redelivered settled task must succeed without acting: %v", err)
	}
	if *f.calls != 0 {
		t.Errorf("the redelivery actuated the verb %d times on a settled operation", *f.calls)
	}
	after, err := f.ops.Get(t.Context(), rec.ID)
	if err != nil {
		t.Fatalf("re-reading the operation: %v", err)
	}
	if after.Error == nil || after.Error.Detail != firstOutcome.Detail {
		t.Errorf("recorded outcome changed under redelivery: %+v, was %+v", after.Error, firstOutcome)
	}

	// Exclusivity was released by the settle, which is what lets the user
	// re-issue the verb they raced.
	if _, err := f.ops.Admit(t.Context(), cspanner.AdmitRequest{
		Verb: operation.VerbDeviceRestart, ResourceID: "d1",
	}); err != nil {
		t.Fatalf("a settled operation must not keep blocking its resource: %v", err)
	}
}
