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

// Package web bridges domain errors to the HTTP transport layer.
//
// MapError (huma_errors.go) is THE mapper from domain error families to
// Huma RFC 9457 responses. Handlers route domain errors through it instead
// of matching messages. errors.go holds the transport error types, and
// response.go the plain-JSON response writers for the raw (non-Huma)
// routes.
package web
