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

package gce

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/compute/apiv1/computepb"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// fakeCompute answers the catalog's four calls from canned data and records
// every request, so a test can assert both what was asked and how often. The
// recording is guarded because the two refreshers are driven concurrently
// below; the canned answers are written once, before any refresh.
type fakeCompute struct {
	mu sync.Mutex
	// calls names each API call in order; its length is the refresh cost.
	calls []string

	zones             []*computepb.Zone
	machineScopes     []MachineTypeScope
	machineUnreached  []string
	acceleratorScopes []AcceleratorScope
	regions           []*computepb.Region

	// machineGets answers machineTypes.get by name, the call that describes a
	// custom shape, which no list ever carries.
	machineGets map[string]*computepb.MachineType

	machineReqs    []*computepb.AggregatedListMachineTypesRequest
	machineGetReqs []*computepb.GetMachineTypeRequest
	regionReqs     []*computepb.ListRegionsRequest
	zoneReqs       []*computepb.ListZonesRequest

	zoneErr error
}

func (f *fakeCompute) MachineTypes(_ context.Context, req *computepb.AggregatedListMachineTypesRequest) (AggregatedList[MachineTypeScope], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "machineTypes.aggregatedList")
	f.machineReqs = append(f.machineReqs, req)
	return AggregatedList[MachineTypeScope]{Scopes: f.machineScopes, Unreachable: f.machineUnreached}, nil
}

func (f *fakeCompute) MachineType(_ context.Context, req *computepb.GetMachineTypeRequest) (*computepb.MachineType, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "machineTypes.get")
	f.machineGetReqs = append(f.machineGetReqs, req)
	if mt, ok := f.machineGets[req.GetMachineType()]; ok {
		return mt, nil
	}
	return nil, errors.New("Invalid value for field 'machineType': not a valid machine type")
}

func (f *fakeCompute) AcceleratorTypes(_ context.Context, _ *computepb.AggregatedListAcceleratorTypesRequest) (AggregatedList[AcceleratorScope], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "acceleratorTypes.aggregatedList")
	return AggregatedList[AcceleratorScope]{Scopes: f.acceleratorScopes}, nil
}

func (f *fakeCompute) Zones(_ context.Context, req *computepb.ListZonesRequest) ([]*computepb.Zone, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "zones.list")
	f.zoneReqs = append(f.zoneReqs, req)
	return f.zones, f.zoneErr
}

func (f *fakeCompute) Regions(_ context.Context, req *computepb.ListRegionsRequest) ([]*computepb.Region, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "regions.list")
	f.regionReqs = append(f.regionReqs, req)
	return f.regions, nil
}

// fakeFamilies is the operator's machine curation. The tests name the shapes
// they care about and the families follow from them, which is the direction
// the real catalog reads too: an operator curates a series, and Compute Engine
// says which shapes it holds.
type fakeFamilies struct{ ids []string }

func (f *fakeFamilies) ListFamilyCuration(context.Context) ([]*machinetype.FamilyCuration, error) {
	seen := map[string]bool{}
	out := make([]*machinetype.FamilyCuration, 0, len(f.ids))
	for _, id := range f.ids {
		family := machinetype.FamilyOf(id)
		if seen[family] {
			continue
		}
		seen[family] = true
		out = append(out, &machinetype.FamilyCuration{Family: family, IsEnabled: true})
	}
	return out, nil
}

// fakeEnabledZones is the operator's zone curation.
type fakeEnabledZones struct {
	zones []string
	err   error
}

func (f *fakeEnabledZones) ListEnabledZones(context.Context) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.zones, nil
}

// gceZone builds one entry of the project's zone list.
func gceZone(name string) *computepb.Zone {
	return &computepb.Zone{
		Name:   new(name),
		Region: new(gcecatalog.RegionFromZone(name)),
		Status: new(computepb.Zone_UP.String()),
	}
}

// offering builds a scope Compute Engine answered with these machine types.
func offering(zone string, names ...string) MachineTypeScope {
	list := &computepb.MachineTypesScopedList{}
	for _, name := range names {
		list.MachineTypes = append(list.MachineTypes, &computepb.MachineType{Name: new(name)})
	}
	if len(names) == 0 {
		list.Warning = &computepb.Warning{Code: new(computepb.Warning_NO_RESULTS_ON_PAGE.String())}
	}
	return MachineTypeScope{Scope: zoneScopePrefix + zone, List: list}
}

// unreadable builds a scope Compute Engine returned without an answer, the
// shape a per-zone failure takes under partial success.
func unreadable(zone string) MachineTypeScope {
	return MachineTypeScope{
		Scope: zoneScopePrefix + zone,
		List: &computepb.MachineTypesScopedList{
			Warning: &computepb.Warning{Code: new(computepb.Warning_UNREACHABLE.String())},
		},
	}
}

// blank builds a scope returned empty with no warning saying why. The API
// never explains it, so it is not an answer.
func blank(zone string) MachineTypeScope {
	return MachineTypeScope{Scope: zoneScopePrefix + zone, List: &computepb.MachineTypesScopedList{}}
}

// deleted builds a scope still listing a machine type Compute Engine has
// deleted from that zone.
func deleted(zone, name string) MachineTypeScope {
	return MachineTypeScope{
		Scope: zoneScopePrefix + zone,
		List: &computepb.MachineTypesScopedList{
			MachineTypes: []*computepb.MachineType{{
				Name: new(name),
				Deprecated: &computepb.DeprecationStatus{
					State: new(computepb.DeprecationStatus_DELETED.String()),
				},
			}},
		},
	}
}

func gpuOffering(zone, name string, cards int32) AcceleratorScope {
	return AcceleratorScope{
		Scope: zoneScopePrefix + zone,
		List: &computepb.AcceleratorTypesScopedList{
			AcceleratorTypes: []*computepb.AcceleratorType{
				{Name: new(name), MaximumCardsPerInstance: new(cards)},
			},
		},
	}
}

func gceRegion(name, metric string, limit, usage float64) *computepb.Region {
	return &computepb.Region{
		Name: new(name),
		Quotas: []*computepb.Quota{
			{Metric: new(metric), Limit: new(limit), Usage: new(usage)},
		},
	}
}

// warnedRegion is a region Compute Engine answered without quota data: the
// call fails open, so the quotas array arrives empty with the warning field
// populated instead of the request failing.
func warnedRegion(name string) *computepb.Region {
	return &computepb.Region{
		Name: new(name),
		QuotaStatusWarning: &computepb.QuotaStatusWarning{
			Code:    new("EXTERNAL_API_WARNING"),
			Message: new("quota information is unavailable for this region"),
		},
	}
}

// fakeDimensioned answers the quota axes the region list cannot carry.
type fakeDimensioned struct {
	rows []*gcecatalog.RegionalQuota
	err  error
}

func (f *fakeDimensioned) Quotas(context.Context, []string) ([]*gcecatalog.RegionalQuota, error) {
	return f.rows, f.err
}

// newTestCatalog wires a catalog over the three fakes.
func newTestCatalog(compute ComputeCatalog, shapes, enabled []string) *Catalog {
	return newTestCatalogWith(compute, shapes, &fakeEnabledZones{zones: enabled}, slog.Default())
}

// newTestCatalogWithQuotaAxes adds the reader for the axes the region list
// cannot carry.
func newTestCatalogWithQuotaAxes(compute ComputeCatalog, enabled []string, axes DimensionedQuotas) *Catalog {
	return NewCatalog("test-project", CatalogDeps{
		Compute:     compute,
		Families:    &fakeFamilies{ids: []string{"n1-standard-8"}},
		Zones:       &fakeEnabledZones{zones: enabled},
		Dimensioned: axes,
	}, slog.Default())
}

// newTestCatalogWith wires a catalog for the tests that keep a handle on the
// zone curation (to change or fail it mid-test) or on the logger.
func newTestCatalogWith(compute ComputeCatalog, shapes []string, zones EnabledZones, logger *slog.Logger) *Catalog {
	return NewCatalog("test-project", CatalogDeps{
		Compute:  compute,
		Families: &fakeFamilies{ids: shapes},
		Zones:    zones,
	}, logger)
}

// Before the first refresh lands, every cell answers unknown, which removes
// no placement candidate. Blocking readiness on a three-second structural
// fetch would put that on every cold start instead.
func TestCatalogColdStartAnswersUnknown(t *testing.T) {
	t.Parallel()
	c := newTestCatalog(&fakeCompute{}, []string{"n1-standard-8"}, []string{"europe-west4-a"})

	if got := c.MachineTypeAvailability("europe-west4-a", "n1-standard-8"); got != gcecatalog.AvailabilityUnknown {
		t.Errorf("machine type availability = %v, want unknown before the first refresh", got)
	}
	if got := c.AcceleratorAvailability("europe-west4-a", "nvidia-tesla-t4"); got != gcecatalog.AvailabilityUnknown {
		t.Errorf("accelerator availability = %v, want unknown before the first refresh", got)
	}
	if c.RegionalQuota("europe-west4", gcecatalog.QuotaTypeCPUs) != nil {
		t.Error("quota answered before the first refresh")
	}
	if c.Snapshot() != nil {
		t.Error("a snapshot exists before any refresh ran")
	}
}

// The refresh reads what Compute Engine offers in a fixed number of calls,
// whatever the fleet's zone count. A per-zone implementation would answer the
// same questions and cost a call per zone, which is the shape this replaces.
func TestCatalogRefreshCostIsIndependentOfZoneCount(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{
		zones: []*computepb.Zone{
			gceZone("europe-west4-a"), gceZone("europe-west4-b"),
			gceZone("us-central1-a"), gceZone("asia-east1-a"),
		},
		machineScopes: []MachineTypeScope{
			offering("europe-west4-a", "n1-standard-8"),
			offering("europe-west4-b", "n1-standard-8"),
			offering("us-central1-a", "n1-standard-8"),
			offering("asia-east1-a", "n1-standard-8"),
		},
		regions: []*computepb.Region{
			gceRegion("europe-west4", gcecatalog.QuotaTypeCPUs, 500, 100),
			gceRegion("us-central1", gcecatalog.QuotaTypeCPUs, 500, 100),
			gceRegion("asia-east1", gcecatalog.QuotaTypeCPUs, 500, 100),
		},
	}

	three := newTestCatalog(fake, []string{"n1-standard-8"},
		[]string{"europe-west4-a", "europe-west4-b", "us-central1-a"})
	if err := three.RefreshStructural(t.Context()); err != nil {
		t.Fatalf("structural refresh: %v", err)
	}
	if err := three.refreshQuota(t.Context()); err != nil {
		t.Fatalf("quota refresh: %v", err)
	}
	want := []string{"zones.list", "machineTypes.aggregatedList", "acceleratorTypes.aggregatedList", "regions.list"}
	if !slices.Equal(fake.calls, want) {
		t.Fatalf("calls with three enabled zones = %v, want %v", fake.calls, want)
	}

	// A fourth zone, in a fourth region, is enabled.
	fake.calls = nil
	four := newTestCatalog(fake, []string{"n1-standard-8"},
		[]string{"europe-west4-a", "europe-west4-b", "us-central1-a", "asia-east1-a"})
	if err := four.RefreshStructural(t.Context()); err != nil {
		t.Fatalf("structural refresh: %v", err)
	}
	if err := four.refreshQuota(t.Context()); err != nil {
		t.Fatalf("quota refresh: %v", err)
	}
	if !slices.Equal(fake.calls, want) {
		t.Fatalf("calls with four enabled zones = %v, want the same %v", fake.calls, want)
	}
}

// The filter string is a wire contract with Compute Engine, pinned verbatim:
// `=` compares literally there and `eq` terms do not compose under OR, so the
// single-eq alternation is the only form the API accepts for this narrowing.
// Any drift back toward per-family `=` terms silently matches nothing, which
// the intersect guard then escalates to a failed refresh.
func TestFamilyFilterIsOneRegexTerm(t *testing.T) {
	t.Parallel()
	got := familyFilter([]string{"n4", "c4", "c4a"})
	want := `name eq "(n4|c4|c4a)-.*"`
	if got != want {
		t.Fatalf("familyFilter = %q, want %q", got, want)
	}
}

// The aggregated machine-type list is narrowed server-side to the curated
// families. A malformed filter is answered with HTTP 200 and everything the
// project offers, so the response is also intersected on the way in. The
// assertion on the request alone would not catch it.
//
// The intersect tokenizes the family off the name rather than matching a
// prefix, and n4a is why: it lives in the n4 namespace, runs Axion rather than
// Intel, and a prefix test would fold it into the family an operator curated.
func TestCatalogAppliesTheServerSideFamilyFilter(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{
		zones: []*computepb.Zone{gceZone("europe-west4-a")},
		machineScopes: []MachineTypeScope{
			// What a broken filter returns: the curated families' shapes plus
			// everything else in the zone.
			offering("europe-west4-a", "n4-standard-80", "n4a-standard-8", "n4d-standard-8",
				"e2-standard-4", "f1-micro"),
		},
	}
	c := newTestCatalog(fake, []string{"c4a-standard-8-metal", "n4-standard-4"}, []string{"europe-west4-a"})
	if err := c.RefreshStructural(t.Context()); err != nil {
		t.Fatalf("structural refresh: %v", err)
	}

	req := fake.machineReqs[0]
	if got, want := req.GetFilter(), `name eq "(c4a|n4)-.*"`; got != want {
		t.Errorf("filter = %q, want %q", got, want)
	}
	if !req.GetReturnPartialSuccess() {
		t.Error("the aggregated call did not ask for partial success")
	}
	if !req.GetIncludeAllScopes() {
		t.Error("the aggregated call did not ask for all scopes; an empty scope would be indistinguishable from a failed one")
	}

	offered := c.Snapshot().Shapes["europe-west4-a"]
	if !offered["n4-standard-80"] {
		t.Error("a shape of a curated family the zone offers is missing from the snapshot")
	}
	for _, uncurated := range []string{"n4a-standard-8", "n4d-standard-8", "e2-standard-4", "f1-micro"} {
		if offered[uncurated] {
			t.Errorf("%s is not in a curated family but was recorded as availability", uncurated)
		}
	}
}

// Discovery is the whole of what the catalog knows about a shape's size, so
// the fold has to carry guestCpus, memoryMb and the integrated accelerator off
// the same response that decides availability.
func TestCatalogDiscoversShapeFactsFromTheAggregatedList(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{
		zones: []*computepb.Zone{gceZone("europe-west4-a")},
		machineScopes: []MachineTypeScope{{
			Scope: zoneScopePrefix + "europe-west4-a",
			List: &computepb.MachineTypesScopedList{MachineTypes: []*computepb.MachineType{
				sized("n4-standard-80", 80, 327680, computepb.MachineType_X86_64.String()),
				withAccelerator(sized("g2-standard-4", 4, 16384, computepb.MachineType_X86_64.String()),
					"nvidia-l4", 1),
			}},
		}},
	}
	c := newTestCatalog(fake, []string{"n4-standard-4", "g2-standard-4"}, []string{"europe-west4-a"})
	if err := c.RefreshStructural(t.Context()); err != nil {
		t.Fatalf("structural refresh: %v", err)
	}

	shapes, landed := c.DiscoveredShapes(t.Context())
	if !landed {
		t.Fatal("discovery did not land after a successful refresh")
	}
	n4 := shapes["n4-standard-80"]
	if n4.VCPUs != 80 || n4.MemoryGB != 320 || n4.Family != "n4" || n4.Architecture != machinetype.ArchX86 {
		t.Errorf("n4-standard-80 discovered as %+v, want 80 vCPUs / 320 GB / n4 / x86", n4)
	}
	g2 := shapes["g2-standard-4"]
	if g2.GPUType != "nvidia-l4" || g2.GPUCount != 1 {
		t.Errorf("g2-standard-4 discovered as %+v, want an integrated nvidia-l4", g2)
	}
}

// A custom shape is absent from every list, so the catalog asks Compute Engine
// to describe it by name, which is also what proves the name is a machine it
// will build. The answer is memoized: a second read must not spend a second
// call.
func TestCatalogResolvesCustomShapesThroughMachineTypesGet(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{
		zones:         []*computepb.Zone{gceZone("europe-west4-a")},
		machineScopes: []MachineTypeScope{offering("europe-west4-a", "n4-standard-4")},
		machineGets: map[string]*computepb.MachineType{
			"n4-custom-8-16384": sized("n4-custom-8-16384", 8, 16384, computepb.MachineType_X86_64.String()),
		},
	}
	c := newTestCatalog(fake, []string{"n4-standard-4"}, []string{"europe-west4-a"})
	if err := c.RefreshStructural(t.Context()); err != nil {
		t.Fatalf("structural refresh: %v", err)
	}

	shape, err := c.ResolveShape(t.Context(), "n4-custom-8-16384")
	if err != nil {
		t.Fatalf("ResolveShape: %v", err)
	}
	if shape.VCPUs != 8 || shape.MemoryGB != 16 {
		t.Errorf("custom shape = %+v, want 8 vCPUs / 16 GB from Compute Engine", shape)
	}
	if got := fake.machineGetReqs[0].GetZone(); got != "europe-west4-a" {
		t.Errorf("the get was scoped to %q, want a zone the family is offered in", got)
	}
	if _, err := c.ResolveShape(t.Context(), "n4-custom-8-16384"); err != nil {
		t.Fatalf("second ResolveShape: %v", err)
	}
	if got := len(fake.machineGetReqs); got != 1 {
		t.Errorf("machineTypes.get calls = %d, want 1; the resolution is memoized until the next refresh", got)
	}

	// A combination the family will not build is a machine type that does not
	// exist, and Compute Engine is the only thing that knows which.
	if _, err := c.ResolveShape(t.Context(), "n4-custom-7-16384"); !errors.Is(err, machinetype.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound for a shape Compute Engine refuses", err)
	}
}

// The quota loop publishes a snapshot before the first structural read lands.
// A custom shape asked about then has no answer yet, which is unavailable, and
// must not read as "no zone offers the family", which is not found.
func TestCatalogResolveShapeBeforeDiscoveryIsUnavailable(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{
		regions: []*computepb.Region{gceRegion("us-east1", gcecatalog.QuotaTypeCPUs, 500, 0)},
	}
	c := newTestCatalog(fake, []string{"n4-standard-4"}, []string{"us-east1-b"})
	if err := c.refreshQuota(t.Context()); err != nil {
		t.Fatalf("quota refresh: %v", err)
	}
	if c.Snapshot() == nil {
		t.Fatal("the quota refresh published no snapshot, so this tests the nil-snapshot path instead")
	}
	if _, err := c.ResolveShape(t.Context(), "n4-custom-8-16384"); !errors.Is(err, machinetype.ErrCatalogUnavailable) {
		t.Errorf("err = %v, want ErrCatalogUnavailable before any structural read", err)
	}
}

// A structural read that missed a scope still publishes what it got, and
// reports itself partial so the loop retries in seconds instead of leaving the
// zone unknown for six hours. Curation's own refresh treats it as done, and a
// read where every scope answered is a plain success.
func TestCatalogStructuralReadReportsPartial(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{
		zones:         []*computepb.Zone{gceZone("us-east1-b"), gceZone("us-central1-a")},
		machineScopes: []MachineTypeScope{offering("us-east1-b", "n1-standard-8"), unreadable("us-central1-a")},
		acceleratorScopes: []AcceleratorScope{
			gpuOffering("us-east1-b", "nvidia-tesla-t4", 4),
			gpuOffering("us-central1-a", "nvidia-tesla-t4", 4),
		},
	}
	c := newTestCatalog(fake, []string{"n1-standard-8"}, []string{"us-east1-b"})

	if err := c.refreshStructural(t.Context()); !telemetry.IsPartial(err) {
		t.Fatalf("structural refresh with an unreadable zone = %v, want a Partial error", err)
	}
	if got := c.MachineTypeAvailability("us-east1-b", "n1-standard-8"); got != gcecatalog.AvailabilityOffered {
		t.Errorf("availability after a partial read = %v, want offered: the zones that answered are published", got)
	}
	if err := c.RefreshStructural(t.Context()); err != nil {
		t.Errorf("curation refresh after a partial read = %v, want nil", err)
	}

	fake.mu.Lock()
	fake.machineScopes = []MachineTypeScope{offering("us-east1-b", "n1-standard-8"), offering("us-central1-a")}
	fake.mu.Unlock()
	if err := c.refreshStructural(t.Context()); err != nil {
		t.Errorf("structural refresh with every scope answered = %v, want nil", err)
	}
}

// A custom shape's zone is its family's: Compute Engine never lists one, and a
// zone that stocks the series stocks the custom forms built on it.
func TestCatalogAnswersCustomShapeAvailabilityFromItsFamily(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{
		zones:         []*computepb.Zone{gceZone("europe-west4-a"), gceZone("europe-west4-b")},
		machineScopes: []MachineTypeScope{offering("europe-west4-a", "n4-standard-4"), offering("europe-west4-b")},
	}
	c := newTestCatalog(fake, []string{"n4-standard-4"}, []string{"europe-west4-a"})
	if err := c.RefreshStructural(t.Context()); err != nil {
		t.Fatalf("structural refresh: %v", err)
	}

	if got := c.MachineTypeAvailability("europe-west4-a", "n4-custom-8-16384"); got != gcecatalog.AvailabilityOffered {
		t.Errorf("availability in the zone stocking n4 = %v, want offered", got)
	}
	if got := c.MachineTypeAvailability("europe-west4-b", "n4-custom-8-16384"); got != gcecatalog.AvailabilityNotOffered {
		t.Errorf("availability in the zone stocking no n4 = %v, want not offered", got)
	}
}

// sized builds a machine type as Compute Engine describes one.
func sized(name string, vcpus, memoryMB int32, architecture string) *computepb.MachineType {
	return &computepb.MachineType{
		Name:         new(name),
		GuestCpus:    new(vcpus),
		MemoryMb:     new(memoryMB),
		Architecture: new(architecture),
	}
}

// withAccelerator attaches the integrated GPU an accelerator-optimized shape
// reports on its own resource.
func withAccelerator(mt *computepb.MachineType, gpuType string, count int32) *computepb.MachineType {
	mt.Accelerators = []*computepb.Accelerators{
		{GuestAcceleratorType: new(gpuType), GuestAcceleratorCount: new(count)},
	}
	return mt
}

// The other half of a broken filter: HTTP 200 with nothing matched anywhere.
// Recording that would say the fleet's own shapes exist in no zone on Earth,
// which is a broken filter rather than news, so the refresh fails and the
// previous snapshot stands.
func TestCatalogRefusesAShapeFilterThatMatchedNothing(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{
		zones:         []*computepb.Zone{gceZone("europe-west4-a"), gceZone("us-central1-a")},
		machineScopes: []MachineTypeScope{offering("europe-west4-a"), offering("us-central1-a")},
	}
	c := newTestCatalog(fake, []string{"n1-standard-8"}, []string{"europe-west4-a"})

	err := c.RefreshStructural(t.Context())
	if err == nil {
		t.Fatal("a response offering none of the curated shapes anywhere was accepted as an answer")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Errorf("error = %v, want it to name the filter as the suspect", err)
	}
	if c.Snapshot() != nil {
		t.Error("a snapshot was published from a refresh that failed")
	}
}

// Under partial success Compute Engine omits a scope it could not read rather
// than failing the call. A zone that is silent must stay unknown. A zone
// wrongly recorded as offering nothing is a zone placement can never use
// again until the next refresh.
func TestCatalogFailedScopeIsNotANegativeAnswer(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{
		zones: []*computepb.Zone{
			gceZone("europe-west4-a"), gceZone("europe-west4-b"),
			gceZone("us-central1-a"), gceZone("asia-east1-a"),
			gceZone("europe-north1-a"),
		},
		machineScopes: []MachineTypeScope{
			offering("europe-west4-a", "n1-standard-8"),
			// Reported, and offers none of them.
			offering("europe-west4-b"),
			// Returned without an answer.
			unreadable("us-central1-a"),
			// Returned empty with no warning saying why.
			blank("europe-north1-a"),
			// asia-east1-a is omitted from the response entirely.
		},
		machineUnreached: []string{zoneScopePrefix + "us-central1-a"},
	}
	c := newTestCatalog(fake, []string{"n1-standard-8"}, []string{"europe-west4-a"})
	if err := c.RefreshStructural(t.Context()); err != nil {
		t.Fatalf("structural refresh: %v", err)
	}

	for zone, want := range map[string]gcecatalog.Availability{
		"europe-west4-a":  gcecatalog.AvailabilityOffered,
		"europe-west4-b":  gcecatalog.AvailabilityNotOffered,
		"us-central1-a":   gcecatalog.AvailabilityUnknown,
		"asia-east1-a":    gcecatalog.AvailabilityUnknown,
		"europe-north1-a": gcecatalog.AvailabilityUnknown,
	} {
		if got := c.MachineTypeAvailability(zone, "n1-standard-8"); got != want {
			t.Errorf("availability in %s = %v, want %v", zone, got, want)
		}
	}

	partial := c.Snapshot().Partial
	for _, want := range []string{
		"machine types: us-central1-a", "machine types: asia-east1-a",
		"machine types: europe-north1-a",
	} {
		if !slices.Contains(partial, want) {
			t.Errorf("partial = %v, want it to name %q", partial, want)
		}
	}
	if slices.Contains(partial, "machine types: europe-west4-b") {
		t.Error("a zone that answered 'none of them' was reported as a scope that failed")
	}
	// The zone that answered nothing must not be holding an empty offered set:
	// that is the map shape a wholesale rebuild would have collapsed it into.
	if _, reported := c.Snapshot().Shapes["us-central1-a"]; reported {
		t.Error("an unreadable scope was recorded as a reported one")
	}
}

// An aggregated list pages by ITEM while repeating every scope key on every
// page, so one drained response holds the same zone as NO_RESULTS_ON_PAGE,
// then its items, then NO_RESULTS_ON_PAGE again. Proven against live Compute
// Engine 2026-08-13 (a real project, the curated 17-shape filter, 4 pages):
// folding by assignment instead of merging left only the final page's 7 zones
// offering anything, so every enabled zone read "offers none" and placement
// excluded the whole fleet.
func TestCatalogMergesPageRepeatedScopes(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{
		zones: []*computepb.Zone{gceZone("europe-west4-a"), gceZone("asia-southeast3-a")},
		machineScopes: []MachineTypeScope{
			// Page 1: both scopes named, europe-west4-a's items elsewhere.
			offering("europe-west4-a"),
			offering("asia-southeast3-a"),
			// Page 2: europe-west4-a's items land.
			offering("europe-west4-a", "n1-standard-8"),
			offering("asia-southeast3-a"),
			// Page 3: the tail page names europe-west4-a empty again.
			offering("europe-west4-a"),
			offering("asia-southeast3-a", "n4-standard-8"),
		},
		acceleratorScopes: []AcceleratorScope{
			gpuOffering("europe-west4-a", "nvidia-l4", 1),
			{Scope: zoneScopePrefix + "europe-west4-a", List: &computepb.AcceleratorTypesScopedList{
				Warning: &computepb.Warning{Code: new(computepb.Warning_NO_RESULTS_ON_PAGE.String())},
			}},
			{Scope: zoneScopePrefix + "asia-southeast3-a", List: &computepb.AcceleratorTypesScopedList{
				Warning: &computepb.Warning{Code: new(computepb.Warning_NO_RESULTS_ON_PAGE.String())},
			}},
		},
	}
	c := newTestCatalog(fake, []string{"n1-standard-8", "n4-standard-8"}, []string{"europe-west4-a"})
	if err := c.RefreshStructural(t.Context()); err != nil {
		t.Fatalf("structural refresh: %v", err)
	}

	if got := c.MachineTypeAvailability("europe-west4-a", "n1-standard-8"); got != gcecatalog.AvailabilityOffered {
		t.Errorf("availability in europe-west4-a = %v, want offered; a later page's empty repeat wiped the items", got)
	}
	if got := c.MachineTypeAvailability("asia-southeast3-a", "n4-standard-8"); got != gcecatalog.AvailabilityOffered {
		t.Errorf("availability in asia-southeast3-a = %v, want offered", got)
	}
	if got := c.AcceleratorAvailability("europe-west4-a", "nvidia-l4"); got != gcecatalog.AvailabilityOffered {
		t.Errorf("accelerator availability = %v, want offered; the accelerator fold has the same page shape", got)
	}
	if partial := c.Snapshot().Partial; len(partial) != 0 {
		t.Errorf("partial = %v, want empty, because every scope answered", partial)
	}
}

// Accelerators carry the cards-per-instance ceiling the console shows beside
// the availability answer.
func TestCatalogAcceleratorsCarryTheirCardCeiling(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{
		zones:             []*computepb.Zone{gceZone("europe-west4-a"), gceZone("us-central1-a")},
		machineScopes:     []MachineTypeScope{offering("europe-west4-a", "n1-standard-8")},
		acceleratorScopes: []AcceleratorScope{gpuOffering("europe-west4-a", "nvidia-tesla-t4", 4)},
	}
	c := newTestCatalog(fake, []string{"n1-standard-8"}, []string{"europe-west4-a"})
	if err := c.RefreshStructural(t.Context()); err != nil {
		t.Fatalf("structural refresh: %v", err)
	}

	if got := c.AcceleratorAvailability("europe-west4-a", "nvidia-tesla-t4"); got != gcecatalog.AvailabilityOffered {
		t.Errorf("accelerator availability = %v, want offered", got)
	}
	if got := c.AcceleratorAvailability("europe-west4-a", "nvidia-l4"); got != gcecatalog.AvailabilityNotOffered {
		t.Errorf("unlisted accelerator in a reported zone = %v, want not offered", got)
	}
	if got := c.AcceleratorAvailability("us-central1-a", "nvidia-tesla-t4"); got != gcecatalog.AvailabilityUnknown {
		t.Errorf("accelerator in an unreported zone = %v, want unknown", got)
	}
	cards := c.ZoneAccelerators("europe-west4-a")
	if len(cards) != 1 || cards[0].AcceleratorType != "nvidia-tesla-t4" || cards[0].MaxCount != 4 {
		t.Errorf("zone accelerators = %+v, want one nvidia-tesla-t4 with 4 cards", cards)
	}
}

// Quota is read for the regions holding an enabled zone, and a region the
// response is silent about lands in Partial rather than as zero capacity.
func TestCatalogQuotaRefreshScopesToEnabledRegions(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{
		regions: []*computepb.Region{gceRegion("europe-west4", gcecatalog.QuotaTypeCPUs, 500, 120)},
	}
	c := newTestCatalog(fake, []string{"n1-standard-8"},
		[]string{"europe-west4-a", "europe-west4-b", "us-central1-a"})
	if err := c.refreshQuota(t.Context()); err != nil {
		t.Fatalf("quota refresh: %v", err)
	}

	if got, want := fake.regionReqs[0].GetFilter(), `(name = "europe-west4") OR (name = "us-central1")`; got != want {
		t.Errorf("region filter = %q, want %q", got, want)
	}
	row := c.RegionalQuota("europe-west4", gcecatalog.QuotaTypeCPUs)
	if row == nil || row.LimitValue != 500 || row.UsageValue != 120 {
		t.Fatalf("quota row = %+v, want limit 500 usage 120", row)
	}
	if row.LastSyncAt == nil {
		t.Error("the quota row carries no fetch time, so ranking can never judge it stale")
	}
	if got := c.RegionalQuotas("us-central1"); len(got) != 0 {
		t.Errorf("quotas for the silent region = %+v, want none", got)
	}
	if !slices.Contains(c.Snapshot().Partial, "quota: us-central1") {
		t.Errorf("partial = %v, want it to name the region the response skipped", c.Snapshot().Partial)
	}
}

// A region whose quota fetch failed open is reported as a region the snapshot
// has no quota answer for. Storing its empty quotas array instead would classify
// every metric unknown and pass the whole region advisorily, fleet-wide, with
// nothing anywhere saying why. The warning field exists to tell the two
// apart.
func TestCatalogQuotaFailOpenRegionIsPartial(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{regions: []*computepb.Region{warnedRegion("europe-west4")}}
	c := newTestCatalog(fake, []string{"n1-standard-8"}, []string{"europe-west4-a"})
	if err := c.refreshQuota(t.Context()); err != nil {
		t.Fatalf("quota refresh: %v", err)
	}

	partial := c.Snapshot().Partial
	if !slices.ContainsFunc(partial, func(s string) bool {
		return strings.Contains(s, "europe-west4")
	}) {
		t.Errorf("partial = %v, want it to name the region whose quota fetch failed open", partial)
	}
}

// The previous reading stands through a fail-open answer rather than being
// replaced by nothing: it ages out on its own timestamp, which degrades to an
// advisory pass on its own schedule instead of losing the region's headroom the
// moment one fetch fails.
func TestCatalogQuotaFailOpenKeepsThePreviousReading(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{
		regions: []*computepb.Region{gceRegion("europe-west4", gcecatalog.QuotaTypeCPUs, 500, 120)},
	}
	c := newTestCatalog(fake, []string{"n1-standard-8"}, []string{"europe-west4-a"})
	if err := c.refreshQuota(t.Context()); err != nil {
		t.Fatalf("first quota refresh: %v", err)
	}

	fake.regions = []*computepb.Region{warnedRegion("europe-west4")}
	if err := c.refreshQuota(t.Context()); err != nil {
		t.Fatalf("second quota refresh: %v", err)
	}
	row := c.RegionalQuota("europe-west4", gcecatalog.QuotaTypeCPUs)
	if row == nil || row.LimitValue != 500 || row.UsageValue != 120 {
		t.Fatalf("quota row after a fail-open answer = %+v, want the previous reading carried forward", row)
	}
}

// Compute Engine reports an unlimited metric as 2^63-1, which is above
// math.MaxInt64 once round-tripped through the float64 the API carries it in;
// converting it directly is undefined and the stored number is garbage.
func TestCatalogUnlimitedQuotaLimitDoesNotOverflow(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{
		regions: []*computepb.Region{
			gceRegion("europe-west4", "COMMITTED_NVIDIA_T4_GPUS", 9.223372036854776e+18, 0),
		},
	}
	c := newTestCatalog(fake, []string{"n1-standard-8"}, []string{"europe-west4-a"})
	if err := c.refreshQuota(t.Context()); err != nil {
		t.Fatalf("quota refresh: %v", err)
	}

	row := c.RegionalQuota("europe-west4", "COMMITTED_NVIDIA_T4_GPUS")
	if row == nil {
		t.Fatal("the unlimited row is missing from the snapshot")
	}
	if row.LimitValue != gcecatalog.LimitUnlimited {
		t.Errorf("limit = %d, want %d (the defined unlimited value)", row.LimitValue, gcecatalog.LimitUnlimited)
	}
	if row.Available() <= 0 {
		t.Errorf("available = %d on an unlimited pool, want positive", row.Available())
	}
}

// The axes the region list cannot carry join the same rows every judgment
// reads, so a caller never has to know which call answered.
func TestCatalogFoldsTheDimensionedAxes(t *testing.T) {
	t.Parallel()
	sampledAt := time.Now().Add(-2 * time.Minute)
	fake := &fakeCompute{
		regions: []*computepb.Region{gceRegion("europe-west4", gcecatalog.QuotaTypeCPUs, 500, 120)},
	}
	dimensioned := &fakeDimensioned{rows: []*gcecatalog.RegionalQuota{{
		Region: "europe-west4", QuotaType: "CPUS_PER_VM_FAMILY:C4",
		LimitValue: 300, UsageValue: 96, LastSyncAt: &sampledAt,
	}}}
	c := newTestCatalogWithQuotaAxes(fake, []string{"europe-west4-a"}, dimensioned)
	if err := c.refreshQuota(t.Context()); err != nil {
		t.Fatalf("quota refresh: %v", err)
	}

	row := c.RegionalQuota("europe-west4", "CPUS_PER_VM_FAMILY:C4")
	if row == nil || row.LimitValue != 300 || row.UsageValue != 96 {
		t.Fatalf("per-family row = %+v, want limit 300 usage 96", row)
	}
	if c.RegionalQuota("europe-west4", gcecatalog.QuotaTypeCPUs) == nil {
		t.Error("folding the per-family axes dropped the region list's own rows")
	}
}

// A read the telemetry could not answer leaves no row and names the regions it
// is silent about. Treating an unread axis as exhausted would make every zone
// serving those families unplaceable on the strength of one failed call.
func TestCatalogDimensionedReadFailureLeavesNoRow(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{
		regions: []*computepb.Region{gceRegion("europe-west4", gcecatalog.QuotaTypeCPUs, 500, 120)},
	}
	dimensioned := &fakeDimensioned{err: errors.New("permission denied")}
	c := newTestCatalogWithQuotaAxes(fake, []string{"europe-west4-a"}, dimensioned)
	if err := c.refreshQuota(t.Context()); err != nil {
		t.Fatalf("quota refresh must survive a failed telemetry read: %v", err)
	}

	if row := c.RegionalQuota("europe-west4", "CPUS_PER_VM_FAMILY:C4"); row != nil {
		t.Errorf("row = %+v, want none: a failed read is not a reading of zero", row)
	}
	if !slices.Contains(c.Snapshot().Partial, "quota (per-family): europe-west4") {
		t.Errorf("partial = %v, want it to name the axes with no answer", c.Snapshot().Partial)
	}
}

// A fail-open region carries its whole previous reading forward, these axes
// included. The fresh reading has to replace that copy rather than sit behind
// it, because a lookup takes the first row for the pool.
func TestCatalogFailOpenRegionTakesTheFreshDimensionedRow(t *testing.T) {
	t.Parallel()
	stale := time.Now().Add(-time.Hour)
	fresh := time.Now()
	fake := &fakeCompute{
		regions: []*computepb.Region{gceRegion("europe-west4", gcecatalog.QuotaTypeCPUs, 500, 120)},
	}
	dimensioned := &fakeDimensioned{rows: []*gcecatalog.RegionalQuota{{
		Region: "europe-west4", QuotaType: "CPUS_PER_VM_FAMILY:C4",
		LimitValue: 300, UsageValue: 8, LastSyncAt: &stale,
	}}}
	c := newTestCatalogWithQuotaAxes(fake, []string{"europe-west4-a"}, dimensioned)
	if err := c.refreshQuota(t.Context()); err != nil {
		t.Fatalf("first quota refresh: %v", err)
	}

	fake.regions = []*computepb.Region{warnedRegion("europe-west4")}
	dimensioned.rows = []*gcecatalog.RegionalQuota{{
		Region: "europe-west4", QuotaType: "CPUS_PER_VM_FAMILY:C4",
		LimitValue: 300, UsageValue: 240, LastSyncAt: &fresh,
	}}
	if err := c.refreshQuota(t.Context()); err != nil {
		t.Fatalf("second quota refresh: %v", err)
	}

	row := c.RegionalQuota("europe-west4", "CPUS_PER_VM_FAMILY:C4")
	if row == nil || row.UsageValue != 240 {
		t.Fatalf("per-family row = %+v, want the fresh reading (usage 240)", row)
	}
	if got := len(c.RegionalQuotas("europe-west4")); got != 2 {
		t.Errorf("rows = %d, want 2: the carried-forward CPUS row and one per-family row", got)
	}
}

// Each refresher owns half the snapshot and carries the other half forward:
// the minute-rate quota refresh must not erase the six-hourly structural half,
// and vice versa.
func TestCatalogRefreshersDoNotOverwriteEachOther(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{
		zones:         []*computepb.Zone{gceZone("europe-west4-a")},
		machineScopes: []MachineTypeScope{offering("europe-west4-a", "n1-standard-8")},
		regions:       []*computepb.Region{gceRegion("europe-west4", gcecatalog.QuotaTypeCPUs, 500, 0)},
	}
	c := newTestCatalog(fake, []string{"n1-standard-8"}, []string{"europe-west4-a"})

	if err := c.refreshQuota(t.Context()); err != nil {
		t.Fatalf("quota refresh: %v", err)
	}
	if got := c.MachineTypeAvailability("europe-west4-a", "n1-standard-8"); got != gcecatalog.AvailabilityUnknown {
		t.Errorf("availability after a quota-only refresh = %v, want unknown", got)
	}
	if err := c.RefreshStructural(t.Context()); err != nil {
		t.Fatalf("structural refresh: %v", err)
	}
	if c.RegionalQuota("europe-west4", gcecatalog.QuotaTypeCPUs) == nil {
		t.Error("the structural refresh erased the quota half of the snapshot")
	}
	if err := c.refreshQuota(t.Context()); err != nil {
		t.Fatalf("second quota refresh: %v", err)
	}
	if got := c.MachineTypeAvailability("europe-west4-a", "n1-standard-8"); got != gcecatalog.AvailabilityOffered {
		t.Errorf("availability after a later quota refresh = %v, want offered", got)
	}
}

// In production the two refreshers publish on their own clocks, so the
// read-modify-write of the shared halves overlaps. Each publish reads back the
// half it does not own, which is a data race on both halves unless the publish
// mutex serializes them. The race detector is what makes this test an
// assertion rather than a description.
func TestCatalogRefreshersPublishConcurrently(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{
		zones:         []*computepb.Zone{gceZone("europe-west4-a")},
		machineScopes: []MachineTypeScope{offering("europe-west4-a", "n1-standard-8")},
		regions:       []*computepb.Region{gceRegion("europe-west4", gcecatalog.QuotaTypeCPUs, 500, 100)},
	}
	c := newTestCatalog(fake, []string{"n1-standard-8"}, []string{"europe-west4-a"})

	ctx := t.Context()
	var wg sync.WaitGroup
	for range 200 {
		wg.Go(func() {
			if err := c.RefreshStructural(ctx); err != nil {
				t.Errorf("structural refresh: %v", err)
			}
		})
		wg.Go(func() {
			if err := c.refreshQuota(ctx); err != nil {
				t.Errorf("quota refresh: %v", err)
			}
		})
	}
	wg.Wait()

	// Whichever half published last, the other one is still in the snapshot.
	if got := c.MachineTypeAvailability("europe-west4-a", "n1-standard-8"); got != gcecatalog.AvailabilityOffered {
		t.Errorf("availability after concurrent refreshes = %v, want offered", got)
	}
	if c.RegionalQuota("europe-west4", gcecatalog.QuotaTypeCPUs) == nil {
		t.Error("the quota half is missing after concurrent refreshes")
	}
}

// A machine type deleted from the zone it is still listed in is not
// availability: creating against it fails at Compute Engine.
func TestCatalogIgnoresAShapeDeletedFromItsZone(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{
		zones: []*computepb.Zone{gceZone("europe-west4-a"), gceZone("us-central1-a")},
		machineScopes: []MachineTypeScope{
			offering("europe-west4-a", "n1-standard-8"),
			deleted("us-central1-a", "n1-standard-8"),
		},
	}
	c := newTestCatalog(fake, []string{"n1-standard-8"}, []string{"europe-west4-a"})
	if err := c.RefreshStructural(t.Context()); err != nil {
		t.Fatalf("structural refresh: %v", err)
	}

	if got := c.MachineTypeAvailability("us-central1-a", "n1-standard-8"); got != gcecatalog.AvailabilityNotOffered {
		t.Errorf("availability of a shape deleted from the zone = %v, want not offered", got)
	}
	if got := c.MachineTypeAvailability("europe-west4-a", "n1-standard-8"); got != gcecatalog.AvailabilityOffered {
		t.Errorf("availability in the zone that still offers it = %v, want offered", got)
	}
	// The zone answered, so it is not a scope the snapshot is silent about.
	if slices.Contains(c.Snapshot().Partial, "machine types: us-central1-a") {
		t.Error("a zone that answered with a deleted shape was reported as a scope that failed")
	}
}

// The warning names the scopes Compute Engine was silent about, and a
// structural silence outlives hundreds of quota publishes, so it is logged
// when the set changes, not on every publish that carries it forward.
func TestCatalogWarnsOnlyWhenTheSilentScopesChange(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{
		zones: []*computepb.Zone{gceZone("europe-west4-a"), gceZone("us-central1-a")},
		machineScopes: []MachineTypeScope{
			offering("europe-west4-a", "n1-standard-8"),
			unreadable("us-central1-a"),
		},
		machineUnreached: []string{zoneScopePrefix + "us-central1-a"},
		regions:          []*computepb.Region{gceRegion("europe-west4", gcecatalog.QuotaTypeCPUs, 500, 100)},
	}
	var logged bytes.Buffer
	enabled := &fakeEnabledZones{zones: []string{"europe-west4-a"}}
	c := newTestCatalogWith(fake, []string{"n1-standard-8"}, enabled,
		slog.New(slog.NewJSONHandler(&logged, nil)))

	if err := c.RefreshStructural(t.Context()); err != nil {
		t.Fatalf("structural refresh: %v", err)
	}
	for range 5 {
		if err := c.refreshQuota(t.Context()); err != nil {
			t.Fatalf("quota refresh: %v", err)
		}
	}
	const warning = "did not report every catalog scope"
	if got := strings.Count(logged.String(), warning); got != 1 {
		t.Errorf("warnings for one unchanging silent scope = %d, want 1", got)
	}

	// A region with no quota row joins the silent set: that is news.
	enabled.zones = append(enabled.zones, "asia-east1-a")
	if err := c.refreshQuota(t.Context()); err != nil {
		t.Fatalf("quota refresh after enabling a silent region: %v", err)
	}
	if got := strings.Count(logged.String(), warning); got != 2 {
		t.Errorf("warnings after the silent set grew = %d, want 2", got)
	}
	if err := c.refreshQuota(t.Context()); err != nil {
		t.Fatalf("repeat quota refresh: %v", err)
	}
	if got := strings.Count(logged.String(), warning); got != 2 {
		t.Errorf("warnings after republishing the same set = %d, want it to stay 2", got)
	}
}

// With no zone enabled there is no region worth a quota call, and an empty
// quota map replaces the previous one: a fleet whose last zone was
// just disabled must stop answering with that region's quota.
func TestCatalogQuotaRefreshWithNoEnabledZonesPublishesAnEmptyPlane(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{
		regions: []*computepb.Region{gceRegion("europe-west4", gcecatalog.QuotaTypeCPUs, 500, 120)},
	}
	enabled := &fakeEnabledZones{zones: []string{"europe-west4-a"}}
	c := newTestCatalogWith(fake, []string{"n1-standard-8"}, enabled, slog.Default())
	if err := c.refreshQuota(t.Context()); err != nil {
		t.Fatalf("quota refresh: %v", err)
	}
	if c.RegionalQuota("europe-west4", gcecatalog.QuotaTypeCPUs) == nil {
		t.Fatal("the first quota refresh published no row to lose")
	}

	enabled.zones = nil
	if err := c.refreshQuota(t.Context()); err != nil {
		t.Fatalf("quota refresh with no enabled zone: %v", err)
	}
	if got := c.RegionalQuotas("europe-west4"); len(got) != 0 {
		t.Errorf("quota rows with no enabled zone = %+v, want none", got)
	}
	if want := []string{"regions.list"}; !slices.Equal(fake.calls, want) {
		t.Errorf("calls = %v, want %v. A fleet with no enabled zone has no region to ask about", fake.calls, want)
	}
}

// The quota refresh scopes itself to the enabled zones, so failing to read
// them is not "nothing is enabled": the cycle fails and the previous rows
// stand, to age out on their own timestamps.
func TestCatalogQuotaRefreshNeedsTheEnabledZones(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{
		regions: []*computepb.Region{gceRegion("europe-west4", gcecatalog.QuotaTypeCPUs, 500, 120)},
	}
	enabled := &fakeEnabledZones{zones: []string{"europe-west4-a"}}
	c := newTestCatalogWith(fake, []string{"n1-standard-8"}, enabled, slog.Default())
	if err := c.refreshQuota(t.Context()); err != nil {
		t.Fatalf("quota refresh: %v", err)
	}

	enabled.zones, enabled.err = nil, errors.New("spanner unavailable")
	if err := c.refreshQuota(t.Context()); err == nil {
		t.Fatal("the quota refresh succeeded without knowing which zones are enabled")
	}
	if c.RegionalQuota("europe-west4", gcecatalog.QuotaTypeCPUs) == nil {
		t.Error("a failed enabled-zone read erased the quota rows it could not re-scope")
	}
	if want := []string{"regions.list"}; !slices.Equal(fake.calls, want) {
		t.Errorf("calls = %v, want %v. The failed cycle asked Compute Engine nothing", fake.calls, want)
	}
}

// A zone list that fails takes the whole structural refresh with it: without
// the project's zones there is no universe for a scope to be missing from, so
// every omission would silently read as "reported and empty".
func TestCatalogStructuralRefreshNeedsTheZoneList(t *testing.T) {
	t.Parallel()
	fake := &fakeCompute{zoneErr: errors.New("compute unavailable")}
	c := newTestCatalog(fake, []string{"n1-standard-8"}, []string{"europe-west4-a"})

	if err := c.RefreshStructural(t.Context()); err == nil {
		t.Fatal("the structural refresh succeeded without a zone list")
	}
	if c.Snapshot() != nil {
		t.Error("a snapshot was published from a refresh that could not list zones")
	}
}
