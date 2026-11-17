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

package gcecatalog

// Availability is what the catalog knows about one (zone, resource) cell.
//
// There are three states because a zone Compute Engine never reported is
// different from a zone that offers nothing. Only a positive "reported and did not list it" may
// remove a placement candidate. Being wrong permissively costs one doomed
// create attempt that zone_health then remembers, while being wrong
// restrictively leaves a fleet unable to provision with no error to explain
// it.
type Availability int

const (
	// AvailabilityUnknown means nothing has been fetched yet, or Compute
	// Engine did not report the scope. It never removes a candidate.
	AvailabilityUnknown Availability = iota
	// AvailabilityOffered means Compute Engine reported the scope and listed
	// the resource.
	AvailabilityOffered
	// AvailabilityNotOffered means Compute Engine reported the scope and did
	// not list the resource.
	AvailabilityNotOffered
)

// Excludes reports whether this state is positive enough to remove a
// placement candidate.
func (a Availability) Excludes() bool { return a == AvailabilityNotOffered }

// String renders the state for exclusion reasons and log lines.
func (a Availability) String() string {
	switch a {
	case AvailabilityOffered:
		return "offered"
	case AvailabilityNotOffered:
		return "not offered"
	default:
		return "unknown"
	}
}
