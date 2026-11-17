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

// Command livesmoke exercises a real device on a real deployment and asserts
// the things only a live run can see.
//
// The other harnesses stop at the process boundary: the emulator suite fakes
// Spanner, queueload and robustness fake GCE and the Host Orchestrator. Every
// defect they cannot see lives past that boundary, in the platform's reaction
// to a slow response, in what a Host Orchestrator actually returns, in whether
// a device that drifted ever converges. Three such defects shipped undetected
// and were found by hand; each is a check here now.
//
// It drives the SHIPPED cfctl binary rather than the client library: cfctl's
// own behavior (its per-request timeout, its exit codes, its JSON) is part of
// what is under test, and Go's internal-package rule fences cmd/cfctl off from
// tests/ anyway. Platform facts come from gcloud, as the deploy flow does.
//
// # Cost
//
// This provisions real VMs and bills real money. There is no default project:
// -project must be named explicitly. The run always tears down what it made,
// including on failure; -keep leaves the device and host up for debugging.
//
// # Checks
//
//	create        a declarative create reaches running, with the elapsed time
//	screenshot    the capture is non-empty, PNG-framed, and typed image/png
//	failsafe      the created VM carries a max_run_duration termination bound
//	verbs         restart/powerwash/stop/start each succeed, with wall-clock
//	snapshot      take → powerwash → stop → restore → powerbtn → delete, the
//	              whole host-pinned snapshot loop on one device (no capture:
//	              the upstream streamer has no restore path, so a restored
//	              guest never serves its display pipeline again)
//	drift         a CVD started behind CCO's back heals the device row back to
//	              running within the health interval (needs -sa; skipped without)
//	teardown      device and host delete cleanly, leaving no VM behind
//	platform      no 5xx and no instance recycles for the whole window
//	dlq           the dead-letter queue is empty afterwards
//
// # Flags
//
//	-project      GCP project (REQUIRED, no default)
//	-region       Cloud Run region (REQUIRED, no default)
//	-service      Cloud Run service name (default cuttlefish-orchestrator)
//	-cfctl        path to a cfctl binary (default: built from ./cmd/cfctl at startup)
//	-sa           service account to mint a /v1 bearer for the drift check
//	-auth-none    network-mode deployment: send no credentials anywhere
//	-scenario     full (default), verbs, or screenshot
//	-interaction  headless (default) or interactive
//	-keep         leave the device and host running at the end
//
// Timeouts (-create-timeout, -verb-timeout, -heal-timeout) bound each phase;
// every cfctl call is given a matching --timeout so the client waits for the
// server's answer instead of capping itself at its own 30s per-request bound.
//
// # Usage
//
//	go run ./tests/livesmoke -project=your-project-id -region=your-region
//	go run ./tests/livesmoke -project=your-project-id -region=your-region -scenario=screenshot
//	go run ./tests/livesmoke -project=your-project-id -region=your-region -sa=cfctl-test@your-project-id.iam.gserviceaccount.com
//
// Run it either side of a deploy. It is not part of CI, because it needs GCP
// credentials, takes minutes, and costs money.
package main
