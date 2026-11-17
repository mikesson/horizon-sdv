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

// Package auth provides the authentication domain types.
//
// Authentication is the only access gate: Google Cloud IAP verifies the
// caller at the edge, and the middleware extracts a verified identity from
// the request.
// There is no in-application authorization. Any authenticated caller has
// full access. The IAP access list (roles/iap.httpsResourceAccessor) is the
// sole security boundary.
//
// Principal is the verified identity attached to request contexts; the
// context helpers in context.go carry it, and CreatorScope (creator_scope.go)
// resolves the created_by/mine list-scoping query pair against it.
package auth
