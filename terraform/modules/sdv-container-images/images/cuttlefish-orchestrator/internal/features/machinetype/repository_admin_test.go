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

package machinetypefeature_test

import (
	"errors"
	"slices"
	"testing"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype/machinetypetest"
	machinetypefeature "cloud-cuttlefish-orchestrator/internal/features/machinetype"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// n1Shapes is what Compute Engine reports for the N1 family. Nothing here is
// curation: the sizes come from the aggregated machine-type list, and the test
// stands in for it.
func n1Shapes() *machinetypetest.FixedDiscovery {
	return machinetypetest.NewFixedDiscovery(
		machinetype.Shape{Name: "n1-standard-8", Family: "n1", VCPUs: 8, MemoryGB: 30, Architecture: machinetype.ArchX86},
		machinetype.Shape{Name: "n1-standard-16", Family: "n1", VCPUs: 16, MemoryGB: 60, Architecture: machinetype.ArchX86},
	)
}

// newCatalog wires the composed catalog over an emulator-backed curation store
// and a written-down discovery, and admits the family the shapes belong to.
func newCatalog(t *testing.T, discovery machinetype.Discovery, gpuMode string) (*machinetypefeature.MachineTypeRepository, *machinetypefeature.CurationRepository) {
	t.Helper()
	db := spannertest.MustNewTestDB(t)
	curation := machinetypefeature.NewCurationRepository(db.Client)
	enabled := true
	maxGPUs := int64(4)
	gpuType := "nvidia-tesla-t4"
	gpuCount := int64(0)
	update := machinetype.FamilyUpdate{IsEnabled: &enabled, GPUMode: &gpuMode}
	if gpuMode == machinetype.GPUModeAttachable {
		update.MaxGPUs, update.DefaultGPUType, update.DefaultGPUCount = &maxGPUs, &gpuType, &gpuCount
	}
	if _, err := curation.UpdateFamilyCuration(t.Context(), "n1", update); err != nil {
		t.Fatalf("curating the n1 family: %v", err)
	}
	return machinetypefeature.NewMachineTypeRepository(curation, discovery), curation
}

// Enabling a family is the whole curation act: every shape Compute Engine
// reports for it is offered, sized by Compute Engine, with no row per shape.
func TestEnablingAFamilyOffersEveryDiscoveredShape(t *testing.T) {
	t.Parallel()
	repo, _ := newCatalog(t, n1Shapes(), machinetype.GPUModeNone)

	types, err := repo.List(t.Context(), machinetype.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(types) != 2 {
		t.Fatalf("catalog holds %d shapes, want both the family offers", len(types))
	}
	got := types[0]
	if got.ID != "n1-standard-8" || got.VCPUs != 8 || got.MemoryGB != 30 {
		t.Errorf("first entry = %s (%d vCPUs, %d GB), want n1-standard-8 sized from discovery",
			got.ID, got.VCPUs, got.MemoryGB)
	}
	if got.Name != "N1 Standard 8" {
		t.Errorf("display name = %q, want the derived %q", got.Name, "N1 Standard 8")
	}
	if !got.IsEnabled {
		t.Error("a shape of an enabled family is not enabled")
	}
}

func TestUpdateAdminSettingsSingleDefault(t *testing.T) {
	t.Parallel()
	repo, _ := newCatalog(t, n1Shapes(), machinetype.GPUModeNone)
	ctx := t.Context()

	makeDefault := true
	if _, err := repo.UpdateAdminSettings(ctx, "n1-standard-8", machinetype.AdminUpdate{IsDefault: &makeDefault}); err != nil {
		t.Fatalf("electing the first default: %v", err)
	}

	// Electing a new default clears the previous one in the same commit.
	updated, err := repo.UpdateAdminSettings(ctx, "n1-standard-16", machinetype.AdminUpdate{IsDefault: &makeDefault})
	if err != nil {
		t.Fatalf("UpdateAdminSettings: %v", err)
	}
	if !updated.IsDefault {
		t.Error("updated entry is not the default")
	}

	previous, err := repo.Get(ctx, "n1-standard-8")
	if err != nil {
		t.Fatalf("Get previous default: %v", err)
	}
	if previous.IsDefault {
		t.Error("previous default was not cleared")
	}

	def, err := repo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault: %v", err)
	}
	if def.ID != "n1-standard-16" {
		t.Errorf("default = %s, want n1-standard-16", def.ID)
	}
}

func TestUpdateAdminSettingsExcludesOneShape(t *testing.T) {
	t.Parallel()
	repo, _ := newCatalog(t, n1Shapes(), machinetype.GPUModeNone)
	ctx := t.Context()

	disabled := false
	updated, err := repo.UpdateAdminSettings(ctx, "n1-standard-8", machinetype.AdminUpdate{IsEnabled: &disabled})
	if err != nil {
		t.Fatalf("UpdateAdminSettings: %v", err)
	}
	if updated.IsEnabled {
		t.Error("entry still enabled")
	}

	// Persisted, not merely echoed back. The shape it excluded is the only
	// one: its sibling in the same family stays offered.
	got, err := repo.Get(ctx, "n1-standard-8")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.IsEnabled {
		t.Error("persisted entry is still enabled")
	}
	sibling, err := repo.Get(ctx, "n1-standard-16")
	if err != nil {
		t.Fatalf("Get sibling: %v", err)
	}
	if !sibling.IsEnabled {
		t.Error("excluding one shape disabled its family's other shapes")
	}
}

// Display order is the family's alone. A shape carrying an override row (here
// an exclusion and the default election) still sorts where its family does, so
// moving the family moves every shape in it.
func TestSortOrderComesFromTheFamily(t *testing.T) {
	t.Parallel()
	repo, curation := newCatalog(t, n1Shapes(), machinetype.GPUModeNone)
	ctx := t.Context()

	makeDefault := true
	if _, err := repo.UpdateAdminSettings(ctx, "n1-standard-8", machinetype.AdminUpdate{IsDefault: &makeDefault}); err != nil {
		t.Fatalf("electing the default: %v", err)
	}
	disabled := false
	if _, err := repo.UpdateAdminSettings(ctx, "n1-standard-16", machinetype.AdminUpdate{IsEnabled: &disabled}); err != nil {
		t.Fatalf("excluding a shape: %v", err)
	}
	sort := int64(42)
	if _, err := curation.UpdateFamilyCuration(ctx, "n1", machinetype.FamilyUpdate{SortOrder: &sort}); err != nil {
		t.Fatalf("moving the family: %v", err)
	}

	for _, id := range []string{"n1-standard-8", "n1-standard-16"} {
		got, err := repo.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if got.SortOrder != 42 {
			t.Errorf("%s sort_order = %d, want the family's 42", id, got.SortOrder)
		}
	}
}

func TestUpdateAdminSettingsNotFound(t *testing.T) {
	t.Parallel()
	repo, _ := newCatalog(t, n1Shapes(), machinetype.GPUModeNone)

	enabled := true
	_, err := repo.UpdateAdminSettings(t.Context(), "no-such-type", machinetype.AdminUpdate{IsEnabled: &enabled})
	if !errors.Is(err, machinetype.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// The whole loop the operator was caught in: the catalog withholds a machine
// too narrow for a guest, the override exists to say the arithmetic is wrong
// for a workload, and until admission read the override the write that would
// have fixed it was refused for the very reason it was being made.
//
// End to end, because that circle is only visible across the two: the PATCH has
// to land on a shape no list carries, and the next read has to serve it.
func TestACapacityOverrideAdmitsABelowFloorShape(t *testing.T) {
	t.Parallel()
	discovery := machinetypetest.NewFixedDiscovery(
		machinetype.Shape{Name: "n1-standard-8", Family: "n1", VCPUs: 8, MemoryGB: 30, Architecture: machinetype.ArchX86},
		// Two vCPUs is one over the host's own CPU reserve: no guest fits,
		// however much memory the machine carries.
		machinetype.Shape{Name: "n1-highmem-2", Family: "n1", VCPUs: 2, MemoryGB: 13, Architecture: machinetype.ArchX86},
	)
	repo, _ := newCatalog(t, discovery, machinetype.GPUModeNone)
	ctx := t.Context()

	_, err := repo.Get(ctx, "n1-highmem-2")
	unoffered, ok := errors.AsType[*machinetype.UnofferedError](err)
	if !ok {
		t.Fatalf("Get before the override = %v, want the catalog's typed refusal", err)
	}
	if unoffered.Reason != machinetype.ReasonBelowFloor {
		t.Errorf("reason = %q, want %q; the family is enabled, so nothing about it is the problem",
			unoffered.Reason, machinetype.ReasonBelowFloor)
	}
	if unoffered.VCPUs != 2 || unoffered.MemoryGB != 13 {
		t.Errorf("refusal states %d vCPUs and %d GB, want the size that refused it", unoffered.VCPUs, unoffered.MemoryGB)
	}

	// The write the old admission made unreachable: it names a shape the served
	// catalog does not carry, and it is what puts the shape there.
	updated, err := repo.UpdateAdminSettings(ctx, "n1-highmem-2", machinetype.AdminUpdate{MaxDevices: new(int64(2))})
	if err != nil {
		t.Fatalf("setting a capacity override on a below-floor shape: %v", err)
	}
	if updated.Fit != 2 || !updated.FitOverridden {
		t.Errorf("PATCH returned fit %d overridden %v, want the operator's 2 marked as theirs",
			updated.Fit, updated.FitOverridden)
	}

	served, err := repo.Get(ctx, "n1-highmem-2")
	if err != nil {
		t.Fatalf("Get after the override: %v", err)
	}
	if served.Fit != 2 {
		t.Errorf("served fit = %d, want the stored 2", served.Fit)
	}

	types, err := repo.List(ctx, machinetype.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	ids := make([]string, len(types))
	for i, mt := range types {
		ids[i] = mt.ID
	}
	if !slices.Contains(ids, "n1-highmem-2") {
		t.Errorf("catalog serves %v, want the overridden shape among them; a ladder is validated against this list", ids)
	}

	// Clearing it returns the machine to the arithmetic, and the arithmetic
	// withholds it again.
	if _, err := repo.UpdateAdminSettings(ctx, "n1-highmem-2", machinetype.AdminUpdate{MaxDevices: new(int64(0))}); err != nil {
		t.Fatalf("clearing the override: %v", err)
	}
	if _, err := repo.Get(ctx, "n1-highmem-2"); !errors.Is(err, machinetype.ErrNotFound) {
		t.Errorf("Get after clearing = %v, want the shape withheld again", err)
	}
}

// GPU policy is the family's alone. A shape with an override row reports the
// family's policy, and a change to the family reaches it on the next read.
func TestGPUPolicyComesFromTheFamily(t *testing.T) {
	t.Parallel()
	repo, curation := newCatalog(t, n1Shapes(), machinetype.GPUModeAttachable)
	ctx := t.Context()

	if _, err := repo.UpdateAdminSettings(ctx, "n1-standard-8", machinetype.AdminUpdate{MaxDevices: new(int64(2))}); err != nil {
		t.Fatalf("storing a capacity: %v", err)
	}
	maxGPUs := int64(2)
	gpuType := "nvidia-l4"
	count := int64(1)
	if _, err := curation.UpdateFamilyCuration(ctx, "n1", machinetype.FamilyUpdate{
		MaxGPUs: &maxGPUs, DefaultGPUType: &gpuType, DefaultGPUCount: &count,
	}); err != nil {
		t.Fatalf("changing the family's GPU policy: %v", err)
	}

	for _, id := range []string{"n1-standard-8", "n1-standard-16"} {
		got, err := repo.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if got.MaxGPUs != 2 || got.DefaultGPUType != "nvidia-l4" || got.DefaultGPUCount != 1 {
			t.Errorf("%s policy = max %d type %q count %d, want the family's 2 nvidia-l4 1",
				id, got.MaxGPUs, got.DefaultGPUType, got.DefaultGPUCount)
		}
	}
}

// A family update is checked against the merged row, so a default GPU count
// above the ceiling is refused whether the patch or the stored row supplies
// either side, and nothing from the refused patch is stored.
func TestUpdateFamilyCurationRefusesDefaultGPUsAboveTheCeiling(t *testing.T) {
	t.Parallel()
	_, curation := newCatalog(t, n1Shapes(), machinetype.GPUModeAttachable)
	ctx := t.Context()

	stored := int64(2)
	if _, err := curation.UpdateFamilyCuration(ctx, "n1", machinetype.FamilyUpdate{DefaultGPUCount: &stored}); err != nil {
		t.Fatalf("storing a default count under the stored ceiling of 4: %v", err)
	}

	cases := []struct {
		name   string
		update machinetype.FamilyUpdate
	}{
		{"over the patched ceiling", machinetype.FamilyUpdate{MaxGPUs: new(int64(1)), DefaultGPUCount: new(int64(2))}},
		{"over the stored ceiling", machinetype.FamilyUpdate{DefaultGPUCount: new(int64(8))}},
		{"ceiling lowered under the stored count", machinetype.FamilyUpdate{MaxGPUs: new(int64(1))}},
	}
	for _, tc := range cases {
		_, err := curation.UpdateFamilyCuration(ctx, "n1", tc.update)
		if _, ok := errors.AsType[*machinetype.GPUPolicyError](err); !ok {
			t.Errorf("%s: err = %v, want a *GPUPolicyError", tc.name, err)
		}
	}

	family, err := curation.GetFamilyCuration(ctx, "n1")
	if err != nil {
		t.Fatalf("GetFamilyCuration: %v", err)
	}
	if family.MaxGPUs != 4 || family.DefaultGPUCount != 2 {
		t.Errorf("stored policy = max %d count %d, want the untouched 4 and 2",
			family.MaxGPUs, family.DefaultGPUCount)
	}
}

// TestUpdateAdminSettingsLeavesUnsetColumnsNull covers the write that is
// easiest to get wrong. A curation toggle must not carry a zero into columns
// the patch never mentioned: the composition reads NULL as "the family
// answers", so a zero written here would become a stored capacity of nothing.
// The domain reads NULL and 0 identically, so this has to be asserted on the
// raw row or it cannot fail.
func TestUpdateAdminSettingsLeavesUnsetColumnsNull(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	curation := machinetypefeature.NewCurationRepository(db.Client)
	ctx := t.Context()
	enabled := true
	if _, err := curation.UpdateFamilyCuration(ctx, "n1", machinetype.FamilyUpdate{IsEnabled: &enabled}); err != nil {
		t.Fatalf("curating the n1 family: %v", err)
	}
	repo := machinetypefeature.NewMachineTypeRepository(curation, n1Shapes())

	makeDefault := true
	if _, err := repo.UpdateAdminSettings(ctx, "n1-standard-8", machinetype.AdminUpdate{IsDefault: &makeDefault}); err != nil {
		t.Fatalf("UpdateAdminSettings: %v", err)
	}

	row, err := db.Client.Single().ReadRow(ctx, "machine_type_overrides", spanner.Key{"n1-standard-8"},
		[]string{"is_default", "is_enabled", "max_devices"})
	if err != nil {
		t.Fatalf("ReadRow: %v", err)
	}
	var (
		isDefault  bool
		isEnabled  spanner.NullBool
		maxDevices spanner.NullInt64
	)
	if err := row.Columns(&isDefault, &isEnabled, &maxDevices); err != nil {
		t.Fatalf("Columns: %v", err)
	}
	if !isDefault {
		t.Error("is_default = false, want the election stored")
	}
	if isEnabled.Valid || maxDevices.Valid {
		t.Errorf("a default election wrote columns it never named: is_enabled %v, max_devices %v\n"+
			"unset must stay NULL so the family's answer shows through", isEnabled, maxDevices)
	}
}

// TestUpdateAdminSettingsKeepsStoredCapacity is the other half: a curation
// toggle leaves an existing capacity override standing.
func TestUpdateAdminSettingsKeepsStoredCapacity(t *testing.T) {
	t.Parallel()
	repo, _ := newCatalog(t, n1Shapes(), machinetype.GPUModeNone)
	ctx := t.Context()

	if _, err := repo.UpdateAdminSettings(ctx, "n1-standard-8", machinetype.AdminUpdate{MaxDevices: new(int64(2))}); err != nil {
		t.Fatalf("storing a capacity: %v", err)
	}
	makeDefault := true
	if _, err := repo.UpdateAdminSettings(ctx, "n1-standard-8", machinetype.AdminUpdate{IsDefault: &makeDefault}); err != nil {
		t.Fatalf("UpdateAdminSettings: %v", err)
	}

	got, err := repo.Get(ctx, "n1-standard-8")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.IsDefault {
		t.Error("the election was not stored")
	}
	if got.Fit != 2 || !got.FitOverridden {
		t.Errorf("capacity changed by a default election: fit %d overridden %v, want the stored 2",
			got.Fit, got.FitOverridden)
	}
}

// A family the curation table has never held is created by the update that
// enables it. This is the N5 case: the day Compute Engine ships a series, the
// operator names it and every shape in it arrives from discovery.
func TestUpdateFamilyCurationAdmitsAnUnknownSeries(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	curation := machinetypefeature.NewCurationRepository(db.Client)
	ctx := t.Context()

	enabled := true
	diskType := "hyperdisk-balanced"
	family, err := curation.UpdateFamilyCuration(ctx, "n5", machinetype.FamilyUpdate{
		IsEnabled: &enabled, BootDiskType: &diskType,
	})
	if err != nil {
		t.Fatalf("UpdateFamilyCuration: %v", err)
	}
	if !family.IsEnabled || family.BootDiskType != diskType {
		t.Errorf("curated family = %+v, want enabled on hyperdisk-balanced", family)
	}
	// Never named, so derived. No display name is ever entered for a series.
	if got := family.DisplayName(); got != "N5 General Purpose" {
		t.Errorf("display name = %q, want the derived %q", got, "N5 General Purpose")
	}

	stored, err := curation.GetFamilyCuration(ctx, "n5")
	if err != nil {
		t.Fatalf("GetFamilyCuration: %v", err)
	}
	if !stored.IsEnabled {
		t.Error("the admission was not persisted")
	}

	// The catalog then offers whatever Compute Engine reports for it, with no
	// second call and no row per shape.
	repo := machinetypefeature.NewMachineTypeRepository(curation, machinetypetest.NewFixedDiscovery(
		machinetype.Shape{Name: "n5-standard-8", Family: "n5", VCPUs: 8, MemoryGB: 32, Architecture: machinetype.ArchX86},
		machinetype.Shape{Name: "n5-highmem-16", Family: "n5", VCPUs: 16, MemoryGB: 128, Architecture: machinetype.ArchX86},
	))
	types, err := repo.List(ctx, machinetype.ListFilter{IsEnabled: &enabled})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(types) != 2 {
		t.Fatalf("the newly admitted family offers %d shapes, want both", len(types))
	}
	if types[0].BootDiskType != diskType {
		t.Errorf("boot disk type = %q, want the family's %q", types[0].BootDiskType, diskType)
	}
}

// A family withdrawn from the catalog takes its shapes with it, and an
// override cannot smuggle one back: enablement is the family's to grant.
func TestDisablingAFamilyDisablesItsShapes(t *testing.T) {
	t.Parallel()
	repo, curation := newCatalog(t, n1Shapes(), machinetype.GPUModeNone)
	ctx := t.Context()

	enabled := true
	if _, err := repo.UpdateAdminSettings(ctx, "n1-standard-8", machinetype.AdminUpdate{IsEnabled: &enabled}); err != nil {
		t.Fatalf("re-admitting the shape: %v", err)
	}
	disabled := false
	if _, err := curation.UpdateFamilyCuration(ctx, "n1", machinetype.FamilyUpdate{IsEnabled: &disabled}); err != nil {
		t.Fatalf("withdrawing the family: %v", err)
	}

	got, err := repo.Get(ctx, "n1-standard-8")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.IsEnabled {
		t.Error("a shape of a withdrawn family is still enabled; its own override outranked the family")
	}
}

// The curation view reports what the served catalog withholds. Composition
// drops a shape below the Cuttlefish floor and reports an excluded one as
// merely disabled, so a family listing built from the offered entries would
// show one shape where Compute Engine reported three, which an operator reads
// as a discovery that lost rows.
func TestFamilyCatalogReportsTheShapesCompositionWithholds(t *testing.T) {
	t.Parallel()
	discovery := machinetypetest.NewFixedDiscovery(
		machinetype.Shape{Name: "n1-standard-8", Family: "n1", VCPUs: 8, MemoryGB: 30},
		machinetype.Shape{Name: "n1-standard-16", Family: "n1", VCPUs: 16, MemoryGB: 60},
		// 4 GB is the host's own reserve exactly: nothing is left for a guest.
		machinetype.Shape{Name: "n1-highcpu-2", Family: "n1", VCPUs: 2, MemoryGB: 4},
	)
	repo, _ := newCatalog(t, discovery, machinetype.GPUModeNone)
	ctx := t.Context()

	excluded := false
	if _, err := repo.UpdateAdminSettings(ctx, "n1-standard-16", machinetype.AdminUpdate{IsEnabled: &excluded}); err != nil {
		t.Fatalf("excluding a shape: %v", err)
	}

	families, err := repo.ListFamilyCatalog(ctx)
	if err != nil {
		t.Fatalf("ListFamilyCatalog: %v", err)
	}
	if len(families) != 1 {
		t.Fatalf("curation view holds %d families, want the one curated", len(families))
	}
	family := families[0]
	if len(family.Shapes) != 3 {
		t.Fatalf("family reports %d shapes, want every shape discovery found", len(family.Shapes))
	}
	if family.Viable != 2 {
		t.Errorf("viable count = %d, want the two shapes that hold a guest", family.Viable)
	}
	// Narrowest first, so the matrix rows read in size order.
	if family.Shapes[0].MachineType != "n1-highcpu-2" {
		t.Errorf("first shape = %s, want the narrowest", family.Shapes[0].MachineType)
	}

	byType := map[string]machinetype.FamilyShape{}
	for _, shape := range family.Shapes {
		byType[shape.MachineType] = shape
	}
	if small := byType["n1-highcpu-2"]; small.Viable || small.Fit != 0 {
		t.Errorf("n1-highcpu-2 = viable %v holding %d, want unviable and holding nothing", small.Viable, small.Fit)
	}
	if standard := byType["n1-standard-8"]; standard.Fit != 3 {
		t.Errorf("n1-standard-8 fit = %d, want the three guests 30 GB holds", standard.Fit)
	}
	if wide := byType["n1-standard-16"]; !wide.Excluded {
		t.Error("the excluded shape is not reported as excluded, so the chip editor would show it as offered")
	}
}
