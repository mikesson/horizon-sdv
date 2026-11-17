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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/compat/legacy/coapi"
	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/platform/ice"
)

type fakeHostService struct {
	createFn   func(context.Context, host.CreateRequest, string) (*host.Host, error)
	listFn     func(context.Context, host.ListFilter) ([]*host.Host, error)
	getFn      func(context.Context, string) (*host.Host, error)
	deleteFn   func(context.Context, string) error
	lastReq    host.CreateRequest
	lastBy     string
	lastOrigin host.Origin
	lastFiltr  host.ListFilter
}

func (f *fakeHostService) Create(ctx context.Context, req host.CreateRequest, by string, origin host.Origin) (*host.Host, error) {
	f.lastReq, f.lastBy, f.lastOrigin = req, by, origin
	return f.createFn(ctx, req, by)
}

func (f *fakeHostService) List(ctx context.Context, filter host.ListFilter) ([]*host.Host, error) {
	f.lastFiltr = filter
	return f.listFn(ctx, filter)
}

func (f *fakeHostService) Get(ctx context.Context, id string) (*host.Host, error) {
	return f.getFn(ctx, id)
}

func (f *fakeHostService) Delete(ctx context.Context, id string) error {
	return f.deleteFn(ctx, id)
}

type fakeZoneLister struct {
	zones []string
	err   error
}

func (f *fakeZoneLister) ListEnabledZones(context.Context) ([]string, error) {
	return f.zones, f.err
}

// serve routes req through a chi router with the handler registered and an
// authenticated principal in context, returning the recorded response.
func serve(t *testing.T, h *COHandler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	h.Register(r)
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(
		auth.ContextWithPrincipal(t.Context(), &auth.Principal{Email: "user@example.com"}),
		method, path, rdr)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func newTestHandler(hosts HostService, zones ZoneLister) *COHandler {
	return newTestHandlerWithICE(hosts, zones, ice.NewComposer(nil, ice.Options{STUNServers: testSTUNServers}))
}

func newTestHandlerWithICE(hosts HostService, zones ZoneLister, composer *ice.Composer) *COHandler {
	return NewCOHandler(hosts, zones, composer, slog.New(slog.DiscardHandler))
}

func TestListZones(t *testing.T) {
	t.Parallel()
	h := newTestHandler(nil, &fakeZoneLister{zones: []string{"us-central1-a", "europe-west4-b"}})

	rec := serve(t, h, http.MethodGet, "/v1/zones", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp coapi.ListZonesResponse
	mustDecode(t, rec, &resp)
	if len(resp.Items) != 2 || resp.Items[0].Name != "us-central1-a" || resp.Items[1].Name != "europe-west4-b" {
		t.Fatalf("zones = %+v, want the two enabled zones by name", resp.Items)
	}
}

func TestCreateHost(t *testing.T) {
	t.Parallel()
	hosts := &fakeHostService{createFn: func(_ context.Context, _ host.CreateRequest, _ string) (*host.Host, error) {
		return &host.Host{ID: "host-123", Status: host.StatusPending}, nil
	}}
	h := newTestHandler(hosts, nil)

	body := `{"host_instance":{"gcp":{"machine_type":"n1-standard-4","accelerator_configs":[{"accelerator_type":"nvidia-tesla-t4","accelerator_count":1}]}}}`
	rec := serve(t, h, http.MethodPost, "/v1/zones/us-central1-a/hosts", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Request mapping: the zone token compiles to a placement HINT by default
	// (never a pin, since an uncataloged upstream token must still admit),
	// machine type + GPU from the GCP block, name server-assigned, creator from
	// the principal.
	if hosts.lastReq.Zone != "" || hosts.lastReq.ZonePinned {
		t.Errorf("zone = %q pinned=%v, want ranked placement (empty zone, unpinned)",
			hosts.lastReq.Zone, hosts.lastReq.ZonePinned)
	}
	if hosts.lastReq.LocationHint != "us-central1-a" {
		t.Errorf("location hint = %q, want the us-central1-a token", hosts.lastReq.LocationHint)
	}
	if hosts.lastReq.MachineType != "n1-standard-4" {
		t.Errorf("machine type = %q, want n1-standard-4", hosts.lastReq.MachineType)
	}
	if hosts.lastReq.GPUType != "nvidia-tesla-t4" || hosts.lastReq.GPUCount != 1 {
		t.Errorf("gpu = %q x%d, want nvidia-tesla-t4 x1", hosts.lastReq.GPUType, hosts.lastReq.GPUCount)
	}
	// The legacy wire shape carries no device capacity; the handler must
	// supply a positive default or host.Validate rejects the create. The
	// literal, not the constant: comparing the constant to itself cannot fail.
	if hosts.lastReq.MaxDevices != 4 {
		t.Errorf("max devices = %d, want 4 (legacy default)", hosts.lastReq.MaxDevices)
	}
	assertGCPCreateFence(t, hosts.lastReq)
	// The server-assigned name is a display label, so it must stay out of the
	// binding namespace. host.ValidateDisplayName rejects the create
	// otherwise, and the name would compose into a "cf-cf-…" instance.
	if err := host.ValidateDisplayName(hosts.lastReq.Name); err != nil {
		t.Errorf("name = %q: %v", hosts.lastReq.Name, err)
	}
	if !strings.HasPrefix(hosts.lastReq.Name, "host-") {
		t.Errorf("name = %q, want a server-assigned host- name", hosts.lastReq.Name)
	}
	if hosts.lastBy != "user@example.com" {
		t.Errorf("createdBy = %q, want the principal email", hosts.lastBy)
	}
	// Provenance: a legacy client asked for this host by name, so it is the
	// caller's. cvdr and OmniLab ATS are the callers here, so stamping it any
	// other way would draw exactly the CI integrations' hosts into the fleet's
	// arithmetic, counted as inventory and eligible to be reclaimed.
	if hosts.lastOrigin != host.OriginUser {
		t.Errorf("origin = %q, want %q", hosts.lastOrigin, host.OriginUser)
	}

	// Response is an async Operation referencing the new host ID.
	var op coapi.Operation
	mustDecode(t, rec, &op)
	if op.Done {
		t.Error("operation should not be done immediately after async create")
	}
	if op.Name != "create.host-123" {
		t.Errorf("operation name = %q, want create.host-123", op.Name)
	}
}

func TestCreateHost_PinModePreservesLiteralZone(t *testing.T) {
	t.Parallel()
	hosts := &fakeHostService{createFn: func(_ context.Context, _ host.CreateRequest, _ string) (*host.Host, error) {
		return &host.Host{ID: "host-123", Status: host.StatusPending}, nil
	}}
	h := newTestHandler(hosts, nil).WithV1ZoneHintPolicy(func(context.Context) (bool, error) {
		return false, nil
	})

	rec := serve(t, h, http.MethodPost, "/v1/zones/us-central1-a/hosts",
		`{"host_instance":{"gcp":{"machine_type":"n1-standard-4"}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if hosts.lastReq.Zone != "us-central1-a" || !hosts.lastReq.ZonePinned {
		t.Errorf("zone = %q pinned=%v, want a literal us-central1-a pin",
			hosts.lastReq.Zone, hosts.lastReq.ZonePinned)
	}
	if hosts.lastReq.LocationHint != "" {
		t.Errorf("location hint = %q, want empty in pin mode", hosts.lastReq.LocationHint)
	}
}

func TestCreateHost_HintPolicyErrorDefaultsToHint(t *testing.T) {
	t.Parallel()
	hosts := &fakeHostService{createFn: func(_ context.Context, _ host.CreateRequest, _ string) (*host.Host, error) {
		return &host.Host{ID: "host-123", Status: host.StatusPending}, nil
	}}
	h := newTestHandler(hosts, nil).WithV1ZoneHintPolicy(func(context.Context) (bool, error) {
		return false, errors.New("spanner unavailable")
	})

	rec := serve(t, h, http.MethodPost, "/v1/zones/z-a/hosts",
		`{"host_instance":{"gcp":{"machine_type":"n1-standard-4"}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if hosts.lastReq.LocationHint != "z-a" || hosts.lastReq.ZonePinned {
		t.Errorf("req = %+v, want hint mode when the policy read fails", hosts.lastReq)
	}
}

// TestCreateHost_CarriesShapeSpec pins the min_cpu_platform and
// boot_disk_size_gb mapping. These two used to be decoded and then dropped on
// the floor, so a cvdr user got no error, only a different VM. The capture is
// the regression gate.
func TestCreateHost_CarriesShapeSpec(t *testing.T) {
	t.Parallel()
	hosts := &fakeHostService{createFn: func(_ context.Context, _ host.CreateRequest, _ string) (*host.Host, error) {
		return &host.Host{ID: "host-123", Status: host.StatusPending}, nil
	}}
	h := newTestHandler(hosts, nil)

	body := `{"host_instance":{"gcp":{"machine_type":"n1-standard-4","min_cpu_platform":"Intel Cascade Lake","boot_disk_size_gb":750}}}`
	rec := serve(t, h, http.MethodPost, "/v1/zones/us-central1-a/hosts", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if hosts.lastReq.MinCPUPlatform != "Intel Cascade Lake" {
		t.Errorf("min cpu platform = %q, want Intel Cascade Lake carried through", hosts.lastReq.MinCPUPlatform)
	}
	if hosts.lastReq.BootDiskSizeGB != 750 {
		t.Errorf("boot disk = %d GB, want the requested 750 carried through", hosts.lastReq.BootDiskSizeGB)
	}
}

// TestCreateHost_MultipleAcceleratorsRejected pins the accurate refusal: CCO
// models one accelerator shape, and this mapping used to truncate a longer
// list to its first element silently.
func TestCreateHost_MultipleAcceleratorsRejected(t *testing.T) {
	t.Parallel()
	hosts := &fakeHostService{createFn: func(_ context.Context, _ host.CreateRequest, _ string) (*host.Host, error) {
		t.Error("Create must not be called for a request CCO cannot honor")
		return nil, errors.New("unreachable")
	}}
	h := newTestHandler(hosts, nil)

	body := `{"host_instance":{"gcp":{"machine_type":"n1-standard-4","accelerator_configs":[` +
		`{"accelerator_type":"nvidia-tesla-t4","accelerator_count":1},` +
		`{"accelerator_type":"nvidia-tesla-p100","accelerator_count":1}]}}}`
	rec := serve(t, h, http.MethodPost, "/v1/zones/z/hosts", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
}

// A host image the create cannot boot answers in upstream's form: GCE's own
// status and message when GCE refused the image, as upstream's synchronous
// insert does, and the unhandled-error envelope for a refusal only CCO makes.
func TestCreateHost_ImageRefusalMatchesUpstream(t *testing.T) {
	t.Parallel()
	const gceMsg = "The resource 'projects/p/global/images/family/cco-host' was not found"
	tests := []struct {
		name string
		err  error
		want string
		code int
	}{
		{
			name: "gce refused the image",
			err: &hostconfig.ImageUnavailableError{
				MachineFamily: "n1", ImageFamily: "cco-host", Project: "p",
				Reason: hostconfig.ImageNotFound, Detail: gceMsg, Status: http.StatusNotFound,
			},
			code: http.StatusNotFound,
			want: `{"code":404,"error":"` + gceMsg + `"}`,
		},
		{
			name: "cco refused the image",
			err: &hostconfig.ImageUnavailableError{
				MachineFamily: "c4a", ImageFamily: "cco-host", Project: "p",
				Reason: hostconfig.ImageArchMismatch, Detail: "image is x86, c4a is arm",
			},
			code: http.StatusInternalServerError,
			want: `{"code":0,"error":"Internal Server Error"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hosts := &fakeHostService{createFn: func(context.Context, host.CreateRequest, string) (*host.Host, error) {
				return nil, fmt.Errorf("creating host: %w", tt.err)
			}}
			rec := serve(t, newTestHandler(hosts, nil), http.MethodPost, "/v1/zones/z/hosts",
				`{"host_instance":{"gcp":{"machine_type":"n1-standard-4"}}}`)
			if rec.Code != tt.code {
				t.Errorf("status = %d, want %d", rec.Code, tt.code)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tt.want {
				t.Errorf("body = %s, want %s", got, tt.want)
			}
		})
	}
}

// assertGCPCreateFence pins the gcp branch's fence fields. The docker shape's
// TTL never leaks (TTL stays with operator policy, zero here), while the
// provenance pair rides both branches: the host's CVDs arrive through the HO
// proxy with no device rows, so it must be census-exempt and fenced from
// automatic placement. listHosts plus the proxy's CVD-create guard key
// on ForeignCVDs, so a create without it is invisible and unusable to the
// client that made it.
func assertGCPCreateFence(t *testing.T, req host.CreateRequest) {
	t.Helper()
	if req.TTLHours != 0 {
		t.Errorf("ttl = %dh, want 0 (operator policy, not the docker default)", req.TTLHours)
	}
	if !req.Exclusive || !req.ForeignCVDs {
		t.Errorf("exclusive=%v foreign_cvds=%v, want both true (census exemption + placement fence)",
			req.Exclusive, req.ForeignCVDs)
	}
}

// TestCreateHost_DockerShape pins the mapping OmniLab ATS depends on: a
// docker-only body (its hardcoded create shape) compiles to the default GCE
// shape. The values are asserted as literals, not the constants: a test
// comparing a constant to itself cannot fail.
func TestCreateHost_DockerShape(t *testing.T) {
	t.Parallel()
	hosts := &fakeHostService{createFn: func(_ context.Context, _ host.CreateRequest, _ string) (*host.Host, error) {
		return &host.Host{ID: "host-123", Status: host.StatusPending}, nil
	}}
	h := newTestHandler(hosts, nil)

	body := `{"host_instance":{"docker":{"image_name":"cuttlefish-orchestration:latest"}}}`
	rec := serve(t, h, http.MethodPost, "/v1/zones/us-central1-a/hosts", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	if hosts.lastReq.MachineType != "n1-standard-8" {
		t.Errorf("machine type = %q, want the compiled n1-standard-8 default", hosts.lastReq.MachineType)
	}
	if hosts.lastReq.TTLHours != 12 {
		t.Errorf("ttl = %dh, want the compiled 12h default", hosts.lastReq.TTLHours)
	}
	// The survival fence: Exclusive keeps automatic placement and the warm
	// walk off the host; MaxDevices narrows explicit targeting.
	if !hosts.lastReq.Exclusive {
		t.Error("exclusive = false, want true (the placement fence)")
	}
	if hosts.lastReq.MaxDevices != 1 {
		t.Errorf("max devices = %d, want 1", hosts.lastReq.MaxDevices)
	}
	if !hosts.lastReq.ForeignCVDs {
		t.Error("foreign_cvds = false, want true (the census exemption)")
	}
	// The zone token compiles the same way as the gcp path: hint by default.
	if hosts.lastReq.LocationHint != "us-central1-a" || hosts.lastReq.ZonePinned {
		t.Errorf("req = %+v, want hint-mode zone mapping", hosts.lastReq)
	}
	// image_name maps to nothing: no GPU, no shape spec beyond the defaults.
	if hosts.lastReq.GPUType != "" || hosts.lastReq.GPUCount != 0 || hosts.lastReq.BootDiskSizeGB != 0 {
		t.Errorf("req = %+v, want no GPU and the default boot disk", hosts.lastReq)
	}
	if err := host.ValidateDisplayName(hosts.lastReq.Name); err != nil {
		t.Errorf("name = %q: %v", hosts.lastReq.Name, err)
	}
}

// TestCreateHost_DockerShapeFromPolicy pins that the operator policy, when
// wired, overrides both compiled defaults.
func TestCreateHost_DockerShapeFromPolicy(t *testing.T) {
	t.Parallel()
	hosts := &fakeHostService{createFn: func(_ context.Context, _ host.CreateRequest, _ string) (*host.Host, error) {
		return &host.Host{ID: "host-123", Status: host.StatusPending}, nil
	}}
	h := newTestHandler(hosts, nil).WithDockerHostPolicy(func(context.Context) (hostconfig.DockerHost, error) {
		return hostconfig.DockerHost{MachineType: "n2-standard-16", TTLHours: 48, IsSet: true}, nil
	})

	rec := serve(t, h, http.MethodPost, "/v1/zones/z/hosts",
		`{"host_instance":{"docker":{"image_name":"whatever"}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if hosts.lastReq.MachineType != "n2-standard-16" || hosts.lastReq.TTLHours != 48 {
		t.Errorf("shape = %q/%dh, want the operator's n2-standard-16/48h",
			hosts.lastReq.MachineType, hosts.lastReq.TTLHours)
	}
}

// TestCreateHost_DockerPolicyErrorDefaults pins the degrade path: a failing
// policy read must land on the compiled defaults, not fail the create.
func TestCreateHost_DockerPolicyErrorDefaults(t *testing.T) {
	t.Parallel()
	hosts := &fakeHostService{createFn: func(_ context.Context, _ host.CreateRequest, _ string) (*host.Host, error) {
		return &host.Host{ID: "host-123", Status: host.StatusPending}, nil
	}}
	h := newTestHandler(hosts, nil).WithDockerHostPolicy(func(context.Context) (hostconfig.DockerHost, error) {
		return hostconfig.DockerHost{}, errors.New("spanner unavailable")
	})

	rec := serve(t, h, http.MethodPost, "/v1/zones/z/hosts",
		`{"host_instance":{"docker":{"image_name":"img"}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if hosts.lastReq.MachineType != "n1-standard-8" || hosts.lastReq.TTLHours != 12 {
		t.Errorf("shape = %q/%dh, want the compiled defaults when the policy read fails",
			hosts.lastReq.MachineType, hosts.lastReq.TTLHours)
	}
}

// TestCreateHost_PrefersGCPWhenBothPresent pins the precedence: a body
// carrying both sub-messages is a gcp create, byte-identical to one without
// the docker block.
func TestCreateHost_PrefersGCPWhenBothPresent(t *testing.T) {
	t.Parallel()
	hosts := &fakeHostService{createFn: func(_ context.Context, _ host.CreateRequest, _ string) (*host.Host, error) {
		return &host.Host{ID: "host-123", Status: host.StatusPending}, nil
	}}
	h := newTestHandler(hosts, nil)

	body := `{"host_instance":{"gcp":{"machine_type":"n1-standard-4"},"docker":{"image_name":"img"}}}`
	rec := serve(t, h, http.MethodPost, "/v1/zones/z/hosts", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if hosts.lastReq.MachineType != "n1-standard-4" {
		t.Errorf("machine type = %q, want the gcp block's n1-standard-4", hosts.lastReq.MachineType)
	}
	if hosts.lastReq.TTLHours != 0 || hosts.lastReq.MaxDevices != 4 {
		t.Errorf("req = %+v, want the gcp mapping (no docker TTL, legacy default capacity)", hosts.lastReq)
	}
}

// TestCreateHost_GCPProvenanceAdmitsLegacyFlow drives the whole legacy
// create→list→create-CVD flow off the createHost mapping instead of
// hand-built rows: whatever provenance the gcp branch actually stamps is what
// the listing filter and the proxy's CVD-create guard judge. Hand-built
// fixtures once encoded the intended provenance while the production branch
// left it unstamped, so cvdr could neither see its own host nor create a CVD on
// it, and this boundary is where that failure hid.
func TestCreateHost_GCPProvenanceAdmitsLegacyFlow(t *testing.T) {
	t.Parallel()
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"name":"op-1"}`)
	}))
	defer ho.Close()
	hoURL, _ := url.Parse(ho.URL)
	hoPort, _ := strconv.ParseInt(hoURL.Port(), 10, 64)

	svc := &fakeHostService{}
	created := func() *host.Host {
		return &host.Host{
			ID: "host-123", Status: host.StatusRunning,
			InternalIP: hoURL.Hostname(), HostOrchestratorPort: hoPort,
			Exclusive:   svc.lastReq.Exclusive,
			ForeignCVDs: svc.lastReq.ForeignCVDs,
		}
	}
	svc.createFn = func(context.Context, host.CreateRequest, string) (*host.Host, error) {
		return created(), nil
	}
	svc.listFn = func(context.Context, host.ListFilter) ([]*host.Host, error) {
		return []*host.Host{created()}, nil
	}
	svc.getFn = func(context.Context, string) (*host.Host, error) {
		return created(), nil
	}
	h := newTestHandler(svc, nil)

	rec := serve(t, h, http.MethodPost, "/v1/zones/z/hosts",
		`{"host_instance":{"gcp":{"machine_type":"n1-standard-4"}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	rec = serve(t, h, http.MethodGet, "/v1/zones/z/hosts", "")
	var list coapi.ListHostsResponse
	mustDecode(t, rec, &list)
	if len(list.Items) != 1 {
		t.Fatalf("listed %d hosts, want the created host visible to its creator", len(list.Items))
	}

	rec = serve(t, h, http.MethodPost, "/v1/zones/z/hosts/host-123/cvds", `{}`)
	if rec.Code == http.StatusForbidden {
		t.Fatalf("CVD create refused on the host this API just created: %s", rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("CVD create status = %d, want 200 via the proxy", rec.Code)
	}
}

func TestCreateHost_MissingInstanceShape(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{}`, `{"host_instance":{}}`} {
		h := newTestHandler(&fakeHostService{}, nil)
		rec := serve(t, h, http.MethodPost, "/v1/zones/z/hosts", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400 (neither gcp nor docker)", body, rec.Code)
		}
	}
}

func TestCreateHost_BadBody(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeHostService{}, nil)
	rec := serve(t, h, http.MethodPost, "/v1/zones/z/hosts", `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCreateHost_BodyTooLarge(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeHostService{}, nil)

	// Valid JSON that only exceeds the 1 MiB body cap.
	body := `{"host_instance":{"gcp":{"machine_type":"` + strings.Repeat("a", 1<<20) + `"}}}`
	rec := serve(t, h, http.MethodPost, "/v1/zones/z/hosts", body)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	var apiErr coapi.Error
	mustDecode(t, rec, &apiErr)
	if apiErr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("error envelope code = %d, want 413", apiErr.Code)
	}
}

func TestListHosts(t *testing.T) {
	t.Parallel()
	// Both were created through this API, so both carry its provenance.
	hosts := &fakeHostService{listFn: func(_ context.Context, _ host.ListFilter) ([]*host.Host, error) {
		return []*host.Host{
			{
				ID: "h1", MachineType: "n1-standard-4", GPUType: "nvidia-tesla-t4", GPUCount: 2,
				MinCPUPlatform: "Intel Cascade Lake", BootDiskSizeGB: 750, ForeignCVDs: true,
			},
			{ID: "h2", MachineType: "e2-standard-2", ForeignCVDs: true},
		}, nil
	}}
	h := newTestHandler(hosts, nil)

	rec := serve(t, h, http.MethodGet, "/v1/zones/us-central1-a/hosts", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// The token partitions by requested OR physical zone, so a failed-over
	// host stays visible under its creation token.
	if hosts.lastFiltr.RequestedOrPhysicalZone == nil || *hosts.lastFiltr.RequestedOrPhysicalZone != "us-central1-a" {
		t.Errorf("list filter = %+v, want requested-or-physical us-central1-a", hosts.lastFiltr)
	}
	if hosts.lastFiltr.Zone != nil {
		t.Errorf("physical-only zone filter = %v, want unset", hosts.lastFiltr.Zone)
	}

	var resp coapi.ListHostsResponse
	mustDecode(t, rec, &resp)
	if len(resp.Items) != 2 {
		t.Fatalf("hosts = %d, want 2", len(resp.Items))
	}
	// Legacy host name is the CCO host ID; GPU maps to an accelerator config.
	if resp.Items[0].Name != "h1" || resp.Items[0].GCP.MachineType != "n1-standard-4" {
		t.Errorf("host[0] = %+v", resp.Items[0])
	}
	if len(resp.Items[0].GCP.AcceleratorConfigs) != 1 ||
		resp.Items[0].GCP.AcceleratorConfigs[0].AcceleratorType != "nvidia-tesla-t4" ||
		resp.Items[0].GCP.AcceleratorConfigs[0].AcceleratorCount != 2 {
		t.Errorf("host[0] accelerators = %+v", resp.Items[0].GCP.AcceleratorConfigs)
	}
	// No GPU => no accelerator configs.
	if resp.Items[1].GCP.AcceleratorConfigs != nil {
		t.Errorf("host[1] should have no accelerators, got %+v", resp.Items[1].GCP.AcceleratorConfigs)
	}
	// The shape spec echoes: the platform verbatim, the boot disk as the
	// EFFECTIVE size. A host created without the setting reports the default,
	// matching upstream, which reports what the VM has.
	if resp.Items[0].GCP.MinCPUPlatform != "Intel Cascade Lake" || resp.Items[0].GCP.BootDiskSizeGB != 750 {
		t.Errorf("host[0] shape = %q/%d GB, want Intel Cascade Lake/750", resp.Items[0].GCP.MinCPUPlatform, resp.Items[0].GCP.BootDiskSizeGB)
	}
	if resp.Items[1].GCP.BootDiskSizeGB != 400 {
		t.Errorf("host[1] boot disk = %d GB, want the 400 GB default echoed as the effective size",
			resp.Items[1].GCP.BootDiskSizeGB)
	}
}

// A legacy client lists hosts and then creates a CVD on one of them, and that
// create is refused on an orchestrator-owned host. Listing it would offer a
// choice that cannot be taken, so the listing is confined to this API's own
// hosts.
func TestListHosts_OmitsOrchestratorOwnedHosts(t *testing.T) {
	t.Parallel()
	hosts := &fakeHostService{listFn: func(_ context.Context, _ host.ListFilter) ([]*host.Host, error) {
		return []*host.Host{
			{ID: "legacy-1", MachineType: "n1-standard-4", ForeignCVDs: true},
			{ID: "cco-1", MachineType: "n1-standard-8"},
			{ID: "legacy-2", MachineType: "e2-standard-2", ForeignCVDs: true},
		}, nil
	}}

	rec := serve(t, newTestHandler(hosts, nil), http.MethodGet, "/v1/zones/us-central1-a/hosts", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp coapi.ListHostsResponse
	mustDecode(t, rec, &resp)
	got := make([]string, 0, len(resp.Items))
	for _, item := range resp.Items {
		got = append(got, item.Name)
	}
	// An exact set, not an absence check: asserting only that "cco-1" is
	// missing would also pass on an empty list.
	if !slices.Equal(got, []string{"legacy-1", "legacy-2"}) {
		t.Errorf("hosts = %v, want the two foreign-CVD hosts in order", got)
	}
}

func TestDeleteHost(t *testing.T) {
	t.Parallel()
	var deletedID string
	hosts := &fakeHostService{deleteFn: func(_ context.Context, id string) error {
		deletedID = id
		return nil
	}}
	h := newTestHandler(hosts, nil)

	rec := serve(t, h, http.MethodDelete, "/v1/zones/z/hosts/host-123", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if deletedID != "host-123" {
		t.Errorf("deleted id = %q, want host-123", deletedID)
	}
	var op coapi.Operation
	mustDecode(t, rec, &op)
	if op.Name != "delete.host-123" || op.Done {
		t.Errorf("operation = %+v, want {delete.host-123, done:false}", op)
	}
}

// The delete is provenance-blind on purpose. It is the other half of the /v1
// census trap, decided the other way. /v1 hands out no orchestrator-owned host
// ids, and a caller holding one out of band can already DELETE /v2/hosts/{id}
// through the same service, so a guard here would narrow one of two identical
// doors while costing a read on a byte-parity path.
//
// The host read is the witness: a provenance check has to look the host up, so
// this goes red the moment one is added, instead of letting the decision be
// reversed with nothing to notice it.
func TestDeleteHost_TakesAnOrchestratorOwnedHostID(t *testing.T) {
	t.Parallel()
	var deletedID string
	hosts := &fakeHostService{
		deleteFn: func(_ context.Context, id string) error {
			deletedID = id
			return nil
		},
		getFn: func(_ context.Context, id string) (*host.Host, error) {
			t.Errorf("deleteHost read host %q; see the decision recorded on deleteHost", id)
			return &host.Host{ID: id}, nil
		},
	}

	rec := serve(t, newTestHandler(hosts, nil), http.MethodDelete, "/v1/zones/z/hosts/cco-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if deletedID != "cco-1" {
		t.Errorf("deleted id = %q, want cco-1", deletedID)
	}
}

func TestDeleteHost_NotFoundMapsTo404(t *testing.T) {
	t.Parallel()
	hosts := &fakeHostService{deleteFn: func(_ context.Context, _ string) error {
		return host.ErrNotFound
	}}
	h := newTestHandler(hosts, nil)

	rec := serve(t, h, http.MethodDelete, "/v1/zones/z/hosts/missing", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var apiErr coapi.Error
	mustDecode(t, rec, &apiErr)
	if apiErr.Code != http.StatusNotFound {
		t.Errorf("error code = %d, want 404", apiErr.Code)
	}
}

func mustDecode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decoding response %q: %v", rec.Body.String(), err)
	}
}
