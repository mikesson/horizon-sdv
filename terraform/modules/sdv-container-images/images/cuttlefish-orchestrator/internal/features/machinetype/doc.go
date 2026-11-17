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

// Package machinetypefeature provides the API handlers and repository for
// the curated machine type catalog host VMs are provisioned from.
//
// The catalog is stored in the database so machine type choices are
// configuration, not code: the console and CLI list from it, and host
// creation validates against it.
//
// The slice is handler.go, repository.go, and types.go, assembled by wire.go.
//
// # Endpoints
//
//   - GET /v2/machine-types - List machine types (with admin/enabled filters)
//   - GET /v2/machine-types/{id} - Get a specific machine type
//   - GET /v2/machine-types/default - Get the catalog default
//   - GET /v2/machine-families - List machine type families
//   - PATCH /v2/admin/machine-types/{id} - Admin curation: enable/disable a
//     type, move the catalog default, or override its device capacity
package machinetypefeature
