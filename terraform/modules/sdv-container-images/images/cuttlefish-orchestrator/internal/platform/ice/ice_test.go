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

package ice

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/platform/turn"
)

const (
	testSecret = "shared-secret"
	testEmail  = "user@example.com"
)

var (
	testSTUN   = []string{"stun:stun.example:19302", "stun:stun1.example:19302"}
	testClient = []string{"turns:relay.example.com:443", "turn:203.0.113.7:3478"}
	testDevice = []string{"turn:10.0.0.7:3478"}
	testSubj   = turn.DeviceSubject("dev-1")
)

// relayed is the composer for a deployment whose media never meets the internet
// at the host: relays on both addresses, clients pinned, devices offline.
func relayed() *Composer {
	return NewComposer(turn.NewCredentialService(testSecret, time.Hour), Options{
		STUNServers:       testSTUN,
		ClientServers:     testClient,
		DeviceServers:     testDevice,
		PinClientsToRelay: true,
	})
}

// direct is the composer for a public deployment: no relay at all, and hosts
// carry external addresses.
func direct() *Composer {
	return NewComposer(nil, Options{
		STUNServers:          testSTUN,
		DevicesReachInternet: true,
	})
}

func TestComposePinsOnlyWhereMediaIsRelayed(t *testing.T) {
	t.Parallel()
	if got := relayed().Compose(testEmail, testSubj).Policy; got != PolicyRelay {
		t.Errorf("policy = %q, want %q where the client cannot reach the host", got, PolicyRelay)
	}
	if got := direct().Compose(testEmail, testSubj).Policy; got != PolicyAll {
		t.Errorf("policy = %q, want %q where a direct pair can form", got, PolicyAll)
	}
}

// STUN goes only to a leg that can both reach it and use it: not to a pinned
// client, which gathers no reflexive candidate whatever it is given, and not to
// a host without an external address, where the query can only time out.
func TestComposeOffersSTUNOnlyWhereItCanBeUsed(t *testing.T) {
	t.Parallel()
	relay := relayed().Compose(testEmail, testSubj)
	if urls := allURLs(relay.ClientServers); slices.ContainsFunc(urls, isSTUN) {
		t.Errorf("pinned client leg carries STUN: %v", urls)
	}
	if urls := allURLs(relay.DeviceServers); slices.ContainsFunc(urls, isSTUN) {
		t.Errorf("device leg carries STUN with no route to it: %v", urls)
	}

	public := direct().Compose(testEmail, testSubj)
	if urls := allURLs(public.ClientServers); !slices.Equal(urls, testSTUN) {
		t.Errorf("client leg = %v, want the STUN servers", urls)
	}
	if urls := allURLs(public.DeviceServers); !slices.Equal(urls, testSTUN) {
		t.Errorf("device leg = %v, want the STUN servers a public host can reach", urls)
	}
}

// The legs name the same relays by their two addresses. Swapping them is the
// failure this package exists to prevent: it shows up as a connection that
// simply never establishes, with nothing logged at either end.
func TestComposeKeepsTheLegsOnTheirOwnAddresses(t *testing.T) {
	t.Parallel()
	cfg := relayed().Compose(testEmail, testSubj)

	if got := allURLs(cfg.ClientServers); !slices.Equal(got, testClient) {
		t.Errorf("client leg = %v, want the external addresses %v", got, testClient)
	}
	if got := allURLs(cfg.DeviceServers); !slices.Equal(got, testDevice) {
		t.Errorf("device leg = %v, want the internal addresses %v", got, testDevice)
	}
}

// One mint serves both legs. A credential verifies against the shared secret
// alone, so two mints would be two allocations' worth of state for nothing,
// and would make a session impossible to attribute to one caller.
func TestComposeSharesOneCredentialAcrossBothLegs(t *testing.T) {
	t.Parallel()
	cfg := relayed().Compose(testEmail, testSubj)

	client, device := cfg.ClientServers[0], cfg.DeviceServers[0]
	if client.Username != device.Username || client.Credential != device.Credential {
		t.Fatalf("legs carry different credentials: client %q, device %q", client.Username, device.Username)
	}
	parsed, err := turn.ParseUsername(client.Username)
	if err != nil {
		t.Fatalf("ParseUsername(%q) error = %v", client.Username, err)
	}
	if parsed.Subject != testSubj {
		t.Errorf("subject = %+v, want %+v", parsed.Subject, testSubj)
	}
	if parsed.Email != testEmail {
		t.Errorf("email = %q, want the authenticated caller", parsed.Email)
	}
}

// A STUN entry must not carry empty credential strings: a browser reads
// username/credential on any entry, and an empty pair on a stun: URI is a
// TURN configuration that can never authenticate.
func TestSTUNEntryCarriesNoCredentials(t *testing.T) {
	t.Parallel()
	entry := direct().Compose(testEmail, testSubj).ClientServers[0]
	if entry.Username != "" || entry.Credential != "" {
		t.Errorf("STUN entry carries credentials: %+v", entry)
	}

	b, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "username") || strings.Contains(string(b), "credential") {
		t.Errorf("STUN entry serializes credential fields: %s", b)
	}
}

// Both legs are lists a client iterates. A nil slice marshals to JSON null,
// which is not one.
func TestEmptyLegsSerializeAsArrays(t *testing.T) {
	t.Parallel()
	bare := NewComposer(nil, Options{})

	b, err := json.Marshal(bare.Compose(testEmail, testSubj))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"client_servers":[]`, `"device_servers":[]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
}

// Nothing is minted where there is no relay to present a credential to, even
// with a secret configured. A deployment carrying one but no servers answers
// exactly as it did before the secret was set.
func TestNoServersMintsNothing(t *testing.T) {
	t.Parallel()
	c := NewComposer(turn.NewCredentialService(testSecret, time.Hour), Options{STUNServers: testSTUN})

	if c.Mints() {
		t.Error("Mints() is true with no server to present a credential to")
	}
	cfg := c.Compose(testEmail, testSubj)
	if len(cfg.ClientServers) != 1 || cfg.ClientServers[0].Username != "" {
		t.Errorf("client leg = %+v, want STUN alone", cfg.ClientServers)
	}
}

// The legacy wire admits one list, so both legs ride it and each end finds its
// own reachable URI. STUN rides it in every topology, unlike the client leg
// above: that surface has no transport policy to tell a client not to bother.
func TestFlattenCarriesBothLegsAndSTUN(t *testing.T) {
	t.Parallel()
	servers := relayed().Flatten(testEmail, turn.HostSubject("host-1"))

	if len(servers) != 2 {
		t.Fatalf("servers = %+v, want STUN plus one credentialed entry", servers)
	}
	if got := servers[0].URLs; !slices.Equal(got, testSTUN) {
		t.Errorf("first entry = %v, want the STUN servers", got)
	}
	want := slices.Concat(testClient, testDevice)
	if got := servers[1].URLs; !slices.Equal(got, want) {
		t.Errorf("turn entry = %v, want client leg then device leg %v", got, want)
	}
	if servers[1].Username == "" || servers[1].Credential == "" {
		t.Error("flattened TURN entry carries no credential")
	}
}

func TestFlattenWithoutTURNIsSTUNAlone(t *testing.T) {
	t.Parallel()
	servers := direct().Flatten(testEmail, turn.HostSubject("host-1"))

	if len(servers) != 1 || !slices.Equal(servers[0].URLs, testSTUN) {
		t.Errorf("servers = %+v, want the STUN entry alone", servers)
	}
}

func allURLs(servers []Server) []string {
	var urls []string
	for _, s := range servers {
		urls = append(urls, s.URLs...)
	}
	return urls
}

func isSTUN(uri string) bool { return strings.HasPrefix(uri, "stun:") }
