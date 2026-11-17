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

// Package machinetypetest provides an in-memory fake of the machine-type
// catalog with the real repository's contract (ErrNotFound on missing
// entries, filter-honoring List).
package machinetypetest

import (
	"context"

	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// FakeRepo is a map-backed machinetype.Repository (and therefore Catalog).
// Seed Types directly; the zero value is usable.
type FakeRepo struct {
	Types map[string]*machinetype.MachineType

	// FamilyCatalogs answers the curation view; nil answers an empty one.
	FamilyCatalogs []*machinetype.FamilyCatalog

	// GetErr, when set, fails every read, to simulate an outage.
	GetErr error
}

// NewFakeRepo creates a fake seeded with the given machine types.
func NewFakeRepo(types ...*machinetype.MachineType) *FakeRepo {
	f := &FakeRepo{Types: make(map[string]*machinetype.MachineType, len(types))}
	for _, mt := range types {
		f.Types[mt.ID] = mt
	}
	return f
}

// Get retrieves a machine type by ID; machinetype.ErrNotFound when absent.
func (f *FakeRepo) Get(_ context.Context, id string) (*machinetype.MachineType, error) {
	if f.GetErr != nil {
		return nil, f.GetErr
	}
	mt, ok := f.Types[id]
	if !ok {
		return nil, machinetype.ErrNotFound
	}
	return mt, nil
}

// List retrieves machine types matching the filter (family, category,
// architecture, GPU support, enablement).
func (f *FakeRepo) List(_ context.Context, filter machinetype.ListFilter) ([]*machinetype.MachineType, error) {
	if f.GetErr != nil {
		return nil, f.GetErr
	}
	var result []*machinetype.MachineType
	for _, mt := range f.Types {
		switch {
		case filter.Family != nil && mt.Family != *filter.Family:
		case filter.Category != nil && mt.Category != *filter.Category:
		case filter.Architecture != nil && mt.Architecture != *filter.Architecture:
		case filter.GPUSupport != nil && mt.GPUSupport != *filter.GPUSupport:
		case filter.IsEnabled != nil && mt.IsEnabled != *filter.IsEnabled:
		default:
			result = append(result, mt)
		}
	}
	return result, nil
}

// GetDefault returns the enabled default machine type;
// machinetype.ErrNotFound when none is flagged.
func (f *FakeRepo) GetDefault(_ context.Context) (*machinetype.MachineType, error) {
	if f.GetErr != nil {
		return nil, f.GetErr
	}
	for _, mt := range f.Types {
		if mt.IsDefault && mt.IsEnabled {
			return mt, nil
		}
	}
	return nil, machinetype.ErrNotFound
}

// ListFamilies aggregates the enabled machine types by family.
func (f *FakeRepo) ListFamilies(_ context.Context) ([]*machinetype.Family, error) {
	byFamily := make(map[string]*machinetype.Family)
	for _, mt := range f.Types {
		if !mt.IsEnabled {
			continue
		}
		fam, ok := byFamily[mt.Family]
		if !ok {
			fam = &machinetype.Family{
				Family:   mt.Family,
				Name:     machinetype.FamilyDisplayName(mt.Family, mt.Category),
				Category: mt.Category, Architecture: mt.Architecture,
			}
			byFamily[mt.Family] = fam
		}
		fam.TypeCount++
		fam.GPUSupport = fam.GPUSupport || mt.GPUSupport
	}
	result := make([]*machinetype.Family, 0, len(byFamily))
	for _, fam := range byFamily {
		result = append(result, fam)
	}
	return result, nil
}

// ListFamilyCatalog answers the seeded curation view.
func (f *FakeRepo) ListFamilyCatalog(_ context.Context) ([]*machinetype.FamilyCatalog, error) {
	if f.GetErr != nil {
		return nil, f.GetErr
	}
	return f.FamilyCatalogs, nil
}

// UpdateAdminSettings applies curation changes, keeping the single-default
// invariant (setting a new default clears the previous one).
func (f *FakeRepo) UpdateAdminSettings(_ context.Context, id string, update machinetype.AdminUpdate) (*machinetype.MachineType, error) {
	mt, ok := f.Types[id]
	if !ok {
		return nil, machinetype.ErrNotFound
	}
	if update.IsEnabled != nil {
		mt.IsEnabled = *update.IsEnabled
	}
	if update.IsDefault != nil && *update.IsDefault {
		for _, other := range f.Types {
			other.IsDefault = false
		}
		mt.IsDefault = true
	}
	return mt, nil
}

// Compile-time check that FakeRepo implements the full repository.
var _ machinetype.Repository = (*FakeRepo)(nil)
