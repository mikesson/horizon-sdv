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

package hostfeature

import (
	"context"
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

// The fleet view lists every host the autoscaler's lens draws, with the standby
// predicate evaluated term by term beside it.
//
// It answers one question the inventory count cannot, WHICH term excluded a
// host, and it answers it by projecting the predicate's own term constants as
// columns. Restating them here would be a second copy that disagrees with the
// count the first time one moves, and the disagreement would surface only as a
// page explaining an exclusion that never happened.

// desiredInstancesExpr is the SQL twin of device.DesiredInstanceCount: how many
// slots a device costs its host.
//
// Zero reads as one, exactly as the Go function does. A row written before the
// spec carried a count restores 0, and a device drawn as costing nothing would
// show a full host as empty. SAFE_CAST rather than CAST because desired_spec is
// opaque JSON written by an actuator, so a malformed value must degrade to one
// slot, which is the value the host's own counter was charged.
const desiredInstancesExpr = `GREATEST(1, IFNULL(SAFE_CAST(JSON_VALUE(desired_spec, '$.instance_count') AS INT64), 0))`

// deviceStatusError is the one device status this file has to recognize: an
// error row holds slots the host's counter still charges, so it fills the host
// while running nothing.
//
// It is not liveDeviceExclusions, which also excludes 'deleting' because a
// shorten must not strand a device on its way out. Here a deleting device is
// still occupying its slots until the teardown commits, and drawing them free
// would show capacity a create cannot use.
const deviceStatusError = "error"

// fleetViewSQL projects every live host with its derived class and each standby
// term as its own column.
//
// Wide rather than narrowed to the fleet class: the page draws the other three
// classes muted so its totals reconcile against the hosts page, and a host
// excluded BY class is exactly the host whose exclusion has to be explainable.
func fleetViewSQL() string {
	return `SELECT
	        host_id,
	        name,
	        zone,
	        IFNULL(machine_type, '') AS machine_type,
	        IFNULL(gpu_type, '') AS gpu_type,
	        gpu_count,
	        status,
	        IFNULL(origin, '') AS origin,
	        IFNULL(pool, '') AS pool,
	        exclusive,
	        max_devices,
	        expires_at,
	        created_at,
	        (` + hostClassExpr + `) AS class,
	        ` + termPlaceable + ` AS placeable,
	        ` + termNotDegraded + ` AS not_degraded,
	        ` + termShareable + ` AS shareable,
	        ` + termIdle + ` AS idle,
	        ` + leaseHoldsExpr + ` AS lease_holds
	      FROM hosts
	      WHERE status != 'deleted'
	      ORDER BY name`
}

// fleetOccupantsSQL lists what sits on those hosts, one row per device, sized
// by the slots it costs.
//
// Sized and never counted: max_devices and current_device_count are both
// denominated in instances, so a device rendered as one unit is the arithmetic
// that oversubscribes a host silently. Deleted rows are left out because their
// slots are already refunded; error rows are kept and marked, because theirs
// are not. A host full of failed creates is full.
const fleetOccupantsSQL = `SELECT
	        host_id,
	        name,
	        status,
	        ` + desiredInstancesExpr + ` AS instances
	      FROM devices
	      WHERE status != 'deleted'
	      ORDER BY host_id, created_at`

// fleetViewRow is the projection fleetViewSQL reads.
type fleetViewRow struct {
	HostID      string           `spanner:"host_id"`
	Name        string           `spanner:"name"`
	Zone        string           `spanner:"zone"`
	MachineType string           `spanner:"machine_type"`
	GPUType     string           `spanner:"gpu_type"`
	GPUCount    int64            `spanner:"gpu_count"`
	Status      string           `spanner:"status"`
	Origin      string           `spanner:"origin"`
	Pool        string           `spanner:"pool"`
	Exclusive   bool             `spanner:"exclusive"`
	MaxDevices  int64            `spanner:"max_devices"`
	ExpiresAt   spanner.NullTime `spanner:"expires_at"`
	CreatedAt   time.Time        `spanner:"created_at"`
	Class       string           `spanner:"class"`
	Placeable   bool             `spanner:"placeable"`
	NotDegraded bool             `spanner:"not_degraded"`
	Shareable   bool             `spanner:"shareable"`
	Idle        bool             `spanner:"idle"`
	LeaseHolds  bool             `spanner:"lease_holds"`
}

func (r *fleetViewRow) toDomain() host.FleetHost {
	h := host.FleetHost{
		ID:           r.HostID,
		Name:         r.Name,
		Zone:         r.Zone,
		MachineType:  r.MachineType,
		GPUType:      r.GPUType,
		GPUCount:     r.GPUCount,
		Status:       host.Status(r.Status),
		Class:        host.Class(r.Class),
		Origin:       host.Origin(r.Origin),
		Pool:         host.NormalizePool(r.Pool),
		Exclusive:    r.Exclusive,
		Degraded:     !r.NotDegraded,
		MaxDevices:   r.MaxDevices,
		CreatedAt:    r.CreatedAt,
		ClassIsFleet: host.Class(r.Class) == host.ClassFleet,
		Placeable:    r.Placeable,
		NotDegraded:  r.NotDegraded,
		Shareable:    r.Shareable,
		Idle:         r.Idle,
		LeaseHolds:   r.LeaseHolds,
	}
	if r.ExpiresAt.Valid {
		h.ExpiresAt = &r.ExpiresAt.Time
	}
	return h
}

// occupantRow is the projection fleetOccupantsSQL reads.
type occupantRow struct {
	HostID    string `spanner:"host_id"`
	Name      string `spanner:"name"`
	Status    string `spanner:"status"`
	Instances int64  `spanner:"instances"`
}

// ListFleetView returns every live host with its class, its occupancy and the
// standby predicate evaluated term by term.
//
// lead is the standby lease margin, the same one StandbyInventory counts
// against, so a host this reports as counted is a host that count included.
func (r *HostRepository) ListFleetView(ctx context.Context, lead time.Duration) ([]host.FleetHost, error) {
	params := standbyParams(lead)
	params[paramSystemOrigins] = systemOrigins()
	hosts, err := spannerplatform.QueryAll(
		r.client.Single().Query(ctx, spanner.Statement{SQL: fleetViewSQL(), Params: params}),
		"fleet view", (*fleetViewRow).toDomain)
	if err != nil {
		return nil, err
	}

	occupants, err := spannerplatform.QueryAll(
		r.client.Single().Query(ctx, spanner.Statement{SQL: fleetOccupantsSQL}),
		"fleet occupants", func(row *occupantRow) occupantRow { return *row })
	if err != nil {
		return nil, err
	}

	byHost := make(map[string][]host.FleetOccupant, len(hosts))
	live := make(map[string]int64, len(hosts))
	errored := make(map[string]int64, len(hosts))
	for _, o := range occupants {
		dead := o.Status == deviceStatusError
		byHost[o.HostID] = append(byHost[o.HostID], host.FleetOccupant{
			Name: o.Name, Instances: o.Instances, Errored: dead,
		})
		if dead {
			errored[o.HostID] += o.Instances
			continue
		}
		live[o.HostID] += o.Instances
	}
	for i := range hosts {
		hosts[i].Occupants = byHost[hosts[i].ID]
		hosts[i].LiveInstances = live[hosts[i].ID]
		hosts[i].ErrorInstances = errored[hosts[i].ID]
	}
	return hosts, nil
}
