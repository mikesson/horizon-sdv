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

// Package live holds the cross-package references that keep some of dead's
// exported names alive.
package live

import "example.test/dead"

// Use reads one type, one plain constant and one iota constant from dead.
func Use() (dead.LiveType, string, dead.Colour) {
	return dead.LiveType{Field: dead.LiveConst}, dead.LiveConst, dead.ColourRed
}
