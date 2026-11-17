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

// Package buildfeature manages registered Android builds, the system
// images devices are created from, either Android Build CI references or
// operator-supplied GCS/HTTPS artifact sets.
//
// # Architecture
//
// The slice is handler.go (Huma HTTP handlers), service.go (registration
// and lifecycle logic), repository.go (Spanner persistence), and types.go
// (request/response DTOs), assembled by wire.go.
//
// # Operations
//
//   - ListBuilds: GET /v2/builds
//   - CreateBuild: POST /v2/builds. Registration is idempotent: a request
//     carrying an already-registered build_id returns the existing record
//   - GetBuild: GET /v2/builds/{id}
//   - UpdateBuild: PATCH /v2/builds/{id}, mutable fields only (name,
//     description, default flag, artifact specs)
//   - DeleteBuild: DELETE /v2/builds/{id}
package buildfeature
