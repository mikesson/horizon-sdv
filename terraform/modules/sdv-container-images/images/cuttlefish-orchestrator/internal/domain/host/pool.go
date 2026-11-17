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

package host

import (
	"fmt"
	"slices"
)

// The pools this platform can partition its machines into.
//
// The set is closed. A pool exists because the system can route a
// create into it and buy the right machine for it, and both halves are compiled
// knowledge: a name an operator invented would be reachable only by asking for
// it, and nothing would know what a standing host in it should look like.
const (
	// PoolDefault is the pool a host belongs to when its row names none: the
	// fleet as it stood before there were several, and where every create with
	// nothing to say about capacity still lands.
	PoolDefault = "default"

	// PoolGPU holds the machines that carry an accelerator. A create whose GPU
	// requirement is HARD belongs here. A preference does not, because a
	// preference degrades onto a CPU host and is served by the default pool.
	PoolGPU = "gpu"

	// PoolARM holds arm64 bare metal. A create requiring the arm architecture
	// is routed here; whether a machine can be BUILT here is a fact about the
	// deployment (an arm64 machine family carrying an enabled host image)
	// that the derivation and the keeper are both handed rather than assume.
	PoolARM = "arm"
)

// Pools is the registry, in the order a derivation considers them.
var Pools = []string{PoolDefault, PoolGPU, PoolARM}

// IsPool reports whether a name is one this platform knows.
func IsPool(name string) bool { return slices.Contains(Pools, name) }

// PoolRefusal is why a named pool was refused. Three values rather than one
// message, because the three have different fixes and only the caller knows
// which of them they can act on.
type PoolRefusal string

// The refusals.
const (
	// RefusalUnknown: no pool of that name exists on this platform.
	RefusalUnknown PoolRefusal = "unknown"
	// RefusalNotEnabled: a real pool no operator has switched on here.
	RefusalNotEnabled PoolRefusal = "not_enabled"
	// RefusalUnbuildable: a pool switched on that no machine can be built for.
	RefusalUnbuildable PoolRefusal = "unbuildable"
	// RefusalArchitecture: the create REQUIRES an architecture whose pool this
	// deployment cannot serve. Its own refusal because the remedy differs: the
	// caller stated a fact about the guest, not a pool preference to drop.
	RefusalArchitecture PoolRefusal = "architecture"
)

// UnavailablePoolError refuses a create that named a pool this deployment will
// not place it in. It has no sentinel: web.MapError matches it structurally,
// the same way it handles ReservedNameError.
type UnavailablePoolError struct {
	Pool    string
	Refusal PoolRefusal
}

func (e *UnavailablePoolError) Error() string {
	switch e.Refusal {
	case RefusalNotEnabled:
		return fmt.Sprintf("pool %q is not enabled on this deployment: enable it in the fleet settings, or leave requirements.pool unset", e.Pool)
	case RefusalUnbuildable:
		return fmt.Sprintf("pool %q holds machines this deployment cannot build: no arm64 machine family carries an enabled host image, so nothing would ever stand in it", e.Pool)
	case RefusalArchitecture:
		return fmt.Sprintf("requirements.architecture %q needs the %s pool, which this deployment cannot serve: enable the pool and give an arm64 machine family an enabled host image", "arm", e.Pool)
	default:
		return fmt.Sprintf("pool %q does not exist: this platform partitions its machines into %v", e.Pool, Pools)
	}
}

// DerivePool decides which subset of the fleet a create's host belongs to.
//
// The ONE derivation, and it has to be: a create classified one way while it is
// admitted and another way while it is placed would provision out of one pool
// and have its demand counted against another, so neither pool's numbers would
// describe what happened.
//
// Order: the pool the create named, then its architecture, then its hard GPU
// requirement, then the default. There is no selector language beside these,
// and no term reads a machine type. The create already states what it needs in
// typed fields, and a pool read back off the shape stops identifying the pool
// the moment two pools share one.
//
// The architecture term never falls through. An arm64 guest runs on an arm64
// host and nowhere else, so a deployment that cannot serve the arm pool refuses
// the create in words. Silently landing it on an x86 machine would place a
// guest on hardware that cannot run it.
//
// A derived term only fires while its pool is enabled here, which is what keeps
// a deployment running one pool answering every create exactly as it did before
// there were several. A NAMED pool is refused instead of falling through: the
// caller asked for something this deployment does not offer, and silently
// serving them another pool's machines would be the wrong answer reported as
// success.
//
// A pool no machine can be built for is refused for the same reason it is never
// bought for. Routing a create there would provision whatever shape the resolver
// could reach and stamp this pool's name on it, leaving a machine in a pool it
// does not belong to, counted as that pool's standing capacity, answering
// creates it cannot serve.
//
// enabled is the pools an operator has switched on. The default pool is not in
// anyone's gift (it is where every machine naming none belongs), so it counts
// as enabled whether or not it is listed. armReady is whether this deployment
// can build an arm64 host at all: an arm64 machine family carrying an enabled
// host image.
func DerivePool(named string, armWanted, gpuRequired bool, enabled []string, armReady bool) (string, error) {
	on := func(pool string) bool {
		return pool == PoolDefault || slices.Contains(enabled, pool)
	}
	if named != "" {
		switch {
		case !IsPool(named):
			return "", &UnavailablePoolError{Pool: named, Refusal: RefusalUnknown}
		case !on(named):
			return "", &UnavailablePoolError{Pool: named, Refusal: RefusalNotEnabled}
		case !PoolProvisionable(named, armReady):
			return "", &UnavailablePoolError{Pool: named, Refusal: RefusalUnbuildable}
		}
		if armWanted && named != PoolARM {
			return "", &UnavailablePoolError{Pool: PoolARM, Refusal: RefusalArchitecture}
		}
		return named, nil
	}
	if armWanted {
		if !on(PoolARM) || !PoolProvisionable(PoolARM, armReady) {
			return "", &UnavailablePoolError{Pool: PoolARM, Refusal: RefusalArchitecture}
		}
		return PoolARM, nil
	}
	if gpuRequired && on(PoolGPU) {
		return PoolGPU, nil
	}
	return PoolDefault, nil
}

// PoolRequiresGPU reports whether every machine in a pool carries an
// accelerator. It is what turns the pool into the GPU fence: a create routed
// here is placed on this pool's hosts and provisioned from this pool's shapes,
// so nothing else has to re-derive "was a GPU wanted" downstream.
func PoolRequiresGPU(pool string) bool { return pool == PoolGPU }

// PoolProvisionable reports whether a machine can be built for a pool: asked by
// the keeper before it spends on one, and by the derivation above before it
// routes a create into one.
//
// A pool nothing can be built for must not be spent against: the buy would land
// somewhere else, the gap that bought it would stand unchanged next cycle, and
// the fleet would create and reclaim real VMs forever with nothing reporting it.
// The arm pool turns on a deployment fact (an arm64 machine family carrying an
// enabled host image) which the caller reads and passes in; the x86 image the
// other two pools boot from is what the platform has always shipped with.
func PoolProvisionable(pool string, armReady bool) bool {
	return pool == PoolDefault || pool == PoolGPU || (pool == PoolARM && armReady)
}

// NormalizePool is the rule every read of the column applies. An empty name
// (the NULL a row written before there were pools carries, or one a create site
// failed to stamp) is the default pool rather than a host in no pool at all,
// so a missed stamp costs the fleet nothing and needs no backfill.
func NormalizePool(stored string) string {
	if stored == "" {
		return PoolDefault
	}
	return stored
}

// StoredPool is what a write puts in the column, and the exact complement of
// NormalizePool: the default pool stores as the empty name the row model writes
// as NULL.
//
// Without the complement a read normalizes NULL to the default pool's name and
// the next write of any unrelated column stores that name back, so a column
// stamped at create and never rewritten is in fact rewritten by the first
// reconciler pass, and every row the column predates is backfilled by it.
func StoredPool(pool string) string {
	if pool == PoolDefault {
		return ""
	}
	return pool
}
