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

package adminfeature

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// MachineCatalog is the curated shape list the capacity surface reports
// against: which families the fleet provisions, how small a host of each one
// can be, and which quota pools it charges. The machine-type repository
// satisfies it.
//
// Get is the refusal's own vocabulary, which the autoscaler snapshot's ladder
// needs for a stored rung the listing no longer holds: three reasons with three
// remedies, and only the catalog can say which one applies. It is asked about
// those rungs alone, never per rung.
type MachineCatalog interface {
	List(ctx context.Context, filter machinetype.ListFilter) ([]*machinetype.MachineType, error)
	Get(ctx context.Context, id string) (*machinetype.MachineType, error)
}

// ZoneFamilyCounter reads the fleet's (zone × machine family) census: the
// machines and the devices they carry. The host feature repository satisfies
// it.
type ZoneFamilyCounter interface {
	CountByZoneFamily(ctx context.Context) ([]host.ZoneFamilyCount, error)
}

// PlacementPolicyReader answers the reserve margin zone ranking filters below,
// so the headroom bars mark the same line placement actually refuses at. The
// settings service satisfies it, same wiring as SweepPolicyReader.
type PlacementPolicyReader interface {
	GetQuotaMargin(ctx context.Context) (int64, bool, error)
}

// CapacityInput is the input for the capacity snapshot.
type CapacityInput struct{}

// The capacity verdict vocabulary. Worst-signal-wins over a fixed precedence,
// computed here so the console renders a word it did not derive.
const (
	verdictClear       = "clear"
	verdictCooling     = "cooling"
	verdictBlocked     = "blocked"
	verdictConstrained = "constrained"
	verdictNoSignal    = "no_signal"
	verdictDisabled    = "disabled"
)

// verdictRank orders the vocabulary for the per-zone rollup: the worst verdict
// any of the zone's families carries is the one the tile answers with.
// no_signal ranks lowest, so one family with something to say speaks for the
// zone rather than being averaged away by the silent ones.
var verdictRank = map[string]int{
	verdictNoSignal:    0,
	verdictClear:       1,
	verdictConstrained: 2,
	verdictCooling:     3,
	verdictBlocked:     4,
}

// Quota-pool states. unknown is not zero and not blocked: a per-family metric
// has no series until the family is first used, and placement treats a pool it
// cannot read as open.
const (
	poolUnknown      = "unknown"
	poolOpen         = "open"
	poolNearReserve  = "near_reserve"
	poolAtWall       = "at_wall"
	regionComfort    = "comfortable"
	regionTight      = "tight"
	regionBlocked    = "blocked"
	regionQuotaUnset = "unknown"
)

// Spot obtainability bands, as the capacity-advice API documents them: 0.7 and
// above high, 0.3 and below low, everything between medium.
const (
	spotBandHigh   = "high"
	spotBandMedium = "medium"
	spotBandLow    = "low"

	spotHighFrom = 0.7
	spotLowUpto  = 0.3
)

// CapacitySpotCell is one synced Spot capacity-advice cell as the surface
// reports it. Absent means no signal, because the cell aged out or the shape
// was never probed. That is never the same as "no capacity".
type CapacitySpotCell struct {
	MachineType       string    `json:"machine_type"`
	Obtainability     float64   `json:"obtainability" doc:"Likelihood a single-VM Spot create succeeds (0.0 to 1.0)"`
	Band              string    `json:"band" enum:"high,medium,low"`
	UptimeTierSeconds int64     `json:"uptime_tier_s" doc:"Estimated minimum run time before preemption (60, 600 or 3600; 0 = unreported)"`
	SyncedAt          time.Time `json:"synced_at,omitzero"`
}

// CapacityCellItem is one (zone × machine family) cell: every capacity signal
// CCO holds about placing that family in that zone, composed into one verdict.
type CapacityCellItem struct {
	MachineFamily string `json:"machine_family"`
	Machines      int64  `json:"machines" doc:"Live hosts of this family in this zone"`
	Arriving      int64  `json:"arriving" doc:"Of those, hosts still coming up (pending or provisioning)"`
	Devices       int64  `json:"devices" doc:"Devices those hosts carry"`

	Verdict string `json:"verdict" enum:"clear,cooling,blocked,constrained,no_signal"`
	// VerdictDetail is the composed reason, ready to render: the console shows
	// the word and this sentence, and derives neither.
	VerdictDetail string    `json:"verdict_detail,omitempty"`
	CoolingUntil  time.Time `json:"cooling_until,omitzero" doc:"When the cooldown lapses; compute the countdown against the snapshot's now"`

	// QuotaPool names the regional CPU pool this family is charged to, empty
	// for a family that spends no CPU quota at all.
	QuotaPool  string `json:"quota_pool,omitempty"`
	QuotaState string `json:"quota_state" enum:"unknown,open,near_reserve,at_wall"`

	Spot     *CapacitySpotCell `json:"spot,omitempty" doc:"Spot capacity advice for this family's shapes; absent = no signal"`
	Cooldown *ZoneCooldownItem `json:"cooldown,omitempty" doc:"The zone_health cell recorded here, live or lapsed"`
}

// CapacityZoneItem is one zone of the placement space, one tile on the board.
// Its verdict is the worst its families carry, so the stripe answers before the
// cells are read.
type CapacityZoneItem struct {
	Zone    string `json:"zone"`
	Region  string `json:"region"`
	Enabled bool   `json:"enabled" doc:"Whether the zone is in the placement space (admin-curated catalog)"`
	Status  string `json:"status" doc:"What Compute Engine last said about the zone (UP/DOWN); empty when the snapshot is silent"`

	Machines int64 `json:"machines"`
	Arriving int64 `json:"arriving"`
	Devices  int64 `json:"devices"`

	Verdict       string    `json:"verdict" enum:"clear,cooling,blocked,constrained,no_signal,disabled"`
	VerdictDetail string    `json:"verdict_detail,omitempty"`
	CoolingUntil  time.Time `json:"cooling_until,omitzero"`

	// Spot is the zone's weakest advice cell across its families, the one the
	// tile's single Spot line reports, picked here so the console never chooses
	// between cells.
	Spot  *CapacitySpotCell  `json:"spot,omitempty"`
	Cells []CapacityCellItem `json:"cells" doc:"The per-family detail behind the tile"`
}

// CapacityRegionItem is one region rail: what the region holds, and how much
// room Google's regional quota leaves it. Quota is accounted per region, so the
// verdict sits on the rail and applies to every tile under it.
type CapacityRegionItem struct {
	Region       string `json:"region"`
	Machines     int64  `json:"machines"`
	Devices      int64  `json:"devices"`
	ZonesEnabled int64  `json:"zones_enabled"`
	Zones        int64  `json:"zones" doc:"Zones of this region on the board, enabled or not"`

	QuotaState string `json:"quota_state" enum:"comfortable,tight,blocked,unknown"`
	// The pool the state came from, with its numbers, so the chip names the
	// pool that is running out.
	QuotaPool  string `json:"quota_pool,omitempty"`
	QuotaUsage int64  `json:"quota_usage,omitempty"`
	QuotaLimit int64  `json:"quota_limit,omitempty"`
}

// CapacityPoolItem is one regional quota pool the fleet draws from, with the
// reserve margin ranking filters below marked on it.
//
// A pool with no series is carried rather than dropped: Google publishes
// nothing for a per-family metric until the family is first used, and a missing
// bar would read as a pool that does not exist.
type CapacityPoolItem struct {
	Region string `json:"region"`
	Pool   string `json:"pool" doc:"Quota metric, e.g. CPUS or CPUS_PER_VM_FAMILY:C4"`
	Kind   string `json:"kind" enum:"cpu,disk"`
	// Families names the fleet's machine families that spend this pool, so the
	// bar can be labeled by what draws it rather than by Google's metric name
	// alone.
	Families []string `json:"families"`
	Spot     bool     `json:"spot" doc:"The preemptible twin: Spot VMs draw it wherever the project has a preemptible grant"`

	State    string    `json:"state" enum:"unknown,open,near_reserve,at_wall"`
	Usage    int64     `json:"usage,omitempty"`
	Limit    int64     `json:"limit,omitempty"`
	Reserve  int64     `json:"reserve,omitempty" doc:"Headroom ranking keeps in reserve; a demand crossing it is refused"`
	SyncedAt time.Time `json:"synced_at,omitzero"`
}

// CapacityFleetItem is the page's four-number answer: what the fleet is, where
// it is, and how much of the placement space is open to it.
type CapacityFleetItem struct {
	Machines     int64 `json:"machines"`
	Devices      int64 `json:"devices"`
	Regions      int64 `json:"regions"`
	ZonesEnabled int64 `json:"zones_enabled"`
	ZonesClear   int64 `json:"zones_clear" doc:"Enabled zones whose verdict is clear"`

	// What the zones that are not clear are doing, counted here from the same
	// verdicts the tiles carry: the page states the breakdown rather than
	// re-judging a tally of its own beside the numbers above.
	ZonesCooling     int64 `json:"zones_cooling" doc:"Enabled zones a live cooldown is holding back"`
	ZonesBlocked     int64 `json:"zones_blocked" doc:"Enabled zones a regional quota wall condemns"`
	ZonesConstrained int64 `json:"zones_constrained" doc:"Enabled zones whose Spot obtainability is low; on-demand is unaffected"`
}

// CapacityOutput is the capacity snapshot: where the fleet can grow right now,
// composed from the four sources that each hold a piece of it: the fleet
// census, the placement plane's reactive memory, Spot capacity advice, and
// Google's regional quota.
type CapacityOutput struct {
	Body struct {
		Now                  time.Time            `json:"now" doc:"Server time of the snapshot; compute ages/countdowns against this, not the client clock"`
		Fleet                CapacityFleetItem    `json:"fleet"`
		Regions              []CapacityRegionItem `json:"regions" doc:"Region rails, each carrying its quota verdict"`
		Zones                []CapacityZoneItem   `json:"zones" doc:"The placement space: enabled zones, plus any zone still holding hosts or health cells"`
		Pools                []CapacityPoolItem   `json:"pools" doc:"Regional quota headroom, one row per pool the fleet draws"`
		Spot                 SpotPanelItem        `json:"spot" doc:"Spot capacity: fleet model split, preemption churn, advice health"`
		ReserveMarginPercent int64                `json:"reserve_margin_percent" doc:"Percent of each quota limit ranking keeps in reserve"`
	}
}

// Capacity returns the capacity snapshot. Every number and every verdict is
// derived here: each input is a TTL'd or event-written row with its own
// timestamp, so two refreshes between syncs render identically.
func (h *AdminHandler) Capacity(ctx context.Context, _ *CapacityInput) (*CapacityOutput, error) {
	out := &CapacityOutput{}
	out.Body.Now = time.Now().UTC()
	out.Body.Regions = []CapacityRegionItem{}
	out.Body.Zones = []CapacityZoneItem{}
	out.Body.Pools = []CapacityPoolItem{}

	spot, err := h.spotPanel(ctx, out.Body.Now)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to read Spot capacity", err)
	}
	out.Body.Spot = spot

	var margin int64 = defaultReserveMarginPercent
	if h.placementPolicy != nil {
		stored, _, merr := h.placementPolicy.GetQuotaMargin(ctx)
		if merr != nil {
			return nil, huma.Error500InternalServerError("failed to read the quota reserve margin", merr)
		}
		margin = stored
	}
	out.Body.ReserveMarginPercent = margin

	board, err := h.capacityBoard(ctx, out.Body.Now, margin)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to compose the placement space", err)
	}
	out.Body.Zones = board.zones
	out.Body.Regions = board.regions
	out.Body.Pools = board.pools
	out.Body.Fleet = board.fleet
	return out, nil
}

// defaultReserveMarginPercent mirrors ranking's default for a deployment whose
// settings service is not wired.
const defaultReserveMarginPercent = 10

// capacityBoard is the assembled placement space: the tiles, the rails they sit
// under, the pools their quota verdicts came from, and the fleet totals.
type capacityBoard struct {
	zones   []CapacityZoneItem
	regions []CapacityRegionItem
	pools   []CapacityPoolItem
	fleet   CapacityFleetItem
}

// capacityBoard composes the board from its four sources. An unwired zone
// catalog or census answers an empty board rather than a wrong one.
func (h *AdminHandler) capacityBoard(ctx context.Context, now time.Time, margin int64) (capacityBoard, error) {
	board := capacityBoard{
		zones:   []CapacityZoneItem{},
		regions: []CapacityRegionItem{},
		pools:   []CapacityPoolItem{},
	}
	if h.zoneCatalog == nil || h.zoneFamily == nil {
		return board, nil
	}

	enabled, err := h.zoneCatalog.ListEnabledZones(ctx)
	if err != nil {
		return board, fmt.Errorf("listing enabled zones: %w", err)
	}
	census, err := h.zoneFamily.CountByZoneFamily(ctx)
	if err != nil {
		return board, fmt.Errorf("counting the fleet by zone and family: %w", err)
	}
	var cells []*gcecatalog.ZoneHealth
	if h.zoneHealth != nil {
		if cells, err = h.zoneHealth.ListCells(ctx); err != nil {
			return board, fmt.Errorf("listing zone health cells: %w", err)
		}
	}
	shapes, err := h.fleetShapes(ctx)
	if err != nil {
		return board, err
	}
	advice, err := h.adviceByZone(ctx)
	if err != nil {
		return board, err
	}

	universe := capacityUniverse(enabled, census, cells, shapes)
	quotas := h.regionQuotaIndex(universe.regions())
	statuses := h.zoneStatuses()

	for _, zone := range slices.Sorted(maps.Keys(universe.zones)) {
		z := universe.zones[zone]
		region := gcecatalog.RegionFromZone(zone)
		item := CapacityZoneItem{
			Zone:    zone,
			Region:  region,
			Enabled: z.enabled,
			Status:  string(statuses[zone]),
			Cells:   []CapacityCellItem{},
		}
		for _, family := range slices.Sorted(maps.Keys(z.families)) {
			cell := capacityCell(cellContext{
				zone: zone, region: region,
				count: z.families[family], shape: shapes[family],
				health: cells, advice: advice[zone], quotas: quotas[region],
				margin: margin, now: now,
			})
			item.Machines += cell.Machines
			item.Arriving += cell.Arriving
			item.Devices += cell.Devices
			item.Cells = append(item.Cells, cell)
		}
		applyZoneRollup(&item)
		board.zones = append(board.zones, item)
	}

	board.regions = capacityRegions(board.zones, shapes, quotas, margin)
	board.pools = capacityPools(board.regions, shapes, quotas, margin)
	board.fleet = capacityFleet(board.zones, board.regions)
	return board, nil
}

// familyShape is what the catalog knows about placing one machine family: the
// smallest host it can place, and the regional pools that host spends. A family
// with no catalog row keeps a zero minVCPUs, which reads as "cannot size a
// host" and never as "fits".
type familyShape struct {
	minVCPUs     int64
	cpuPool      string
	diskPool     string
	diskGB       int64
	machineTypes []string
}

// fleetShapes reads the curated catalog into per-family placement facts. The
// enabled rows alone: a disabled shape is not one the fleet can provision, so
// judging quota against it would answer for a host nobody can create.
func (h *AdminHandler) fleetShapes(ctx context.Context) (map[string]familyShape, error) {
	shapes := map[string]familyShape{}
	if h.machineTypes == nil {
		return shapes, nil
	}
	enabled := true
	rows, err := h.machineTypes.List(ctx, machinetype.ListFilter{IsEnabled: &enabled})
	if err != nil {
		return nil, fmt.Errorf("listing the machine-type catalog: %w", err)
	}
	for _, mt := range rows {
		family := mt.Family
		if family == "" {
			family = machinetype.FamilyOf(mt.ID)
		}
		shape := shapes[family]
		shape.machineTypes = append(shape.machineTypes, mt.ID)
		// The smallest host the family can place decides the quota question: if
		// that one does not fit, nothing of the family does.
		if shape.minVCPUs == 0 || mt.VCPUs < shape.minVCPUs {
			shape.minVCPUs = mt.VCPUs
			shape.cpuPool = gcecatalog.CPUQuotaTypeForFamily(family)
			shape.diskPool = gcecatalog.BootDiskQuotaType(mt.BootDiskType)
			shape.diskGB = host.DefaultBootDiskSizeGB
		}
		shapes[family] = shape
	}
	return shapes, nil
}

// adviceByZone indexes the synced Spot advice cells by zone.
func (h *AdminHandler) adviceByZone(ctx context.Context) (map[string][]*gcecatalog.SpotAdvice, error) {
	byZone := map[string][]*gcecatalog.SpotAdvice{}
	if h.spotAdvice == nil {
		return byZone, nil
	}
	cells, err := h.spotAdvice.ListCells(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing spot advice cells: %w", err)
	}
	for _, c := range cells {
		byZone[c.Zone] = append(byZone[c.Zone], c)
	}
	return byZone, nil
}

// regionQuotaIndex reads each region's quota rows once, keyed by pool.
func (h *AdminHandler) regionQuotaIndex(regions []string) map[string]map[string]*gcecatalog.RegionalQuota {
	index := make(map[string]map[string]*gcecatalog.RegionalQuota, len(regions))
	for _, region := range regions {
		rows := map[string]*gcecatalog.RegionalQuota{}
		if h.regionQuotas != nil {
			for _, q := range h.regionQuotas.RegionalQuotas(region) {
				rows[q.QuotaType] = q
			}
		}
		index[region] = rows
	}
	return index
}

// zoneStatuses reads what Compute Engine last said about each zone; empty
// without a wired snapshot, which renders as an unknown status rather than as
// down.
func (h *AdminHandler) zoneStatuses() map[string]gcecatalog.ZoneStatus {
	if h.zoneUniverse == nil {
		return nil
	}
	return h.zoneUniverse.Zones()
}

// boardZone is one zone of the universe with the families that have something
// to say in it.
type boardZone struct {
	enabled  bool
	families map[string]host.ZoneFamilyCount
}

// boardUniverse is the row set the board is drawn over.
type boardUniverse struct {
	zones map[string]*boardZone
}

func (u boardUniverse) regions() []string {
	regions := map[string]bool{}
	for zone := range u.zones {
		regions[gcecatalog.RegionFromZone(zone)] = true
	}
	return slices.Sorted(maps.Keys(regions))
}

// capacityUniverse builds the row set: every enabled zone, and every disabled
// one still holding hosts or carrying a health cell. A zone Compute Engine
// offers that nobody enabled and nothing ran in is not placement state and
// belongs on the zone catalog.
//
// An enabled zone carries every curated family, so the advice and quota data
// have a cell to land in before anything runs there. A zone just admitted must
// answer "where can the fleet grow", never "nothing observed". A disabled zone
// keeps only the families that ran or failed in it: it is history, and history
// has no empty rows.
func capacityUniverse(enabled []string, census []host.ZoneFamilyCount, cells []*gcecatalog.ZoneHealth, shapes map[string]familyShape) boardUniverse {
	universe := boardUniverse{zones: map[string]*boardZone{}}
	zoneAt := func(zone string) *boardZone {
		z, ok := universe.zones[zone]
		if !ok {
			z = &boardZone{families: map[string]host.ZoneFamilyCount{}}
			universe.zones[zone] = z
		}
		return z
	}
	for _, zone := range enabled {
		z := zoneAt(zone)
		z.enabled = true
		for family := range shapes {
			z.families[family] = host.ZoneFamilyCount{Zone: zone, MachineFamily: family}
		}
	}
	for _, c := range census {
		zoneAt(c.Zone).families[c.MachineFamily] = c
	}
	for _, cell := range cells {
		z := zoneAt(cell.Zone)
		if _, ok := z.families[cell.MachineFamily]; !ok {
			z.families[cell.MachineFamily] = host.ZoneFamilyCount{
				Zone: cell.Zone, MachineFamily: cell.MachineFamily,
			}
		}
	}
	return universe
}

// cellContext is everything one (zone × family) cell is composed from: the
// census row, the catalog's facts about the family, and the three sources that
// have something to say about placing it here.
type cellContext struct {
	zone   string
	region string
	count  host.ZoneFamilyCount
	shape  familyShape
	health []*gcecatalog.ZoneHealth
	advice []*gcecatalog.SpotAdvice
	quotas map[string]*gcecatalog.RegionalQuota
	margin int64
	now    time.Time
}

// capacityCell composes one (zone × family) cell and its verdict.
func capacityCell(c cellContext) CapacityCellItem {
	family := c.count.MachineFamily
	item := CapacityCellItem{
		MachineFamily: family,
		Machines:      c.count.Machines,
		Arriving:      c.count.Arriving,
		Devices:       c.count.Devices,
		QuotaPool:     c.shape.cpuPool,
		QuotaState:    poolUnknown,
	}
	if cell := findZoneHealth(c.health, c.zone, family); cell != nil {
		cooldown := zoneCooldownItem(cell, c.now)
		item.Cooldown = &cooldown
	}
	item.Spot = weakestAdvice(c.advice, c.shape.machineTypes)

	quota := judgeFamilyQuota(c.shape, c.quotas, c.margin)
	item.QuotaState = quota.state
	if quota.pool != "" {
		item.QuotaPool = quota.pool
	}
	item.Verdict, item.VerdictDetail, item.CoolingUntil = capacityVerdict(item, quota, c.region)
	return item
}

// capacityVerdict is the deterministic worst-signal-wins derivation, in a fixed
// precedence: a live cooldown first, then a quota wall the family cannot clear,
// then a low Spot obtainability band. That band annotates the Spot line and
// never condemns on-demand, so it ranks below the other two. Absence of every
// signal is "no signal", never "clear".
func capacityVerdict(cell CapacityCellItem, quota quotaJudgment, region string) (string, string, time.Time) {
	if cd := cell.Cooldown; cd != nil && cd.InCooldown {
		return verdictCooling,
			fmt.Sprintf("%s failed here %d time(s) in a row on %s", cd.MetricLabel, cd.ConsecutiveFailures, cell.MachineFamily),
			cd.CooldownUntil
	}
	if quota.state == poolAtWall {
		return verdictBlocked,
			fmt.Sprintf("%s in %s is spent to its reserve, %s of %s used",
				quota.pool, region, groupDigits(quota.usage), groupDigits(quota.limit)),
			time.Time{}
	}
	if cell.Spot != nil && cell.Spot.Band == spotBandLow {
		return verdictConstrained,
			fmt.Sprintf("Spot obtainability is low for %s here; on-demand is unaffected", cell.MachineFamily),
			time.Time{}
	}
	if cell.Cooldown == nil && cell.Spot == nil && quota.state == poolUnknown {
		return verdictNoSignal, "nothing has been observed about this cell yet", time.Time{}
	}
	return verdictClear, "", time.Time{}
}

// applyZoneRollup lifts the worst of a zone's cell verdicts onto the tile. A
// disabled zone answers "disabled" whatever its cells say: it is not in the
// placement space, so nothing about capacity there is actionable until an
// operator puts it back. Its Spot line still rides along, because the advice
// cells are measurements of the zone and not of the operator's choice, and
// reporting them as absent would claim the zone was never probed.
func applyZoneRollup(item *CapacityZoneItem) {
	item.Spot = weakestZoneAdvice(item.Cells)
	if !item.Enabled {
		item.Verdict = verdictDisabled
		item.VerdictDetail = "not in the placement space, ranking never picks this zone"
		return
	}
	item.Verdict = verdictNoSignal
	item.VerdictDetail = "nothing has been observed about this zone yet"
	for i := range item.Cells {
		cell := item.Cells[i]
		if verdictRank[cell.Verdict] <= verdictRank[item.Verdict] {
			continue
		}
		item.Verdict = cell.Verdict
		item.VerdictDetail = cell.VerdictDetail
		item.CoolingUntil = cell.CoolingUntil
	}
}

// weakestZoneAdvice picks the tile's single Spot line: the least obtainable
// cell across the zone's families, since that is the one that would refuse a
// create first.
func weakestZoneAdvice(cells []CapacityCellItem) *CapacitySpotCell {
	var weakest *CapacitySpotCell
	for i := range cells {
		spot := cells[i].Spot
		if spot == nil {
			continue
		}
		if weakest == nil || spot.Obtainability < weakest.Obtainability {
			weakest = spot
		}
	}
	return weakest
}

// weakestAdvice picks the least obtainable advice cell among a family's shapes;
// nil when none of them carries one.
func weakestAdvice(advice []*gcecatalog.SpotAdvice, machineTypes []string) *CapacitySpotCell {
	var weakest *gcecatalog.SpotAdvice
	for _, c := range advice {
		if !slices.Contains(machineTypes, c.MachineType) {
			continue
		}
		if weakest == nil || c.Obtainability < weakest.Obtainability {
			weakest = c
		}
	}
	if weakest == nil {
		return nil
	}
	return &CapacitySpotCell{
		MachineType:       weakest.MachineType,
		Obtainability:     weakest.Obtainability,
		Band:              spotBand(weakest.Obtainability),
		UptimeTierSeconds: weakest.UptimeTierSeconds,
		SyncedAt:          weakest.SyncedAt,
	}
}

// groupDigits renders a quota figure with thousands separators, so a sentence
// composed here reads the way the console prints the same pool beside it.
func groupDigits(n int64) string {
	digits := strconv.FormatInt(n, 10)
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}
	var grouped strings.Builder
	for i, digit := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(digit)
	}
	return sign + grouped.String()
}

// spotBand maps an obtainability score onto the API's documented bands.
func spotBand(obtainability float64) string {
	switch {
	case obtainability >= spotHighFrom:
		return spotBandHigh
	case obtainability <= spotLowUpto:
		return spotBandLow
	default:
		return spotBandMedium
	}
}

// findZoneHealth returns the (zone, family) health cell, or nil. Only the
// cell recorded in this zone: a regional quota cell repels its whole region at
// ranking time, but it exists in exactly one zone, and that is the one an
// operator can clear it from.
func findZoneHealth(cells []*gcecatalog.ZoneHealth, zone, family string) *gcecatalog.ZoneHealth {
	for _, cell := range cells {
		if cell.Zone == zone && cell.MachineFamily == family {
			return cell
		}
	}
	return nil
}

// judgeFamilyQuota evaluates the pools one host of this family would spend (its
// CPU pool and its boot disk's capacity pool) against the reserve margin,
// and reports the binding one. A pool with no row, no ceiling or no grant is
// unknown rather than blocked: the per-family series does not exist until the
// family is first used, and placement treats what it cannot read as open.
func judgeFamilyQuota(
	shape familyShape,
	quotas map[string]*gcecatalog.RegionalQuota,
	margin int64,
) quotaJudgment {
	judgment := quotaJudgment{state: poolUnknown}
	if shape.minVCPUs == 0 {
		return judgment
	}
	type demand struct {
		pool   string
		amount int64
	}
	for _, d := range []demand{{shape.cpuPool, shape.minVCPUs}, {shape.diskPool, shape.diskGB}} {
		if d.pool == "" {
			continue
		}
		row := quotas[d.pool]
		if !judgeable(row) {
			continue
		}
		reserve := poolReserve(row.LimitValue, margin)
		state := poolOpen
		switch {
		case row.Available()-d.amount < reserve:
			state = poolAtWall
		case row.Available()-d.amount < 2*reserve:
			state = poolNearReserve
		}
		if poolRank(state) <= poolRank(judgment.state) {
			continue
		}
		judgment = quotaJudgment{
			pool: d.pool, state: state, usage: row.UsageValue, limit: row.LimitValue,
		}
	}
	return judgment
}

// quotaJudgment is the binding pool's answer for one family: which pool binds,
// how close it is to the reserve, and the numbers behind that.
type quotaJudgment struct {
	pool  string
	state string
	usage int64
	limit int64
}

// judgeable reports whether a quota row carries a ceiling worth judging. A
// missing row, a pool granted nothing and a pool with no ceiling are all left
// unjudged, because any verdict on them would be wrong.
func judgeable(row *gcecatalog.RegionalQuota) bool {
	return row != nil && row.LimitValue > 0 && row.LimitValue != gcecatalog.LimitUnlimited
}

// poolReserve is the headroom ranking keeps back. Divided before multiplied:
// a pool with no ceiling carries the largest int64 there is, and taking a
// percentage of it the other way round overflows into a negative reserve.
func poolReserve(limit, margin int64) int64 {
	return limit / 100 * margin
}

// poolRank orders the pool states so the binding one wins.
func poolRank(state string) int {
	switch state {
	case poolOpen:
		return 1
	case poolNearReserve:
		return 2
	case poolAtWall:
		return 3
	default:
		return 0
	}
}

// capacityRegions builds the rails: each region's totals and the quota verdict
// its tiles inherit.
func capacityRegions(
	zones []CapacityZoneItem,
	shapes map[string]familyShape,
	quotas map[string]map[string]*gcecatalog.RegionalQuota,
	margin int64,
) []CapacityRegionItem {
	demand, _ := poolDemand(shapes)
	byRegion := map[string]*CapacityRegionItem{}
	for _, z := range zones {
		item, ok := byRegion[z.Region]
		if !ok {
			item = &CapacityRegionItem{Region: z.Region, QuotaState: regionQuotaUnset}
			byRegion[z.Region] = item
		}
		item.Machines += z.Machines
		item.Devices += z.Devices
		item.Zones++
		if z.Enabled {
			item.ZonesEnabled++
		}
	}
	regions := make([]CapacityRegionItem, 0, len(byRegion))
	for _, region := range slices.Sorted(maps.Keys(byRegion)) {
		item := byRegion[region]
		applyRegionQuota(item, demand, quotas[region], margin)
		regions = append(regions, *item)
	}
	return regions
}

// applyRegionQuota puts the region's tightest judgeable pool on the rail,
// among the pools the fleet's shapes draw. The region carries quota rows for
// families the fleet never provisions, and a wall in one of those is not this
// fleet's wall.
func applyRegionQuota(item *CapacityRegionItem, demand map[string]string, quotas map[string]*gcecatalog.RegionalQuota, margin int64) {
	best := 0
	for _, pool := range slices.Sorted(maps.Keys(quotas)) {
		if _, draws := demand[pool]; !draws {
			continue
		}
		row := quotas[pool]
		if !judgeable(row) {
			continue
		}
		reserve := poolReserve(row.LimitValue, margin)
		state := regionComfort
		switch {
		case row.Available() <= reserve:
			state = regionBlocked
		case row.Available() <= 2*reserve:
			state = regionTight
		}
		rank := regionRank(state)
		if rank <= best {
			continue
		}
		best = rank
		item.QuotaState, item.QuotaPool = state, pool
		item.QuotaUsage, item.QuotaLimit = row.UsageValue, row.LimitValue
	}
	if best == 0 {
		item.QuotaState = regionQuotaUnset
	}
}

func regionRank(state string) int {
	switch state {
	case regionComfort:
		return 1
	case regionTight:
		return 2
	case regionBlocked:
		return 3
	default:
		return 0
	}
}

// capacityPools builds the headroom rows: every pool the fleet's families draw
// in every region on the board, whether or not Google has published a series
// for it yet.
func capacityPools(
	regions []CapacityRegionItem,
	shapes map[string]familyShape,
	quotas map[string]map[string]*gcecatalog.RegionalQuota,
	margin int64,
) []CapacityPoolItem {
	kinds, families := poolDemand(shapes)
	pools := make([]CapacityPoolItem, 0, len(regions)*len(kinds))
	for _, region := range regions {
		for _, pool := range slices.Sorted(maps.Keys(kinds)) {
			item := CapacityPoolItem{
				Region:   region.Region,
				Pool:     pool,
				Kind:     kinds[pool],
				Families: families[pool],
				Spot:     gcecatalog.IsPreemptibleQuotaType(pool),
				State:    poolUnknown,
			}
			if row := quotas[region.Region][pool]; judgeable(row) {
				item.Usage, item.Limit = row.UsageValue, row.LimitValue
				item.Reserve = poolReserve(row.LimitValue, margin)
				item.State = poolOpen
				switch {
				case row.Available() <= item.Reserve:
					item.State = poolAtWall
				case row.Available() <= 2*item.Reserve:
					item.State = poolNearReserve
				}
				if row.LastSyncAt != nil {
					item.SyncedAt = *row.LastSyncAt
				}
			}
			pools = append(pools, item)
		}
	}
	return pools
}

// poolDemand collects the pools the fleet's shapes spend, with the families
// behind each one. The preemptible CPU pool rides along unconditionally: Spot
// hosts draw it wherever the project has a preemptible grant, and a project
// without one shows it as unknown, which is the truth.
func poolDemand(shapes map[string]familyShape) (map[string]string, map[string][]string) {
	kinds := map[string]string{gcecatalog.QuotaTypePreemptibleCPUs: "cpu"}
	families := map[string][]string{}
	for _, family := range slices.Sorted(maps.Keys(shapes)) {
		shape := shapes[family]
		if shape.cpuPool != "" {
			kinds[shape.cpuPool] = "cpu"
			families[shape.cpuPool] = append(families[shape.cpuPool], family)
			families[gcecatalog.QuotaTypePreemptibleCPUs] = append(
				families[gcecatalog.QuotaTypePreemptibleCPUs], family)
		}
		if shape.diskPool != "" {
			kinds[shape.diskPool] = "disk"
			families[shape.diskPool] = append(families[shape.diskPool], family)
		}
	}
	for pool := range kinds {
		if families[pool] == nil {
			families[pool] = []string{}
		}
	}
	return kinds, families
}

// capacityFleet totals the board into the page's KPI answer.
func capacityFleet(zones []CapacityZoneItem, regions []CapacityRegionItem) CapacityFleetItem {
	fleet := CapacityFleetItem{Regions: int64(len(regions))}
	for _, z := range zones {
		fleet.Machines += z.Machines
		fleet.Devices += z.Devices
		if !z.Enabled {
			continue
		}
		fleet.ZonesEnabled++
		switch z.Verdict {
		case verdictClear:
			fleet.ZonesClear++
		case verdictCooling:
			fleet.ZonesCooling++
		case verdictBlocked:
			fleet.ZonesBlocked++
		case verdictConstrained:
			fleet.ZonesConstrained++
		}
	}
	return fleet
}

// zoneMetricLabel renders a zone_health binding metric as the English the
// surface states. The store keys are identifiers, and this is the one place
// they are translated. The console renders what it is given.
func zoneMetricLabel(metric string) string {
	switch metric {
	case gcecatalog.ZoneMetricCapacity:
		return "capacity"
	case gcecatalog.ZoneMetricAvailability:
		return "shape availability"
	case gcecatalog.ZoneMetricPreemption:
		return "Spot reclaim"
	case gcecatalog.ZoneMetricQuotaCPU:
		return "CPU quota"
	case gcecatalog.ZoneMetricQuotaGPU:
		return "GPU quota"
	case gcecatalog.ZoneMetricQuotaIP:
		return "IP quota"
	case gcecatalog.ZoneMetricQuotaSSD:
		return "SSD quota"
	case gcecatalog.ZoneMetricQuotaHyperdisk:
		return "hyperdisk quota"
	case gcecatalog.ZoneMetricQuotaInstance:
		return "instance quota"
	default:
		return strings.ReplaceAll(metric, "_", " ")
	}
}

// ZoneCooldownItem is one zone_health cell attached to the zone it was
// recorded in.
type ZoneCooldownItem struct {
	MachineFamily string `json:"machine_family" doc:"Machine family the failures were observed for, e.g. n1"`
	BindingMetric string `json:"binding_metric" doc:"The capacity/quota axis that failed, e.g. capacity, quota_cpu"`
	// MetricLabel is the binding metric as English. It rides the cell so the
	// vocabulary is translated in exactly one place: the verdict sentence and
	// the operator's clear-confirmation name the same axis the same way.
	MetricLabel         string    `json:"metric_label"`
	Regional            bool      `json:"regional" doc:"Quota metrics cool every zone in the region, not only this one"`
	ConsecutiveFailures int64     `json:"consecutive_failures"`
	InCooldown          bool      `json:"in_cooldown" doc:"Whether the cell still repels placement at the snapshot's now"`
	CooldownUntil       time.Time `json:"cooldown_until,omitzero"`
	LastFailureAt       time.Time `json:"last_failure_at,omitzero"`
	LastReason          string    `json:"last_reason,omitempty"`
}

// ZoneCooldownClearInput addresses one zone_health cell by its composite key.
type ZoneCooldownClearInput struct {
	Zone          string `path:"zone" maxLength:"64" doc:"Zone the cell was recorded in, e.g. us-central1-a"`
	MachineFamily string `path:"machine_family" maxLength:"32" doc:"Machine family the cell covers, e.g. n1"`
}

// SpotFleetCountItem is one provisioning-model × status cell of the fleet
// census.
type SpotFleetCountItem struct {
	Model  string `json:"model" doc:"Provisioning model" enum:"SPOT,STANDARD"`
	Status string `json:"status" doc:"Host status, e.g. running"`
	Count  int64  `json:"count"`
}

// SpotAdviceHealth reports the Spot capacity-advice sync state.
type SpotAdviceHealth struct {
	Enabled        bool      `json:"enabled" doc:"Whether the advice sync is enabled (CCO_SPOT_ADVICE)"`
	Cells          int64     `json:"cells" doc:"Synced (zone × machine type) advice cells"`
	NewestSyncedAt time.Time `json:"newest_synced_at,omitzero" doc:"Freshest cell's sync time"`
	// Stale is judged against the same bound ranking consults, so the surface
	// and placement cannot disagree about when advice stopped steering.
	Stale bool `json:"stale" doc:"Whether the newest cell has aged past the bound ranking consults; ranking then falls back to quota and health"`
}

// SpotPanelItem is the Spot-capacity block of the snapshot: the fleet's
// model split, 7-day preemption churn, and advice-sync health.
type SpotPanelItem struct {
	Fleet []SpotFleetCountItem `json:"fleet" doc:"Live hosts by provisioning model and status"`
	// The model split the card states, totalled across the census rows above so
	// the surface reads one number per side rather than reducing the cells.
	SpotMachines     int64 `json:"spot_machines" doc:"Live hosts running on Spot capacity"`
	StandardMachines int64 `json:"standard_machines" doc:"Live hosts running on on-demand capacity"`
	// The churn KPIs count audit entries: host rows are torn down with the
	// fleet, so surviving-row counts would erase history (the page rule).
	Preempted7d            int64            `json:"preempted_7d" doc:"Spot preemptions observed in the last 7 days"`
	Recovered7d            int64            `json:"recovered_7d" doc:"Preemption recoveries completed in the last 7 days"`
	Unrecovered7d          int64            `json:"unrecovered_7d" doc:"Preemptions in the window with no recovery recorded yet"`
	TypicalRecoverySeconds int64            `json:"typical_recovery_seconds" doc:"Mean recovery duration across the window's newest recoveries (0 = no data)"`
	Advice                 SpotAdviceHealth `json:"advice"`
}

// spotPanel assembles the Spot-capacity block: the fleet's model split, the
// 7-day preemption churn from the audit log, and the advice-sync health.
func (h *AdminHandler) spotPanel(ctx context.Context, now time.Time) (SpotPanelItem, error) {
	item := SpotPanelItem{
		Fleet:  []SpotFleetCountItem{},
		Advice: SpotAdviceHealth{Enabled: h.spotAdviceEnabled},
	}

	if h.spotFleet != nil {
		counts, err := h.spotFleet.CountByModelStatus(ctx)
		if err != nil {
			return item, fmt.Errorf("counting hosts by model: %w", err)
		}
		for _, c := range counts {
			item.Fleet = append(item.Fleet, SpotFleetCountItem{
				Model: string(c.Model), Status: string(c.Status), Count: c.Count,
			})
			switch c.Model {
			case host.ProvisioningModelSpot:
				item.SpotMachines += c.Count
			case host.ProvisioningModelStandard:
				item.StandardMachines += c.Count
			}
		}
	}

	if h.auditReader != nil {
		if err := h.spotChurn(ctx, now, &item); err != nil {
			return item, err
		}
	}

	if h.spotAdvice != nil {
		cells, err := h.spotAdvice.ListCells(ctx)
		if err != nil {
			return item, fmt.Errorf("listing spot advice cells: %w", err)
		}
		item.Advice.Cells = int64(len(cells))
		for _, c := range cells {
			if c.SyncedAt.After(item.Advice.NewestSyncedAt) {
				item.Advice.NewestSyncedAt = c.SyncedAt
			}
		}
	}
	item.Advice.Stale = adviceStale(item.Advice.NewestSyncedAt, now, h.spotAdviceStale)

	return item, nil
}

// defaultSpotAdviceStale mirrors ranking's compiled bound for a deployment
// whose advice sync interval is not wired through.
const defaultSpotAdviceStale = time.Hour

// adviceStale judges the advice block by the bound ranking consults: past it
// the cells no longer steer placement, and advice that never synced is stale
// rather than fresh with nothing in it.
func adviceStale(newest, now time.Time, after time.Duration) bool {
	if after <= 0 {
		after = defaultSpotAdviceStale
	}
	return newest.IsZero() || now.Sub(newest) > after
}

// spotChurn fills the 7-day preemption KPIs from the audit log.
func (h *AdminHandler) spotChurn(ctx context.Context, now time.Time, item *SpotPanelItem) error {
	since := now.Add(-provisioningWindow)
	preemptedAction := audit.ActionHostPreempted
	preempted, err := h.auditReader.Count(ctx, audit.ListFilter{Action: &preemptedAction, StartTime: &since})
	if err != nil {
		return fmt.Errorf("counting preemptions: %w", err)
	}
	item.Preempted7d = preempted

	recoveredAction := audit.ActionHostPreemptionRecovered
	recovered, err := h.auditReader.Count(ctx, audit.ListFilter{Action: &recoveredAction, StartTime: &since})
	if err != nil {
		return fmt.Errorf("counting preemption recoveries: %w", err)
	}
	item.Recovered7d = recovered
	item.Unrecovered7d = max(0, preempted-recovered)
	if recovered == 0 {
		return nil
	}

	entries, err := h.auditReader.Query(ctx, audit.ListFilter{
		Action: &recoveredAction, StartTime: &since, Limit: recoveryDurationSample,
	})
	if err != nil {
		return fmt.Errorf("reading preemption recoveries: %w", err)
	}
	item.TypicalRecoverySeconds = meanRecoverySeconds(entries)
	return nil
}

// recoveryDurationSample bounds how many recovery entries the duration stat
// averages over. It is the newest slice of the window, never an unbounded scan.
const recoveryDurationSample = 50

// meanRecoverySeconds averages the duration_seconds metadata of the given
// recovery entries; entries without one (recovery of a row missing its
// preemption timestamp) are skipped. 0 = no measurable recoveries.
func meanRecoverySeconds(entries []audit.Entry) int64 {
	var sum, n int64
	for _, e := range entries {
		switch v := e.Metadata["duration_seconds"].(type) {
		case float64: // JSON round-trip renders numbers as float64
			sum, n = sum+int64(v), n+1
		case int64:
			sum, n = sum+v, n+1
		}
	}
	if n == 0 {
		return 0
	}
	return sum / n
}

// zoneCooldownItem maps one zone_health cell into the panel's wire shape.
func zoneCooldownItem(cell *gcecatalog.ZoneHealth, now time.Time) ZoneCooldownItem {
	item := ZoneCooldownItem{
		MachineFamily:       cell.MachineFamily,
		BindingMetric:       cell.BindingMetric,
		MetricLabel:         zoneMetricLabel(cell.BindingMetric),
		Regional:            gcecatalog.IsRegionalMetric(cell.BindingMetric),
		ConsecutiveFailures: cell.ConsecutiveFailures,
		InCooldown:          cell.InCooldown(now),
		LastReason:          cell.LastReason,
	}
	if cell.CooldownUntil != nil {
		item.CooldownUntil = *cell.CooldownUntil
	}
	if cell.LastFailureAt != nil {
		item.LastFailureAt = *cell.LastFailureAt
	}
	return item
}

// ClearZoneCooldown drops a zone_health cell: the cooldown countdown ends and
// the consecutive-failure count that decays the zone's ranking score goes with
// it. A successful provision clears cells the same way. This is the manual
// door for when the operator fixed the cause and the cell would otherwise keep
// steering placement away for the rest of its retention.
//
// Always 204. The delete is a mutation, so a missing cell is a no-op, and a
// cell legitimately vanishes under the caller when a concurrent provision
// succeeds in it; a 404 here would report that benign race as a failure.
func (h *AdminHandler) ClearZoneCooldown(ctx context.Context, input *ZoneCooldownClearInput) (*struct{}, error) {
	if h.zoneHealth == nil {
		return nil, huma.Error500InternalServerError("zone health is not configured")
	}
	if err := h.zoneHealth.ClearCell(ctx, input.Zone, input.MachineFamily); err != nil {
		return nil, huma.Error500InternalServerError("failed to clear the zone cooldown", err)
	}
	audit.LogChange(ctx, h.auditLogger, audit.ActionZoneCooldownClear, audit.ResourceSystem,
		input.Zone+"/"+input.MachineFamily, nil)
	// Non-nil and empty: the output type carries no Body, so Huma writes the
	// bare 204. Returning a nil pointer would mean the same thing to Huma and
	// trip nilnil on the way.
	return &struct{}{}, nil
}
