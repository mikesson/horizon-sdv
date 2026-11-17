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

import (
	"context"
	"time"
)

// ZoneFamilyCount is one cell of the fleet's (zone × machine family) census:
// where the fleet's machines are, what shape family they are, and how much they
// carry.
//
// Arriving is a subset of Machines, not a term beside it: a host still coming
// up is a machine the fleet is paying for and a machine placement can already
// count on, so a surface that added them would double every provisioning host.
type ZoneFamilyCount struct {
	Zone          string
	MachineFamily string
	Machines      int64
	Arriving      int64
	Devices       int64
}

// ModelStatusCount is one cell of the fleet's provisioning-model × status
// census (the Spot/on-demand split).
type ModelStatusCount struct {
	Model  ProvisioningModel
	Status Status
	Count  int64
}

// ShapeCount is one cell of the fleet's shape census: how many hosts of a given
// machine shape are held by a given class in a given status.
//
// The accelerator rides the cell rather than being looked up per machine type,
// because it is what the host was actually built with. A catalog that changed
// after the host was provisioned would otherwise relabel history.
type ShapeCount struct {
	MachineType string
	GPUType     string
	GPUCount    int64
	Class       Class
	Status      Status
	Count       int64
}

// StandbyInventory is what ONE POOL holds at ONE instant: capacity a create can
// count on, and capacity whose lease is running out from under it.
//
// One value rather than two counts, because the two are complements about a
// single cutoff. An idle fleet host is standby or draining according to whether
// its lease outlasts the provisioning lead, never both and never neither. Read
// separately they answer to two cutoffs, and a host whose expiry falls between
// them is counted twice or lost. They are published side by side so an
// operator can see that a standby count dropped because leases ran down rather
// than because devices arrived, and that reading is only true of one instant.
type StandbyInventory struct {
	// Standby is the inventory the keeper's deficit arithmetic spends against.
	Standby int64
	// Draining is the buffer's outflow: idle and placeable, but with too little
	// lease left to be counted on.
	Draining int64
	// Total is every fleet-class host in the pool placement can still land on,
	// occupied or free. It is what the pool's FLOOR is held against, where the
	// two counts above answer to its buffer.
	//
	// It is not Standby + Draining + something: those two are the
	// idle population, and a host filling with devices leaves them while still
	// being a host the fleet is running. Counted in the same statement for the
	// same reason they are counted in one: three answers about one instant.
	Total int64
}

// FleetInventory is what every pool holds, from the one statement that counts
// them all: the map the keeper's per-pool arithmetic is spent against.
//
// A pool with no rows is simply absent rather than present at zero. The zero
// StandbyInventory is exactly what an empty pool holds, so a caller indexing a
// pool the fleet has no machines in gets the right answer without the read
// having to know which pools an operator has configured.
type FleetInventory map[string]StandbyInventory

// Fleet is what the pools hold together, for a caller whose other half of the
// comparison is fleet-wide: a census of every host the keeper created, or a
// per-host verdict that says nothing about which pool the host is in. Anything
// judging a BOUND reads the pool's own counts instead, because a bound met by
// another pool's machines is not met.
func (f FleetInventory) Fleet() StandbyInventory {
	var fleet StandbyInventory
	for _, pool := range f {
		fleet.Standby += pool.Standby
		fleet.Draining += pool.Draining
		fleet.Total += pool.Total
	}
	return fleet
}

// ListFilter provides filtering options for listing hosts
type ListFilter struct {
	Zone      *string
	Status    *Status
	CreatedBy *string

	// Name matches hosts with exactly this name. It never matches a deleted
	// host, whatever Status and IncludeDeleted say.
	Name *string

	// RequestedOrPhysicalZone matches hosts whose requested_zone OR physical
	// zone equals the value. It is the legacy /v1 listing's stable partition: a
	// host created under a zone token stays visible there wherever placement
	// or failover physically lands it, while auto hosts (NULL requested_zone)
	// appear under their physical zone.
	RequestedOrPhysicalZone *string

	Limit  int
	Offset int

	// AfterCreatedAt and AfterID are the sort key of the last row of the
	// previous keyset page. A non-empty AfterID returns only rows after it in
	// (created_at DESC, host_id DESC) order. Count ignores them.
	AfterCreatedAt time.Time
	AfterID        string

	IncludeDeleted bool // If true, include deleted hosts in the results

	// Degraded, when set, filters to hosts whose poison condition is (true) or
	// is not (false) active, meaning degraded_at IS [NOT] NULL.
	Degraded *bool

	// Class, when set, filters to hosts of that class. The class is derived
	// rather than stored, so the repository evaluates the derivation in SQL.
	// Filtering it after the fetch would page over the wrong row set.
	Class *Class
}

// Getter is the read-one port for transport handlers that only resolve a
// device's host (displays, TURN, WebRTC signaling). The host repository
// satisfies it.
type Getter interface {
	// Get retrieves a host by ID
	Get(ctx context.Context, id string) (*Host, error)
}

// Repository defines the data access interface for hosts
type Repository interface {
	Getter

	// GetByIDs retrieves the hosts with the given IDs in one query, keyed by
	// ID. Missing hosts are simply absent from the map.
	GetByIDs(ctx context.Context, ids []string) (map[string]*Host, error)

	// RecordCreateFailure counts a poison-signature create failure against the
	// host in one transaction, reporting whether it newly degraded the host
	// ClearCreateFailures resets the counter/condition after a clean create
	// or successful remediation.
	RecordCreateFailure(ctx context.Context, hostID, reason string) (newlyDegraded bool, err error)
	ClearCreateFailures(ctx context.Context, hostID string) error

	// MarkHOReady records first Host Orchestrator contact (the ho_reachable
	// condition) and re-derives the phase, promoting a first-boot 'starting'
	// host to 'running'. Idempotent (no-op once already recorded); reports
	// whether the status changed.
	MarkHOReady(ctx context.Context, hostID string) (promoted bool, err error)

	// List retrieves hosts matching the filter
	List(ctx context.Context, filter ListFilter) ([]*Host, error)

	// Count returns the number of hosts matching the filter, ignoring
	// pagination (same predicates as List).
	Count(ctx context.Context, filter ListFilter) (int64, error)

	// CountByStatus returns host counts per status
	CountByStatus(ctx context.Context) (map[Status]int64, error)

	// FindAvailableHosts finds hosts with capacity in a zone
	FindAvailableHosts(ctx context.Context, zone string, requiredSlots int64) ([]*Host, error)
}

// GCEStateUpdate holds the fields for a batch GCE state update.
// This is used by the GCE sync service to efficiently update multiple hosts.
type GCEStateUpdate struct {
	ID string
	// VMStatus is the raw GCE state observed this cycle, written even when
	// the mapped lifecycle status is blocked (the observation is accurate
	// regardless of who owns the enum).
	VMStatus string
	// PrevStatus is the status the update was computed from. The write is
	// preconditioned on it, so a plan gone stale against a concurrent
	// lifecycle transition (e.g. a user delete between the sync's snapshot
	// and its apply) skips the row instead of clobbering it.
	PrevStatus Status
	Status     Status
	IP         string
	InternalIP string
	// VMTerminatesAt is when GCE will stop the VM under its max_run_duration,
	// as this cycle observed it; nil writes NULL.
	VMTerminatesAt *time.Time
}
