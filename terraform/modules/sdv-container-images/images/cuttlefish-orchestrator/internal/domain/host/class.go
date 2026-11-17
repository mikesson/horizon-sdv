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

package host

// Origin is which path created a host. It is provenance, stamped at create
// and never rewritten. Every create site passes one explicitly, so a new
// site cannot compile without choosing.
//
// OriginAuto and OriginPool carry no behavioral difference (both derive
// ClassFleet) and are kept apart only because "did the pool ever top up" is a
// question an operator asks.
type Origin string

const (
	// OriginUser is a host somebody asked for: the native /v2 create, or the
	// /v1 compat surface cvdr and OmniLab ATS use.
	OriginUser Origin = "user"
	// OriginAuto is a host a declarative create provisioned because placement
	// found no capacity.
	OriginAuto Origin = "auto"
	// OriginPool is a fleet-keeper top-up: standing inventory nobody asked for.
	OriginPool Origin = "pool"
	// OriginBatch is a host provisioned to carry a batch's members.
	OriginBatch Origin = "batch"
)

// Origins is the stored vocabulary, in the order the class table reads. The
// origin struct tag mirrors it and an agreement test binds the two copies.
var Origins = []Origin{OriginUser, OriginAuto, OriginPool, OriginBatch}

// System reports whether the orchestrator created this host on its own
// initiative rather than on request. It is the term the class derivation turns
// on, so the two arms that consult provenance cannot drift apart.
func (o Origin) System() bool { return o == OriginAuto || o == OriginPool }

// Class answers "whose lifecycle rules apply to this host", which is separate
// from what the host is doing right now. A fleet host that receives a device
// has not changed kind, only state. The vocabulary that predates this
// conflated the two.
//
// It is DERIVED, never stored: a stored copy beside its three determinants is
// two copies that drift, and the determinants are already on the row.
type Class string

const (
	// ClassReserved is a host somebody is relying on. Its lease is a promise
	// and runs its full term; nothing reclaims it but a user delete. It is
	// also the fail-safe every unrecognized row lands in.
	ClassReserved Class = "reserved"
	// ClassFleet is shared standing capacity the orchestrator manages. Its
	// lease is renewable rather than promised: counted as inventory, shortened
	// when surplus, renewed while demand justifies it.
	ClassFleet Class = "fleet"
	// ClassDedicated is a host the orchestrator fenced for one group. It can
	// never be placed onto again, so an empty one is pure cost.
	ClassDedicated Class = "dedicated"
	// ClassBatch is a host provisioned for a batch; batch settlement owns its
	// whole lifecycle and nothing else may reclaim it.
	ClassBatch Class = "batch"
)

// Classes is the derived vocabulary. Same agreement-test contract as Origins.
var Classes = []Class{ClassReserved, ClassFleet, ClassDedicated, ClassBatch}

// DeriveClass computes a host's class from the three stored facts that decide
// it. Total by construction: anything unrecognized (an origin from a future
// version, or the NULL a create site that forgot to stamp leaves behind)
// derives ClassReserved, which is never counted as inventory and never
// reclaimed. A missed stamp then costs one host the fleet ignores instead of
// drawing somebody's host into managed arithmetic.
//
// The arm order matters in two places.
//
// Batch comes first so an unstamped batch host keeps being reaped by batch
// settlement instead of falling through to the reserved fallback, which
// nothing reclaims.
//
// The exclusive arm must test the origin as well, because exclusive is a field
// USERS SET: it fences a host from automatic placement, which is how somebody
// protects an in-flight test from noisy neighbors. Keyed on exclusivity
// alone, that host would derive ClassDedicated, whose rule is "reclaim it
// when empty", and be taken away between the very test runs it was fenced
// for.
//
// That arm reads System() rather than OriginAuto alone. Only the declarative
// miss path creates exclusive hosts today (a multi-instance group provisions
// one), so the keeper case is unreachable. Were it ever reachable, an
// exclusive host is one placement can never use again, and leaving it out of
// the dedicated arm would mean nothing reclaims it while it bills.
func DeriveClass(origin Origin, exclusive bool, batchID string) Class {
	switch {
	case batchID != "":
		return ClassBatch
	case exclusive && origin.System():
		return ClassDedicated
	case origin.System():
		return ClassFleet
	default:
		return ClassReserved
	}
}
