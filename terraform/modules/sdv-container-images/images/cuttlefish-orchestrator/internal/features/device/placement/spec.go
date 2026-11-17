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

package placement

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// WorkloadClass selects the Spot min-uptime policy a resolution draws:
// batch members demand the hour tier by default (a CI shard dying at minute
// 9 of 10 wastes the whole run), singles the 10-minute tier.
type WorkloadClass int

// Workload classes.
const (
	WorkloadSingle WorkloadClass = iota
	WorkloadBatch
)

// spotUptimePolicy names the min-uptime policy key and default for a
// workload class.
type spotUptimePolicy struct {
	key            string
	defaultSeconds int64
}

// uptimePolicyFor maps a workload class to its Spot min-uptime policy.
func uptimePolicyFor(class WorkloadClass) spotUptimePolicy {
	if class == WorkloadBatch {
		return spotUptimePolicy{models.ConfigKeySpotMinUptimeBatch, 3600}
	}
	return spotUptimePolicy{models.ConfigKeySpotMinUptimeSingle, 600}
}

// WantsGPU reports whether a create should be offered GPU capacity: the pool it
// was routed into, interactive intent, or an explicit requirement, unless the
// pinned rendering mode fences it off GPU hosts
// (device.GPUMode.RequiresCPUHost).
//
// The fence cancels the want, so it never counts as degraded. A fenced create on
// a CPU host got what it asked for, so reporting it as a GPU shortfall would
// both warn about nothing and skew the ratio that decides whether GPU capacity
// is worth standing up.
//
// Placement and provisioning both ask this, and they have to agree: a device
// fenced off existing GPU hosts must not have a GPU host provisioned for it.
func WantsGPU(req device.Requirements, gpuMode device.GPUMode, pool string) bool {
	return !gpuMode.RequiresCPUHost() &&
		(host.PoolRequiresGPU(pool) || req.Interaction == device.InteractionInteractive || req.GPURequired)
}

// RequiresGPU is the HARD half of the same question: a create that must have an
// accelerator or fail, rather than one that prefers one and degrades.
//
// The pool counts here as well as in the preference above, and that is what the
// fence is now made of. A create routed into a pool whose machines all carry an
// accelerator is asking for one whether or not it said so. An operator who
// named the pool asked for its capacity, not for whatever the default pool had
// spare.
func RequiresGPU(req device.Requirements, pool string) bool {
	return req.GPURequired || host.PoolRequiresGPU(pool)
}

// ResolvePool decides which subset of the fleet a create belongs to, and is the
// single evaluation of that decision: admission takes the answer, and so does
// every placement and provisioning call the create then makes. Two evaluations
// could disagree, with a create placed against one pool's hosts and counted as
// the other's demand, and neither pool's numbers would then describe what
// happened.
//
// ONE policy read whatever the fleet holds: the pools an operator switched on
// are the keys of a single map, so the question costs the same at three pools as
// at one. A map that will not parse reads as no pool beyond the default, which
// is the fleet this deployment ran before pools existed and the answer that
// changes nothing.
func (r *Resolver) ResolvePool(ctx context.Context, req device.Requirements, gpuMode device.GPUMode) (string, error) {
	raw, err := r.policyValue(ctx, models.ConfigKeyFleetPoolBounds)
	if err != nil {
		return "", err
	}
	bounds, ok := hostconfig.ParsePoolBounds(raw)
	if !ok {
		r.logger.Warn("the stored pool bounds are unreadable; every create is routed to the default pool",
			slog.String("key", models.ConfigKeyFleetPoolBounds))
	}
	enabled := make([]string, 0, len(bounds))
	for name := range bounds {
		enabled = append(enabled, name)
	}
	// Returned unwrapped so *host.UnavailablePoolError reaches the transport as
	// a 400 naming the pool. The arm-readiness read costs nothing while no term
	// asks for arm, which is every create this deployment has ever seen.
	armWanted := req.Architecture == device.ArchitectureARM
	armReady := false
	if armWanted || req.Pool == host.PoolARM {
		armReady = r.arm != nil && r.arm.Ready(ctx)
	}
	pool, err := host.DerivePool(req.Pool, armWanted, req.GPURequired, enabled, armReady)
	if err != nil {
		return "", err
	}
	// The one conflict the requirement rules cannot see: a pool whose machines
	// all carry an accelerator, asked for by name, under a rendering mode whose
	// guarantee depends on the host having none. Refused rather than ranked, for
	// the reason gpu_required and that mode are refused together: an explicit
	// flag the platform cannot honor is never resolved without a word.
	if host.PoolRequiresGPU(pool) && gpuMode.RequiresCPUHost() {
		return "", &device.ValidationError{Message: fmt.Sprintf(
			"requirements.pool %q holds GPU machines, which gpu_mode %q (software rendering) fences this device off: drop one",
			pool, gpuMode)}
	}
	return pool, nil
}

// ProvisionSpec builds the host request for the auto-provision branch: the
// machine shape from operator policy, the provisioning model from the capacity
// intent, and the zone from capacity-ranked placement (biased by the location
// preference; proximity dominates for interactive intent). The chosen zone is
// a pre-ranked binding, never a pin, so provisioning may still fail over to
// another ranked zone.
//
// gpuMode is the create's pinned rendering mode, which the placement fence and
// this branch have to agree on: a mode that requires a CPU host must not have
// one provisioned with an accelerator either.
//
// pool is the subset of the fleet this host joins, decided once by ResolvePool
// and passed in rather than derived again here. A create classified one way
// while it was admitted and another way while it is provisioned would buy out of
// one pool and be counted against the other. It is a parameter and not a
// requirements field for the reason origin is one on the host create: a call
// site that does not choose a pool does not compile.
func (r *Resolver) ProvisionSpec(ctx context.Context, req device.Requirements, gpuMode device.GPUMode, class WorkloadClass, shape GroupShape, pool string) (*host.SpecResolution, error) {
	// Resolved once, up front. provisionShape reaches the default only on the
	// CPU path, since a GPU create returns before it, and it is also what a
	// create resolving a shape its pool does not stand up is judged against. A
	// catalog with no default cannot answer either question, and cannot serve an
	// ordinary headless create either.
	defaultMT, err := r.machineTypes.GetDefault(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving the default machine type: %w", err)
	}
	// Canceling the want is also what keeps the GPU shape and the accelerator
	// attach below out of a fenced create's spec.
	gpuWanted, gpuRequired := WantsGPU(req, gpuMode, pool), RequiresGPU(req, pool)
	mt, degraded, err := r.provisionShape(ctx, gpuWanted, gpuRequired, defaultMT)
	if err != nil {
		return nil, err
	}
	var warnings []string
	if degraded {
		warnings = append(warnings, shapeDegradedWarning)
	}
	// The pool's shape ladder replaces the catalog default as the shape this
	// pool prefers, and only where the resolution landed on the pool's own
	// standing template. A GPU shape must not be swapped for a CPU pool's
	// preferred rung, nor a CPU shape for a GPU pool's: both arms are reachable
	// in either pool, since a CPU pool serves interactive creates with an
	// accelerator and a GPU want the catalog cannot meet lands on the default
	// shape.
	onPoolTemplate := (gpuWanted && !degraded) == host.PoolRequiresGPU(pool)
	ladder, err := r.shapeLadder(ctx, pool, onPoolTemplate)
	if err != nil {
		return nil, err
	}
	if len(ladder.rungs) > 0 {
		mt = ladder.rows[ladder.rungs[0].MachineType]
	}
	// The shape a STANDING host in this pool takes, which is what the demand
	// signal judges a miss against. This create can only ever be absorbed by
	// capacity standing in the pool it was routed into. Judged against the fleet
	// default instead, every miss in a pool that buys a larger machine reads as
	// beyond what standing capacity offers, and the pool's own demand could never
	// raise its own target.
	//
	// This create's own resolution IS that shape wherever the two agree, which is
	// every create but one: a GPU want inside a CPU pool resolves an accelerated
	// shape that pool never stands up, and there the fleet default is what a
	// standby host offers, and it is also what such a create degrades onto.
	standing := defaultMT
	if onPoolTemplate {
		standing = mt
	}

	in, err := r.shapeInputsFor(ctx, req, class, shape, standing,
		gpuWant{attach: gpuWanted && !degraded, required: gpuRequired})
	if err != nil {
		return nil, err
	}
	bound, err := r.bindShape(ctx, mt, in)
	if err != nil {
		return nil, err
	}
	bound, preferredType, err := r.walkLadder(ctx, ladder, bound, in)
	if err != nil {
		return nil, err
	}
	if preferredType != "" {
		warnings = append(warnings, hostconfig.ShapeFallbackWarning(preferredType, bound.mt.ID))
	}
	warnings = append(warnings, bound.warnings...)

	spec := &host.CreateRequest{
		Name:        "auto",
		MachineType: bound.mt.ID,
		MaxDevices:  bound.maxDevices,
		Zone:        bound.zone.Zone,
		// The pool the create was judged into, carried to the row rather than
		// re-derived at the create: the shape, the ladder and the demand this
		// host answers were all decided against this pool, and a second
		// derivation could put the machine in another one.
		Pool: pool,
		// Everything the fleet's demand signal needs about this create, frozen
		// now. Only this branch fills it: the resolver is the one place that
		// knows all three, and a create that reached the host service by any
		// other route was judged by nobody.
		Resolved: &host.ResolvedFacts{
			Density: bound.density,
			// The pool counts as a hard requirement here, because it is one: a
			// create routed into a pool whose machines all carry an accelerator
			// cannot be served by a standby host that does not, whether it said
			// so itself or was put there by name.
			GPURequired:     gpuRequired,
			FitsDefaultHost: fitsShape(standing, shape),
			PreferredType:   preferredType,
		},
		// A group gets the host to itself. Its instances share the host's
		// netsim and rootcanal medium, so "the devices that can see each other"
		// and "the devices on this host" have to be the same set. A stranger's
		// device landing here would join their radio medium.
		//
		// Never ForeignCVDs: that flag exempts a host from the orphan-CVD
		// census, and the census is what notices these CVDs going missing. It
		// belongs to hosts whose CVDs arrive with no device row, which is the
		// opposite of this one.
		Exclusive:         shape.Instances > 1,
		ProvisioningModel: bound.model,
	}
	spec.GPUType, spec.GPUCount = bound.gpuType, bound.gpuCount
	if req.Location != nil {
		spec.LocationHint = req.Location.PreferredRegion
	}
	return &host.SpecResolution{
		Spec:             spec,
		Warnings:         warnings,
		PlacementReasons: bound.zone.Reasons,
		GPUDegraded:      degraded,
	}, nil
}

// boundShape is one machine type carried all the way to a zone: grown to hold
// the group, sized, and ranked. The ladder binds twice at most, once for the
// preferred rung and once for the rung it stepped to, so the work is a
// function rather than a stretch of ProvisionSpec that would have to be read
// twice to see it is the same work.
type boundShape struct {
	mt         *machinetype.MachineType
	maxDevices int64
	density    int64
	model      host.ProvisioningModel
	zone       gcecatalog.ZoneCandidate
	// pool is every zone ranking offered this shape, winner first. The ladder
	// judges its rungs against it: "is there anywhere left to probe" is asked
	// of the zones placement would actually have used, never of the catalog
	// at large.
	pool     []string
	warnings []string

	// gpuType and gpuCount are the accelerator this shape attaches, decided
	// once so ranking and the insert ask for the same machine: a zone that has
	// the machine type and not the GPU is not a zone this create can go to.
	gpuType  string
	gpuCount int64
}

// shapeInputs are everything a create fixes before any machine type is chosen:
// the template every capacity judgement is made against, the group being
// placed, the capacity intent, and the ranking options. The shape ladder binds
// twice at most, and it must bind the second shape from exactly these. A
// second resolution that re-derived any of them could answer differently and
// nothing would say so.
type shapeInputs struct {
	// standingMT is the shape a standing host in this create's pool takes. It
	// does not move with the ladder walk: what a rung the create stepped to
	// holds is the create's own ceiling, while this stays the buffer the miss
	// would have been absorbed by.
	standingMT *machinetype.MachineType
	shape      GroupShape
	capacity   device.CapacityModel
	// gpu is the create's already-fenced accelerator want, carried rather than
	// re-derived so the fence is decided in exactly one place.
	gpu   gpuWant
	model host.ProvisioningModel
	opts  gcecatalog.RankOptions
}

// gpuWant is what a create asks of an accelerator once the rendering fence has
// been applied: whether a bound shape attaches one, and whether a shape that
// cannot carry one refuses the create or provisions without it.
type gpuWant struct {
	attach   bool
	required bool
}

// shapeInputsFor resolves the capacity intent and the ranking options a create
// binds every candidate shape with.
func (r *Resolver) shapeInputsFor(
	ctx context.Context,
	req device.Requirements,
	class WorkloadClass,
	shape GroupShape,
	standingMT *machinetype.MachineType,
	gpu gpuWant,
) (shapeInputs, error) {
	model, err := r.capacityModel(ctx, req)
	if err != nil {
		return shapeInputs{}, err
	}
	opts := gcecatalog.RankOptions{}
	if req.Location != nil {
		opts.PreferredRegion = req.Location.PreferredRegion
		opts.GeoDominant = req.Interaction == device.InteractionInteractive
	}
	if model.IsSpot() {
		if opts.SpotMinUptimeSeconds, err = r.spotMinUptime(ctx, uptimePolicyFor(class)); err != nil {
			return shapeInputs{}, err
		}
	}
	return shapeInputs{
		standingMT: standingMT, shape: shape, capacity: req.Capacity,
		gpu: gpu, model: model, opts: opts,
	}, nil
}

// bindShape completes one machine type into a placeable shape.
func (r *Resolver) bindShape(ctx context.Context, mt *machinetype.MachineType, in shapeInputs) (boundShape, error) {
	grown, sizeWarnings, err := r.growShapeToFit(ctx, mt, in.shape)
	if err != nil {
		return boundShape{}, err
	}
	maxDevices, density := r.hostCapacityFor(in.shape.Instances, grown, in.standingMT)
	bound := boundShape{mt: grown, maxDevices: maxDevices, density: density, model: in.model}
	// GCE sells bare metal on demand and never as Spot. A create that REQUIRED
	// Spot is refused in words, because billing it on demand without saying so
	// would be the wrong answer reported as a discount. A policy-resolved Spot
	// lands on the model the hardware actually offers.
	if grown.IsBareMetal() && bound.model == host.ProvisioningModelSpot {
		if in.capacity == device.CapacitySpot {
			return boundShape{}, &device.ValidationError{Message: fmt.Sprintf(
				"requirements.capacity %q cannot be honored on %q: GCE never offers bare metal as Spot",
				device.CapacitySpot, grown.ID)}
		}
		bound.model = host.ProvisioningModelStandard
	}
	if in.gpu.attach && grown.GPUMode == machinetype.GPUModeAttachable && grown.DefaultGPUType != "" {
		// The width decides the count: one card serves a bounded number of
		// vCPUs, and an insert under that ratio is refused by Compute Engine.
		count, ok := machinetype.AttachableGPUCount(grown.DefaultGPUType, grown.VCPUs, grown.MaxGPUs, grown.DefaultGPUCount)
		switch {
		case ok:
			bound.gpuType = grown.DefaultGPUType
			bound.gpuCount = count
		case in.gpu.required:
			// Attaching nothing here builds a host with no accelerator, which is
			// the machine the ladder refuses from a GPU pool for the same reason.
			// A create that must have one is refused with a validation error.
			return boundShape{}, &device.ValidationError{Message: fmt.Sprintf(
				"gpu is required but %q cannot carry %s: its %d vCPUs exceed what max_gpus (%d) of that card can serve",
				grown.ID, grown.DefaultGPUType, grown.VCPUs, max(grown.MaxGPUs, 1))}
		}
	}
	fallbackWarnings, err := r.rankCapacityZone(ctx, &bound, in.capacity, in.opts)
	if err != nil {
		return boundShape{}, err
	}
	bound.warnings = sizeWarnings
	bound.warnings = append(bound.warnings, fallbackWarnings...)
	return bound, nil
}

// walkLadder is the ladder's one consult of the stockout memory: it reports the
// shape the create should actually be built from, and the shape it preferred
// when those differ.
//
// It runs only where there is somewhere to step. A rung the memory already says
// is exhausted everywhere it could go is skipped rather than probed. The GCE
// insert would be refused, the create would wait out a redelivery, and nothing
// would have been learned that was not already known.
func (r *Resolver) walkLadder(ctx context.Context, ladder resolvedLadder, bound boundShape, in shapeInputs) (boundShape, string, error) {
	if len(ladder.rungs) < 2 {
		return bound, "", nil
	}
	next, stepped := r.stepLadder(ctx, ladder, 0, bound.maxDevices, bound.pool)
	if !stepped {
		return bound, "", nil
	}
	preferredType := bound.mt.ID
	rebound, err := r.bindShape(ctx, ladder.rows[ladder.rungs[next].MachineType], in)
	if err != nil {
		return boundShape{}, "", err
	}
	return rebound, preferredType, nil
}

// rankCapacityZone ranks placement zones for the bound shape's provisioning
// model and returns the winner beside every zone that was offered. An
// auto-resolved Spot intent with no viable Spot zone degrades to on-demand
// capacity with a warning (the GPU stock-out posture), mutating the bound
// shape's model; explicit spot fails outright.
//
// The whole ranked set comes back because the shape ladder asks a question of
// it that the winner cannot answer: whether there is anywhere left to probe
// for this shape at all. Ranking never returns an empty set on cooldowns
// alone, since a cooled-everywhere shape still ranks so that cooldowns can
// never make the fleet unprovisionable. "Nowhere left" is therefore a
// judgement about these zones, made against the stockout memory, and it needs
// the zones.
func (r *Resolver) rankCapacityZone(ctx context.Context, bound *boundShape, capacity device.CapacityModel, opts gcecatalog.RankOptions) ([]string, error) {
	vreq := gcecatalog.ValidationRequest{
		MachineType:       bound.mt.ID,
		GPUType:           bound.gpuType,
		GPUCount:          bound.gpuCount,
		ProvisioningModel: string(bound.model),
	}
	candidates, err := r.ranker.RankZones(ctx, vreq, opts)
	if err != nil {
		return nil, fmt.Errorf("ranking placement zones: %w", err)
	}
	if len(candidates) > 0 {
		bound.bindZones(candidates)
		return nil, nil
	}
	if bound.model.IsSpot() && capacity != device.CapacitySpot {
		vreq.ProvisioningModel = string(host.ProvisioningModelStandard)
		candidates, err = r.ranker.RankZones(ctx, vreq, opts)
		if err != nil {
			return nil, fmt.Errorf("ranking standard-capacity fallback zones: %w", err)
		}
		if len(candidates) > 0 {
			bound.model = host.ProvisioningModelStandard
			bound.bindZones(candidates)
			return []string{"no viable zone for Spot capacity; provisioning standard (on-demand) capacity"}, nil
		}
	}
	if bound.model.IsSpot() {
		return nil, &host.CapacityExceededError{
			Message: "no viable zone for Spot capacity for the requested shape: enable a zone with Spot capacity or request standard capacity",
		}
	}
	return nil, &host.CapacityExceededError{
		Message: "no viable zone for the requested shape: enable a zone with capacity in the catalog",
	}
}

// bindZones records the ranking's verdict on this shape: the zone it goes to,
// and every zone it was offered.
func (b *boundShape) bindZones(candidates []gcecatalog.ZoneCandidate) {
	b.zone = candidates[0]
	b.pool = make([]string, len(candidates))
	for i, c := range candidates {
		b.pool[i] = c.Zone
	}
}

// growShapeToFit returns the machine type the host is actually provisioned as:
// the resolved shape when it already holds the group, else the smallest enabled
// type in the SAME FAMILY that does.
//
// Staying in the family is what makes this a shape change and nothing else. The
// host image is chosen per machine family (host_image_configs), the GPU mode and
// the required boot-disk type are family properties, and the zone ranking runs
// afterwards against whatever this returns, so growing n1-standard-8 into
// n1-standard-16 changes the size of the host and no other fact about it.
//
// It only ever grows. A create whose instances declare very little must not
// silently shrink the host the operator's catalog default promises, because
// that default is also the shape every capacity and image assumption was set
// against.
//
// A group larger than the family's biggest enabled type still provisions, on
// that type, with a warning naming the shortfall. Refusing would strand a create
// on a catalog the operator can fix in a click, while the warning says plainly
// that the host is oversubscribed, which is the fact the caller needs and did
// not have before.
func (r *Resolver) growShapeToFit(ctx context.Context, mt *machinetype.MachineType, shape GroupShape) (*machinetype.MachineType, []string, error) {
	if fitsShape(mt, shape) {
		return mt, nil, nil
	}
	vcpus, memoryGB := shape.VCPUs, shape.hostMemoryGB()
	enabled := true
	types, err := r.machineTypes.List(ctx, machinetype.ListFilter{Family: &mt.Family, IsEnabled: &enabled})
	if err != nil {
		return nil, nil, fmt.Errorf("listing %s machine types to size a %d-instance group: %w",
			mt.Family, shape.Instances, err)
	}
	// Never below the resolved default in either dimension, whichever of the two
	// demands drove the growth.
	minVCPUs, minMemoryGB := max(vcpus, mt.VCPUs), max(memoryGB, mt.MemoryGB)
	fits := make([]*machinetype.MachineType, 0, len(types))
	for _, t := range types {
		if t.VCPUs >= minVCPUs && t.MemoryGB >= minMemoryGB {
			fits = append(fits, t)
		}
	}
	if len(fits) == 0 {
		return mt, []string{fmt.Sprintf(
			"no enabled %s machine type holds %d instances (%d GB of declared guest RAM plus host reserve); "+
				"provisioning %s, whose %d GB is oversubscribed",
			mt.Family, shape.Instances, memoryGB, mt.ID, mt.MemoryGB)}, nil
	}
	slices.SortFunc(fits, func(a, b *machinetype.MachineType) int {
		if c := cmp.Compare(a.MemoryGB, b.MemoryGB); c != 0 {
			return c
		}
		return cmp.Compare(a.VCPUs, b.VCPUs)
	})
	return fits[0], nil, nil
}

// fitsShape reports whether mt holds this whole group as declared: wide enough
// for its widest guest, and large enough for their summed RAM plus the host's
// own reserve.
//
// A catalog row that does not state its own size cannot be compared against
// anything, and every shape would read as too small against it, so it reports a
// fit. That is the fail-safe direction on both of its callers: growth provisions
// the row as resolved rather than growing every create off a broken row, and the
// stamped verdict says "a standby host could have held this", which keeps an
// unjudgeable miss counted as demand instead of silently discarding it.
func fitsShape(mt *machinetype.MachineType, shape GroupShape) bool {
	if mt.VCPUs <= 0 || mt.MemoryGB <= 0 {
		return true
	}
	return mt.VCPUs >= shape.VCPUs && mt.MemoryGB >= shape.hostMemoryGB()
}

// slotCeiling is how many instance slots a create of this shape takes on mt:
// what the machine is packed to, raised to the instance count the create has to
// fit, and capped at the per-host ceiling.
//
// The fit bounds the CAPACITY TERM ALONE, never the group's own demand, which
// is why max sits outside it. Bounding the group would refuse it from
// the very host provisioned for it, and that case is reachable:
// growShapeToFit's no-fit path returns the family's largest type with an
// oversubscription warning, and there the fit is below the instance count by
// construction. The warning is the accurate answer there; a refusal is not.
//
// The composed Fit is the capacity, either the cores-and-memory arithmetic or
// the operator's per-shape override, so a row the operator resized carries their
// number here too; PackedSlots re-derives only where composition served none.
func slotCeiling(mt *machinetype.MachineType, instances int64) int64 {
	fit := mt.Fit
	if fit <= 0 && !mt.FitOverridden {
		fit = device.PackedSlots(mt.VCPUs, mt.MemoryGB)
	}
	return min(max(fit, instances, 1), host.MaxDevicesPerHost)
}

// hostCapacityFor sizes the new host's slot ceiling and reports what a STANDBY
// host would have offered. Two applications of one rule: they are different
// numbers, and expressing the second any other way lets them drift.
//
// The CEILING is judged against resolved, the machine actually being
// provisioned after any growth, because that is the machine whose RAM the
// guests will be mapped into.
//
// The STAMP is slotCeiling applied to the standing template at one instance,
// which is exactly what a standby host in this create's pool is. The fleet's
// demand signal compares a create's ceiling against it, so it has to describe
// the host that could have absorbed the create rather than the one this create
// provisioned: a ceiling above it is a create no standing buffer could have
// served. It is a count of slots, so it is at least 1, which is what lets a
// consumer read its absence as "nobody judged this create" rather than as a
// density of zero.
func (r *Resolver) hostCapacityFor(instances int64, resolved, standingMT *machinetype.MachineType) (int64, int64) {
	return slotCeiling(resolved, instances), slotCeiling(standingMT, 1)
}

// spotMinUptime resolves the workload class's minimum estimated-uptime tier
// from policy; an absent or malformed key reads as the compiled default.
func (r *Resolver) spotMinUptime(ctx context.Context, uptime spotUptimePolicy) (int64, error) {
	raw, err := r.policyValue(ctx, uptime.key)
	if err != nil {
		return 0, err
	}
	if seconds, perr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); perr == nil && seconds >= 0 {
		return seconds, nil
	}
	if raw != "" {
		r.logger.Warn("invalid spot min-uptime policy; using default",
			slog.String("key", uptime.key), slog.String("value", raw))
	}
	return uptime.defaultSeconds, nil
}

// capacityModel resolves the capacity intent to a provisioning model: explicit
// spot/standard verbatim; auto (or absent) asks operator policy, where
// spot.auto_default routes headless intent to Spot, while interactive always
// resolves to standard (a streaming session dying mid-debug is not worth the
// discount).
func (r *Resolver) capacityModel(ctx context.Context, req device.Requirements) (host.ProvisioningModel, error) {
	switch req.Capacity {
	case device.CapacitySpot:
		return host.ProvisioningModelSpot, nil
	case device.CapacityStandard:
		return host.ProvisioningModelStandard, nil
	case device.CapacityAuto, "":
	}
	if req.Interaction != device.InteractionHeadless {
		return host.ProvisioningModelStandard, nil
	}
	spotDefault, err := r.policyBool(ctx, models.ConfigKeySpotAutoDefault)
	if err != nil {
		return "", err
	}
	if spotDefault {
		return host.ProvisioningModelSpot, nil
	}
	return host.ProvisioningModelStandard, nil
}

// resolvedLadder is the operator's shape preference order, admitted against
// the catalog, with the rows behind it. The rows ride along because the
// listing that produced them is the ONE catalog read a resolution makes for
// the ladder: looking a rung's row up again when the walk steps to it would
// put a statement inside the loop this is built to keep out of one.
type resolvedLadder struct {
	rungs []hostconfig.LadderRung
	rows  map[string]*machinetype.MachineType
}

// shapeLadder reads the machine-shape preference order of the pool this create
// provisions into: one policy read, and one catalog listing only when that pool
// has a ladder stored.
//
// One read for every pool's ladders rather than a read for this one's, so the
// statement count stays what it was however many pools exist.
//
// An absent, empty or unreadable ladder returns nothing, and every caller
// reads that as the catalog default, which is the resolution this branch made
// before the ladder existed, unchanged down to the ranking request.
func (r *Resolver) shapeLadder(ctx context.Context, pool string, wanted bool) (resolvedLadder, error) {
	if !wanted {
		return resolvedLadder{}, nil
	}
	raw, err := r.policyValue(ctx, models.ConfigKeyFleetPoolLadders)
	if err != nil {
		return resolvedLadder{}, err
	}
	ladders, ok := hostconfig.ParsePoolLadders(raw)
	if !ok {
		r.logger.Warn("the stored shape ladders are unreadable; provisioning takes the catalog default",
			slog.String("key", models.ConfigKeyFleetPoolLadders))
		return resolvedLadder{}, nil
	}
	stored := ladders[pool]
	if len(stored) == 0 {
		return resolvedLadder{}, nil
	}
	catalog, err := r.machineTypes.List(ctx, machinetype.ListFilter{})
	if err != nil {
		return resolvedLadder{}, fmt.Errorf("listing machine types to resolve the shape ladder: %w", err)
	}
	ladder := resolvedLadder{
		rungs: hostconfig.ResolveLadder(pool, stored, catalog),
		rows:  make(map[string]*machinetype.MachineType, len(catalog)),
	}
	for _, mt := range catalog {
		ladder.rows[mt.ID] = mt
	}
	return ladder, nil
}

// stepLadder answers whether the rung at from is worth probing, and which rung
// is if it is not.
//
// ONE read of the stockout memory, covering every rung's machine family at
// once, and every skip decided from it in memory. A read per rung is the shape
// this exists to make unrepresentable: the walk is at most eight rungs and the
// question it asks of each is the same one.
func (r *Resolver) stepLadder(ctx context.Context, ladder resolvedLadder, from int, slots int64, pool []string) (int, bool) {
	if r.zoneHealth == nil {
		return from, false // no memory wired: probe the preferred shape
	}
	cells, err := r.zoneHealth.ListCapacityCells(ctx, hostconfig.LadderFamilies(ladder.rungs))
	if err != nil {
		// Advisory memory, exactly as ranking treats it: a walk that cannot
		// read what it already knows probes the preferred shape rather than
		// refusing to provision at all.
		r.logger.Warn("stockout memory unavailable; probing the preferred shape",
			slog.Any("error", err))
		return from, false
	}
	stocked := gcecatalog.StockedOutZones(cells, time.Now())
	if !ladder.rungs[from].StockedOut(pool, stocked) {
		return from, false
	}
	return hostconfig.FirstProbableRung(ladder.rungs, from+1, slots, pool, stocked)
}

// shapeDegradedWarning names this file's degradation: the intent wanted a GPU
// and the catalog has no enabled machine type that can carry one. Derived from
// the typed fact rather than written beside it, so a caller counting
// degradations and a caller reading the warning can never disagree.
const shapeDegradedWarning = "no GPU-capable machine type enabled; provisioning a CPU host (software rendering)"

// provisionShape picks the machine type for the auto-provisioned host: the
// GPU shape when the intent wants one (hard failure when required but none
// exists), else defaultMT. The second result reports that the GPU want went
// unmet. This is the second of two places that happens, placement's
// fall-through onto an existing CPU host being the other, and both have to be
// counted or "how often did we hand out software rendering" is answered from
// whichever half the catalog happens to exercise.
//
// defaultMT is passed rather than read here because the caller needs it
// whichever branch this takes.
func (r *Resolver) provisionShape(ctx context.Context, gpuWanted, gpuRequired bool, defaultMT *machinetype.MachineType) (*machinetype.MachineType, bool, error) {
	if gpuWanted {
		gpuType, found, err := r.gpuMachineType(ctx)
		if err != nil {
			return nil, false, err
		}
		if found {
			return gpuType, false, nil
		}
		if gpuRequired {
			return nil, false, &device.ValidationError{
				Message: "gpu is required but no GPU capacity exists and no GPU-capable machine type is enabled",
			}
		}
		return defaultMT, true, nil
	}
	return defaultMT, false, nil
}

// gpuMachineType resolves the shape for a GPU host: the operator override
// when it names an enabled GPU-capable type (a stale override reads as
// unset), else the first enabled GPU-capable catalog type, integrated shapes
// first (self-contained, no attach params). found=false when none is
// available.
func (r *Resolver) gpuMachineType(ctx context.Context) (*machinetype.MachineType, bool, error) {
	if id, err := r.policyValue(ctx, models.ConfigKeyDefaultGPUMachineType); err != nil {
		return nil, false, err
	} else if id != "" {
		mt, err := r.machineTypes.Get(ctx, id)
		switch {
		case err == nil && mt.IsEnabled && mt.GPUMode != machinetype.GPUModeNone:
			return mt, true, nil
		case err != nil && !errors.Is(err, machinetype.ErrNotFound):
			return nil, false, fmt.Errorf("resolving GPU machine-type policy %q: %w", id, err)
		default:
			// The catalog's own account of why where it has one, because the
			// reasons it can give need different repairs and a bare fallback
			// hides all of them.
			reason := "the catalog offers it, but disabled or with no GPU"
			if err != nil {
				reason = err.Error()
			}
			r.logger.Warn("GPU machine-type policy names an unusable type; falling back to the catalog",
				slog.String("machine_type", id), slog.String("reason", reason))
		}
	}

	enabled := true
	gpu := true
	types, err := r.machineTypes.List(ctx, machinetype.ListFilter{IsEnabled: &enabled, GPUSupport: &gpu})
	if err != nil {
		return nil, false, fmt.Errorf("listing GPU-capable machine types: %w", err)
	}
	if len(types) == 0 {
		return nil, false, nil
	}
	slices.SortFunc(types, func(a, b *machinetype.MachineType) int {
		if a.GPUMode != b.GPUMode {
			if a.GPUMode == machinetype.GPUModeIntegrated {
				return -1
			}
			if b.GPUMode == machinetype.GPUModeIntegrated {
				return 1
			}
		}
		if c := cmp.Compare(a.SortOrder, b.SortOrder); c != 0 {
			return c
		}
		// Every shape of a family shares its display order, and the sort is not
		// stable, so without a total order two resolutions of the same catalog
		// could provision different GPU machines.
		return cmp.Compare(a.ID, b.ID)
	})
	return types[0], true, nil
}

// CapacityModelName renders a provisioning model in the resolution's
// requirement vocabulary ("spot"/"standard", because the resolution answers
// the requirements and not the GCE API).
func CapacityModelName(m host.ProvisioningModel) string {
	if m.IsSpot() {
		return string(device.CapacitySpot)
	}
	return string(device.CapacityStandard)
}
