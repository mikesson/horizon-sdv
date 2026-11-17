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

// Command relayhook lets a TURN relay delegate authentication to the
// orchestrator, so the relay holds no shared secret.
//
// It runs beside the relay on the same VM, serves the relay's gRPC
// authentication hook on loopback, and answers each call by asking the
// orchestrator over HTTPS. The relay learns only the long-term key of
// credentials the orchestrator accepts; the secret those keys derive from
// never leaves Cloud Run.
//
// # Why a separate process
//
// The relay's hook client cannot reach the orchestrator directly. It has no
// way to attach a bearer token (its only transport setting is a CA
// certificate for trusting the server), so it cannot authenticate to an
// IAP-gated service. Running the hook on loopback sidesteps that: this
// process holds the VM's identity and speaks ordinary authenticated HTTPS.
//
// # The one-second budget
//
// The relay's generated client fixes a one-second per-request timeout,
// overriding whatever its config file asks for, and that timeout applies to
// every authentication. A round trip to Cloud Run does not reliably fit, so
// answers are cached per credential until the credential itself expires. A
// session then costs one orchestrator call rather than one per
// authentication.
//
// Nothing is cached that the orchestrator refused, and nothing outlives the
// credential it came from. A call that cannot be answered in time fails
// closed: the relay reads any hook failure as "no password" and returns 401.
//
// # Authentication to the orchestrator
//
// The identity is the VM's service account, presented as a Google-signed ID
// token. The audience defaults to the service URL with a path wildcard, the
// form this deployment's IAP is known to accept from a service account, and
// -audience overrides it for a custom IAP OAuth client.
//
// # Flags
//
//	-orchestrator  base URL of the orchestrator (REQUIRED)
//	-listen        loopback address to serve the hook on (default 127.0.0.1:3001)
//	-audience      ID token audience (default <orchestrator>/*)
//	-timeout       per-call bound on the orchestrator request (default 800ms)
//	-max-cache     ceiling on how long a key is cached (default 5m)
//	-log-level     debug, info, warn, or error (default info)
package main
