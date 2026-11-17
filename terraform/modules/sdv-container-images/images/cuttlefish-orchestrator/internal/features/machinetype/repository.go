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
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"

	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// MachineTypeRepository serves the machine-type catalog by composing the three
// things that each hold a piece of it: the families an operator curated, the
// shapes Compute Engine offers in them, and the per-shape overrides laid on
// top.
//
// Nothing about a shape is stored. Its existence, its width and its memory
// come from Compute Engine every time the catalog is read, so a family gains
// its whole shape range the moment it is enabled, an entry Google retires
// stops being offered on the next refresh, and no release of this service
// stands between a new machine generation and a host running on it.
type MachineTypeRepository struct {
	curation  *CurationRepository
	discovery machinetype.Discovery
}

// NewMachineTypeRepository composes the catalog over the operator's curation
// and the platform's shape discovery.
func NewMachineTypeRepository(curation *CurationRepository, discovery machinetype.Discovery) *MachineTypeRepository {
	return &MachineTypeRepository{curation: curation, discovery: discovery}
}

// catalog is one composed reading of the whole catalog: every entry it holds,
// and the curation behind them.
type catalog struct {
	entries   []*machinetype.MachineType
	byID      map[string]*machinetype.MachineType
	shapes    map[string]machinetype.Shape
	families  map[string]*machinetype.FamilyCuration
	overrides map[string]*machinetype.ShapeOverride
}

// compose reads the curation and the discovery and renders every entry the
// catalog offers, in display order.
//
// A discovery that has not landed is refused rather than answered empty:
// composing zero entries from it would report the fleet as having no machine
// shapes, which is a statement about the operator's catalog and not about this
// instance's cold start.
func (r *MachineTypeRepository) compose(ctx context.Context) (catalog, error) {
	shapes, discovered := r.discovery.DiscoveredShapes(ctx)
	if !discovered {
		return catalog{}, machinetype.ErrCatalogUnavailable
	}
	families, err := r.curationByFamily(ctx)
	if err != nil {
		return catalog{}, err
	}
	overrides, err := r.curation.ListOverrides(ctx)
	if err != nil {
		return catalog{}, err
	}

	composed := catalog{
		entries:   make([]*machinetype.MachineType, 0, len(shapes)),
		byID:      make(map[string]*machinetype.MachineType, len(shapes)),
		shapes:    shapes,
		families:  families,
		overrides: overrides,
	}
	for _, shape := range shapes {
		family := families[shape.Family]
		// A shape whose family left the curation table is not the fleet's to
		// offer; the next structural refresh stops fetching it either way.
		if family == nil {
			continue
		}
		// Composed before it is judged, because the operator's per-shape
		// capacity is part of the answer: the arithmetic refuses a machine too
		// small for a default guest, and a stored capacity on that machine is
		// the operator saying the arithmetic is wrong for their workload.
		entry := machinetype.Compose(shape, family, overrides[shape.Name])
		if !entry.Admissible() {
			continue
		}
		composed.entries = append(composed.entries, entry)
		composed.byID[entry.ID] = entry
	}
	slices.SortFunc(composed.entries, byDisplayOrder)
	return composed, nil
}

// byDisplayOrder is the catalog's ordering: the family's curated sort order,
// then the family id, then the machine's own size. The id breaks the last tie
// so two shapes of one family with the same width (a standard and a highmem)
// never swap places between two reads of the same catalog.
func byDisplayOrder(a, b *machinetype.MachineType) int {
	if c := cmp.Compare(a.SortOrder, b.SortOrder); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Family, b.Family); c != 0 {
		return c
	}
	if c := cmp.Compare(a.VCPUs, b.VCPUs); c != 0 {
		return c
	}
	return cmp.Compare(a.ID, b.ID)
}

// curationByFamily reads the curated families, keyed by series.
func (r *MachineTypeRepository) curationByFamily(ctx context.Context) (map[string]*machinetype.FamilyCuration, error) {
	rows, err := r.curation.ListFamilyCuration(ctx)
	if err != nil {
		return nil, err
	}
	byFamily := make(map[string]*machinetype.FamilyCuration, len(rows))
	for _, family := range rows {
		byFamily[family.Family] = family
	}
	return byFamily, nil
}

// Get retrieves a machine type by id, or the typed reason the catalog does not
// offer it.
//
// The typed reason matters. A rung an operator wrote, a create naming a shape by
// hand and a console lookup all arrive here, and "not found" was answering
// three questions at once: a name nobody can fix, a family to enable, and a
// machine the packing arithmetic refuses that a per-shape capacity override
// admits. Each has a different remedy, so each gets its own sentence.
func (r *MachineTypeRepository) Get(ctx context.Context, id string) (*machinetype.MachineType, error) {
	composed, err := r.compose(ctx)
	if err != nil {
		return nil, err
	}
	if entry, ok := composed.byID[id]; ok {
		return entry, nil
	}
	if shape, ok := composed.shapes[id]; ok {
		return nil, withheldShape(composed, shape)
	}
	return r.getCustom(ctx, composed, id)
}

// withheldShape names why composition dropped a shape discovery did report:
// its family left the curation table, or the machine holds no guest and no
// stored capacity says otherwise.
func withheldShape(composed catalog, shape machinetype.Shape) error {
	if composed.families[shape.Family] == nil {
		return &machinetype.UnofferedError{
			MachineType: shape.Name,
			Reason:      machinetype.ReasonFamilyNotCurated,
			Family:      shape.Family,
		}
	}
	return &machinetype.UnofferedError{
		MachineType: shape.Name,
		Reason:      machinetype.ReasonBelowFloor,
		Family:      shape.Family,
		VCPUs:       shape.VCPUs,
		MemoryGB:    shape.MemoryGB,
	}
}

// getCustom composes an entry for a shape Compute Engine builds on demand.
//
// Compute Engine never lists a custom shape, so the catalog asks it to describe
// the machine by name, which is also what proves the name is a machine it will
// build.
func (r *MachineTypeRepository) getCustom(ctx context.Context, composed catalog, id string) (*machinetype.MachineType, error) {
	family, ok := machinetype.CustomFamily(id)
	if !ok {
		// Not discovered, and not a name any family builds on demand. An
		// uncurated series is the reason worth naming; anything else is a name
		// Compute Engine has no machine for.
		series := machinetype.FamilyOf(id)
		if composed.families[series] == nil {
			return nil, &machinetype.UnofferedError{
				MachineType: id, Reason: machinetype.ReasonFamilyNotCurated, Family: series,
			}
		}
		return nil, &machinetype.UnofferedError{MachineType: id, Reason: machinetype.ReasonUnknown}
	}
	curated := composed.families[family]
	if curated == nil {
		return nil, &machinetype.UnofferedError{
			MachineType: id, Reason: machinetype.ReasonFamilyNotCurated, Family: family,
		}
	}
	shape, err := r.discovery.ResolveShape(ctx, id)
	if err != nil {
		return nil, err
	}
	entry := machinetype.Compose(shape, curated, composed.overrides[id])
	if !entry.Admissible() {
		return nil, &machinetype.UnofferedError{
			MachineType: id, Reason: machinetype.ReasonBelowFloor, Family: family,
			VCPUs: shape.VCPUs, MemoryGB: shape.MemoryGB,
		}
	}
	return entry, nil
}

// List retrieves machine types with filtering, in the catalog's display order.
// A positive Limit truncates; the serving paths leave it zero and get the
// whole catalog.
func (r *MachineTypeRepository) List(ctx context.Context, filter machinetype.ListFilter) ([]*machinetype.MachineType, error) {
	composed, err := r.compose(ctx)
	if err != nil {
		return nil, err
	}
	entries := matching(composed.entries, filter)
	if filter.Limit > 0 && filter.Limit < len(entries) {
		entries = entries[:filter.Limit]
	}
	return entries, nil
}

// matching keeps the entries the filter admits, in order.
func matching(entries []*machinetype.MachineType, filter machinetype.ListFilter) []*machinetype.MachineType {
	kept := make([]*machinetype.MachineType, 0, len(entries))
	for _, entry := range entries {
		if admits(entry, filter) {
			kept = append(kept, entry)
		}
	}
	return kept
}

// admits reports whether one entry passes every named filter term.
func admits(entry *machinetype.MachineType, filter machinetype.ListFilter) bool {
	switch {
	case filter.Family != nil && entry.Family != *filter.Family:
		return false
	case filter.Category != nil && entry.Category != *filter.Category:
		return false
	case filter.Architecture != nil && entry.Architecture != *filter.Architecture:
		return false
	case filter.GPUSupport != nil && entry.GPUSupport != *filter.GPUSupport:
		return false
	case filter.IsEnabled != nil && entry.IsEnabled != *filter.IsEnabled:
		return false
	case filter.Zone != nil && len(entry.Zones) > 0 && !slices.Contains(entry.Zones, *filter.Zone):
		return false
	}
	return true
}

// GetDefault returns the default machine type: the one shape an operator
// elected, composed like every other entry.
func (r *MachineTypeRepository) GetDefault(ctx context.Context) (*machinetype.MachineType, error) {
	composed, err := r.compose(ctx)
	if err != nil {
		return nil, err
	}
	for _, entry := range composed.entries {
		if entry.IsDefault && entry.IsEnabled {
			return entry, nil
		}
	}
	return nil, errors.New("no default machine type configured")
}

// UpdateAdminSettings applies curation changes to one shape and returns the
// updated entry.
//
// The write lands on the shape's override row; the entry that comes back is
// composed from the same discovery every read uses, so what the PATCH returns
// and what the next list serves are the same rendering by construction.
//
// The shape has to EXIST (Compute Engine reported it, or will build it) and a
// curated family has to hold it, or the override row would be one nothing ever
// reads. It does not have to be offered: a machine the packing arithmetic
// refuses is exactly the machine a capacity override is written for, and
// requiring the catalog to offer it first would make the escape hatch reachable
// only where it is not needed.
func (r *MachineTypeRepository) UpdateAdminSettings(ctx context.Context, id string, update machinetype.AdminUpdate) (*machinetype.MachineType, error) {
	shape, err := r.shapeOf(ctx, id)
	if err != nil {
		return nil, err
	}
	family, err := r.curation.GetFamilyCuration(ctx, shape.Family)
	if err != nil {
		return nil, err
	}
	overrides, err := r.curation.UpdateOverride(ctx, id, update)
	if err != nil {
		return nil, err
	}
	return machinetype.Compose(shape, family, overrides[id]), nil
}

// shapeOf reads one shape's discovered facts, falling back to the on-demand
// resolution a custom machine type needs.
func (r *MachineTypeRepository) shapeOf(ctx context.Context, id string) (machinetype.Shape, error) {
	shapes, discovered := r.discovery.DiscoveredShapes(ctx)
	if !discovered {
		return machinetype.Shape{}, machinetype.ErrCatalogUnavailable
	}
	if shape, ok := shapes[id]; ok {
		return shape, nil
	}
	return r.discovery.ResolveShape(ctx, id)
}

// ListFamilies aggregates the enabled machine types by family.
func (r *MachineTypeRepository) ListFamilies(ctx context.Context) ([]*machinetype.Family, error) {
	composed, err := r.compose(ctx)
	if err != nil {
		return nil, err
	}
	byFamily := map[string]*machinetype.Family{}
	for _, entry := range composed.entries {
		if !entry.IsEnabled {
			continue
		}
		summary, ok := byFamily[entry.Family]
		if !ok {
			summary = &machinetype.Family{
				Family:       entry.Family,
				Name:         composed.families[entry.Family].DisplayName(),
				Category:     entry.Category,
				Architecture: entry.Architecture,
			}
			byFamily[entry.Family] = summary
		}
		summary.TypeCount++
		summary.GPUSupport = summary.GPUSupport || entry.GPUSupport
	}
	families := make([]*machinetype.Family, 0, len(byFamily))
	for _, family := range slices.Sorted(maps.Keys(byFamily)) {
		families = append(families, byFamily[family])
	}
	return families, nil
}

// ListFamilyCatalog answers the curation view: each curated family with every
// shape Compute Engine offers under it.
//
// It reads the discovery instead of the composed catalog, because composition
// withholds two kinds of shape an operator curating a family has to see: the
// ones below the Cuttlefish floor that no capacity override admits, and the
// ones an override excludes. A view built from the served entries could show
// neither, and the first is the row an operator goes to this page to override.
func (r *MachineTypeRepository) ListFamilyCatalog(ctx context.Context) ([]*machinetype.FamilyCatalog, error) {
	shapes, discovered := r.discovery.DiscoveredShapes(ctx)
	if !discovered {
		return nil, machinetype.ErrCatalogUnavailable
	}
	families, err := r.curationByFamily(ctx)
	if err != nil {
		return nil, err
	}
	overrides, err := r.curation.ListOverrides(ctx)
	if err != nil {
		return nil, err
	}

	catalogs := make(map[string]*machinetype.FamilyCatalog, len(families))
	for name, curation := range families {
		catalogs[name] = &machinetype.FamilyCatalog{Curation: curation}
	}
	for _, shape := range shapes {
		catalog := catalogs[shape.Family]
		// A shape whose family left the curation table is not the fleet's to
		// report; the next structural refresh stops fetching it either way.
		if catalog == nil {
			continue
		}
		entry := machinetype.FamilyShapeOf(shape, overrides[shape.Name])
		catalog.Shapes = append(catalog.Shapes, entry)
		if entry.Viable {
			catalog.Viable++
		}
	}

	listed := make([]*machinetype.FamilyCatalog, 0, len(catalogs))
	for _, catalog := range catalogs {
		slices.SortFunc(catalog.Shapes, byShapeSize)
		listed = append(listed, catalog)
	}
	slices.SortFunc(listed, byFamilyOrder)
	return listed, nil
}

// byShapeSize is the order a family's shapes read in: narrowest first, with the
// id breaking the tie so a standard and a highmem of one width never swap
// places between two reads of the same family.
func byShapeSize(a, b machinetype.FamilyShape) int {
	if c := cmp.Compare(a.VCPUs, b.VCPUs); c != 0 {
		return c
	}
	return cmp.Compare(a.MachineType, b.MachineType)
}

// byFamilyOrder is the operator's display order, the series breaking ties.
func byFamilyOrder(a, b *machinetype.FamilyCatalog) int {
	if c := cmp.Compare(a.Curation.SortOrder, b.Curation.SortOrder); c != 0 {
		return c
	}
	return cmp.Compare(a.Curation.Family, b.Curation.Family)
}

// Ensure the composed catalog satisfies the domain repository.
var _ machinetype.Repository = (*MachineTypeRepository)(nil)
