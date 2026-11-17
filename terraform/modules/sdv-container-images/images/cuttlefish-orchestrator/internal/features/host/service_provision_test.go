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

package hostfeature

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	"google.golang.org/api/option"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

// adoptionFakeClient serves the provision path where the instance name is
// already taken: insert always answers 409 alreadyExists; get answers with the
// configured status code and instance JSON.
func adoptionFakeClient(t *testing.T, getStatus int, instJSON string) *compute.InstancesClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/instances"):
			http.Error(w, `{"error":{"code":409,"message":"alreadyExists: instance exists","errors":[{"reason":"alreadyExists"}]}}`, http.StatusConflict)
		case r.Method == http.MethodGet:
			if getStatus != http.StatusOK {
				http.Error(w, `{"error":{"code":404,"message":"notFound: instance not found","errors":[{"reason":"notFound"}]}}`, getStatus)
				return
			}
			fmt.Fprint(w, instJSON)
		default:
			fmt.Fprint(w, `{"name":"op-1","status":"DONE"}`)
		}
	}))
	t.Cleanup(srv.Close)
	client, err := compute.NewInstancesRESTClient(t.Context(),
		option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("creating fake GCE client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func provisioningHost() *host.Host {
	return &host.Host{
		ID: testHostID, Name: "spot", Zone: "europe-west4-a",
		GCEInstanceName:   "cf-spot-11111111",
		MachineType:       "n1-standard-8",
		Status:            host.StatusProvisioning,
		ProvisioningModel: host.ProvisioningModelSpot,
		MaxDevices:        4,
	}
}

func adoptInstanceJSON(status string) string {
	return fmt.Sprintf(`{"name":"cf-spot-11111111","status":%q,"networkInterfaces":[{"networkIP":"10.0.0.5","accessConfigs":[{"natIP":"34.0.0.5"}]}]}`, status)
}

// TestRunProvisionAdoptGuard pins the Spot husk race: Retry reuses the
// deterministic instance name and GCE deletes a preempted VM asynchronously
// (termination action DELETE), so a fast-following provision attempt can find
// the dying husk under its own name. Adopting it would observe STOPPING, which
// provisioning cannot reach, return success, and strand the host in
// provisioning with no task left.
func TestRunProvisionAdoptGuard(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		getStatus      int
		inst           string
		wantReschedule bool
		wantErr        bool
		wantHostStatus host.Status
		wantIP         string
		wantVMStatus   string // raw observation recorded when observeProvisioned is reached
	}{
		{
			name:      "dying husk postpones",
			getStatus: http.StatusOK, inst: adoptInstanceJSON("STOPPING"),
			wantReschedule: true, wantErr: true,
			wantHostStatus: host.StatusProvisioning,
		},
		{
			name:           "vanished husk postpones",
			getStatus:      http.StatusNotFound,
			wantReschedule: true, wantErr: true,
			wantHostStatus: host.StatusProvisioning,
		},
		{
			name:      "coming-up instance postpones",
			getStatus: http.StatusOK, inst: adoptInstanceJSON("PROVISIONING"),
			wantReschedule: true, wantErr: true,
			wantHostStatus: host.StatusProvisioning,
		},
		{
			// Adopting a live RUNNING instance lands on starting, not running:
			// the VM is up but the ho_reachable latch is unset until the
			// device controller reaches HO. The raw observation is still recorded.
			name:      "live instance adopted as starting",
			getStatus: http.StatusOK, inst: adoptInstanceJSON("RUNNING"),
			wantHostStatus: host.StatusStarting,
			wantIP:         "34.0.0.5",
			wantVMStatus:   "RUNNING",
		},
		{
			name:      "parked instance burns the attempt",
			getStatus: http.StatusOK, inst: adoptInstanceJSON("TERMINATED"),
			wantErr:        true,
			wantHostStatus: host.StatusProvisioning,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Every seed carries a capacity refusal from an earlier attempt: only
			// an observed VM may lift it, so the arms that never reach the
			// observation have to leave it standing.
			seed := provisioningHost()
			seed.MarkProvisionBlocked("The zone does not have enough resources")
			repo := newAtomicHostRepo(seed)
			svc, _ := newTestService(t, repo, adoptionFakeClient(t, tt.getStatus, tt.inst))

			err := svc.RunProvision(t.Context(), testHostID, "")

			if (err != nil) != tt.wantErr {
				t.Fatalf("RunProvision = %v, wantErr %v", err, tt.wantErr)
			}
			if _, isResched := errors.AsType[*task.RescheduleError](err); isResched != tt.wantReschedule {
				t.Errorf("reschedule = %v, want %v (err=%v)", isResched, tt.wantReschedule, err)
			}
			got := repo.Hosts[testHostID]
			if got.Status != tt.wantHostStatus {
				t.Errorf("host status = %q, want %q", got.Status, tt.wantHostStatus)
			}
			if tt.wantIP != "" && got.IPAddress != tt.wantIP {
				t.Errorf("host IP = %q, want %q", got.IPAddress, tt.wantIP)
			}
			if tt.wantVMStatus != "" {
				if got.VMStatus != tt.wantVMStatus {
					t.Errorf("host VMStatus = %q, want %q (raw observation recorded)", got.VMStatus, tt.wantVMStatus)
				}
				if got.VMObservedAt == nil {
					t.Error("VMObservedAt not stamped by observeProvisioned")
				}
				if got.ProvisionBlockedAt != nil {
					t.Error("an observed VM must lift the provision-blocked condition in the same commit")
				}
			} else if got.ProvisionBlockedAt == nil {
				t.Error("no VM was observed, so the provision-blocked condition must still stand")
			}
		})
	}
}

// A late observation must not commit against a row a concurrent zone failover
// (or preemption recovery) already re-homed: it would stamp the old VM's IP
// and could promote the host to starting, which RunProvision treats as
// nothing-to-do, leaving the new zone's instance never inserted. The stale
// attempt reschedules instead, and the re-homed identity survives untouched.
func TestObserveProvisionedRefusesSupersededRow(t *testing.T) {
	t.Parallel()
	// The row has been re-homed: new zone, bumped incarnation, new name.
	current := provisioningHost()
	current.Zone = "europe-west4-b"
	current.GCEInstanceName = "cf-spot-11111111-2"
	repo := newAtomicHostRepo(current)
	svc, _ := newTestService(t, repo, adoptionFakeClient(t, http.StatusOK, adoptInstanceJSON("RUNNING")))

	// The stale attempt still holds the pre-failover snapshot and has just
	// read the OLD instance back as RUNNING with the old VM's IP.
	stale := provisioningHost()
	err := svc.observeProvisioned(t.Context(), stale, false)

	if _, ok := errors.AsType[*task.RescheduleError](err); !ok {
		t.Fatalf("observeProvisioned = %v, want a reschedule", err)
	}
	got := repo.Hosts[testHostID]
	if got.Status != host.StatusProvisioning || got.IPAddress != "" || got.VMStatus != "" {
		t.Errorf("superseded observation must not touch the row: status=%q ip=%q vmstatus=%q",
			got.Status, got.IPAddress, got.VMStatus)
	}
	if got.Zone != "europe-west4-b" || got.GCEInstanceName != "cf-spot-11111111-2" {
		t.Errorf("re-homed identity must survive: zone=%q name=%q", got.Zone, got.GCEInstanceName)
	}
}

// A reschedule returned on what looks like the final attempt is waiting, not
// failure: the worker postpones without burning the attempt, so the handler
// must not mark the host failed while the task is inside adoptWaitCap.
func TestProvisionHandlerRescheduleNeverMarksFailed(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo(provisioningHost())
	svc, _ := newTestService(t, repo, adoptionFakeClient(t, http.StatusOK, adoptInstanceJSON("STOPPING")))
	h := NewProvisionHostHandler(svc)

	err := h.Handle(t.Context(), &task.Task{
		ID: "t1", Type: TaskTypeProvisionHost, ResourceID: testHostID,
		Attempts: 10, MaxAttempts: 10, // exhausted-looking delivery
		CreatedAt: time.Now(),
	})

	if _, ok := errors.AsType[*task.RescheduleError](err); !ok {
		t.Fatalf("Handle = %v, want RescheduleError to pass through", err)
	}
	if got := repo.Hosts[testHostID].Status; got != host.StatusProvisioning {
		t.Errorf("host status = %q, want provisioning (a waiting host must never be marked failed)", got)
	}
}

// Past adoptWaitCap a still-waiting provision is stuck (a parked instance
// nothing will remove, a replayed requestId): the wait converts to a terminal
// failure instead of postponing forever.
func TestProvisionHandlerRescheduleFailsPastWaitCap(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo(provisioningHost())
	svc, _ := newTestService(t, repo, adoptionFakeClient(t, http.StatusOK, adoptInstanceJSON("STOPPING")))
	h := NewProvisionHostHandler(svc)

	err := h.Handle(t.Context(), &task.Task{
		ID: "t1", Type: TaskTypeProvisionHost, ResourceID: testHostID,
		Attempts: 1, MaxAttempts: 10,
		CreatedAt: time.Now().Add(-adoptWaitCap - time.Minute),
	})
	if err != nil {
		t.Fatalf("Handle = %v, want nil (task completes; the host carries the failure)", err)
	}
	if got := repo.Hosts[testHostID].Status; got != host.StatusError {
		t.Errorf("host status = %q, want error (wait capped)", got)
	}
}

// A provision completes a preemption recovery only when it directly
// supersedes the preempted incarnation. Later incarnations are ordinary
// retries and must not re-report a long-finished recovery.
func TestRecoveredPreemption(t *testing.T) {
	t.Parallel()
	preemptedAt := time.Now().Add(-45 * time.Second)
	inc := func(n int64) *int64 { return &n }

	if _, ok := recoveredPreemption(&host.Host{Incarnation: 1}); ok {
		t.Error("a host never preempted must not report a recovery")
	}
	if _, ok := recoveredPreemption(&host.Host{Incarnation: 3, PreemptedIncarnation: inc(1)}); ok {
		t.Error("an incarnation past the recovery must not re-report it")
	}
	d, ok := recoveredPreemption(&host.Host{
		Incarnation: 2, PreemptedIncarnation: inc(1), PreemptedAt: &preemptedAt,
	})
	if !ok {
		t.Fatal("the superseding incarnation must report the recovery")
	}
	if d < 44*time.Second || d > time.Minute {
		t.Errorf("recovery duration = %v, want ~45s", d)
	}
	if _, ok := recoveredPreemption(&host.Host{Incarnation: 2, PreemptedIncarnation: inc(1)}); !ok {
		t.Error("a missing PreemptedAt must not suppress the recovery record")
	}
}
