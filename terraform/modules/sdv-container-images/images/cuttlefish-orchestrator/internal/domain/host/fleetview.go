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

import "time"

// StandbyTerm names one term of the standby predicate, so a host that is idle
// and still not counted can say which term excluded it.
//
// "Why isn't this idle host counted?" has no answer anywhere today, and the
// answer must be derived beside the predicate rather than re-implemented by a
// reader: five terms plus a class narrowing all have to agree, and a second copy
// in another language disagrees the first time one of them moves.
type StandbyTerm string

const (
	// TermClass is the term that says the host is not the fleet's: a user's
	// host, a batch's, or one fenced to a single group, none of them the
	// keeper's to count or replace.
	TermClass StandbyTerm = "class"
	// TermPlaceable is the term that fails when the status is one placement will
	// not land on, or when the host is still coming up and GCE has refused its
	// provision for capacity (Host.ProvisionBlockedAt).
	TermPlaceable StandbyTerm = "placeable"
	// TermNotDegraded is the term the poison condition fails: a degraded host is
	// fenced from placement.
	TermNotDegraded StandbyTerm = "not_degraded"
	// TermShareable is the term an exclusive host fails, because placement will
	// not put a stranger on it.
	TermShareable StandbyTerm = "shareable"
	// TermIdle is the term a host carrying devices fails, so it is not spare
	// capacity.
	TermIdle StandbyTerm = "idle"
	// TermLease is the term a lease ending inside the provisioning lead fails:
	// the host is reaped before a replacement could land, so no create can
	// count on it.
	TermLease StandbyTerm = "lease"
)

// standbyTermOrder is the order a failing term is reported in: the predicate's
// own order, with the class narrowing first.
//
// Class leads because it is the one term that says the host is not the fleet's
// at all. Every term after it describes a fleet host, and naming "carries
// devices" for somebody's hand-made VM would answer a question nobody asked.
var standbyTermOrder = []StandbyTerm{
	TermClass, TermPlaceable, TermNotDegraded, TermShareable, TermIdle, TermLease,
}

// FleetOccupant is one device sitting on a host, sized by the instance slots it
// costs rather than counted as one.
//
// It carries a size because max_devices and current_device_count are both
// denominated in INSTANCES, and one device can want eight of them. A device
// drawn as a unit is the same arithmetic that oversubscribes a host silently.
type FleetOccupant struct {
	Name string
	// Instances is the slots this device costs its host: the desired count, not
	// the observed array, matching what the host's counter was charged.
	Instances int64
	// Errored marks a device row in error. Its slots are still charged, so the
	// host can be full while running nothing; they are NOT unbacked slots, which
	// is a claim about the host image rather than about a failed create.
	Errored bool
}

// FleetHost is one host as the autoscaler's lens sees it: what it is, what it
// holds, and whether the keeper counts it.
//
// A read model rather than a projection of Host: this answers a question Host
// cannot (which term of the standby predicate excluded it) and carries the
// device census the page draws instead of the forty columns it does not.
type FleetHost struct {
	ID          string
	Name        string
	Zone        string
	MachineType string
	GPUType     string
	GPUCount    int64
	Status      Status
	Class       Class
	Origin      Origin
	Pool        string
	Exclusive   bool
	Degraded    bool

	// MaxDevices is the slot ceiling the host was admitted with, what it
	// PROMISES. What it can actually back is BackedSlots below.
	MaxDevices int64
	// LiveInstances and ErrorInstances are the slots its devices hold, split by
	// whether anything is running in them. Their sum is what the host's counter
	// charges, which is why a host full of dead rows still refuses placement.
	LiveInstances  int64
	ErrorInstances int64
	Occupants      []FleetOccupant

	ExpiresAt *time.Time
	CreatedAt time.Time

	// The standby predicate, term by term, evaluated beside the predicate itself.
	ClassIsFleet bool
	Placeable    bool
	NotDegraded  bool
	Shareable    bool
	Idle         bool
	LeaseHolds   bool
}

// BackedSlots is what this host can actually run: its own admission ceiling
// bounded by what the image preallocates a TAP set for.
//
// Every total on a fleet view sums THIS and never MaxDevices. Summing the
// promise is exactly the claim the ruler exists to refute: on a fleet of
// 16-slot hosts against a 10-slot image it overstates capacity by 60%.
func (h FleetHost) BackedSlots() int64 { return min(h.MaxDevices, HostImageCVDAccounts) }

// IsStandby reports whether this host is inventory the keeper counts. It is the
// conjunction of the same terms StandbyInventory's SQL applies, so the two
// agree by construction rather than by review.
func (h FleetHost) IsStandby() bool {
	return h.ClassIsFleet && h.Placeable && h.NotDegraded && h.Shareable && h.Idle && h.LeaseHolds
}

// FailingTerm is the term that excluded this host from standby, or empty when
// it is counted. First failing term in standbyTermOrder, never all of them: the
// page states one reason per host, and a list of six booleans is the raw
// predicate again rather than an answer.
func (h FleetHost) FailingTerm() StandbyTerm {
	holds := map[StandbyTerm]bool{
		TermClass:       h.ClassIsFleet,
		TermPlaceable:   h.Placeable,
		TermNotDegraded: h.NotDegraded,
		TermShareable:   h.Shareable,
		TermIdle:        h.Idle,
		TermLease:       h.LeaseHolds,
	}
	for _, term := range standbyTermOrder {
		if !holds[term] {
			return term
		}
	}
	return ""
}
