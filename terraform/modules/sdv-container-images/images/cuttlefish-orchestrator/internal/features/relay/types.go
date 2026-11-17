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

package relayfeature

// AuthorizeInput is the input for a relay's authentication callback.
type AuthorizeInput struct {
	Body struct {
		Username  string `json:"username" minLength:"1" maxLength:"512" doc:"Username presented by the TURN client"`
		Realm     string `json:"realm" minLength:"1" maxLength:"253" doc:"Realm the relay is authenticating under" example:"cco"`
		Algorithm string `json:"algorithm,omitempty" enum:"md5,sha256" default:"md5" doc:"Digest used to derive the long-term key"`
	}
}

// AuthorizeOutput carries the derived key back to the relay.
type AuthorizeOutput struct {
	Body AuthorizedKey
}

// AuthorizedKey is the long-term key for an accepted credential.
type AuthorizedKey struct {
	// Key is the base64 long-term key the relay checks MESSAGE-INTEGRITY
	// against. It is the digest of "username:realm:password", never the
	// password itself and never the shared secret.
	Key string `json:"key"`

	// ExpiresAt is when the credential lapses, as a Unix timestamp. A relay
	// may cache the key until then, and no longer.
	ExpiresAt int64 `json:"expires_at"`
}
