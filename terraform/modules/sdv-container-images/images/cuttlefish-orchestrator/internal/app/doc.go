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

// Package app is the composition root of the orchestrator.
//
// It is responsible for:
//  1. Loading and validating configuration.
//  2. Initializing infrastructure clients (Spanner, GCE, GCS).
//  3. Wiring together the vertical feature slices (Handlers -> Services -> Repositories).
//  4. Starting background jobs (device controller, reaper, reconcilers).
//  5. Launching the HTTP server with the composed router.
//
// Run (app.go) owns the process lifecycle: the signal context, the client
// close order, and the shutdown sequence. The wiring stages live one per
// file: clients.go (infrastructure client builders), telemetry.go,
// features.go (the Wire calls and the cross-feature graph), jobs.go (the
// background-job bundle and leader election), legacy.go (the /v1
// compatibility surface), and server.go (router, edge middleware, and the
// HTTP server).
package app
