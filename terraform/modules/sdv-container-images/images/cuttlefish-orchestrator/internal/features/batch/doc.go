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

// Package batchfeature admits, reports, and tears down device batches. Member
// devices carry batch_id and the status is derived from them; the persisted
// batch row holds only policy and the sweep's settle markers. Admission drives
// the existing device engine (resolve, provision, create). There is no batch
// reconcile loop; the atomic fill policy is enforced by the batchsettle job's
// sweep.
//
// The slice is handler.go (Huma HTTP handlers), service.go (admission and
// rollup), repository.go (Spanner batch-policy rows), and types.go (DTOs),
// assembled by wire.go.
package batchfeature
