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

package web

// BearerAuth is the security requirement carried by every /v2 operation,
// naming the bearerAuth scheme the router registers on the OpenAPI document.
//
// It builds a fresh value per call: Huma keeps whatever it is handed in the
// document, so a shared one would alias across every operation and an
// in-place edit to one operation's requirement would silently reach them all.
func BearerAuth() []map[string][]string {
	return []map[string][]string{{"bearerAuth": {}}}
}
