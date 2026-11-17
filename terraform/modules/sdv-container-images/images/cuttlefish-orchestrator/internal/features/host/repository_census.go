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
	"fmt"
	"slices"
	"strings"
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// This file is the census cluster: read-only counts and fleet aggregations
// consumed by the stats endpoints, the operations page, the GCE sync's zone
// derivation, and quota reconciliation.

// CountByStatus returns host counts per status
func (r *HostRepository) CountByStatus(ctx context.Context) (map[host.Status]int64, error) {
	stmt := spanner.Statement{
		SQL: `SELECT status, COUNT(*) as count FROM hosts GROUP BY status`,
	}

	counts := make(map[host.Status]int64)
	if err := r.client.Single().Query(ctx, stmt).Do(func(row *spanner.Row) error {
		var status string
		var count int64
		if err := row.Columns(&status, &count); err != nil {
			return err
		}
		counts[host.Status(status)] = count
		return nil
	}); err != nil {
		return nil, fmt.Errorf("counting hosts by status: %w", err)
	}

	return counts, nil
}

// CountByZoneFamily returns the fleet's (zone × machine family) census: live
// machines, how many of them are still coming up, and the devices they carry.
//
// The family is folded in Go rather than derived in SQL because the derivation
// is domain policy (the same ExtractMachineFamily zone_health cells are keyed
// by), and a second copy of it in a query would let a cooldown and a census
// disagree about which family a host belongs to.
//
// The device count comes off the host rows rather than from the devices table:
// it is the same counter every host listing shows, so the two surfaces cannot
// report different fleets, and it needs no join.
func (r *HostRepository) CountByZoneFamily(ctx context.Context) ([]host.ZoneFamilyCount, error) {
	stmt := spanner.Statement{
		SQL: `SELECT zone, IFNULL(machine_type, '') AS shape, status,
		             COUNT(*) AS machines, SUM(current_device_count) AS devices
		      FROM hosts
		      WHERE status != 'deleted'
		      GROUP BY zone, shape, status
		      ORDER BY zone, shape, status`,
	}

	type zoneFamily struct {
		zone   string
		family string
	}
	folded := make(map[zoneFamily]host.ZoneFamilyCount)
	if err := r.client.Single().Query(ctx, stmt).Do(func(row *spanner.Row) error {
		var zone, shape, status string
		var machines, devices int64
		if err := row.Columns(&zone, &shape, &status, &machines, &devices); err != nil {
			return err
		}
		key := zoneFamily{zone: zone, family: machinetype.FamilyOf(shape)}
		cell := folded[key]
		cell.Zone, cell.MachineFamily = key.zone, key.family
		cell.Machines += machines
		cell.Devices += devices
		if host.Status(status) == host.StatusPending || host.Status(status) == host.StatusProvisioning {
			cell.Arriving += machines
		}
		folded[key] = cell
		return nil
	}); err != nil {
		return nil, fmt.Errorf("counting hosts by zone and family: %w", err)
	}

	counts := make([]host.ZoneFamilyCount, 0, len(folded))
	for _, cell := range folded {
		counts = append(counts, cell)
	}
	slices.SortFunc(counts, func(a, b host.ZoneFamilyCount) int {
		if c := strings.Compare(a.Zone, b.Zone); c != 0 {
			return c
		}
		return strings.Compare(a.MachineFamily, b.MachineFamily)
	})
	return counts, nil
}

// CountByModelStatus returns the fleet's provisioning-model × status census
// (the Spot/on-demand split behind the operations view). Rows predating the
// model column read as STANDARD, the create-time default.
func (r *HostRepository) CountByModelStatus(ctx context.Context) ([]host.ModelStatusCount, error) {
	stmt := spanner.Statement{
		SQL: `SELECT IFNULL(provisioning_model, '') as model, status, COUNT(*) as count FROM hosts
		      WHERE status != 'deleted'
		      GROUP BY model, status
		      ORDER BY model, status`,
	}

	// Fold in Go: a legacy NULL-model row and an explicit STANDARD row must
	// land in the same cell.
	type modelStatus struct {
		model  host.ProvisioningModel
		status host.Status
	}
	folded := make(map[modelStatus]int64)
	if err := r.client.Single().Query(ctx, stmt).Do(func(row *spanner.Row) error {
		var model, status string
		var count int64
		if err := row.Columns(&model, &status, &count); err != nil {
			return err
		}
		if model == "" {
			model = string(host.ProvisioningModelStandard)
		}
		folded[modelStatus{host.ProvisioningModel(model), host.Status(status)}] += count
		return nil
	}); err != nil {
		return nil, fmt.Errorf("counting hosts by model and status: %w", err)
	}

	counts := make([]host.ModelStatusCount, 0, len(folded))
	for cell, count := range folded {
		counts = append(counts, host.ModelStatusCount{Model: cell.model, Status: cell.status, Count: count})
	}
	slices.SortFunc(counts, func(a, b host.ModelStatusCount) int {
		if c := strings.Compare(string(a.Model), string(b.Model)); c != 0 {
			return c
		}
		return strings.Compare(string(a.Status), string(b.Status))
	})
	return counts, nil
}

// CountByShape returns the fleet's machine-shape × class × status census: what
// the fleet is MADE OF, where CountByZoneFamily says where it is and
// CountByModelStatus says how it was bought.
//
// The accelerator comes off the host row, not from the machine-type catalog. A
// catalog edit after a host was provisioned would otherwise relabel a host that
// has not changed, and on an integrated-GPU shape the row is where the stamped
// accelerator lives at all.
//
// The aliases are distinct from the columns they wrap on purpose. An alias
// that repeats its own column name gives GROUP BY a name that resolves two
// ways, to the base column or to the projected expression, and which one it
// picks is not something to leave to a reading of the grammar.
func (r *HostRepository) CountByShape(ctx context.Context) ([]host.ShapeCount, error) {
	stmt := spanner.Statement{
		SQL: `SELECT IFNULL(machine_type, '') AS shape,
		             IFNULL(gpu_type, '') AS accelerator,
		             gpu_count,
		             (` + hostClassExpr + `) AS class,
		             status,
		             COUNT(*) AS count
		      FROM hosts
		      WHERE status != 'deleted'
		      GROUP BY shape, accelerator, gpu_count, class, status
		      ORDER BY shape, accelerator, class, status`,
		Params: map[string]any{paramSystemOrigins: systemOrigins()},
	}

	var counts []host.ShapeCount
	if err := r.client.Single().Query(ctx, stmt).Do(func(row *spanner.Row) error {
		var shape, accelerator, class, status string
		var gpuCount, count int64
		if err := row.Columns(&shape, &accelerator, &gpuCount, &class, &status, &count); err != nil {
			return err
		}
		counts = append(counts, host.ShapeCount{
			MachineType: shape,
			GPUType:     accelerator,
			GPUCount:    gpuCount,
			Class:       host.Class(class),
			Status:      host.Status(status),
			Count:       count,
		})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("counting hosts by shape: %w", err)
	}

	return counts, nil
}

// CountOutcomesSince returns how many hosts were created since the cutoff and
// how many of those sit in error (total, errored), for the
// provisioning-success lens.
func (r *HostRepository) CountOutcomesSince(ctx context.Context, since time.Time) (int64, int64, error) {
	stmt := spanner.Statement{
		SQL: `SELECT COUNT(*), COUNTIF(status = 'error')
		      FROM hosts WHERE created_at >= @since`,
		Params: map[string]any{"since": since},
	}

	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if err != nil {
		return 0, 0, fmt.Errorf("counting host outcomes: %w", err)
	}
	var total, errored int64
	if err := row.Columns(&total, &errored); err != nil {
		return 0, 0, fmt.Errorf("scanning host outcomes: %w", err)
	}
	return total, errored, nil
}

// ListActiveZones returns the distinct zones of non-deleted hosts. The GCE
// sync derives its zone list from it, so hosts are reconciled wherever they
// live without a static zone configuration.
func (r *HostRepository) ListActiveZones(ctx context.Context) ([]string, error) {
	stmt := spanner.Statement{SQL: `SELECT DISTINCT zone FROM hosts WHERE status != 'deleted'`}
	var zones []string
	if err := r.client.Single().Query(ctx, stmt).Do(func(row *spanner.Row) error {
		var zone string
		if err := row.Columns(&zone); err != nil {
			return err
		}
		zones = append(zones, zone)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("listing active zones: %w", err)
	}
	return zones, nil
}
