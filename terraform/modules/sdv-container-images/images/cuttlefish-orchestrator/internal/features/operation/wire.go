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

package operationfeature

import "errors"

// Wire assembles the operations read slice.
//
// Pure construction, no I/O, called exactly once from the composition root. There
// is no service layer to build: the handler reads and composes, and every rule
// about an operation lives where it is admitted or actuated.
func Wire(ops Reader) (*Handler, error) {
	if ops == nil {
		return nil, errors.New("operations reader is required")
	}
	return NewHandler(ops), nil
}
