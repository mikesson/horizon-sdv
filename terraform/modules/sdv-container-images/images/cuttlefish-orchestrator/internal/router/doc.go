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

// Package router composes the independent vertical slices into a unified
// HTTP API.
//
// It mounts the feature handlers on a chi router, applies the global
// middleware chain (request ID, client info, logging, recovery, auth, CSRF,
// request deadlines), and configures the Huma OpenAPI generator (huma.go)
// plus the Scalar documentation UI (docs.go). Raw chi routes exist only for
// what Huma cannot express: WebRTC signaling, storage byte streams, and the
// legacy /v1 compatibility surface.
//
// Use New to create a configured router with all routes registered.
package router
