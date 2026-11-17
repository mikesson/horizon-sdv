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

// Package relayfeature makes the orchestrator the authority for TURN relay
// credentials, so relay servers hold no shared secret.
//
// A relay that delegates authentication asks this slice for the long-term key
// of each username it is presented with, instead of deriving the key from a
// secret of its own. Two properties follow. The shared secret never leaves the
// orchestrator, so a compromised relay VM yields no ability to mint
// credentials. And the credential lifetime becomes real: the relay is checked
// against the orchestrator on every authentication, so an expired credential
// is refused here even when the relay itself does not parse the expiry, which
// the server this was built for does not.
//
// Expiry enforcement is therefore not an optimisation. Without it a leaked
// credential stays valid until the shared secret rotates, which would silently
// falsify the short-lived-credential property that makes relay mode worth
// having.
//
// The slice is handler.go, service.go, and types.go, assembled by wire.go.
//
// # Access
//
// The endpoint is gated by the same access list as every other route; there is
// no separate relay role. It confers no capability an authorized caller does
// not already have, since any of them can mint credentials for any device
// through the device API.
//
// # Endpoints
//
//   - POST /v2/relay/authorize - Return the long-term key for a credential,
//     or refuse it
package relayfeature
