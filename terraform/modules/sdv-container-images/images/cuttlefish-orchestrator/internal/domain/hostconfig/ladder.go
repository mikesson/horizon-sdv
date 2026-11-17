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

package hostconfig

import (
	"fmt"
	"slices"
	"strings"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// MaxLadderRungs bounds the shape ladder. Eight is more machine shapes than
// any deployment has validated a host image against; a list long enough to
// need paging is a catalog, and the catalog is already the place that holds
// every shape this platform can run.
const MaxLadderRungs = 8

// FieldShapeLadder names the ladder in a ValidationError. Per-rung failures
// extend it with the index, so a refusal says which rung is wrong rather than
// that one of them is.
const FieldShapeLadder = "shape_ladder"

// LadderRung is one admissible step of a resolved shape ladder: the machine
// type an operator asked for, plus the catalog facts the walk decides with.
//
// Resolved against the catalog rather than carried as a bare id, because every
// decision downstream (which zones the rung can go to, which stockout
// observation applies to it, whether it still holds the host's slot ceiling)
// is a catalog fact, and re-reading one per rung is the shape this avoids.
type LadderRung struct {
	// MachineType is the catalog id the operator wrote.
	MachineType string
	// Family keys the zone_health observation this rung is judged against.
	Family string
	// Zones is the catalog's availability list; empty means every zone.
	Zones []string
	// VCPUs and MemoryGB size the rung, so a walk never lands a host on a
	// shape too small for the slot ceiling it already promised.
	VCPUs    int64
	MemoryGB int64
	// Fit is the composed device capacity of a machine built here: the
	// cores-and-memory arithmetic, or the operator's per-shape override.
	Fit int64
	// GPUType and GPUCount are what a machine built on this rung carries, in
	// the pool that owns the ladder: the accelerator welded onto an integrated
	// shape, or the curated default an attachable one is given because the gpu
	// pool asks. Empty outside that pool, because nothing attaches where no
	// create wants one, and stating a card the machine would not carry is the
	// false claim the editor exists to prevent.
	GPUType  string
	GPUCount int64
}

// WhyUnoffered answers why the machine-type catalog does not offer one shape,
// as the catalog's own error, or nil when it does offer it.
//
// The catalog is the only place that holds the answer, and the three it can
// give (no such machine, a family to enable, a machine too small for a guest
// that a capacity override would admit) need three different remedies. It is
// asked only about a rung the catalog listing does not hold: the one a
// validation is about to refuse, or each one a view draws as refused. A ladder
// every rule admits costs nothing.
type WhyUnoffered func(machineType string) error

// validateLadderShape checks what is true of the list itself, whatever the
// catalog says: its length, that every rung names something, and that no rung
// repeats. A repeat re-probes a stockout the walk already knows about and
// reports a longer walk than happened.
func validateLadderShape(rungs []string) error {
	if len(rungs) > MaxLadderRungs {
		return &ValidationError{
			Field:   FieldShapeLadder,
			Message: fmt.Sprintf("must not exceed %d rungs", MaxLadderRungs),
		}
	}
	seen := make(map[string]bool, len(rungs))
	for i, id := range rungs {
		field := fmt.Sprintf("%s[%d]", FieldShapeLadder, i)
		if strings.TrimSpace(id) == "" {
			return &ValidationError{Field: field, Message: "a rung must name a machine type"}
		}
		if seen[id] {
			return &ValidationError{
				Field:   field,
				Message: fmt.Sprintf("%q appears more than once; a repeated rung re-probes a stockout the walk already found", id),
			}
		}
		seen[id] = true
	}
	return nil
}

// ValidateShapeLadder refuses a ladder at the door, naming the rule each
// refused rung broke.
//
// pool is the pool that OWNS the ladder, and admission depends on it: bare
// metal belongs to a pool whose machines are bare metal and nowhere else, and an
// integrated-GPU shape would attach an accelerator to a CPU pool's host. One
// list valid in one pool and refused in another cannot be one list, which is why
// a ladder is a property of a pool rather than an object pools point at.
//
// catalog is the whole enabled-and-disabled machine-type list, read once.
// Admission is decided in memory against it, never by a lookup per rung.
//
// Refusing is the half of the feature that does the work. The newest machine
// families carry exactly the quirks GCE's own machine-type resource does not
// state (which boot disk they will accept, whether they virtualize at all)
// and those live in the family curation the catalog composes from. A ladder
// that took any string would answer a stockout with a fleet of machines that
// boot and cannot launch a device. A wanted rung the catalog does not offer is
// a family to enable, never a rule to relax.
//
// A custom machine type is not admissible here. The catalog offers one only
// on lookup, and admitting one would mean a Compute
// Engine call per rung inside the walk this is built to keep statement-free,
// for a preference order whose whole purpose, stepping past an exhausted
// family, a custom form of that same family cannot serve.
//
// why is asked only about a rung the listing does not hold, and only about the
// first such rung, so the refusal states the catalog's own reason rather than
// the likeliest one. A nil why leaves the refusal generic.
func ValidateShapeLadder(pool string, rungs []string, catalog []*machinetype.MachineType, why WhyUnoffered) error {
	if err := validateLadderShape(rungs); err != nil {
		return err
	}
	rows := make(map[string]*machinetype.MachineType, len(catalog))
	for _, mt := range catalog {
		rows[mt.ID] = mt
	}
	for i, id := range rungs {
		if reason := refusalFor(pool, rows[id], id, why); reason != "" {
			return &ValidationError{
				Field:   fmt.Sprintf("%s[%d]", FieldShapeLadder, i),
				Message: reason,
			}
		}
	}
	return nil
}

// refusalFor names the rule a rung breaks in the pool that owns it, in the
// catalog's own words wherever the catalog is the one refusing it. Everything
// that judges a stored rung goes through here, so a write and a view can never
// say different things about the same rung.
//
// Only the unoffered refusal is replaced, and only when a catalog answer is at
// hand. A ladder written for a pool this platform has not is refused for the
// pool, whatever the catalog thinks of the shapes in it.
func refusalFor(pool string, mt *machinetype.MachineType, id string, why WhyUnoffered) string {
	reason := rungRefusal(pool, mt, id)
	if reason == "" || mt != nil || why == nil || !host.IsPool(pool) {
		return reason
	}
	if unoffered := why(id); unoffered != nil {
		return unoffered.Error()
	}
	return reason
}

// rungRefusal names the rule a rung breaks in the pool that owns it, or "" when
// it is admissible there. One function so the write path and the resolution can
// never disagree about what a ladder may hold: a rung the write accepts and the
// resolution drops is a preference an operator set and never got.
//
// A pool this platform does not have admits nothing. Its ladder can only have
// been hand-written, and building whatever it names would put machines in a pool
// no create is ever routed to.
//
// The unoffered sentence names no remedy, because there are three and only the
// catalog knows which one applies. refusalFor asks it and says so; everything
// else here only has to know the rung is out.
func rungRefusal(pool string, mt *machinetype.MachineType, id string) string {
	switch {
	case !host.IsPool(pool):
		return fmt.Sprintf("%q belongs to no pool this platform has", id)
	case mt == nil:
		return fmt.Sprintf("%q is not a machine type the catalog offers, and the catalog is the only "+
			"authority on what can run Cuttlefish here", id)
	case !mt.IsEnabled:
		return fmt.Sprintf("%q is disabled in the machine-type catalog", id)
	}
	if pool == host.PoolARM {
		return armRungRefusal(mt, id)
	}
	switch {
	case mt.Architecture != machinetype.ArchX86:
		return fmt.Sprintf("%q is %s; the %s pool provisions the x86 path", id, mt.Architecture, pool)
	case mt.IsBareMetal():
		return fmt.Sprintf("%q is bare metal, which GCE never offers as Spot capacity", id)
	case host.PoolRequiresGPU(pool) && !carriesGPU(mt):
		return fmt.Sprintf("%q carries no GPU and none the catalog can attach to it, so a host built "+
			"on it could not serve the creates the %s pool exists for", id, pool)
	case !host.PoolRequiresGPU(pool) && mt.GPUMode == machinetype.GPUModeIntegrated:
		return fmt.Sprintf("%q has an integrated GPU, which would attach an accelerator to a "+
			"host that never asked for one; the %s pool provisions the CPU path", id, pool)
	}
	return ""
}

// carriesGPU reports whether a host built on this shape really ends up with an
// accelerator: welded on, or attached at insert from the type the catalog names
// for it.
//
// The named type is half of the attachable answer, and provisioning is why: it
// attaches nothing to a row that names none, so such a rung would build a host
// with no accelerator, in a pool whose every create is fenced onto accelerated
// machines. The pool would count it as standing capacity while nothing could
// ever be placed on it, and each miss would buy another.
//
// Stated as what CAN carry one rather than as what cannot, so a row that names
// no mode at all reads as no GPU: the fail-closed direction for a pool that
// exists to hold accelerated machines.
func carriesGPU(mt *machinetype.MachineType) bool {
	if mt.GPUMode == machinetype.GPUModeAttachable {
		if mt.DefaultGPUType == "" {
			return false
		}
		_, ok := machinetype.AttachableGPUCount(mt.DefaultGPUType, mt.VCPUs, mt.MaxGPUs, mt.DefaultGPUCount)
		return ok
	}
	return mt.GPUMode == machinetype.GPUModeIntegrated
}

// armRungRefusal is the arm pool's own admission. Cuttlefish on C4A runs on bare
// metal alone, so the two rules the x86 pools apply are inverted here rather than
// relaxed: an arm rung that is not bare metal cannot launch a device at all.
func armRungRefusal(mt *machinetype.MachineType, id string) string {
	switch {
	case mt.Architecture != machinetype.ArchArm:
		return fmt.Sprintf("%q is %s; the %s pool provisions arm64", id, mt.Architecture, host.PoolARM)
	case !mt.IsBareMetal():
		return fmt.Sprintf("%q is not bare metal, and Cuttlefish on arm64 runs on bare metal alone", id)
	}
	return ""
}

// LadderJudgement is one stored rung as a surface reports it: the resolved
// rung where the owning pool still admits it, or the rule it now breaks.
//
// It exists so a rung that has fallen out of the catalog is a step an operator
// can SEE the fleet no longer takes. Provisioning only needs the admitted
// subset, and gets it from ResolveLadder; a page drawing the order from that
// alone would draw a shorter ladder than the one stored, with nothing anywhere
// saying a preference was lost.
type LadderJudgement struct {
	// MachineType is the id the operator wrote, which is all a refused rung has.
	MachineType string

	// Rung is the resolved rung; its zero value on a refused one.
	Rung LadderRung

	// Refusal names the rule the owning pool refuses this rung by, empty when
	// the pool admits it.
	Refusal string
}

// ResolveLadder turns a stored ladder into the rungs a walk may actually
// probe, in the operator's order, dropping any the owning pool no longer admits.
//
// Dropping rather than refusing, because the catalog moves under a stored
// ladder: disabling a shape must cost the fleet that rung and nothing else.
// An empty result is a ladder that no longer says anything, and every caller
// reads it as the catalog default, which is today's behavior exactly.
//
// The refusals are dropped here, so the catalog is never asked to explain one:
// provisioning has nobody to say it to.
func ResolveLadder(pool string, rungs []string, catalog []*machinetype.MachineType) []LadderRung {
	judged := JudgeLadder(pool, rungs, catalog, nil)
	resolved := make([]LadderRung, 0, len(judged))
	for _, judgement := range judged {
		if judgement.Refusal == "" {
			resolved = append(resolved, judgement.Rung)
		}
	}
	return resolved
}

// JudgeLadder resolves a stored ladder and keeps the rungs the owning pool no
// longer admits, each with the rule it breaks.
//
// One walk over the stored order, so what provisioning probes and what a page
// draws come from one reading of the catalog and cannot disagree.
//
// why is asked about each rung the listing does not hold, so a drawn refusal
// states the catalog's own reason rather than the likeliest one, in the same
// sentence a write of that rung is refused with. A nil why leaves those
// refusals generic.
func JudgeLadder(pool string, rungs []string, catalog []*machinetype.MachineType, why WhyUnoffered) []LadderJudgement {
	rows := make(map[string]*machinetype.MachineType, len(catalog))
	for _, mt := range catalog {
		rows[mt.ID] = mt
	}
	judged := make([]LadderJudgement, 0, len(rungs))
	for _, id := range rungs {
		mt := rows[id]
		if refusal := refusalFor(pool, mt, id, why); refusal != "" {
			judged = append(judged, LadderJudgement{MachineType: id, Refusal: refusal})
			continue
		}
		family := mt.Family
		if family == "" {
			family = machinetype.FamilyOf(mt.ID)
		}
		rung := LadderRung{
			MachineType: mt.ID,
			Family:      family,
			Zones:       mt.Zones,
			VCPUs:       mt.VCPUs,
			MemoryGB:    mt.MemoryGB,
			Fit:         mt.Fit,
		}
		if host.PoolRequiresGPU(pool) {
			// The same facts the attach binds at provisioning: the shape's own
			// accelerator, or the curated default with the same floor-of-one the
			// attach applies, since a pool requiring a GPU never attaches zero.
			switch mt.GPUMode {
			case machinetype.GPUModeIntegrated:
				rung.GPUType, rung.GPUCount = mt.IntegratedGPUType, max(1, mt.IntegratedGPUCount)
			case machinetype.GPUModeAttachable:
				count, _ := machinetype.AttachableGPUCount(mt.DefaultGPUType, mt.VCPUs, mt.MaxGPUs, mt.DefaultGPUCount)
				rung.GPUType, rung.GPUCount = mt.DefaultGPUType, count
			}
		}
		judged = append(judged, LadderJudgement{MachineType: mt.ID, Rung: rung})
	}
	return judged
}

// LadderIndex reports where a machine type sits on the ladder, or -1 when it
// sits nowhere. A host whose shape left the ladder (an operator reordered it,
// a rung was disabled) walks no further: the position is derived from the
// host's own shape every time rather than remembered, so nothing outlives the
// stockout that caused it.
func LadderIndex(rungs []LadderRung, machineType string) int {
	return slices.IndexFunc(rungs, func(r LadderRung) bool {
		return r.MachineType == machineType
	})
}

// PlaceableZones narrows a ranked zone pool to the zones this rung could go
// to. An empty catalog availability list means the shape is offered everywhere,
// so the pool passes through unchanged.
func (r LadderRung) PlaceableZones(zonePool []string) []string {
	if len(r.Zones) == 0 {
		return zonePool
	}
	placeable := make([]string, 0, len(zonePool))
	for _, zone := range zonePool {
		if slices.Contains(r.Zones, zone) {
			placeable = append(placeable, zone)
		}
	}
	return placeable
}

// StockedOut reports whether every zone this rung could be placed in carries a
// fresh stockout observation. It answers "is there anything left to probe
// here" from memory rather than by probing.
//
// zonePool is the ranked zone set the placement plane offered for this walk. A
// rung with no zone left at all counts as stocked out: there is nowhere to send
// it either way.
//
// stocked is keyed by machine family, which is the scope GCE exhausts and the
// scope the observation is recorded at. Two rungs in one family therefore
// share one observation, which errs toward stepping past a rung that might
// have worked: a preference lost for one cooldown, never capacity lost.
func (r LadderRung) StockedOut(zonePool []string, stocked map[string]map[string]bool) bool {
	cooled := stocked[r.Family]
	for _, zone := range r.PlaceableZones(zonePool) {
		if !cooled[zone] {
			return false
		}
	}
	// Every zone this rung could have gone to was cooled, or it had none,
	// which leaves nothing to probe for the same reason.
	return true
}

// Slots is how many device slots a host built on this rung carries: the
// composed fit (computed from the machine's cores and memory, or the
// operator's per-shape override) floored at one, the same reading the host
// that eventually lands there takes.
func (r LadderRung) Slots() int64 {
	return max(r.Fit, 1)
}

// Holds reports whether this rung can carry a slot ceiling a host already
// promised. A walk may only move a host onto a shape that still holds what its
// devices were admitted against; a rung that cannot is skipped rather than
// landed on and then silently overcommitted.
//
// A catalog row that does not state its own size reports true, matching how
// every other fit judgement here treats an unmeasurable row: the row is the
// defect, and refusing every shape against it would strand the walk.
func (r LadderRung) Holds(slots int64) bool {
	if r.VCPUs <= 0 || r.MemoryGB <= 0 {
		return true
	}
	return r.Slots() >= slots
}

// LadderFamilies are the machine families a ladder's rungs belong to, in the
// operator's order and without repeats. It is the key set one stockout read
// covers, so a walk asks about eight rungs in the statement it would have spent
// on one.
func LadderFamilies(rungs []LadderRung) []string {
	families := make([]string, 0, len(rungs))
	for _, rung := range rungs {
		if !slices.Contains(families, rung.Family) {
			families = append(families, rung.Family)
		}
	}
	return families
}

// FirstProbableRung returns the index of the first rung at or after from that
// is worth a GCE insert: one that still holds slots and that memory does not
// already say is stocked out everywhere it could go.
//
// The whole walk is decided here, in one pass over rungs already resolved and
// one stockout map already read. A rung skipped now is re-earned the moment
// its observations decay, which is what keeps a transient stockout from
// demoting the fleet for a day. The walk always starts from the operator's
// preference and re-proves the demotion every time.
func FirstProbableRung(rungs []LadderRung, from int, slots int64, zonePool []string, stocked map[string]map[string]bool) (int, bool) {
	for i := max(from, 0); i < len(rungs); i++ {
		if rungs[i].Holds(slots) && !rungs[i].StockedOut(zonePool, stocked) {
			return i, true
		}
	}
	return 0, false
}

// ShapeFallbackWarning states a ladder fallback in both shapes, so a caller
// reading the resolution learns what it asked for as well as what it got.
func ShapeFallbackWarning(preferred, built string) string {
	return fmt.Sprintf("wanted %s · built %s (%s stocked out)", preferred, built, preferred)
}
