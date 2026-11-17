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

// Package storagefeature is the artifact-storage surface: bucket and object
// browsing plus the chi-only object byte streams, served over the platform
// GCS client (which the device slice shares for artifact capture). The slice
// is handler.go and types.go, assembled by wire.go. Its own tests cover the
// error paths; the thin bindings are proven by the schema-drift gate and the
// console e2e suite.
package storagefeature
