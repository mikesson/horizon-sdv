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
	"maps"
	"slices"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/iterator"
)

// MachineTypeScope is one scope of an aggregated machine-type list: the key
// Compute Engine returned ("zones/us-central1-a") and the scoped list under
// it, warning included. The warning is what separates "this zone offers none
// of the shapes" from "this zone could not be read", so it travels with the
// list rather than being flattened away here.
type MachineTypeScope struct {
	Scope string
	List  *computepb.MachineTypesScopedList
}

// AcceleratorScope is the accelerator list's equivalent of MachineTypeScope.
type AcceleratorScope struct {
	Scope string
	List  *computepb.AcceleratorTypesScopedList
}

// AggregatedList is one aggregated response: the scopes it reported and the
// scopes Compute Engine says it could not reach.
type AggregatedList[T any] struct {
	Scopes []T
	// Unreachable holds the scope keys the API reported as unreachable under
	// partial success. They are scopes with no answer, never scopes with an
	// empty answer.
	Unreachable []string
}

// ComputeCatalog is the Compute Engine read surface a Catalog snapshot is
// built from. It takes the request messages whole so the server-side filter
// and the partial-success flag are part of the contract a fake can assert on,
// and it returns drained pages because the generated iterators cannot be
// constructed outside their own package.
type ComputeCatalog interface {
	// MachineTypes answers an aggregated machine-type list.
	MachineTypes(ctx context.Context, req *computepb.AggregatedListMachineTypesRequest) (AggregatedList[MachineTypeScope], error)

	// MachineType answers one machine type in one zone. It is the only call
	// that can describe a custom shape: those are absent from every list and
	// synthesized on demand, so this both proves the name is one Compute
	// Engine will build and states how big the machine is.
	MachineType(ctx context.Context, req *computepb.GetMachineTypeRequest) (*computepb.MachineType, error)

	// AcceleratorTypes answers an aggregated accelerator-type list.
	AcceleratorTypes(ctx context.Context, req *computepb.AggregatedListAcceleratorTypesRequest) (AggregatedList[AcceleratorScope], error)

	// Zones answers the project's zone list.
	Zones(ctx context.Context, req *computepb.ListZonesRequest) ([]*computepb.Zone, error)

	// Regions answers the project's region list, quotas included.
	Regions(ctx context.Context, req *computepb.ListRegionsRequest) ([]*computepb.Region, error)
}

// computeCatalog adapts the generated Compute clients to ComputeCatalog.
type computeCatalog struct {
	machineTypes *compute.MachineTypesClient
	accelerators *compute.AcceleratorTypesClient
	zones        *compute.ZonesClient
	regions      *compute.RegionsClient
}

// NewComputeCatalog wraps the shared Compute clients as the catalog's read
// surface. Every client is required: a nil one panics on the refresh loop's
// first cycle, and nothing recovers there, so the process dies at startup
// rather than answering from a list it never fetched.
func NewComputeCatalog(
	machineTypes *compute.MachineTypesClient,
	accelerators *compute.AcceleratorTypesClient,
	zones *compute.ZonesClient,
	regions *compute.RegionsClient,
) ComputeCatalog {
	return &computeCatalog{machineTypes: machineTypes, accelerators: accelerators, zones: zones, regions: regions}
}

// MachineTypes drains the aggregated machine-type iterator.
func (c *computeCatalog) MachineTypes(ctx context.Context, req *computepb.AggregatedListMachineTypesRequest) (AggregatedList[MachineTypeScope], error) {
	it := c.machineTypes.AggregatedList(ctx, req)
	return drainScopes("machine types", it.Next,
		func(pair compute.MachineTypesScopedListPair) MachineTypeScope {
			return MachineTypeScope{Scope: pair.Key, List: pair.Value}
		},
		func() []string {
			page, _ := it.Response.(*computepb.MachineTypeAggregatedList)
			return page.GetUnreachables()
		})
}

// MachineType reads one machine type in one zone.
func (c *computeCatalog) MachineType(ctx context.Context, req *computepb.GetMachineTypeRequest) (*computepb.MachineType, error) {
	mt, err := c.machineTypes.Get(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("getting machine type %s in %s: %w", req.GetMachineType(), req.GetZone(), err)
	}
	return mt, nil
}

// AcceleratorTypes drains the aggregated accelerator-type iterator.
func (c *computeCatalog) AcceleratorTypes(ctx context.Context, req *computepb.AggregatedListAcceleratorTypesRequest) (AggregatedList[AcceleratorScope], error) {
	it := c.accelerators.AggregatedList(ctx, req)
	return drainScopes("accelerator types", it.Next,
		func(pair compute.AcceleratorTypesScopedListPair) AcceleratorScope {
			return AcceleratorScope{Scope: pair.Key, List: pair.Value}
		},
		func() []string {
			page, _ := it.Response.(*computepb.AcceleratorTypeAggregatedList)
			return page.GetUnreachables()
		})
}

// drainScopes collects an aggregated-list iterator into its scopes plus the
// unreachable scope keys the pages carried. The two aggregated lists differ
// only in their generated pair type, which is what the function parameters
// stand in for. The unreachable list lives on the raw page rather than in the
// pairs, and the generated iterators type it as any, so the caller reads it
// from the iterator it holds concretely. A failed assertion there answers the
// nil-safe getter, never a panic.
func drainScopes[P, S any](
	what string,
	next func() (P, error),
	scope func(P) S,
	unreachable func() []string,
) (AggregatedList[S], error) {
	var out AggregatedList[S]
	seen := make(map[string]bool)
	for {
		pair, err := next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return AggregatedList[S]{}, fmt.Errorf("listing %s: %w", what, err)
		}
		out.Scopes = append(out.Scopes, scope(pair))
		for _, key := range unreachable() {
			seen[key] = true
		}
	}
	out.Unreachable = slices.Sorted(maps.Keys(seen))
	return out, nil
}

// Zones drains the project's zone list.
func (c *computeCatalog) Zones(ctx context.Context, req *computepb.ListZonesRequest) ([]*computepb.Zone, error) {
	var out []*computepb.Zone
	it := c.zones.List(ctx, req)
	for {
		zone, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("listing zones: %w", err)
		}
		out = append(out, zone)
	}
}

// Regions drains the project's region list, quotas included.
func (c *computeCatalog) Regions(ctx context.Context, req *computepb.ListRegionsRequest) ([]*computepb.Region, error) {
	var out []*computepb.Region
	it := c.regions.List(ctx, req)
	for {
		region, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("listing regions: %w", err)
		}
		out = append(out, region)
	}
}
