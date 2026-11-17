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
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cloud.google.com/go/compute/apiv1/computepb"
	"go.opentelemetry.io/otel/attribute"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// Refresh cadences, matched to two very different volatilities. A machine
// family reaches a new zone monthly at most and Google adds a zone a few times
// a year, so the structural lists are re-read four times a day; live quota
// usage moves with every create, so it is re-read every minute. Both are
// constant work, the same calls whether the fleet is empty or saturated, and
// independent of how many zones are enabled.
const (
	StructuralRefreshInterval = 6 * time.Hour
	QuotaRefreshInterval      = time.Minute
)

// structuralRetryAfterFailure is the first retry delay after a failed or
// partial structural read, doubling per further failure up to
// StructuralRefreshInterval.
const structuralRetryAfterFailure = 30 * time.Second

// zoneScopePrefix is the scope key Compute Engine's aggregated lists use for a
// zone ("zones/us-central1-a").
const zoneScopePrefix = "zones/"

// Snapshot is one immutable reading of what Compute Engine offers this
// project. Readers take it whole with a single atomic load and never see a
// half-built catalog: each refresh builds a new value and swaps it in.
//
// Absence carries meaning here. A zone key exists in Shapes
// and Accelerators only for a scope Compute Engine actually reported, so a
// missing key means "not known" and an empty set means "reported, and offers
// none of them". Under partial success a failed scope is omitted from the
// response rather than failing the call, and collapsing that into an empty set
// would turn a transient per-zone error into a confident "offers nothing".
type Snapshot struct {
	// Shapes maps a zone to the curated machine types offered there.
	Shapes map[string]map[string]bool
	// Families maps a zone to the machine families offered there, folded from
	// the same response as Shapes. Custom machine types are never listed,
	// because Compute Engine constructs them from the name at insert time, so
	// a custom shape's zone offering is read from its family's: a zone that
	// stocks the series stocks the custom forms built on it.
	Families map[string]map[string]bool
	// Discovered is every shape the aggregated list reported, keyed by machine
	// type id, with the size Compute Engine gave it. It is the union across
	// zones because a machine type's size is a property of the type and not of
	// where it is offered. Which zones stock it is what Shapes answers.
	Discovered map[string]machinetype.Shape
	// Accelerators maps a zone to the accelerator types offered there and how
	// many cards of each may attach to one instance.
	Accelerators map[string]map[string]int64
	// Zones is every zone Compute Engine offers the project, with its status.
	Zones map[string]gcecatalog.ZoneStatus
	// Quota maps a region to its Compute quota metrics, limit and usage. The
	// region list supplies the flat ones. The pools it cannot name (CPU quota
	// per VM family, hyperdisk capacity) join the same map from the project's
	// quota telemetry, so a reader never has to know which call answered.
	Quota map[string][]*gcecatalog.RegionalQuota
	// BuiltAt is when the structural lists were fetched. Quota carries its own
	// per-row timestamp because it refreshes on a different clock.
	BuiltAt time.Time
	// Partial names the scopes Compute Engine did not report, so the operations
	// view can say which cells the snapshot is silent about rather than
	// implying it knows they are empty.
	Partial []string
}

// CuratedFamilies lists the machine families this deployment will provision.
// It is the filter the aggregated machine-type call is narrowed to, and
// Compute Engine cannot supply it: GCE reports what a zone offers, and only
// CCO knows which series can nest KVM and host a device.
//
// It names families only. Which shapes a family contains is Google's fact
// and arrives from the very call this narrows, so a series Google ships
// tomorrow needs one curation row and nothing else. No shape names to enter,
// and no release here.
type CuratedFamilies interface {
	ListFamilyCuration(ctx context.Context) ([]*machinetype.FamilyCuration, error)
}

// EnabledZones lists the zones an operator has admitted for host creation.
// The quota refresh reads it to scope the region list to regions this fleet can
// actually place in.
type EnabledZones interface {
	ListEnabledZones(ctx context.Context) ([]string, error)
}

// DimensionedQuotas reads the regional quota axes the region list cannot
// carry. Its metric array is a fixed flat enum: it has no way to express a pool
// selected by a dimension (CPU quota per VM family) and does not carry the
// hyperdisk capacity pools at all. Cloud Monitoring's quota metrics are where
// those live.
type DimensionedQuotas interface {
	// Quotas answers one row per axis it could read in the given regions.
	// An axis with no reading is simply absent. It is never a zero limit,
	// which would read as a pool granted nothing.
	Quotas(ctx context.Context, regions []string) ([]*gcecatalog.RegionalQuota, error)
}

// CatalogDeps carries the collaborators a Catalog reads through.
type CatalogDeps struct {
	Compute  ComputeCatalog
	Families CuratedFamilies
	Zones    EnabledZones
	// Dimensioned is optional: without it the per-family CPU and hyperdisk
	// axes stay unknown, which passes them advisorily and removes no zone.
	Dimensioned DimensionedQuotas
}

// Catalog holds what Compute Engine offers, in memory, refreshed by two
// background loops. Facts about Google are held by Google on an API with
// better availability than any mirror of it, so they are cached here and never
// stored. What CCO holds the only copy of (which shapes can host Cuttlefish,
// which zones an operator enabled, which stockouts CCO suffered) stays in
// Spanner.
//
// The first snapshot lands asynchronously: a structural fetch is seconds of
// wall time, and blocking readiness on it would put that on every cold start.
// Until it lands every read answers "unknown", which never removes a
// placement candidate.
type Catalog struct {
	project     string
	compute     ComputeCatalog
	families    CuratedFamilies
	zones       EnabledZones
	dimensioned DimensionedQuotas
	logger      *slog.Logger

	snapshot atomic.Pointer[Snapshot]

	// publishMu serializes the two refreshers' read-modify-write of the
	// published value. Each refresher owns half the snapshot and carries the
	// other half forward unchanged, so without this the slower one would
	// overwrite the faster one's half with what it read before that half was
	// replaced. Readers never take it. They load the pointer.
	publishMu  sync.Mutex
	structural *structuralFacts
	quota      *quotaFacts

	// resolved memoizes the per-name machineTypes.get behind ResolveShape,
	// which is the only Compute Engine call the catalog makes on a read path.
	// It is dropped whenever a structural refresh lands, because the family
	// set it was decided against is what that refresh replaces.
	resolvedMu sync.Mutex
	resolved   map[string]resolvedShape

	runners []*telemetry.Runner
}

// resolvedShape is one remembered machineTypes.get, verdict included.
type resolvedShape struct {
	shape machinetype.Shape
	err   error
}

// structuralFacts is the half of the snapshot the six-hourly refresh owns.
type structuralFacts struct {
	shapes       map[string]map[string]bool
	families     map[string]map[string]bool
	discovered   map[string]machinetype.Shape
	accelerators map[string]map[string]int64
	zones        map[string]gcecatalog.ZoneStatus
	builtAt      time.Time
	partial      []string
}

// quotaFacts is the half the per-minute refresh owns.
type quotaFacts struct {
	quota   map[string][]*gcecatalog.RegionalQuota
	partial []string
}

// NewCatalog builds the catalog for a project. Nothing is fetched until Start.
func NewCatalog(project string, deps CatalogDeps, logger *slog.Logger) *Catalog {
	c := &Catalog{
		project:     project,
		compute:     deps.Compute,
		families:    deps.Families,
		zones:       deps.Zones,
		dimensioned: deps.Dimensioned,
		logger:      logger,
	}
	loop := func(name string, interval time.Duration, cycle func(context.Context) error) *telemetry.Runner {
		return &telemetry.Runner{
			Interval: interval,
			Cycle:    cycle,
			Metrics:  telemetry.NewLoopMetrics("gcecatalog", attribute.String("loop", name)),
			// Every instance keeps its own snapshot, so there is no leadership
			// gate: a leader-only refresh would leave nine instances ranking
			// against nothing.
			RunAtStart: true,
		}
	}
	structural := loop("structural", StructuralRefreshInterval, c.refreshStructural)
	// A failed structural read would otherwise stand for six hours, with
	// every catalog read answering unavailable meanwhile. On a fresh install
	// the first read fails while the orchestrator's Compute grants take
	// effect. The quota loop needs no retry: it ticks every minute.
	structural.RetryAfterFailure = structuralRetryAfterFailure
	c.runners = []*telemetry.Runner{
		structural,
		loop("quota", QuotaRefreshInterval, c.refreshQuota),
	}
	return c
}

// Start launches the refresh loops. Each runs its first cycle inside its own
// goroutine, so startup and readiness never wait on Compute Engine.
func (c *Catalog) Start(ctx context.Context) {
	for _, r := range c.runners {
		r.Start(ctx)
	}
}

// Stop cancels the refresh loops and joins them.
func (c *Catalog) Stop() {
	for _, r := range c.runners {
		r.Stop()
	}
}

// Snapshot returns the current reading, or nil before the first refresh lands.
func (c *Catalog) Snapshot() *Snapshot { return c.snapshot.Load() }

// Zones returns every zone Compute Engine offers the project, with its status.
// Empty before the first structural refresh lands. The map belongs to the
// published snapshot and is never written after it is swapped in, so callers
// read it directly rather than being handed a copy.
func (c *Catalog) Zones() map[string]gcecatalog.ZoneStatus {
	s := c.snapshot.Load()
	if s == nil {
		return nil
	}
	return s.Zones
}

// BuiltAt reports when the structural lists were last fetched, or the zero
// time before the first refresh lands.
func (c *Catalog) BuiltAt() time.Time {
	s := c.snapshot.Load()
	if s == nil {
		return time.Time{}
	}
	return s.BuiltAt
}

// Partial names the scopes Compute Engine did not report, so a reader can say
// which cells the snapshot is silent about rather than implying it knows they
// are empty.
func (c *Catalog) Partial() []string {
	s := c.snapshot.Load()
	if s == nil {
		return nil
	}
	return s.Partial
}

// MachineTypeAvailability reports what the catalog knows about a machine type
// in a zone.
//
// A custom machine type's ZONE is judged by its family: the aggregated list
// never carries one, and a zone that stocks the series stocks the custom forms
// built on the same hardware. Whether the name is a machine Compute Engine
// will build at all is a different question, settled authoritatively by
// ResolveShape's machineTypes.get before a create ever reaches ranking.
//
// The split is intentional. This answer is a map lookup with no context and no
// error by contract, and zone ranking asks it once per candidate zone: making
// it a per-zone RPC would put an unbounded, uncancellable fan-out of Compute
// Engine calls on the placement path to re-answer, per zone, a legality
// question that is a property of the family.
func (c *Catalog) MachineTypeAvailability(zone, machineType string) gcecatalog.Availability {
	s := c.snapshot.Load()
	if s == nil {
		return gcecatalog.AvailabilityUnknown
	}
	offered, reported := s.Shapes[zone]
	if !reported {
		return gcecatalog.AvailabilityUnknown
	}
	if offered[machineType] {
		return gcecatalog.AvailabilityOffered
	}
	if family, ok := machinetype.CustomFamily(machineType); ok && s.Families[zone][family] {
		return gcecatalog.AvailabilityOffered
	}
	return gcecatalog.AvailabilityNotOffered
}

// DiscoveredShapes answers every shape Compute Engine reported for the curated
// families, and whether a discovery has landed at all. It satisfies
// machinetype.Discovery, which the catalog composes its entries from.
//
// The map belongs to the published snapshot and is never written after it is
// swapped in, so callers read it directly rather than being handed a copy.
func (c *Catalog) DiscoveredShapes(context.Context) (map[string]machinetype.Shape, bool) {
	s := c.snapshot.Load()
	if s == nil || s.Discovered == nil {
		return nil, false
	}
	return s.Discovered, true
}

// ResolveShape asks Compute Engine to describe one machine type by name, in a
// zone that stocks its family.
//
// This is the custom shape's whole admission. Compute Engine synthesizes the
// resource for a legal name (guestCpus and memoryMb included, which is where
// the CVD-viability floor and every packing decision then get their numbers)
// and refuses an illegal one, naming the rule it broke. CCO holds no opinion
// about vCPU increments or memory bands, and gains none when Google ships a
// series.
//
// Zone-scoped because the API is, but the answer is not per-zone: whether a
// family builds custom shapes, and on what increments, is a property of the
// series. The zone chosen is one the snapshot says stocks the family, so a
// refusal is about the name rather than about where it was asked.
func (c *Catalog) ResolveShape(ctx context.Context, machineType string) (machinetype.Shape, error) {
	family, ok := machinetype.CustomFamily(machineType)
	if !ok {
		return machinetype.Shape{}, fmt.Errorf("%q: %w", machineType, machinetype.ErrNotFound)
	}
	zone, found := c.zoneStocking(family)
	if !found {
		// A quota publish builds a snapshot before any structural read lands,
		// so a nil Discovered is the signal that discovery has not happened.
		s := c.snapshot.Load()
		if s == nil || s.Discovered == nil {
			return machinetype.Shape{}, machinetype.ErrCatalogUnavailable
		}
		return machinetype.Shape{}, fmt.Errorf(
			"no zone offers the %s family, which %q would be built on: %w", family, machineType, machinetype.ErrNotFound)
	}
	if shape, cached, err := c.cachedShape(machineType); cached {
		return shape, err
	}
	mt, err := c.compute.MachineType(ctx, &computepb.GetMachineTypeRequest{
		Project:     c.project,
		Zone:        zone,
		MachineType: machineType,
	})
	if err != nil {
		// A name Compute Engine will not build is a machine type that does not
		// exist, and its own prose is the only accurate account of why,
		// whether that is the vCPU increment, the memory band, or a family
		// that offers no custom shapes at all.
		resolved := fmt.Errorf("%q is not a machine type Compute Engine will build: %s: %w",
			machineType, err.Error(), machinetype.ErrNotFound)
		c.cacheShape(machineType, machinetype.Shape{}, resolved)
		return machinetype.Shape{}, resolved
	}
	shape := discoveredShape(mt)
	c.cacheShape(machineType, shape, nil)
	return shape, nil
}

// zoneStocking names a zone the snapshot says offers the family, lowest name
// first so two instances resolving the same shape ask the same zone.
func (c *Catalog) zoneStocking(family string) (string, bool) {
	s := c.snapshot.Load()
	if s == nil {
		return "", false
	}
	for _, zone := range slices.Sorted(maps.Keys(s.Families)) {
		if s.Families[zone][family] {
			return zone, true
		}
	}
	return "", false
}

// maxResolvedShapes bounds the memo below. A custom shape is named by an
// operator setting or a create, so the live set is small; the cap is what
// keeps a caller that names a thousand different illegal shapes from growing
// the map without bound. Past it resolution still answers, just uncached.
const maxResolvedShapes = 256

// cachedShape reads a previous resolution, verdicts included: a name Compute
// Engine refused is worth remembering exactly as much as one it built.
func (c *Catalog) cachedShape(machineType string) (machinetype.Shape, bool, error) {
	c.resolvedMu.Lock()
	defer c.resolvedMu.Unlock()
	resolved, ok := c.resolved[machineType]
	return resolved.shape, ok, resolved.err
}

// cacheShape remembers one resolution until the next structural refresh
// replaces the family set it was decided against.
func (c *Catalog) cacheShape(machineType string, shape machinetype.Shape, err error) {
	c.resolvedMu.Lock()
	defer c.resolvedMu.Unlock()
	if len(c.resolved) >= maxResolvedShapes {
		return
	}
	if c.resolved == nil {
		c.resolved = make(map[string]resolvedShape)
	}
	c.resolved[machineType] = resolvedShape{shape: shape, err: err}
}

// AcceleratorAvailability reports what the catalog knows about an accelerator
// type in a zone.
func (c *Catalog) AcceleratorAvailability(zone, acceleratorType string) gcecatalog.Availability {
	s := c.snapshot.Load()
	if s == nil {
		return gcecatalog.AvailabilityUnknown
	}
	offered, reported := s.Accelerators[zone]
	if !reported {
		return gcecatalog.AvailabilityUnknown
	}
	if _, ok := offered[acceleratorType]; ok {
		return gcecatalog.AvailabilityOffered
	}
	return gcecatalog.AvailabilityNotOffered
}

// ZoneAccelerators returns the accelerators offered in a zone, sorted by type.
// An unreported zone and a zone offering none both answer empty. Callers that
// must tell those apart ask AcceleratorAvailability.
func (c *Catalog) ZoneAccelerators(zone string) []*gcecatalog.Accelerator {
	s := c.snapshot.Load()
	if s == nil {
		return nil
	}
	offered := s.Accelerators[zone]
	out := make([]*gcecatalog.Accelerator, 0, len(offered))
	for _, name := range slices.Sorted(maps.Keys(offered)) {
		out = append(out, &gcecatalog.Accelerator{
			Region:          gcecatalog.RegionFromZone(zone),
			Zone:            zone,
			AcceleratorType: name,
			IsAvailable:     true,
			MaxCount:        offered[name],
		})
	}
	return out
}

// RegionalQuota returns one region's quota metric, or nil when the region or
// the metric is not in the snapshot.
func (c *Catalog) RegionalQuota(region, quotaType string) *gcecatalog.RegionalQuota {
	for _, q := range c.RegionalQuotas(region) {
		if q.QuotaType == quotaType {
			return q
		}
	}
	return nil
}

// RegionalQuotas returns every quota metric held for a region.
func (c *Catalog) RegionalQuotas(region string) []*gcecatalog.RegionalQuota {
	s := c.snapshot.Load()
	if s == nil {
		return nil
	}
	return s.Quota[region]
}

// RefreshStructural rereads what Compute Engine offers, now, rather than at
// the next six-hourly cycle. Curation calls it: a family an operator just
// admitted is unplaceable until its shapes are discovered, and waiting out the
// interval would mean a catalog entry the console shows and every create
// refuses.
func (c *Catalog) RefreshStructural(ctx context.Context) error {
	if err := c.readStructural(ctx); err != nil && !telemetry.IsPartial(err) {
		return err
	}
	return nil
}

// refreshStructural is the structural loop's cycle. It logs a failed read
// itself, since the Runner does not, and a partial read is already logged by
// publish.
func (c *Catalog) refreshStructural(ctx context.Context) error {
	err := c.readStructural(ctx)
	if err != nil && !telemetry.IsPartial(err) && ctx.Err() == nil {
		c.logger.Warn("the structural catalog read failed; the previous snapshot stands until a retry succeeds",
			slog.Any("error", err))
	}
	return err
}

// readStructural rebuilds the machine-type, accelerator and zone lists
// from three calls, whatever the size of the fleet or of Google Cloud: one
// aggregated machine-type list narrowed server-side to the curated families,
// one aggregated accelerator list, and the zone list. On any failure the
// previous snapshot stands, because a failed fetch is not an answer. A read
// that missed some scopes still publishes and returns a Partial error, so the
// loop retries it early.
func (c *Catalog) readStructural(ctx context.Context) error {
	curated, err := c.curatedFamilyNames(ctx)
	if err != nil {
		return err
	}
	if len(curated) == 0 {
		return errors.New("no machine family is curated; refusing to build a catalog snapshot from it")
	}

	zones, err := c.compute.Zones(ctx, &computepb.ListZonesRequest{
		Project:              c.project,
		ReturnPartialSuccess: new(true),
	})
	if err != nil {
		return fmt.Errorf("fetching zones: %w", err)
	}
	// The zone list is the universe a scope can be missing from: without it,
	// a scope Compute Engine omitted is indistinguishable from a scope that
	// does not exist, and Partial would always be empty.
	universe := make(map[string]gcecatalog.ZoneStatus, len(zones))
	for _, z := range zones {
		universe[z.GetName()] = zoneStatus(z)
	}
	if len(universe) == 0 {
		return errors.New("the Compute Engine zone list came back empty; refusing to build a catalog snapshot from it")
	}

	fetched, shapePartial, err := c.fetchShapes(ctx, curated, universe)
	if err != nil {
		return err
	}
	accelerators, acceleratorPartial, err := c.fetchAccelerators(ctx, universe)
	if err != nil {
		return err
	}

	partial := slices.Concat(shapePartial, acceleratorPartial)
	c.publish(&structuralFacts{
		shapes:       fetched.byZone,
		families:     fetched.familiesByZone,
		discovered:   fetched.discovered,
		accelerators: accelerators,
		zones:        universe,
		builtAt:      time.Now(),
		partial:      partial,
	}, nil)
	if len(partial) > 0 {
		return telemetry.Partial(fmt.Errorf("compute engine did not report %d catalog scopes: %s",
			len(partial), strings.Join(partial, ", ")))
	}
	return nil
}

// curatedFamilyNames reads the machine families the fleet provisions. Every
// row is taken, disabled ones included. A disabled family's shapes are what
// let the catalog answer "disabled" rather than "unknown" about a machine type
// somebody still names, and re-enabling one must not wait six hours for the
// next structural refresh to notice it.
func (c *Catalog) curatedFamilyNames(ctx context.Context) ([]string, error) {
	rows, err := c.families.ListFamilyCuration(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the curated machine families: %w", err)
	}
	names := make([]string, 0, len(rows))
	for _, f := range rows {
		if f.Family != "" {
			names = append(names, f.Family)
		}
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// fetchedShapes is one aggregated machine-type reading, folded three ways: the
// shapes each zone offers, the families each zone offers, and every shape's
// own size.
type fetchedShapes struct {
	byZone         map[string]map[string]bool
	familiesByZone map[string]map[string]bool
	discovered     map[string]machinetype.Shape
}

// fetchShapes reads the aggregated machine-type list, narrowed server-side to
// the curated families, and folds it into the three maps above.
//
// The response is intersected with the curated families rather than trusted. A
// malformed filter is answered with HTTP 200 and every machine type in the
// project, which would otherwise be recorded as availability, and, now that
// the catalog's own entries are composed from this reading, as shapes the
// fleet offers. The empty direction of the same mistake is caught below. A
// filter that matches nothing anywhere is a broken filter, and says nothing
// about the fleet's own families.
func (c *Catalog) fetchShapes(ctx context.Context, curated []string, universe map[string]gcecatalog.ZoneStatus) (fetchedShapes, []string, error) {
	resp, err := c.compute.MachineTypes(ctx, &computepb.AggregatedListMachineTypesRequest{
		Project: c.project,
		Filter:  new(familyFilter(curated)),
		// Without this an empty scope may be dropped from the response
		// entirely, which would make "offers none of the curated shapes"
		// indistinguishable from "could not be read".
		IncludeAllScopes:     new(true),
		ReturnPartialSuccess: new(true),
	})
	if err != nil {
		return fetchedShapes{}, nil, fmt.Errorf("fetching machine types: %w", err)
	}

	keep := make(map[string]bool, len(curated))
	for _, family := range curated {
		keep[family] = true
	}
	fetched := fetchedShapes{
		byZone:         make(map[string]map[string]bool, len(resp.Scopes)),
		familiesByZone: make(map[string]map[string]bool, len(resp.Scopes)),
		discovered:     make(map[string]machinetype.Shape),
	}
	for _, scope := range resp.Scopes {
		zone, ok := zoneOfScope(scope.Scope)
		if !ok || !scopeReported(scope.List.GetWarning(), len(scope.List.GetMachineTypes())) {
			continue
		}
		// An aggregated list pages by ITEM while repeating every scope key on
		// every page, so the page carrying a zone's machine types is followed
		// by pages naming the same zone with NO_RESULTS_ON_PAGE. Entries for
		// one zone therefore merge; assignment would keep whichever entry came
		// last, which live meant every zone off the final page read as
		// "offers none" and placement excluded the whole fleet.
		inZone := fetched.byZone[zone]
		if inZone == nil {
			inZone = make(map[string]bool)
			fetched.byZone[zone] = inZone
		}
		familiesInZone := fetched.familiesByZone[zone]
		if familiesInZone == nil {
			familiesInZone = make(map[string]bool)
			fetched.familiesByZone[zone] = familiesInZone
		}
		for _, mt := range scope.List.GetMachineTypes() {
			shape := discoveredShape(mt)
			if !keep[shape.Family] || deprecatedAway(mt.GetDeprecated()) {
				continue
			}
			inZone[shape.Name] = true
			familiesInZone[shape.Family] = true
			fetched.discovered[shape.Name] = shape
		}
	}
	if len(fetched.discovered) == 0 {
		return fetchedShapes{}, nil, fmt.Errorf(
			"no zone offers a machine type of any of the %d curated families; treating the machine-type filter as broken rather than the fleet as unplaceable",
			len(curated))
	}
	return fetched, missingScopes("machine types", universe, fetched.byZone, resp.Unreachable), nil
}

// discoveredShape reads one machine type as Compute Engine described it.
//
// Memory is truncated to whole gigabytes, which is the unit every sizing rule
// in the fleet works in and under-reports a machine by at most a gigabyte,
// the direction that cannot oversubscribe a host. An accelerators entry means
// the GPU is part of the machine type; N1's attachable GPUs never appear here,
// which is exactly the distinction the catalog's GPU mode draws.
func discoveredShape(mt *computepb.MachineType) machinetype.Shape {
	shape := machinetype.Shape{
		Name:         mt.GetName(),
		Family:       machinetype.FamilyOf(mt.GetName()),
		VCPUs:        int64(mt.GetGuestCpus()),
		MemoryGB:     int64(mt.GetMemoryMb()) / 1024,
		Architecture: architectureOf(mt.GetArchitecture()),
	}
	if accelerators := mt.GetAccelerators(); len(accelerators) > 0 {
		shape.GPUType = accelerators[0].GetGuestAcceleratorType()
		shape.GPUCount = int64(accelerators[0].GetGuestAcceleratorCount())
	}
	return shape
}

// architectureOf maps Compute Engine's architecture enum onto the catalog's
// vocabulary. An unset or unrecognized value answers empty, which leaves the
// family's curated architecture standing rather than asserting x86 about a
// machine nobody classified.
func architectureOf(architecture string) machinetype.Architecture {
	switch architecture {
	case computepb.MachineType_X86_64.String():
		return machinetype.ArchX86
	case computepb.MachineType_ARM64.String():
		return machinetype.ArchArm
	default:
		return ""
	}
}

// fetchAccelerators reads the aggregated accelerator-type list and folds it
// into zone → type → cards per instance.
func (c *Catalog) fetchAccelerators(ctx context.Context, universe map[string]gcecatalog.ZoneStatus) (map[string]map[string]int64, []string, error) {
	resp, err := c.compute.AcceleratorTypes(ctx, &computepb.AggregatedListAcceleratorTypesRequest{
		Project:              c.project,
		IncludeAllScopes:     new(true),
		ReturnPartialSuccess: new(true),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("fetching accelerator types: %w", err)
	}

	accelerators := make(map[string]map[string]int64, len(resp.Scopes))
	for _, scope := range resp.Scopes {
		zone, ok := zoneOfScope(scope.Scope)
		if !ok || !scopeReported(scope.List.GetWarning(), len(scope.List.GetAcceleratorTypes())) {
			continue
		}
		// Merged across page-repeated scope entries for the reason fetchShapes
		// states.
		inZone := accelerators[zone]
		if inZone == nil {
			inZone = make(map[string]int64)
			accelerators[zone] = inZone
		}
		for _, at := range scope.List.GetAcceleratorTypes() {
			inZone[at.GetName()] = int64(at.GetMaximumCardsPerInstance())
		}
	}
	return accelerators, missingScopes("accelerators", universe, accelerators, resp.Unreachable), nil
}

// refreshQuota rereads live quota with one region list, filtered to the
// regions holding an enabled zone. On failure the previous quota rows stand
// and age out of trust on their own timestamps.
func (c *Catalog) refreshQuota(ctx context.Context) error {
	enabled, err := c.zones.ListEnabledZones(ctx)
	if err != nil {
		return fmt.Errorf("listing enabled zones for the quota refresh: %w", err)
	}
	wanted := make(map[string]bool, len(enabled))
	for _, zone := range enabled {
		wanted[gcecatalog.RegionFromZone(zone)] = true
	}
	if len(wanted) == 0 {
		// No enabled zone means nothing to place, so there is no region worth
		// a quota call. Publishing an empty quota map keeps a disabled-everything
		// catalog from serving quota rows for regions nobody can use.
		c.publish(nil, &quotaFacts{quota: map[string][]*gcecatalog.RegionalQuota{}})
		return nil
	}

	regions := slices.Sorted(maps.Keys(wanted))
	resp, err := c.compute.Regions(ctx, &computepb.ListRegionsRequest{
		Project:              c.project,
		Filter:               new(nameFilter(regions)),
		ReturnPartialSuccess: new(true),
	})
	if err != nil {
		return fmt.Errorf("fetching regional quotas: %w", err)
	}

	now := time.Now()
	quota := make(map[string][]*gcecatalog.RegionalQuota, len(resp))
	var partial []string
	for _, r := range resp {
		region := r.GetName()
		if !wanted[region] {
			continue
		}
		// The region list fails open: when Compute Engine cannot read a
		// region's quota it answers 200 with the array empty and this field
		// populated. Folding that would record "no metric is tracked here",
		// which passes every judgment advisorily and says nothing about why.
		// The last real reading stands instead and ages out on its own
		// timestamps, and the region is named as one the snapshot is silent
		// about.
		if warning := r.GetQuotaStatusWarning(); warning != nil {
			partial = append(partial, "quota: "+region+" ("+warning.GetCode()+")")
			// Cloned because the published snapshot readers hold owns that
			// slice, and the telemetry rows are folded into this one below.
			if previous := c.RegionalQuotas(region); len(previous) > 0 {
				quota[region] = slices.Clone(previous)
			}
			continue
		}
		rows := make([]*gcecatalog.RegionalQuota, 0, len(r.GetQuotas()))
		for _, q := range r.GetQuotas() {
			rows = append(rows, &gcecatalog.RegionalQuota{
				Region:     region,
				QuotaType:  q.GetMetric(),
				LimitValue: quotaValue(q.GetLimit()),
				UsageValue: quotaValue(q.GetUsage()),
				LastSyncAt: &now,
			})
		}
		quota[region] = rows
	}

	for _, region := range regions {
		if _, ok := quota[region]; !ok {
			partial = append(partial, "quota: "+region)
		}
	}
	if c.dimensioned != nil {
		partial = append(partial, c.foldDimensionedQuota(ctx, regions, quota)...)
	}
	c.publish(nil, &quotaFacts{quota: quota, partial: partial})
	return nil
}

// foldDimensionedQuota folds in the axes the region list structurally cannot
// carry, and names the regions it could not answer for.
//
// A failed read is never a verdict. The rows simply do not appear, every
// judgment against them reads unknown, and placement passes advisorily.
// Treating an unread axis as exhausted would make every zone serving those
// families unplaceable on the strength of one failed call.
func (c *Catalog) foldDimensionedQuota(ctx context.Context, regions []string, quota map[string][]*gcecatalog.RegionalQuota) []string {
	rows, err := c.dimensioned.Quotas(ctx, regions)
	if err != nil {
		c.logger.Warn("the per-family quota axes could not be read; placement judges them unknown",
			slog.Any("error", err))
		partial := make([]string, 0, len(regions))
		for _, region := range regions {
			partial = append(partial, "quota (per-family): "+region)
		}
		return partial
	}
	for _, row := range rows {
		quota[row.Region] = replaceQuotaRow(quota[row.Region], row)
	}
	return nil
}

// replaceQuotaRow puts a freshly read row in place of the region's previous one
// for the same pool, appending when there is none.
//
// Replacing rather than appending is what keeps a fail-open region accurate.
// It carries its whole previous reading forward, these axes included, and a
// second row for the same pool would leave the stale copy first, where every
// lookup, which takes the first match, would find it.
func replaceQuotaRow(rows []*gcecatalog.RegionalQuota, row *gcecatalog.RegionalQuota) []*gcecatalog.RegionalQuota {
	for i, existing := range rows {
		if existing.QuotaType == row.QuotaType {
			rows[i] = row
			return rows
		}
	}
	return append(rows, row)
}

// quotaValue converts one Compute quota number to the catalog's integer form.
// An unlimited metric arrives as 2^63-1, which is above math.MaxInt64 once the
// API's float64 has round-tripped it: converting that directly is undefined and
// lands on the most negative int64, which reads as a pool permanently in
// deficit.
func quotaValue(v float64) int64 {
	switch {
	case v >= float64(gcecatalog.LimitUnlimited):
		return gcecatalog.LimitUnlimited
	case v <= 0:
		return 0
	default:
		return int64(v)
	}
}

// publish swaps in a new snapshot built from whichever half was just
// refreshed plus the other half carried forward.
func (c *Catalog) publish(structural *structuralFacts, quota *quotaFacts) {
	c.publishMu.Lock()

	if structural != nil {
		c.structural = structural
		c.resolvedMu.Lock()
		c.resolved = nil
		c.resolvedMu.Unlock()
	}
	if quota != nil {
		c.quota = quota
	}

	next := &Snapshot{}
	if c.structural != nil {
		next.Shapes = c.structural.shapes
		next.Families = c.structural.families
		next.Discovered = c.structural.discovered
		next.Accelerators = c.structural.accelerators
		next.Zones = c.structural.zones
		next.BuiltAt = c.structural.builtAt
		next.Partial = append(next.Partial, c.structural.partial...)
	}
	if c.quota != nil {
		next.Quota = c.quota.quota
		next.Partial = append(next.Partial, c.quota.partial...)
	}
	slices.Sort(next.Partial)
	previous := c.snapshot.Swap(next)
	c.publishMu.Unlock()

	// A structural partial entry stands until the six-hourly refresh replaces
	// it, and every minute's quota publish carries it forward, so warning on
	// every non-empty set would repeat one flaky zone hundreds of times.
	if len(next.Partial) == 0 || (previous != nil && slices.Equal(previous.Partial, next.Partial)) {
		return
	}
	c.logger.Warn("Compute Engine did not report every catalog scope",
		slog.Int("scopes", len(next.Partial)), slog.Any("partial", next.Partial))
}

// nameFilter builds the server-side list filter matching any of the names.
// The region call discards every region the fleet cannot place in with it.
func nameFilter(names []string) string {
	terms := make([]string, 0, len(names))
	for _, name := range names {
		terms = append(terms, fmt.Sprintf("(name = %q)", name))
	}
	return strings.Join(terms, " OR ")
}

// familyFilter builds the server-side machine-type filter matching every shape
// of any of the families. It discards the great majority of the ~36,000
// machine-type resources a project can see, which is the work the whole-planet
// crawl it replaces did client-side.
//
// The one legal form, established against the live API: the list filter's `=`
// compares LITERALLY (a "n4-.*" value matches nothing), `eq` is the RE2
// operator, and `eq` terms do not compose under OR ("Invalid list filter
// expression"), so every family rides ONE `eq` term as an alternation. The
// trailing separator inside each branch is what keeps the families apart:
// "n4-" cannot match an n4a or n4d shape, because those spell their fourth
// character before the hyphen. The same separator is what the client-side
// intersect in fetchShapes tokenizes on, so filter and guard agree on where a
// family ends.
func familyFilter(families []string) string {
	return fmt.Sprintf("name eq %q", "("+strings.Join(families, "|")+")-.*")
}

// zoneOfScope extracts the zone from an aggregated-list scope key. Global and
// regional scopes appear in the same response and are not zones.
func zoneOfScope(scope string) (string, bool) {
	zone, ok := strings.CutPrefix(scope, zoneScopePrefix)
	return zone, ok && zone != ""
}

// scopeReported reports whether a scoped list is an answer about the scope
// rather than a failure to read it. An empty list is an answer only when the
// API says why it is empty. Anything else (an unreachable scope, or a scope
// returned blank with no explanation) is treated as no answer at all, which
// is the direction that cannot make a fleet unplaceable.
func scopeReported(warning *computepb.Warning, items int) bool {
	if items > 0 {
		return true
	}
	return warning.GetCode() == computepb.Warning_NO_RESULTS_ON_PAGE.String()
}

// deprecatedAway reports whether a machine type has been deleted from the
// zone it is still listed in.
func deprecatedAway(status *computepb.DeprecationStatus) bool {
	return status.GetState() == computepb.DeprecationStatus_DELETED.String()
}

// missingScopes names the zones an aggregated response said nothing about:
// scopes the API reported unreachable, plus zones in the project's zone list
// that the response simply omitted.
func missingScopes[T any](plane string, universe map[string]gcecatalog.ZoneStatus, reported map[string]T, unreachable []string) []string {
	missing := make(map[string]bool)
	for _, scope := range unreachable {
		if zone, ok := zoneOfScope(scope); ok {
			missing[zone] = true
		}
	}
	for zone := range universe {
		if _, ok := reported[zone]; !ok {
			missing[zone] = true
		}
	}
	out := make([]string, 0, len(missing))
	for _, zone := range slices.Sorted(maps.Keys(missing)) {
		out = append(out, plane+": "+zone)
	}
	return out
}

// Ensure the snapshot answers the catalog's shape discovery.
var _ machinetype.Discovery = (*Catalog)(nil)

// zoneStatus maps a Compute zone's status onto the catalog vocabulary.
// Anything the API does not call UP is treated as down.
func zoneStatus(z *computepb.Zone) gcecatalog.ZoneStatus {
	if z.GetStatus() == computepb.Zone_UP.String() {
		return gcecatalog.ZoneStatusUp
	}
	return gcecatalog.ZoneStatusDown
}
