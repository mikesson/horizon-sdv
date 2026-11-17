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
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/platform/ice"
	"cloud-cuttlefish-orchestrator/tests/parityfile"
)

// TestUpstreamParity replays every recording in testdata/parity, the
// upstream Cloud Orchestrator's exact wire answers captured by
// tools/legacyparity, against the /v1 handler and byte-diffs status, headers
// and body. The fakes below answer with the same scenario values the
// recorder's fakes used, so the diff pins serialization and mapping, and the
// only tolerated differences are the ones in expectedParityHeaders. Anything
// else is drift a legacy client would see.
func TestUpstreamParity(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(filepath.Join("testdata", "parity", "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no parity recordings; regenerate with: go -C tools/legacyparity run .")
	}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".txt")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			rec, err := parityfile.Unmarshal(data)
			if err != nil {
				t.Fatalf("parsing recording: %v", err)
			}
			replayParity(t, name, rec)
		})
	}
}

// The recorder's scenario, in domain vocabulary. Operation and host names are
// server-generated in production; here both sides run on these fixed values
// so any byte difference is a divergence, never value noise.
func parityFullHost() *host.Host {
	return &host.Host{
		ID:             "host-56f0e8a1",
		Status:         host.StatusRunning,
		MachineType:    "n1-standard-4",
		MinCPUPlatform: "Intel Cascade Lake",
		BootDiskSizeGB: 750,
		GPUType:        "nvidia-tesla-t4",
		GPUCount:       2,
		// The recorded client created its hosts through this API, so they
		// carry its provenance, which is what keeps them in its listing.
		ForeignCVDs: true,
	}
}

// parityHandler wires the fakes for one recording. A recording without an arm
// here fails loudly, so growing the corpus forces the matching replay wiring.
func parityHandler(t *testing.T, name string) *COHandler {
	t.Helper()
	switch name {
	case "zones":
		return newTestHandler(&fakeHostService{}, &fakeZoneLister{
			zones: []string{"us-central1-b", "europe-west4-a"},
		})
	case "zones-unclassified-error":
		return newTestHandler(&fakeHostService{}, &fakeZoneLister{
			err: fakeSpannerError("spanner unavailable: session pool exhausted"),
		})
	case "config", "create-host-bad-json", "list-hosts-bad-max-results":
		return newTestHandler(&fakeHostService{}, nil)
	case "create-host":
		return newTestHandler(&fakeHostService{createFn: func(_ context.Context, _ host.CreateRequest, _ string) (*host.Host, error) {
			return &host.Host{ID: "host-56f0e8a1", Status: host.StatusPending}, nil
		}}, nil)
	case "list-hosts":
		return newTestHandler(&fakeHostService{listFn: func(_ context.Context, _ host.ListFilter) ([]*host.Host, error) {
			// The second host leaves BootDiskSizeGB zero on purpose: the
			// recording says 400, so this pins the effective-size defaulting
			// in hostToInstance as well as the pass-through.
			return []*host.Host{
				parityFullHost(),
				{
					ID: "host-9c2d11b4", Status: host.StatusRunning,
					MachineType: "n1-standard-8", ForeignCVDs: true,
				},
			}, nil
		}}, nil)
	case "wait-create-done", "wait-escaped-verb":
		return fastWaitHandler(&fakeHostService{getFn: func(_ context.Context, _ string) (*host.Host, error) {
			return parityFullHost(), nil
		}})
	case "wait-timeout":
		return fastWaitHandler(&fakeHostService{getFn: func(_ context.Context, id string) (*host.Host, error) {
			return &host.Host{ID: id, Status: host.StatusProvisioning}, nil
		}})
	case "delete-host":
		return newTestHandler(&fakeHostService{deleteFn: func(_ context.Context, _ string) error {
			return nil
		}}, nil)
	case "wait-delete-done":
		return fastWaitHandler(&fakeHostService{getFn: func(_ context.Context, _ string) (*host.Host, error) {
			return nil, host.ErrNotFound
		}})
	case "delete-missing-host":
		return newTestHandler(&fakeHostService{deleteFn: func(_ context.Context, _ string) error {
			return &host.NotFoundError{ID: "host-000000"}
		}}, nil)
	case "infra-config":
		return newTestHandlerWithICE(&fakeHostService{}, nil,
			ice.NewComposer(nil, ice.Options{STUNServers: []string{"stun:stun.l.google.com:19302"}}))
	case "proxy-echo", "proxy-root":
		echo := httptest.NewServer(parityfile.EchoHostOrchestrator())
		t.Cleanup(echo.Close)
		return newTestHandler(hostPointingAt(echo), nil)
	default:
		t.Fatalf("no scenario wiring for recording %q; add an arm to parityHandler", name)
		return nil
	}
}

func replayParity(t *testing.T, name string, rec *parityfile.Recording) {
	t.Helper()
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.ContextWithPrincipal(req.Context(), &auth.Principal{Email: "user@example.com"})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	parityHandler(t, name).Register(r)
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	var body io.Reader
	if len(rec.ReqBody) > 0 {
		body = bytes.NewReader(rec.ReqBody)
	}
	req, err := http.NewRequest(rec.Method, server.URL+rec.Target, body)
	if err != nil {
		t.Fatal(err)
	}
	for k, vs := range rec.ReqHeaders {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	gotBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != rec.Status {
		t.Errorf("status = %d, upstream answered %d", resp.StatusCode, rec.Status)
	}
	if !bytes.Equal(gotBody, rec.RespBody) {
		t.Errorf("body diverges from upstream:\n  ours:     %q\n  upstream: %q", gotBody, rec.RespBody)
	}
	got := resp.Header.Clone()
	got.Del("Date")
	if want := expectedParityHeaders(rec); !reflect.DeepEqual(got, want) {
		t.Errorf("headers diverge from upstream (after the documented exceptions):\n  ours:     %v\n  expected: %v", got, want)
	}
}

// expectedParityHeaders is the complete list of accepted wire divergences
// from upstream. Exactly one exists: error responses carry
// Content-Type: application/json here, while upstream's replyJSON sets the
// header only after WriteHeader has already snapshotted it, so Go's sniffer
// answers text/plain for every non-200. The body is JSON either way and no
// known client dispatches on the header, so serving the accurate type is the
// better answer. Everything else must match byte for byte.
func expectedParityHeaders(rec *parityfile.Recording) http.Header {
	want := rec.RespHeaders.Clone()
	if rec.Status != http.StatusOK && want.Get("Content-Type") == "text/plain; charset=utf-8" {
		want.Set("Content-Type", "application/json")
	}
	return want
}

// fakeSpannerError is the recorder's unclassified backend error: a plain
// error no statusFromError arm matches.
type fakeSpannerError string

func (e fakeSpannerError) Error() string { return string(e) }
