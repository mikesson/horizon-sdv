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
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// fakeOperationStore records terminal writes and serves one operation.
type fakeOperationStore struct {
	mu  sync.Mutex
	rec *operation.Record

	getErr       error
	terminateErr error

	terminated   int
	lastProblem  *operation.Problem
	lastResponse []byte
}

func (f *fakeOperationStore) GetByTask(context.Context, string) (*operation.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	copied := *f.rec
	return &copied, nil
}

func (f *fakeOperationStore) Terminate(_ context.Context, _ string, problem *operation.Problem, response []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.terminateErr != nil {
		return f.terminateErr
	}
	f.terminated++
	f.lastProblem = problem
	f.lastResponse = response
	f.rec.Done = true
	return nil
}

func (f *fakeOperationStore) snapshot() (int, *operation.Problem, []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.terminated, f.lastProblem, f.lastResponse
}

// verbHandlerFixture builds a handler over a running device whose actuation is a
// stub, so the tests exercise the handler's own decisions rather than a fake host.
func verbHandlerFixture(t *testing.T, verb operation.Verb, rec *operation.Record,
	call func(hoclient.Client, context.Context, string, string) error,
) (*VerbOperationHandler, *fakeOperationStore) {
	t.Helper()

	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h1"] = &host.Host{
		ID: "h1", Status: host.StatusRunning, MaxDevices: 4,
		IPAddress: "127.0.0.1", HostOrchestratorPort: 1080,
	}
	deviceRepo := newDeviceRepoFake()
	deviceRepo.Devices["d1"] = &device.Device{
		ID: "d1", Name: "dev", HostID: "h1", Status: device.StatusRunning, CVDID: "cvdd1/dev",
		Instances: []device.Instance{{Name: "dev"}, {Name: "beta"}},
	}
	svc, err := NewDeviceService(deviceRepo, hostRepo, newMockBuildRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
		WithTaskEnqueuer(&fakeEnqueuer{}))
	if err != nil {
		t.Fatalf("building service: %v", err)
	}

	store := &fakeOperationStore{rec: rec}
	h, err := NewVerbOperationHandler(VerbDefinition{
		Verb: verb, Label: "restart", Action: audit.ActionDeviceRestart, Call: call,
	}, svc, store, slog.Default())
	if err != nil {
		t.Fatalf("building handler: %v", err)
	}
	return h, store
}

func liveOperation(verb operation.Verb) *operation.Record {
	return &operation.Record{
		ID: "op-1", Verb: verb, ResourceType: operation.ResourceTypeDevice,
		ResourceID: "d1", TaskID: "t1", CreatedBy: "someone@example.com",
	}
}

// The guard that closes the redelivery hole. The handler writes the terminal state
// before returning, and the queue's Complete runs afterwards and can fail, which is
// a sanctioned and logged path. When the lease lapses the task is redelivered, and
// by then the operation's exclusivity has already been released by that same
// terminal write, so a second actuation could overlap a newly admitted one.
//
// Checking `done` BEFORE actuating is what makes the redelivery a no-op.
func TestVerbHandlerDoesNotActuateASettledOperation(t *testing.T) {
	t.Parallel()

	rec := liveOperation(operation.VerbDeviceRestart)
	rec.Done = true

	var calls int
	h, store := verbHandlerFixture(t, operation.VerbDeviceRestart, rec,
		func(hoclient.Client, context.Context, string, string) error {
			calls++
			return nil
		})

	if err := h.Handle(t.Context(), &task.Task{ID: "t1", MaxAttempts: 3, Attempts: 1}); err != nil {
		t.Fatalf("a redelivered settled operation must succeed without acting: %v", err)
	}
	if calls != 0 {
		t.Errorf("the verb actuated %d times on a settled operation; the device is what this guard protects", calls)
	}
	if n, _, _ := store.snapshot(); n != 0 {
		t.Errorf("terminal write attempted %d times; the outcome was already recorded", n)
	}
}

// Success settles the operation with the verb's declared response, and the audit
// entry carries the actor from the operation row. A worker context has no
// principal, which is why created_by is stored.
func TestVerbHandlerSettlesOnSuccess(t *testing.T) {
	t.Parallel()

	h, store := verbHandlerFixture(t, operation.VerbDeviceRestart, liveOperation(operation.VerbDeviceRestart),
		func(hoclient.Client, context.Context, string, string) error { return nil })

	if err := h.Handle(t.Context(), &task.Task{ID: "t1", MaxAttempts: 3, Attempts: 1}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	n, problem, response := store.snapshot()
	if n != 1 {
		t.Fatalf("terminal writes = %d, want 1", n)
	}
	if problem != nil {
		t.Errorf("a success recorded a problem document: %+v", problem)
	}
	var body map[string]any
	if err := json.Unmarshal(response, &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if _, ok := body["restarted_at"]; !ok {
		t.Errorf("response %v is missing the field the registry declares", body)
	}
}

// A transient failure leaves the operation OPEN. Retries are invisible progress,
// reported from the task row and never written to the operation.
func TestVerbHandlerLeavesOperationOpenOnATransientFailure(t *testing.T) {
	t.Parallel()

	h, store := verbHandlerFixture(t, operation.VerbDeviceRestart, liveOperation(operation.VerbDeviceRestart),
		func(hoclient.Client, context.Context, string, string) error {
			return errors.New("host unreachable")
		})

	err := h.Handle(t.Context(), &task.Task{ID: "t1", MaxAttempts: 3, Attempts: 1})
	if err == nil {
		t.Fatal("a failed actuation must reach the queue as an error so it retries")
	}
	if n, _, _ := store.snapshot(); n != 0 {
		t.Errorf("terminal writes = %d on a retryable attempt; the operation must stay open", n)
	}
}

// The LAST attempt records the failure, with the same problem document the
// synchronous path would have returned, then still returns the error so the queue
// dead-letters as usual.
func TestVerbHandlerRecordsTerminalFailureWhenExhausted(t *testing.T) {
	t.Parallel()

	h, store := verbHandlerFixture(t, operation.VerbDevicePowerwash, liveOperation(operation.VerbDevicePowerwash),
		func(hoclient.Client, context.Context, string, string) error {
			return &hoclient.HostError{StatusCode: 502, Message: "cvd powerwash failed"}
		})

	err := h.Handle(t.Context(), &task.Task{ID: "t1", MaxAttempts: 1, Attempts: 1})
	if err == nil {
		t.Fatal("the error must still reach the queue so the task dead-letters")
	}
	n, problem, response := store.snapshot()
	if n != 1 {
		t.Fatalf("terminal writes = %d, want 1", n)
	}
	if problem == nil {
		t.Fatal("an exhausted failure must record a problem document; it is the only durable trace")
	}
	if problem.Status != 502 {
		t.Errorf("problem status = %d, want the host client's classification (502)", problem.Status)
	}
	if problem.Detail == "" {
		t.Error("the host's own reason is the diagnostic and it was dropped")
	}
	if response != nil {
		t.Errorf("a failure recorded a response payload: %s", response)
	}
}

// The one path where both the CAS and the done guard are blind. Returning the
// error hands the outcome back to the queue rather than reporting a success as an
// abandonment.
func TestVerbHandlerReturnsErrorWhenRecordingSuccessFails(t *testing.T) {
	t.Parallel()

	h, store := verbHandlerFixture(t, operation.VerbDeviceRestart, liveOperation(operation.VerbDeviceRestart),
		func(hoclient.Client, context.Context, string, string) error { return nil })
	store.terminateErr = errors.New("spanner unavailable")

	if err := h.Handle(t.Context(), &task.Task{ID: "t1", MaxAttempts: 3, Attempts: 1}); err == nil {
		t.Fatal("a failed terminal write must retry, not report the operation abandoned")
	}
}

// A verb whose device moved past it never actuates, and SETTLES instead of
// retrying. Re-derivation at execution time is why it finds out at all. The
// pre-flight guard at admission produces a good 4xx and establishes nothing,
// because minutes pass before the claim.
//
// Attempts are left on the task on purpose. Waiting for exhaustion would retry
// against a state that cannot come back and then dead-letter, reporting the
// system broken over a race the handler just resolved.
func TestVerbHandlerSettlesWhenTheDeviceMovedPastTheVerb(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     device.Status
		rowGone    bool
		wantStatus int
		wantDetail string
	}{
		{name: "stopping", status: device.StatusStopping, wantStatus: http.StatusConflict, wantDetail: "current status stopping"},
		{name: "stopped", status: device.StatusStopped, wantStatus: http.StatusConflict, wantDetail: "current status stopped"},
		{name: "deleting", status: device.StatusDeleting, wantStatus: http.StatusNotFound, wantDetail: "device d1 is deleting"},
		{name: "deleted", status: device.StatusDeleted, wantStatus: http.StatusNotFound, wantDetail: "device d1 is deleted"},
		{name: "row gone", rowGone: true, wantStatus: http.StatusNotFound, wantDetail: "device not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var calls int
			h, store := verbHandlerFixture(t, operation.VerbDeviceRestart, liveOperation(operation.VerbDeviceRestart),
				func(hoclient.Client, context.Context, string, string) error {
					calls++
					return nil
				})
			repo := h.svc.deviceRepo.(*atomicDeviceRepo)
			if tt.rowGone {
				delete(repo.Devices, "d1")
			} else {
				repo.Devices["d1"].Status = tt.status
			}

			if err := h.Handle(t.Context(), &task.Task{ID: "t1", MaxAttempts: 3, Attempts: 1}); err != nil {
				t.Fatalf("a raced verb must finish its task, not hand it back to the queue: %v", err)
			}
			if calls != 0 {
				t.Errorf("the verb actuated %d times against a device that had moved on", calls)
			}
			n, problem, response := store.snapshot()
			if n != 1 {
				t.Fatalf("terminal writes = %d, want 1 on the first attempt", n)
			}
			if problem == nil {
				t.Fatal("the conflict is the operation's outcome and nothing recorded it")
			}
			if problem.Status != tt.wantStatus {
				t.Errorf("problem status = %d, want %d, the same classification the synchronous path returns",
					problem.Status, tt.wantStatus)
			}
			if !strings.Contains(problem.Detail, tt.wantDetail) {
				t.Errorf("problem detail = %q, want it to name %q", problem.Detail, tt.wantDetail)
			}
			if response != nil {
				t.Errorf("a refused verb recorded a response payload: %s", response)
			}
		})
	}
}

// Status error is NOT a superseding state. A device marks error transiently
// (a failed create attempt, a health blip) and comes back to running, so
// retrying through it is progress instead of waiting for the past.
// Both of the older arms survive intact: open while attempts remain, then a
// recorded terminal failure and the error handed back so the queue
// dead-letters.
func TestVerbHandlerRetriesThroughAnErrorStatus(t *testing.T) {
	t.Parallel()

	var calls int
	h, store := verbHandlerFixture(t, operation.VerbDeviceRestart, liveOperation(operation.VerbDeviceRestart),
		func(hoclient.Client, context.Context, string, string) error {
			calls++
			return nil
		})
	h.svc.deviceRepo.(*atomicDeviceRepo).Devices["d1"].Status = device.StatusError

	if err := h.Handle(t.Context(), &task.Task{ID: "t1", MaxAttempts: 3, Attempts: 1}); err == nil {
		t.Fatal("an error-status device must go back to the queue: it can heal back to running")
	}
	if n, _, _ := store.snapshot(); n != 0 {
		t.Errorf("terminal writes = %d with attempts left; the operation must stay open", n)
	}

	if err := h.Handle(t.Context(), &task.Task{ID: "t1", MaxAttempts: 3, Attempts: 3}); err == nil {
		t.Fatal("the last attempt must still hand the error back so the task dead-letters")
	}
	n, problem, _ := store.snapshot()
	if n != 1 || problem == nil {
		t.Fatalf("terminal writes = %d, problem = %+v; the exhausted attempt records the failure", n, problem)
	}
	if calls != 0 {
		t.Errorf("the verb actuated %d times against an error device", calls)
	}
}

// The registry declares each verb's response fields, and the OpenAPI schema cannot
// enforce them. One Operation type makes `response` an untyped object in the spec
// (verified: it serializes as {}). So this test is the only thing standing between
// a handler writing the wrong shape and a breaking change, because a response type
// cannot be widened after release.
func TestVerbResponseMatchesItsRegistryEntry(t *testing.T) {
	t.Parallel()

	for _, verb := range operation.Verbs() {
		policy, err := operation.PolicyFor(verb)
		if err != nil {
			t.Fatalf("PolicyFor(%q): %v", verb, err)
		}
		h, _ := verbHandlerFixture(t, verb, liveOperation(verb),
			func(hoclient.Client, context.Context, string, string) error { return nil })

		raw, err := h.successResponse()
		if err != nil {
			t.Fatalf("%s: successResponse: %v", verb, err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("%s: response is not a JSON object: %v", verb, err)
		}

		got := make([]string, 0, len(body))
		for k := range body {
			got = append(got, k)
		}
		slices.Sort(got)
		want := slices.Clone(policy.ResponseFields)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s response fields = %v, registry declares %v", verb, got, want)
		}
		if len(body) == 0 {
			t.Errorf("%s returns an empty response; AIP-151 makes that unwidenable later", verb)
		}
	}
}

// The dispatch key comes from the registry, so the handler and the admission path
// cannot disagree about which type carries which verb.
func TestVerbHandlerTaskTypeComesFromTheRegistry(t *testing.T) {
	t.Parallel()

	for _, verb := range operation.Verbs() {
		policy, err := operation.PolicyFor(verb)
		if err != nil {
			t.Fatalf("PolicyFor(%q): %v", verb, err)
		}
		h, _ := verbHandlerFixture(t, verb, liveOperation(verb),
			func(hoclient.Client, context.Context, string, string) error { return nil })
		if got := h.TaskType(); got != policy.TaskType {
			t.Errorf("%s TaskType = %q, registry says %q", verb, got, policy.TaskType)
		}
	}
}

// An unregistered verb is refused at construction. Every default the registry
// would otherwise supply is wrong, and boot is the right place to find out.
func TestNewVerbOperationHandlerRejectsAnUnregisteredVerb(t *testing.T) {
	t.Parallel()

	_, err := NewVerbOperationHandler(VerbDefinition{
		Verb: operation.Verb("device.nope"), Label: "nope", Action: audit.ActionDeviceRestart,
		Call: func(hoclient.Client, context.Context, string, string) error { return nil },
	}, nil, nil, slog.Default())
	if !errors.Is(err, operation.ErrUnknownVerb) {
		t.Errorf("err = %v, want ErrUnknownVerb", err)
	}
}

// The instance a per-instance verb actuates comes off the operation record and
// never from the device's CVD ID.
//
// It is the one part of the target the caller chose, and re-deriving it is
// exactly the failure this route exists to remove. The CVD ID names the group's
// first instance, so a restart admitted against `beta` would silently reboot
// `dev` instead, a destructive verb applied to the wrong Android VM, reported
// as success.
func TestVerbHandlerActuatesTheInstanceTheOperationRecorded(t *testing.T) {
	t.Parallel()

	rec := liveOperation(operation.VerbDeviceRestart)
	rec.InstanceName = "beta"

	var actuated string
	h, _ := verbHandlerFixture(t, operation.VerbDeviceRestart, rec,
		func(_ hoclient.Client, _ context.Context, _, name string) error {
			actuated = name
			return nil
		})

	if err := h.Handle(t.Context(), &task.Task{ID: "t1", MaxAttempts: 3, Attempts: 1}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if actuated != "beta" {
		t.Errorf("actuated instance %q, want beta, since the record names the instance the caller chose", actuated)
	}
}

// A verb naming an instance the device does not have is refused instead of
// falling back to the group's first. The device's own instances are the only
// record of what its members are called, and the host names its whole fleet in
// one namespace, so forwarding an unknown name can reach another device's VM.
func TestVerbHandlerRefusesAnInstanceTheDeviceDoesNotHave(t *testing.T) {
	t.Parallel()

	rec := liveOperation(operation.VerbDeviceRestart)
	rec.InstanceName = "gamma"

	var calls int
	h, _ := verbHandlerFixture(t, operation.VerbDeviceRestart, rec,
		func(hoclient.Client, context.Context, string, string) error {
			calls++
			return nil
		})

	err := h.Handle(t.Context(), &task.Task{ID: "t1", MaxAttempts: 3, Attempts: 1})
	if err == nil {
		t.Fatal("Handle on an unknown instance = nil, want an error")
	}
	if calls != 0 {
		t.Errorf("actuated %d times against an instance the device does not have", calls)
	}
}
