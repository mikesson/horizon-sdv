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

// Package ice composes the ICE configuration a WebRTC connection to a device
// needs: which servers each end of the connection is given, and one ephemeral
// credential for both.
//
// It exists because the two ends are not interchangeable. A browser reaches a
// relay on its external address; a host VM has no external address and the VPC
// has no NAT, so it reaches the same relay only on the internal one. Naming the
// same instances by their two addresses is what makes relayed media work at
// all, and getting the two legs the wrong way round fails as a connection that
// simply never establishes.
//
// Both API surfaces compose from here. The legacy one flattens the result,
// because its wire carries a single list.
package ice

import (
	"slices"

	"cloud-cuttlefish-orchestrator/internal/platform/turn"
)

// The ICE transport policies, spelled as the browser's RTCConfiguration
// spells them.
//
// The policy binds the client leg only. A device builds its peer connection
// from a server list and a port range and never sets a transport type, and
// there is no signaling field that would carry one, so a device cannot be
// held to relayed transport however the deployment is configured.
const (
	PolicyAll   = "all"
	PolicyRelay = "relay"
)

// Server is one ICE server entry.
//
// The JSON is at once the browser's RTCIceServer and the ice_servers entry the
// device parses out of request-offer, so a client passes the device leg
// straight through without mapping it.
type Server struct {
	URLs       []string `json:"urls" doc:"Server URIs, in the stun:, turn: or turns: scheme"`
	Username   string   `json:"username,omitempty" doc:"TURN username; absent on STUN entries"`
	Credential string   `json:"credential,omitempty" doc:"TURN password; absent on STUN entries"`
}

// Configuration is the per-leg answer for one device's media.
type Configuration struct {
	ClientServers []Server `json:"client_servers" doc:"ICE servers for the client leg (browser or CLI)"`
	DeviceServers []Server `json:"device_servers" doc:"ICE servers for the device leg, to be sent to the device in request-offer"`
	Policy        string   `json:"ice_transport_policy" enum:"all,relay" doc:"Transport policy the client leg is held to"`
}

// Options are the deployment facts a composition depends on.
type Options struct {
	// STUNServers are reachable by anything with a route to the public
	// internet.
	STUNServers []string

	// ClientServers and DeviceServers name the same relays by their external
	// and internal addresses respectively.
	ClientServers []string
	DeviceServers []string

	// PinClientsToRelay holds the client leg to relayed transport. True where
	// device media never meets the internet at the host, so a direct candidate
	// pair cannot form and gathering one only delays the connection.
	PinClientsToRelay bool

	// DevicesReachInternet reports whether host VMs can reach a public STUN
	// server, meaning whether they have external addresses, since the VPC
	// has no NAT.
	DevicesReachInternet bool
}

// Composer builds ICE configurations for one deployment.
type Composer struct {
	creds *turn.CredentialService
	opts  Options
}

// NewComposer creates the composer. creds may be nil, and then no credential
// is minted and the answer is whatever the deployment offers without one.
func NewComposer(creds *turn.CredentialService, opts Options) *Composer {
	return &Composer{creds: creds, opts: opts}
}

// Compose returns the configuration for one subject's media, per leg.
func (c *Composer) Compose(userEmail string, subject turn.Subject) Configuration {
	// Empty rather than nil: a nil slice marshals to JSON null, and both legs
	// are lists a client iterates.
	cfg := Configuration{
		ClientServers: []Server{},
		DeviceServers: []Server{},
		Policy:        PolicyAll,
	}
	if c.opts.PinClientsToRelay {
		cfg.Policy = PolicyRelay
	}

	// STUN goes only to a leg that can both reach it and use it. A relay-pinned
	// client gathers no server-reflexive candidate whatever it is given, and a
	// host VM without an external address has no route to a public STUN server
	// at all, so the query can only time out, on the way to every connection.
	if stun, ok := c.stun(); ok {
		if !c.opts.PinClientsToRelay {
			cfg.ClientServers = append(cfg.ClientServers, stun)
		}
		if c.opts.DevicesReachInternet {
			cfg.DeviceServers = append(cfg.DeviceServers, stun)
		}
	}

	if cred, ok := c.mint(userEmail, subject); ok {
		if len(c.opts.ClientServers) > 0 {
			cfg.ClientServers = append(cfg.ClientServers, turnServer(c.opts.ClientServers, cred))
		}
		if len(c.opts.DeviceServers) > 0 {
			cfg.DeviceServers = append(cfg.DeviceServers, turnServer(c.opts.DeviceServers, cred))
		}
	}
	return cfg
}

// Flatten returns both legs as the single list the legacy surface serves.
//
// That surface hands one list to its own peer connection and sends the same
// one to the device, so each leg has to find its own reachable URI in a list
// that also carries the other's: the client leg times out on an internal
// address and the device leg on an external one, and they meet on whichever
// instance appears in both.
//
// STUN rides it in every topology, unlike the client leg above. The legacy
// wire has no transport policy to tell a client not to bother with it, and
// upstream's own sample configuration always carries one.
func (c *Composer) Flatten(userEmail string, subject turn.Subject) []Server {
	servers := []Server{}
	if stun, ok := c.stun(); ok {
		servers = append(servers, stun)
	}
	if cred, ok := c.mint(userEmail, subject); ok {
		servers = append(servers, turnServer(slices.Concat(c.opts.ClientServers, c.opts.DeviceServers), cred))
	}
	return servers
}

// stun returns the STUN entry, when the deployment has one.
func (c *Composer) stun() (Server, bool) {
	if len(c.opts.STUNServers) == 0 {
		return Server{}, false
	}
	return Server{URLs: slices.Clone(c.opts.STUNServers)}, true
}

// Mints reports whether this deployment issues credentials at all, which is
// what decides whether composing needs a caller identity and a live subject.
func (c *Composer) Mints() bool {
	return c.creds != nil && len(c.opts.ClientServers)+len(c.opts.DeviceServers) > 0
}

// mint issues the credential both legs share. One mint serves both because a
// credential names no server: it verifies against the shared secret alone, so
// it authenticates on whichever relay a leg reaches.
//
// Nothing is minted where there is no relay to present it to.
func (c *Composer) mint(userEmail string, subject turn.Subject) (turn.Credential, bool) {
	if !c.Mints() {
		return turn.Credential{}, false
	}
	return c.creds.Mint(userEmail, subject), true
}

func turnServer(uris []string, cred turn.Credential) Server {
	return Server{
		URLs:       slices.Clone(uris),
		Username:   cred.Username,
		Credential: cred.Password,
	}
}
