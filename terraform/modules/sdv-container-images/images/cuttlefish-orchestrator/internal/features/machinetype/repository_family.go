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

package machinetypefeature

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/grpc/codes"

	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// Table and column names the curation writes repeat (goconst).
const (
	familiesTable  = "machine_families"
	overridesTable = "machine_type_overrides"
	colMachineType = "machine_type"
	colIsDefault   = "is_default"
	colIsEnabled   = "is_enabled"
	colSortOrder   = "sort_order"
	colMaxDevices  = "max_devices"
	colUpdatedAt   = "updated_at"
	colCreatedAt   = "created_at"
	colMaxGPUs     = "max_gpus"
	colGPUType     = "default_gpu_type"
	colGPUCount    = "default_gpu_count"
)

// familyColumnsSQL and overrideColumnsSQL are explicit projections; SELECT *
// breaks on additive migrations because ToStruct rejects unknown columns, and
// this repo deploys DDL-first, so the old binary keeps serving through the
// schema-change window.
var (
	familyColumnsSQL   = strings.Join(familyColumns(), ", ")
	overrideColumnsSQL = strings.Join(overrideColumns(), ", ")
)

// CurationRepository holds the whole of the operator's machine curation: the
// families the fleet provisions, and the per-shape exclusions and elections
// laid over them.
//
// It is separate from the machine-type repository that reads it, because the
// catalog snapshot narrows its Compute Engine fetch to these families, and the
// composed catalog is built from that snapshot. One type doing both would be a
// cycle.
type CurationRepository struct {
	client *spannerplatform.Client
}

// NewCurationRepository creates the machine-curation repository.
func NewCurationRepository(client *spannerplatform.Client) *CurationRepository {
	return &CurationRepository{client: client}
}

// ListFamilyCuration reads every curated family, enabled or not.
func (r *CurationRepository) ListFamilyCuration(ctx context.Context) ([]*machinetype.FamilyCuration, error) {
	stmt := spanner.Statement{
		SQL: `SELECT ` + familyColumnsSQL + ` FROM ` + familiesTable + ` ORDER BY sort_order ASC, family ASC`,
	}
	return spannerplatform.QueryAll(r.client.Single().Query(ctx, stmt),
		"machine families", (*models.MachineFamilyRow).ToDomain)
}

// GetFamilyCuration reads one family's curation row.
func (r *CurationRepository) GetFamilyCuration(ctx context.Context, family string) (*machinetype.FamilyCuration, error) {
	row, err := r.client.Single().ReadRow(ctx, familiesTable, spanner.Key{family}, familyColumns())
	if err != nil {
		if spanner.ErrCode(err) == codes.NotFound {
			return nil, fmt.Errorf("machine family %s: %w", family, machinetype.ErrFamilyNotFound)
		}
		return nil, fmt.Errorf("failed to get machine family: %w", err)
	}
	var famRow models.MachineFamilyRow
	if err := row.ToStruct(&famRow); err != nil {
		return nil, fmt.Errorf("failed to scan machine family row: %w", err)
	}
	return famRow.ToDomain(), nil
}

// UpdateFamilyCuration applies curation changes to a family, creating the row
// when the family has never been curated.
//
// It creates a missing family instead of refusing it, so admitting a
// series Compute Engine shipped this morning is one call naming it, and every
// shape it holds arrives from discovery. A family that had to exist before it
// could be enabled would put a table edit between the operator and a new
// machine generation.
func (r *CurationRepository) UpdateFamilyCuration(ctx context.Context, family string, update machinetype.FamilyUpdate) (*machinetype.FamilyCuration, error) {
	var result *machinetype.FamilyCuration
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		result = nil // Reset: the transaction body may run more than once.
		now := time.Now()
		current, err := readFamily(ctx, txn, family, now)
		if err != nil {
			return err
		}
		applyFamilyUpdate(current, update)
		if err := current.CheckGPUPolicy(); err != nil {
			return err
		}
		current.UpdatedAt = now
		result = current
		return txn.BufferWrite([]*spanner.Mutation{
			spanner.InsertOrUpdateMap(familiesTable, familyColumnValues(current)),
		})
	})
	if err != nil {
		return nil, fmt.Errorf("updating machine family curation: %w", err)
	}
	return result, nil
}

// readFamily loads a family's row, or the defaults a never-curated family
// starts from: disabled, general-purpose x86, no GPU policy. Disabled because
// an update that says nothing about enablement must not admit a family by
// accident.
func readFamily(ctx context.Context, txn *spanner.ReadWriteTransaction, family string, now time.Time) (*machinetype.FamilyCuration, error) {
	row, err := txn.ReadRow(ctx, familiesTable, spanner.Key{family}, familyColumns())
	if spanner.ErrCode(err) == codes.NotFound {
		return &machinetype.FamilyCuration{
			Family:       family,
			Category:     machinetype.CategoryGeneralPurpose,
			Architecture: machinetype.ArchX86,
			GPUMode:      machinetype.GPUModeNone,
			CreatedAt:    now,
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading machine family: %w", err)
	}
	var famRow models.MachineFamilyRow
	if err := row.ToStruct(&famRow); err != nil {
		return nil, fmt.Errorf("scanning machine family row: %w", err)
	}
	return famRow.ToDomain(), nil
}

// applyFamilyUpdate overlays the patch's named fields onto the curation row.
func applyFamilyUpdate(family *machinetype.FamilyCuration, update machinetype.FamilyUpdate) {
	setString(&family.Name, update.Name)
	setString(&family.Description, update.Description)
	if update.Category != nil {
		family.Category = *update.Category
	}
	if update.Architecture != nil {
		family.Architecture = *update.Architecture
	}
	setString(&family.CPUType, update.CPUType)
	setString(&family.BootDiskType, update.BootDiskType)
	setString(&family.GPUMode, update.GPUMode)
	setInt64(&family.MaxGPUs, update.MaxGPUs)
	setString(&family.DefaultGPUType, update.DefaultGPUType)
	setInt64(&family.DefaultGPUCount, update.DefaultGPUCount)
	if update.IsEnabled != nil {
		family.IsEnabled = *update.IsEnabled
	}
	setInt64(&family.SortOrder, update.SortOrder)
}

func setString(into, from *string) {
	if from != nil {
		*into = *from
	}
}

func setInt64(into, from *int64) {
	if from != nil {
		*into = *from
	}
}

// familyColumnValues renders a curation row for the write. An empty optional
// stays NULL rather than becoming the empty string: the composition reads the
// two identically, but a NULL says an operator never wrote the label.
func familyColumnValues(family *machinetype.FamilyCuration) map[string]any {
	return map[string]any{
		"family":         family.Family,
		"name":           nullString(family.Name),
		"description":    nullString(family.Description),
		"category":       string(family.Category),
		"architecture":   string(family.Architecture),
		"cpu_type":       nullString(family.CPUType),
		"boot_disk_type": nullString(family.BootDiskType),
		"gpu_mode":       family.GPUMode,
		colMaxGPUs:       nullInt64(family.MaxGPUs),
		colGPUType:       nullString(family.DefaultGPUType),
		colGPUCount:      nullInt64(family.DefaultGPUCount),
		colIsEnabled:     family.IsEnabled,
		colSortOrder:     family.SortOrder,
		colCreatedAt:     family.CreatedAt,
		colUpdatedAt:     family.UpdatedAt,
	}
}

func nullString(value string) spanner.NullString {
	return spanner.NullString{StringVal: value, Valid: value != ""}
}

func nullInt64(value int64) spanner.NullInt64 {
	return spanner.NullInt64{Int64: value, Valid: value != 0}
}

// ListOverrides reads every per-shape override, keyed by machine type.
func (r *CurationRepository) ListOverrides(ctx context.Context) (map[string]*machinetype.ShapeOverride, error) {
	stmt := spanner.Statement{SQL: `SELECT ` + overrideColumnsSQL + ` FROM ` + overridesTable}
	rows, err := spannerplatform.QueryAll(r.client.Single().Query(ctx, stmt),
		"machine type overrides", (*models.MachineTypeOverrideRow).ToDomain)
	if err != nil {
		return nil, err
	}
	byType := make(map[string]*machinetype.ShapeOverride, len(rows))
	for _, override := range rows {
		byType[override.MachineType] = override
	}
	return byType, nil
}

// UpdateOverride applies curation changes to one shape, creating the override
// row when the shape has never had one, and returns every override afterwards
// so the caller can recompose the catalog from one read.
//
// Becoming the default clears every other default in the same commit
// (single-default invariant).
func (r *CurationRepository) UpdateOverride(ctx context.Context, machineType string, update machinetype.AdminUpdate) (map[string]*machinetype.ShapeOverride, error) {
	var overrides map[string]*machinetype.ShapeOverride
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		now := time.Now()
		current, err := readOverrides(ctx, txn)
		if err != nil {
			return err
		}
		target := current[machineType]
		if target == nil {
			target = &machinetype.ShapeOverride{MachineType: machineType, CreatedAt: now}
			current[machineType] = target
		}
		applyShapeUpdate(target, update)
		target.UpdatedAt = now

		muts := []*spanner.Mutation{spanner.InsertOrUpdateMap(overridesTable, overrideColumnValues(target))}
		if update.IsDefault != nil && *update.IsDefault {
			for id, other := range current {
				if id == machineType || !other.IsDefault {
					continue
				}
				other.IsDefault = false
				other.UpdatedAt = now
				muts = append(muts, spanner.UpdateMap(overridesTable, map[string]any{
					colMachineType: id, colIsDefault: false, colUpdatedAt: now,
				}))
			}
		}
		overrides = current
		return txn.BufferWrite(muts)
	})
	if err != nil {
		return nil, fmt.Errorf("updating machine type curation: %w", err)
	}
	return overrides, nil
}

// readOverrides loads every override inside the transaction. Whole-table
// because the single-default invariant is a statement about the set: electing
// a default has to see the one it replaces, and the table holds one row per
// curated shape instead of one row per shape.
func readOverrides(ctx context.Context, txn *spanner.ReadWriteTransaction) (map[string]*machinetype.ShapeOverride, error) {
	byType := map[string]*machinetype.ShapeOverride{}
	iter := txn.Query(ctx, spanner.Statement{SQL: `SELECT ` + overrideColumnsSQL + ` FROM ` + overridesTable})
	if err := iter.Do(func(row *spanner.Row) error {
		var overrideRow models.MachineTypeOverrideRow
		if err := row.ToStruct(&overrideRow); err != nil {
			return err
		}
		byType[overrideRow.MachineType] = overrideRow.ToDomain()
		return nil
	}); err != nil {
		return nil, fmt.Errorf("reading machine type overrides: %w", err)
	}
	return byType, nil
}

// applyShapeUpdate overlays the patch's named fields onto the override.
func applyShapeUpdate(override *machinetype.ShapeOverride, update machinetype.AdminUpdate) {
	if update.IsEnabled != nil {
		override.IsEnabled = update.IsEnabled
	}
	if update.MaxDevices != nil {
		// Zero clears: an override exists to disagree with the arithmetic, and
		// clearing it is returning to the arithmetic rather than storing a
		// capacity of nothing.
		if *update.MaxDevices == 0 {
			override.MaxDevices = nil
		} else {
			override.MaxDevices = update.MaxDevices
		}
	}
	if update.IsDefault != nil {
		override.IsDefault = *update.IsDefault
	}
}

// overrideColumnValues renders an override row for the write. A field the
// operator never set stays NULL, which is what makes the family's answer show
// through rather than being shadowed by a zero.
func overrideColumnValues(override *machinetype.ShapeOverride) map[string]any {
	return map[string]any{
		colMachineType: override.MachineType,
		colIsEnabled:   nullBoolPtr(override.IsEnabled),
		colIsDefault:   override.IsDefault,
		colMaxDevices:  nullInt64Ptr(override.MaxDevices),
		colCreatedAt:   override.CreatedAt,
		colUpdatedAt:   override.UpdatedAt,
	}
}

func nullBoolPtr(value *bool) spanner.NullBool {
	if value == nil {
		return spanner.NullBool{}
	}
	return spanner.NullBool{Bool: *value, Valid: true}
}

func nullInt64Ptr(value *int64) spanner.NullInt64 {
	if value == nil {
		return spanner.NullInt64{}
	}
	return spanner.NullInt64{Int64: *value, Valid: true}
}

func familyColumns() []string {
	return []string{
		"family", "name", "description", "category", "architecture", "cpu_type",
		"boot_disk_type", "gpu_mode", colMaxGPUs, colGPUType,
		colGPUCount, colIsEnabled, colSortOrder, colCreatedAt, colUpdatedAt,
	}
}

func overrideColumns() []string {
	return []string{
		colMachineType, colIsEnabled, colIsDefault, colMaxDevices, colCreatedAt, colUpdatedAt,
	}
}
