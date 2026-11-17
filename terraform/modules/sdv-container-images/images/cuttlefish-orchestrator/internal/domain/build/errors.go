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

package build

// ValidationError reports an invalid build registration or update. It maps
// to a 400 on the wire.
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

// NotFoundError is returned when a build cannot be found.
type NotFoundError struct {
	ID string
}

func (e *NotFoundError) Error() string {
	if e.ID != "" {
		return "build not found: " + e.ID
	}
	return "build not found"
}

// Is makes any *NotFoundError match the ErrNotFound sentinel via errors.Is.
func (e *NotFoundError) Is(target error) bool {
	_, ok := target.(*NotFoundError)
	return ok
}

// ErrNotFound is the sentinel any *NotFoundError matches via errors.Is.
// There is no already-exists error, because Register is idempotent and
// returns the existing record on a duplicate build_id.
var ErrNotFound = &NotFoundError{}
