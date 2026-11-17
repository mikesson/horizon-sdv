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

package host_test

import (
	"errors"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// With only the default pool enabled and no create naming one, every derivation
// lands in the default pool, which is the fleet as it stood before pools were
// selectable, down to the shape a create provisions and the hosts it may be
// placed on.
//
// This is the row that keeps the whole feature inert until an operator switches
// a pool on. A derivation that read the GPU requirement without asking whether
// the pool exists would route a create into a pool with no bounds, no machines
// and nothing that can buy one, and refuse it, or strand it, on a deployment
// that had changed nothing.
func TestDerivePoolIsInertUntilAPoolIsEnabled(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		gpuRequired bool
	}{
		{"an ordinary create", false},
		{"a create that requires a GPU", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pool, err := host.DerivePool("", false, tc.gpuRequired, nil, false)
			if err != nil {
				t.Fatalf("DerivePool: %v", err)
			}
			if pool != host.PoolDefault {
				t.Errorf("pool = %q, want %q", pool, host.PoolDefault)
			}
		})
	}
}

// The order, each term proved by the one below it not firing.
func TestDerivePoolFollowsItsOrder(t *testing.T) {
	t.Parallel()
	enabled := []string{host.PoolDefault, host.PoolGPU, host.PoolARM}
	for _, tc := range []struct {
		name        string
		named       string
		gpuRequired bool
		want        string
	}{
		// Naming the default pool under a hard GPU requirement is the order
		// itself: the name wins and the pool the requirement would have taken is
		// not taken.
		{"a named pool outranks the requirements", host.PoolDefault, true, host.PoolDefault},
		{"a hard GPU requirement routes to the gpu pool", "", true, host.PoolGPU},
		{"a create with nothing to say lands in the default pool", "", false, host.PoolDefault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pool, err := host.DerivePool(tc.named, false, tc.gpuRequired, enabled, false)
			if err != nil {
				t.Fatalf("DerivePool: %v", err)
			}
			if pool != tc.want {
				t.Errorf("pool = %q, want %q", pool, tc.want)
			}
		})
	}
}

// A NAMED pool is refused rather than fallen back from, and the refusal names
// the pool. Falling back would serve the caller another pool's machines and
// report it as success, which is the wrong answer, delivered silently, to a
// caller who asked for particular capacity.
//
// The three refusals are separate because their fixes are: a name nothing will
// ever accept, a pool this deployment has not switched on, and a pool switched
// on that no machine can be built for.
//
// The last one is the pool that would otherwise be served silently and wrongly:
// nothing refuses it downstream, so the create would provision whatever shape
// the resolver could reach, stamp this pool's name on it, and stand a machine in
// a pool that can never buy one.
func TestDerivePoolRefusesAPoolItCannotServe(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		named   string
		enabled []string
		refusal host.PoolRefusal
	}{
		{"a pool this platform does not have", "experiment", []string{host.PoolGPU}, host.RefusalUnknown},
		{"a pool this deployment has not enabled", host.PoolGPU, nil, host.RefusalNotEnabled},
		{"a pool switched on that nothing can be built for", host.PoolARM, []string{host.PoolARM}, host.RefusalUnbuildable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := host.DerivePool(tc.named, false, false, tc.enabled, false)
			refusal, ok := errors.AsType[*host.UnavailablePoolError](err)
			if !ok {
				t.Fatalf("error = %v, want an *UnavailablePoolError so the transport can answer 400", err)
			}
			if refusal.Pool != tc.named {
				t.Errorf("refused pool = %q, want %q; the caller has to be told which name failed", refusal.Pool, tc.named)
			}
			if refusal.Refusal != tc.refusal {
				t.Errorf("refusal = %q, want %q: the three refusals have different fixes", refusal.Refusal, tc.refusal)
			}
		})
	}
}

// The GPU fence and the buy filter are properties of the pool, because every
// consumer downstream reads them from the pool. Placement fences on the pool,
// provisioning shapes on it, and the keeper spends on it.
func TestPoolPropertiesAreTheOnesEveryPlaneReads(t *testing.T) {
	t.Parallel()
	if !host.PoolRequiresGPU(host.PoolGPU) {
		t.Error("the gpu pool must require a GPU, or nothing fences its creates onto accelerated machines")
	}
	for _, pool := range []string{host.PoolDefault, host.PoolARM} {
		if host.PoolRequiresGPU(pool) {
			t.Errorf("%s must not require a GPU", pool)
		}
	}
	if !host.PoolProvisionable(host.PoolDefault, false) || !host.PoolProvisionable(host.PoolGPU, false) {
		t.Error("the keeper must be able to buy for both pools a machine exists for")
	}
	if host.PoolProvisionable(host.PoolARM, false) {
		t.Error("without an arm64 host image, buying for the arm pool would create and reclaim VMs forever")
	}
	if !host.PoolProvisionable(host.PoolARM, true) {
		t.Error("with an arm64 host image enabled, the arm pool is a pool machines can be bought for")
	}
}

// The architecture term is a hard constraint that never falls through: an
// arm64 guest runs only on an arm64 host, so a deployment that cannot serve
// the arm pool refuses the create in words instead of landing it on x86.
func TestDerivePoolRoutesAndRefusesTheArmArchitecture(t *testing.T) {
	t.Parallel()
	armOn := []string{host.PoolARM}
	pool, err := host.DerivePool("", true, false, armOn, true)
	if err != nil {
		t.Fatalf("DerivePool: %v", err)
	}
	if pool != host.PoolARM {
		t.Errorf("pool = %q, want %q; an arm64 guest belongs to the arm pool", pool, host.PoolARM)
	}

	for _, tc := range []struct {
		name     string
		enabled  []string
		armReady bool
	}{
		{"the arm pool is not enabled", nil, true},
		{"no arm64 host image exists", armOn, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := host.DerivePool("", true, false, tc.enabled, tc.armReady)
			refusal, ok := errors.AsType[*host.UnavailablePoolError](err)
			if !ok {
				t.Fatalf("error = %v, want an *UnavailablePoolError so the transport can answer 400", err)
			}
			if refusal.Refusal != host.RefusalArchitecture {
				t.Errorf("refusal = %q, want %q; the caller stated a guest fact, not a pool preference", refusal.Refusal, host.RefusalArchitecture)
			}
		})
	}

	// A named pool that contradicts the architecture is refused too: the guest
	// cannot run there, and honoring the name would just fail later and worse.
	if _, err := host.DerivePool(host.PoolDefault, true, false, armOn, true); err == nil {
		t.Error("naming an x86 pool for an arm64 guest must be refused, not honored")
	}
}
