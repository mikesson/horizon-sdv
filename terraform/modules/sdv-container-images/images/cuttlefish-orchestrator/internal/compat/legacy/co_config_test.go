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
	"slices"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/compat/legacy/coapi"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/platform/ice"
	"cloud-cuttlefish-orchestrator/internal/platform/turn"
)

const (
	testTURNSecret = "legacy-shared-secret"
	testHostID     = "host-123"
)

// testSTUNServers stands in for the deployment default, which every topology
// serves on this surface.
var testSTUNServers = []string{"stun:stun.l.google.com:19302", "stun:stun1.l.google.com:19302"}

// relayComposer mints against the given legs, as a relay deployment does.
func relayComposer(clientServers, deviceServers []string) *ice.Composer {
	return ice.NewComposer(turn.NewCredentialService(testTURNSecret, 12*time.Hour), ice.Options{
		STUNServers:   testSTUNServers,
		ClientServers: clientServers,
		DeviceServers: deviceServers,
	})
}

func TestGetConfig(t *testing.T) {
	t.Parallel()
	rec := serve(t, newTestHandler(nil, nil), http.MethodGet, "/v1/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var cfg coapi.Config
	mustDecode(t, rec, &cfg)
	if cfg.InstanceManagerType != "GCP" {
		t.Errorf("instance_manager_type = %q, want GCP", cfg.InstanceManagerType)
	}
}

// Without TURN the answer is the upstream default sample, and, as upstream,
// host-independent: nothing is resolved, so no host service is needed.
func TestGetInfraConfig_DefaultsToSTUN(t *testing.T) {
	t.Parallel()
	rec := serve(t, newTestHandler(nil, nil), http.MethodGet, "/v1/zones/z/hosts/h1/infra_config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var infra coapi.InfraConfig
	mustDecode(t, rec, &infra)
	if len(infra.IceServers) != 1 || !slices.Equal(infra.IceServers[0].URLs, testSTUNServers) {
		t.Errorf("default infra_config = %+v, want the public STUN servers", infra.IceServers)
	}
	if infra.IceServers[0].Username != "" || infra.IceServers[0].Credential != "" {
		t.Error("default STUN server must carry no credentials")
	}
}

// A deployment carrying a secret but no relay has nothing to present a
// credential to, so it still answers the way it always has, and, as upstream,
// without resolving the host the path names.
func TestGetInfraConfig_NoServersStaysSTUNOnly(t *testing.T) {
	t.Parallel()
	h := newTestHandlerWithICE(nil, nil, relayComposer(nil, nil))

	rec := serve(t, h, http.MethodGet, "/v1/zones/z/hosts/h1/infra_config", "")
	var infra coapi.InfraConfig
	mustDecode(t, rec, &infra)
	if len(infra.IceServers) != 1 {
		t.Fatalf("ice servers = %+v, want STUN alone", infra.IceServers)
	}
}

// The shape stock cvdr parses: STUN, then one credentialed entry whose URL
// list carries BOTH legs. libhoclient hands the single list it reads here to
// its own peer connection and to the device, so a split would strand one of
// them.
func TestGetInfraConfig_MintsMergedTURNEntry(t *testing.T) {
	t.Parallel()
	hosts := runningHost(t)
	h := newTestHandlerWithICE(hosts, nil, relayComposer(
		[]string{"turns:relay.example.com:443", "turn:203.0.113.7:3478"},
		[]string{"turn:10.0.0.7:3478"}))

	rec := serve(t, h, http.MethodGet, "/v1/zones/z/hosts/"+testHostID+"/infra_config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var infra coapi.InfraConfig
	mustDecode(t, rec, &infra)
	if len(infra.IceServers) != 2 {
		t.Fatalf("ice servers = %+v, want STUN plus one TURN entry", infra.IceServers)
	}

	got := infra.IceServers[1]
	want := []string{"turns:relay.example.com:443", "turn:203.0.113.7:3478", "turn:10.0.0.7:3478"}
	if !slices.Equal(got.URLs, want) {
		t.Errorf("turn urls = %v, want %v (client leg then device leg)", got.URLs, want)
	}
	if got.Credential == "" {
		t.Error("minted TURN entry carries no credential")
	}
}

// The minted username is the whole contract with the relay's authority: it has
// to name this host, this caller, and an expiry a workday cannot outlive.
func TestGetInfraConfig_CredentialNamesTheHost(t *testing.T) {
	t.Parallel()
	h := newTestHandlerWithICE(runningHost(t), nil,
		relayComposer([]string{"turn:203.0.113.7:3478"}, nil))

	rec := serve(t, h, http.MethodGet, "/v1/zones/z/hosts/"+testHostID+"/infra_config", "")
	var infra coapi.InfraConfig
	mustDecode(t, rec, &infra)

	parsed, err := turn.ParseUsername(infra.IceServers[1].Username)
	if err != nil {
		t.Fatalf("ParseUsername(%q) error = %v", infra.IceServers[1].Username, err)
	}
	if parsed.Subject != turn.HostSubject(testHostID) {
		t.Errorf("subject = %+v, want the host from the path", parsed.Subject)
	}
	if parsed.Email != "user@example.com" {
		t.Errorf("email = %q, want the authenticated caller", parsed.Email)
	}
	// Long, because libhoclient reads infra_config once and then holds the
	// session: expiry lands mid-stream, with no path to renew.
	if got := time.Until(parsed.ExpiresAt); got < 11*time.Hour {
		t.Errorf("credential expires in %v, want a workday-length lifetime", got)
	}
}

// A credential minted for a host that does not exist would be refused at its
// first use by a relay that cannot explain itself. Fail here instead.
func TestGetInfraConfig_UnknownHostIsNotFound(t *testing.T) {
	t.Parallel()
	hosts := &fakeHostService{getFn: func(context.Context, string) (*host.Host, error) {
		return nil, host.ErrNotFound
	}}
	h := newTestHandlerWithICE(hosts, nil, relayComposer([]string{"turn:203.0.113.7:3478"}, nil))

	rec := serve(t, h, http.MethodGet, "/v1/zones/z/hosts/nope/infra_config", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func runningHost(t *testing.T) *fakeHostService {
	t.Helper()
	return &fakeHostService{getFn: func(_ context.Context, id string) (*host.Host, error) {
		return &host.Host{ID: id, Status: host.StatusRunning}, nil
	}}
}
