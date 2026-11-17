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

package coapi

// Error is the legacy error envelope returned by the Cloud Orchestrator.
type Error struct {
	Code     int    `json:"code"`
	ErrorMsg string `json:"error"`
}

// The signaling messages (connect, forward, poll) have no Go types here: /v1
// signaling is reverse-proxied to the Host Orchestrator byte for byte, so the
// payloads pass through untyped.

// InfraConfig is the body of .../hosts/{host}/infra_config: the WebRTC ICE servers.
type InfraConfig struct {
	IceServers []IceServer `json:"ice_servers"`
}

// IceServer is a single ICE (STUN/TURN) server entry.
type IceServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}
