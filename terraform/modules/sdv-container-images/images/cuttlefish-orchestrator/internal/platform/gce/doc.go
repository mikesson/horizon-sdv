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

// Package gce provides Google Compute Engine error classification and small
// domain-mapping helpers for Cuttlefish host virtual machines.
//
// The convention: this package does not wrap the GAPIC-covered Compute API
// for the packages that hold the generated clients directly. The host
// feature's provisioning code and the host-reconciler/catalog-sync jobs
// route their raw clients' errors through ClassifyError here for
// consistent typing, retryability, and user-facing suggestions. A few
// wrappers exist on purpose:
//
//   - SpotAdviceClient: the pinned client libraries carry no call binding
//     for the beta advice.capacity method, so ProbeCell issues that single
//     REST call itself (official beta types on the wire, shared
//     authenticated transport). It is the quarantine pattern for any future
//     missing-GAPIC surface.
//   - ImageClient: resolves a host image family to the concrete image it
//     points at, for the hostconfig feature's image resolution. The adapter
//     keeps the Compute SDK out of that feature slice; only the host
//     feature's provision engine holds compute clients in features/.
//   - Catalog: the in-memory snapshot of what Compute Engine offers,
//     rebuilt by two background refreshes and read by placement and
//     admission. It wraps the aggregated list calls because absence has to
//     survive into the answer. See catalog.go.
//
// Contents:
//   - ClassifyError: structured classification of GCE API errors (typed
//     reasons, gRPC codes, message rules) into ClassifiedError
//   - SuggestedRetryDelay: server-guided backoff for rate-limited calls
//     (Retry-After, with a conservative default)
//   - SpotAdviceClient.ProbeCell: single-cell Spot capacity-advice probe
//   - ImageClient.ResolveFamily: image-family resolution behind host create
//     preflight, provisioning and the admin host-image status
//   - Catalog: three-state shape and accelerator availability, live regional
//     quota, and the scopes Compute Engine did not report
//   - UpdateHostFromGCEInstance: copies instance IPs and the VM stop time
//     onto the host (the raw GCE state maps to a lifecycle phase in the
//     domain via host.DerivePhase, not here)
//   - VMTerminatesAt: when max_run_duration stops a running instance, from
//     its last start time; nil when the instance is not running
package gce
