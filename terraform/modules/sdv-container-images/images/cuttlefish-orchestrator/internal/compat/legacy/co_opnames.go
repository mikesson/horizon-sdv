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

package legacy

import "strings"

// The legacy CO host create/delete calls are asynchronous and return an
// Operation whose name the client later polls via
// POST /v1/zones/{zone}/operations/{operation}/:wait. CCO has no operations
// store; instead the operation name encodes the action and the target host ID,
// and :wait resolves status by reading the host record. The name must be a
// single URL path segment, so it uses '.' as the delimiter (host IDs are UUIDs,
// which contain '-' but never '.').
const (
	opActionCreate = "create"
	opActionDelete = "delete"
)

// newOperationName encodes a host action and ID into an operation name.
func newOperationName(action, hostID string) string {
	return action + "." + hostID
}

// parseOperationName splits an operation name into its action and host ID,
// returning ok=false if the name is malformed.
func parseOperationName(name string) (string, string, bool) {
	action, hostID, found := strings.Cut(name, ".")
	if !found || action == "" || hostID == "" {
		return "", "", false
	}
	return action, hostID, true
}
