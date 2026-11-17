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

package legacy

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/compat/legacy/coapi"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// fastWaitHandler is a handler whose :wait loop polls quickly so tests don't block.
func fastWaitHandler(hosts HostService) *COHandler {
	h := newTestHandler(hosts, nil)
	h.waitTimeout = 200 * time.Millisecond
	h.pollInterval = 2 * time.Millisecond
	return h
}

func TestWaitOperation_CreateRunning(t *testing.T) {
	t.Parallel()
	hosts := &fakeHostService{getFn: func(_ context.Context, id string) (*host.Host, error) {
		return &host.Host{ID: id, MachineType: "n1-standard-4", Status: host.StatusRunning}, nil
	}}
	rec := serve(t, fastWaitHandler(hosts), http.MethodPost, "/v1/zones/z/operations/create.host-123/:wait", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var hi coapi.HostInstance
	mustDecode(t, rec, &hi)
	if hi.Name != "host-123" || hi.GCP == nil || hi.GCP.MachineType != "n1-standard-4" {
		t.Errorf(":wait returned %+v, want the resolved HostInstance", hi)
	}
}

func TestWaitOperation_CreatePollsUntilRunning(t *testing.T) {
	t.Parallel()
	calls := 0
	hosts := &fakeHostService{getFn: func(_ context.Context, id string) (*host.Host, error) {
		calls++
		st := host.StatusProvisioning
		if calls >= 3 {
			st = host.StatusRunning
		}
		return &host.Host{ID: id, Status: st}, nil
	}}
	rec := serve(t, fastWaitHandler(hosts), http.MethodPost, "/v1/zones/z/operations/create.h1/:wait", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if calls < 3 {
		t.Errorf("polled %d times, expected to keep polling until running", calls)
	}
}

func TestWaitOperation_CreateError(t *testing.T) {
	t.Parallel()
	hosts := &fakeHostService{getFn: func(_ context.Context, id string) (*host.Host, error) {
		return &host.Host{ID: id, Status: host.StatusError, ErrorMessage: "no capacity"}, nil
	}}
	rec := serve(t, fastWaitHandler(hosts), http.MethodPost, "/v1/zones/z/operations/create.h1/:wait", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestWaitOperation_DeleteGone(t *testing.T) {
	t.Parallel()
	hosts := &fakeHostService{getFn: func(_ context.Context, _ string) (*host.Host, error) {
		return nil, host.ErrNotFound
	}}
	rec := serve(t, fastWaitHandler(hosts), http.MethodPost, "/v1/zones/z/operations/delete.h1/:wait", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// Legacy returns an empty object for a completed delete.
	if got := strings.TrimSpace(rec.Body.String()); got != "{}" {
		t.Errorf("body = %q, want {}", got)
	}
}

func TestWaitOperation_DeleteStatusDeleted(t *testing.T) {
	t.Parallel()
	hosts := &fakeHostService{getFn: func(_ context.Context, id string) (*host.Host, error) {
		return &host.Host{ID: id, Status: host.StatusDeleted}, nil
	}}
	rec := serve(t, fastWaitHandler(hosts), http.MethodPost, "/v1/zones/z/operations/delete.h1/:wait", "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Fatalf("status=%d body=%q, want 200 {}", rec.Code, rec.Body.String())
	}
}

func TestWaitOperation_TimesOut(t *testing.T) {
	t.Parallel()
	hosts := &fakeHostService{getFn: func(_ context.Context, id string) (*host.Host, error) {
		return &host.Host{ID: id, Status: host.StatusProvisioning}, nil // never settles
	}}
	rec := serve(t, fastWaitHandler(hosts), http.MethodPost, "/v1/zones/z/operations/create.h1/:wait", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (timeout)", rec.Code)
	}
}

func TestWaitOperation_InvalidName(t *testing.T) {
	t.Parallel()
	rec := serve(t, fastWaitHandler(&fakeHostService{}), http.MethodPost, "/v1/zones/z/operations/bogus/:wait", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
