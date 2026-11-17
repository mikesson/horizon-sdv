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

package main

import (
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"

	apiv1 "github.com/google/cloud-android-orchestration/api/v1"
	"github.com/google/cloud-android-orchestration/pkg/app/accounts"
	apperr "github.com/google/cloud-android-orchestration/pkg/app/errors"
	"github.com/google/cloud-android-orchestration/pkg/app/instances"
)

// The scenario vocabulary. The upstream fakes below and the replay fakes in
// internal/compat/legacy/parity_test.go answer with these same values, so a
// recording pins serialization (field order, omitted-vs-empty fields, headers,
// the trailing newline). Any byte difference is then a real divergence and
// never value noise. Operation and host names are server-generated in
// production, so their values differ per run; here they are fixed inputs on
// both sides for that reason.
const (
	zoneToken   = "us-central1-b"
	otherZone   = "europe-west4-a"
	hostFull    = "host-56f0e8a1"
	hostMinimal = "host-9c2d11b4"
	hostMissing = "host-000000"
	basicAuth   = "Basic dXNlcjo="
	stunServer  = "stun:stun.l.google.com:19302"
)

var (
	authHeaders     = map[string]string{"Authorization": basicAuth}
	authJSONHeaders = map[string]string{"Authorization": basicAuth, "Content-Type": "application/json"}
)

// fullHost exercises every GCPInstance field: accelerators, CPU platform,
// non-default boot disk.
func fullHost() *apiv1.HostInstance {
	return &apiv1.HostInstance{
		Name: hostFull,
		GCP: &apiv1.GCPInstance{
			MachineType:    "n1-standard-4",
			MinCPUPlatform: "Intel Cascade Lake",
			AcceleratorConfigs: []*apiv1.AcceleratorConfig{
				{AcceleratorCount: 2, AcceleratorType: "nvidia-tesla-t4"},
			},
			BootDiskSizeGB: 750,
		},
	}
}

// minimalHost exercises the other arm of every optional field: no
// accelerators (omitted), no CPU platform (present but empty, because the
// field has no omitempty), the default boot disk.
func minimalHost() *apiv1.HostInstance {
	return &apiv1.HostInstance{
		Name: hostMinimal,
		GCP: &apiv1.GCPInstance{
			MachineType:    "n1-standard-8",
			BootDiskSizeGB: 400,
		},
	}
}

// parityCase is one corpus entry: the request to issue and the upstream fake
// behavior that produces the canonical response.
type parityCase struct {
	name    string
	method  string
	target  string
	headers map[string]string
	body    string
	im      *fakeIM
}

// corpus returns every parity case. echoHO is the deterministic Host
// Orchestrator stand-in the proxy cases forward to.
func corpus(echoHO *url.URL) []parityCase {
	return slices.Concat(hostCases(), operationCases(), proxyCases(echoHO))
}

func hostCases() []parityCase {
	zones := &apiv1.ListZonesResponse{Items: []*apiv1.Zone{{Name: zoneToken}, {Name: otherZone}}}
	return []parityCase{
		{
			name: "zones", method: http.MethodGet, target: "/v1/zones", headers: authHeaders,
			im: &fakeIM{listZones: func() (*apiv1.ListZonesResponse, error) { return zones, nil }},
		},
		{
			// The unclassified-error dispatch. The error is not an AppError,
			// so upstream masks it as {"code":0,"error":"Internal Server Error"}.
			name: "zones-unclassified-error", method: http.MethodGet, target: "/v1/zones", headers: authHeaders,
			im: &fakeIM{listZones: func() (*apiv1.ListZonesResponse, error) {
				return nil, errors.New("spanner unavailable: session pool exhausted")
			}},
		},
		{
			name: "config", method: http.MethodGet, target: "/v1/config", headers: authHeaders,
			im: &fakeIM{},
		},
		{
			name: "create-host", method: http.MethodPost, target: "/v1/zones/" + zoneToken + "/hosts",
			headers: authJSONHeaders,
			body:    `{"host_instance":{"gcp":{"machine_type":"n1-standard-4"}}}`,
			im: &fakeIM{createHost: func(string, *apiv1.CreateHostRequest) (*apiv1.Operation, error) {
				return &apiv1.Operation{Name: "create." + hostFull, Done: false}, nil
			}},
		},
		{
			name: "create-host-bad-json", method: http.MethodPost, target: "/v1/zones/" + zoneToken + "/hosts",
			headers: authJSONHeaders,
			body:    `{not json`,
			im:      &fakeIM{},
		},
		{
			name: "list-hosts", method: http.MethodGet, target: "/v1/zones/" + zoneToken + "/hosts",
			headers: authHeaders,
			im: &fakeIM{listHosts: func(string) (*apiv1.ListHostsResponse, error) {
				return &apiv1.ListHostsResponse{Items: []*apiv1.HostInstance{fullHost(), minimalHost()}}, nil
			}},
		},
		{
			name:   "list-hosts-bad-max-results",
			method: http.MethodGet, target: "/v1/zones/" + zoneToken + "/hosts?maxResults=abc",
			headers: authHeaders,
			im:      &fakeIM{},
		},
		{
			name:   "delete-host",
			method: http.MethodDelete, target: "/v1/zones/" + zoneToken + "/hosts/" + hostFull,
			headers: authHeaders,
			im: &fakeIM{deleteHost: func(string, string) (*apiv1.Operation, error) {
				return &apiv1.Operation{Name: "delete." + hostFull, Done: false}, nil
			}},
		},
		{
			name:   "delete-missing-host",
			method: http.MethodDelete, target: "/v1/zones/" + zoneToken + "/hosts/" + hostMissing,
			headers: authHeaders,
			im: &fakeIM{deleteHost: func(string, string) (*apiv1.Operation, error) {
				return nil, apperr.NewNotFoundError("host not found: "+hostMissing, nil)
			}},
		},
		{
			name:   "infra-config",
			method: http.MethodGet, target: "/v1/zones/" + zoneToken + "/hosts/" + hostFull + "/infra_config",
			headers: authHeaders,
			im:      &fakeIM{},
		},
	}
}

func operationCases() []parityCase {
	resolved := func(string, string) (any, error) { return fullHost(), nil }
	return []parityCase{
		{
			name:   "wait-create-done",
			method: http.MethodPost, target: "/v1/zones/" + zoneToken + "/operations/create." + hostFull + "/:wait",
			headers: authJSONHeaders,
			im:      &fakeIM{waitOperation: resolved},
		},
		{
			// Some clients percent-encode the colon verb; both routers must
			// treat %3Await and :wait as the same route.
			name:   "wait-escaped-verb",
			method: http.MethodPost, target: "/v1/zones/" + zoneToken + "/operations/create." + hostFull + "/%3Await",
			headers: authJSONHeaders,
			im:      &fakeIM{waitOperation: resolved},
		},
		{
			// The GCE instance manager's timeout answer, mirrored by the fake:
			// gce.go returns 503 "Wait for operation timed out" when the
			// operation is not DONE within the wait window.
			name:   "wait-timeout",
			method: http.MethodPost, target: "/v1/zones/" + zoneToken + "/operations/create." + hostMinimal + "/:wait",
			headers: authJSONHeaders,
			im: &fakeIM{waitOperation: func(string, string) (any, error) {
				return nil, apperr.NewServiceUnavailableError("Wait for operation timed out", nil)
			}},
		},
		{
			name:   "wait-delete-done",
			method: http.MethodPost, target: "/v1/zones/" + zoneToken + "/operations/delete." + hostFull + "/:wait",
			headers: authJSONHeaders,
			im: &fakeIM{waitOperation: func(string, string) (any, error) {
				return struct{}{}, nil
			}},
		},
	}
}

func proxyCases(echoHO *url.URL) []parityCase {
	proxyIM := &fakeIM{hostClient: proxyHostClient{httputil.NewSingleHostReverseProxy(echoHO)}}
	return []parityCase{
		{
			name:   "proxy-echo",
			method: http.MethodGet, target: "/v1/zones/" + zoneToken + "/hosts/" + hostFull + "/cvds?foo=bar",
			headers: authHeaders,
			im:      proxyIM,
		},
		{
			// The trailing-slash root, which is the readiness probe OmniLab
			// ATS and cvdr both poll. The catch-all must deliver "/" to the HO.
			name:   "proxy-root",
			method: http.MethodGet, target: "/v1/zones/" + zoneToken + "/hosts/" + hostFull + "/",
			headers: authHeaders,
			im:      proxyIM,
		},
	}
}

// fakeIM implements instances.Manager for exactly the calls a corpus case
// makes; anything else panics so a case exercising an unexpected path fails
// loudly at record time.
type fakeIM struct {
	listZones     func() (*apiv1.ListZonesResponse, error)
	createHost    func(zone string, req *apiv1.CreateHostRequest) (*apiv1.Operation, error)
	listHosts     func(zone string) (*apiv1.ListHostsResponse, error)
	deleteHost    func(zone, name string) (*apiv1.Operation, error)
	waitOperation func(zone, name string) (any, error)
	hostClient    instances.HostClient
}

func (f *fakeIM) ListZones() (*apiv1.ListZonesResponse, error) {
	if f.listZones == nil {
		panic("corpus case does not stub ListZones")
	}
	return f.listZones()
}

func (f *fakeIM) CreateHost(zone string, req *apiv1.CreateHostRequest, _ accounts.User) (*apiv1.Operation, error) {
	if f.createHost == nil {
		panic("corpus case does not stub CreateHost")
	}
	return f.createHost(zone, req)
}

func (f *fakeIM) ListHosts(zone string, _ accounts.User, _ *instances.ListHostsRequest) (*apiv1.ListHostsResponse, error) {
	if f.listHosts == nil {
		panic("corpus case does not stub ListHosts")
	}
	return f.listHosts(zone)
}

func (f *fakeIM) DeleteHost(zone string, _ accounts.User, name string) (*apiv1.Operation, error) {
	if f.deleteHost == nil {
		panic("corpus case does not stub DeleteHost")
	}
	return f.deleteHost(zone, name)
}

func (f *fakeIM) WaitOperation(zone string, _ accounts.User, name string) (any, error) {
	if f.waitOperation == nil {
		panic("corpus case does not stub WaitOperation")
	}
	return f.waitOperation(zone, name)
}

func (f *fakeIM) GetHostClient(_, _ string) (instances.HostClient, error) {
	if f.hostClient == nil {
		panic("corpus case does not stub GetHostClient")
	}
	return f.hostClient, nil
}

// proxyHostClient satisfies instances.HostClient with just the reverse proxy;
// the corpus never uses the typed Get/Post helpers.
type proxyHostClient struct {
	rp *httputil.ReverseProxy
}

func (c proxyHostClient) Get(_, _ string, _ *instances.HostResponse) (int, error) {
	panic("corpus case does not use HostClient.Get")
}

func (c proxyHostClient) Post(_, _ string, _ any, _ *instances.HostResponse) (int, error) {
	panic("corpus case does not use HostClient.Post")
}

func (c proxyHostClient) GetReverseProxy() *httputil.ReverseProxy {
	return c.rp
}
