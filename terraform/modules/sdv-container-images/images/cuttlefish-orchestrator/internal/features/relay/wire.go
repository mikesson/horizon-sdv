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

import (
	"log/slog"

	"cloud-cuttlefish-orchestrator/internal/platform/turn"
)

// Wire creates the relay feature: the credential authority and its handler.
//
// The composition root calls this only when a TURN shared secret is
// configured. Without one there is no key to derive, and every credential
// would verify against the empty secret.
func Wire(creds *turn.CredentialService, devices DeviceLookup, hosts HostLookup, realm string, logger *slog.Logger) *Handler {
	return NewHandler(NewService(creds, devices, hosts, realm, logger))
}
