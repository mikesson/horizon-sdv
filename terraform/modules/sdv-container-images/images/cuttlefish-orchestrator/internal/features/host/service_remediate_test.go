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
	"context"
	"log/slog"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/tests/harness/fakeho"
)

// fastRemediateFactory builds an HO client factory that fails fast, so the
// reset-error path does not spin on retries/fallback ports.
func fastRemediateFactory() *hoclient.Factory {
	cfg := hoclient.DefaultClientConfig()
	cfg.MaxRetries = 0
	cfg.RetryDelay = time.Millisecond
	cfg.Timeout = 2 * time.Second
	cfg.FallbackPorts = nil
	return hoclient.NewFactory(cfg)
}

// constLiveCounter returns a live-device counter that always reports n.
func constLiveCounter(n int64) func(context.Context, string) (int64, error) {
	return func(context.Context, string) (int64, error) { return n, nil }
}

// poisonedHost returns a degraded running host addressed at ip:port. It crosses
// the poison threshold so DegradedAt is set. CurrentDeviceCount is left high on
// purpose: remediation must gate on LIVE devices (the injected counter), never
// the raw row count, which still includes the error rows the poison failures
// left.
func poisonedHost(id, ip string, port int) *host.Host {
	h := &host.Host{
		ID: id, Name: id, Zone: "z", MaxDevices: 4,
		Status:               host.StatusRunning,
		InternalIP:           ip,
		HostOrchestratorPort: int64(port),
		CurrentDeviceCount:   host.HostPoisonThreshold, // the failed-create error rows
	}
	for range host.HostPoisonThreshold {
		h.RecordCreateFailure("tap interface busy")
	}
	return h
}

func remediateService(repo *atomicHostRepo, f *hoclient.Factory, live func(context.Context, string) (int64, error)) *HostService {
	return &HostService{hostRepo: repo, hoFactory: f, liveDeviceCounter: live, logger: slog.New(slog.DiscardHandler)}
}

func TestRunRemediateHostNoOpWhenHealthy(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo(&host.Host{ID: "h1", Name: "h", Zone: "z", Status: host.StatusRunning})
	svc := remediateService(repo, nil, nil)

	if err := svc.RunRemediateHost(t.Context(), "h1"); err != nil {
		t.Fatalf("RunRemediateHost: %v", err)
	}
	if repo.Hosts["h1"].IsDegraded() {
		t.Error("a healthy host must not become degraded")
	}
}

func TestRunRemediateHostNoOpWhenNotRunning(t *testing.T) {
	t.Parallel()
	h := poisonedHost("h1", "127.0.0.1", 1)
	h.Status = host.StatusStopped
	repo := newAtomicHostRepo(h)
	svc := remediateService(repo, fastRemediateFactory(), constLiveCounter(0))

	if err := svc.RunRemediateHost(t.Context(), "h1"); err != nil {
		t.Fatalf("RunRemediateHost: %v", err)
	}
	if !repo.Hosts["h1"].IsDegraded() {
		t.Error("a non-running host is owned by the lifecycle; remediation must leave it fenced")
	}
}

func TestRunRemediateHostFencesTenantedHost(t *testing.T) {
	t.Parallel()
	ho, err := fakeho.New()
	if err != nil {
		t.Fatal(err)
	}
	defer ho.Close()

	// A degraded host that still carries live devices must NOT be reset, because
	// the reset would destroy those tenants, so it stays fenced for an operator.
	h := poisonedHost("h1", "127.0.0.1", ho.Port())
	ho.Poison("127.0.0.1")
	repo := newAtomicHostRepo(h)
	svc := remediateService(repo, fastRemediateFactory(), constLiveCounter(2))

	if err := svc.RunRemediateHost(t.Context(), "h1"); err != nil {
		t.Fatalf("RunRemediateHost: %v", err)
	}
	if !repo.Hosts["h1"].IsDegraded() {
		t.Error("tenanted host was un-fenced; a reset would have destroyed its devices")
	}
}

// The regression guard for the original bug: a freshly-poisoned host reads
// CurrentDeviceCount == HostPoisonThreshold (its failed-create error rows), but
// zero of those are live, so the reset MUST fire.
func TestRunRemediateHostResetsHostWithOnlyErrorRows(t *testing.T) {
	t.Parallel()
	ho, err := fakeho.New()
	if err != nil {
		t.Fatal(err)
	}
	defer ho.Close()

	h := poisonedHost("h1", "127.0.0.1", ho.Port())
	if h.CurrentDeviceCount == 0 {
		t.Fatal("precondition: the raw row count should be non-zero (the error rows)")
	}
	ho.Poison("127.0.0.1")
	repo := newAtomicHostRepo(h)
	svc := remediateService(repo, fastRemediateFactory(), constLiveCounter(0))

	if err := svc.RunRemediateHost(t.Context(), "h1"); err != nil {
		t.Fatalf("RunRemediateHost: %v", err)
	}
	if repo.Hosts["h1"].IsDegraded() {
		t.Error("host with only error rows was not reset; the row-count guard regressed")
	}
	if repo.Hosts["h1"].ConsecutiveCreateFails != 0 {
		t.Error("failure counter not reset after remediation")
	}
}

func TestRunRemediateHostKeepsFenceWhenResetFails(t *testing.T) {
	t.Parallel()
	ho, err := fakeho.New()
	if err != nil {
		t.Fatal(err)
	}
	defer ho.Close()
	ho.SetErrorAll(true)

	h := poisonedHost("h1", "127.0.0.1", ho.Port())
	repo := newAtomicHostRepo(h)
	svc := remediateService(repo, fastRemediateFactory(), constLiveCounter(0))

	if err := svc.RunRemediateHost(t.Context(), "h1"); err == nil {
		t.Fatal("expected the failed reset to surface an error for retry")
	}
	if !repo.Hosts["h1"].IsDegraded() {
		t.Error("host must stay fenced when the reset did not succeed")
	}
}

// Without a live-device counter the destructive reset must never fire.
func TestRunRemediateHostFencesWhenCounterUnset(t *testing.T) {
	t.Parallel()
	h := poisonedHost("h1", "127.0.0.1", 1)
	repo := newAtomicHostRepo(h)
	svc := remediateService(repo, fastRemediateFactory(), nil)

	if err := svc.RunRemediateHost(t.Context(), "h1"); err != nil {
		t.Fatalf("RunRemediateHost: %v", err)
	}
	if !repo.Hosts["h1"].IsDegraded() {
		t.Error("no counter means the host cannot be confirmed safe to reset; must stay fenced")
	}
}

func TestFindAvailableHostsExcludesDegraded(t *testing.T) {
	t.Parallel()
	healthy := &host.Host{ID: "ok", Name: "ok", Zone: "z", Status: host.StatusRunning, MaxDevices: 4}
	repo := newAtomicHostRepo(healthy, poisonedHost("bad", "127.0.0.1", 1))

	got, err := repo.FindAvailableHosts(t.Context(), "z", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "ok" {
		ids := make([]string, len(got))
		for i, h := range got {
			ids[i] = h.ID
		}
		t.Errorf("FindAvailableHosts returned %v, want only [ok] (degraded host must be fenced)", ids)
	}
}
