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

// Package hostconfigfeature is the admin configuration surface: host image
// configurations plus the operator-tunable system_config settings that
// steer placement, Spot economics, builds, and artifacts.
//
// # Host image configurations
//
// Which GCE image family provisions hosts for each machine type prefix. The
// image family resolves in the row's source_project, else in the deployment's
// CCO_HOST_IMAGE_PROJECT; a family with no enabled row is not provisionable.
// Every read reports the resolved image (or why there is none), and every
// write of an enabled row is refused unless it resolves:
//
//   - GET /v2/admin/host-image-configs - List all configurations
//   - GET /v2/admin/host-image-configs/{family} - Get one configuration
//   - POST /v2/admin/host-image-configs - Create a configuration
//   - PUT /v2/admin/host-image-configs/{family} - Update a configuration
//   - DELETE /v2/admin/host-image-configs/{family} - Delete a configuration
//
// # System-config settings
//
// GET/PUT pairs under /v2/admin/system-config/, one per runtime policy key
// (changes take effect without a redeploy), among them:
//
//   - zone-pin - pin placement to one zone (placement.zone_pin)
//   - quota-margin - placement quota headroom margin percent
//   - v1-zone-hint - whether the legacy /v1 zone token is a hint or a pin
//   - spot-auto-default - Spot as the default provisioning model for
//     headless intent
//   - spot-min-uptime - minimum advice uptime tier for Spot placement
//   - default-gpu-machine-type - machine type for gpu-required intent
//   - default-upstream-build - branch/target for declarative build intent
//   - artifact-buckets - per-type artifact bucket overrides
//
// Each settings GET returns an ETag over the stored keys behind the setting,
// and each write takes an optional If-Match. A tag that no longer matches
// fails with 412 and writes nothing. The fleet writes share one tag.
//
// All endpoints are admin-surface (the IAP access list is the only gate)
// and audited. wire.go assembles the repository, settings store, service,
// and handler.
package hostconfigfeature
