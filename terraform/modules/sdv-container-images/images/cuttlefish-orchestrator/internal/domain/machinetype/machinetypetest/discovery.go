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

package machinetypetest

import (
	"context"
	"fmt"

	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// FixedDiscovery stands in for what Compute Engine offers: a set of shapes
// written down once, with no refresh behind it.
//
// The zero value has landed no discovery at all, which is the cold-start state
// the composed catalog refuses to answer from, so a caller has to say which
// of the two situations it is exercising.
type FixedDiscovery struct {
	Shapes map[string]machinetype.Shape

	// Landed reports whether a discovery has happened. NewFixedDiscovery sets
	// it; leave it false to exercise the cold instance.
	Landed bool

	// Resolved answers ResolveShape for the shapes no list carries. A name
	// absent from it is one Compute Engine would refuse.
	Resolved map[string]machinetype.Shape

	// Calls counts ResolveShape, so a test can prove a resolution was memoized
	// rather than repeated.
	Calls int
}

// NewFixedDiscovery builds a landed discovery over the given shapes.
func NewFixedDiscovery(shapes ...machinetype.Shape) *FixedDiscovery {
	d := &FixedDiscovery{Shapes: make(map[string]machinetype.Shape, len(shapes)), Landed: true}
	for _, shape := range shapes {
		d.Shapes[shape.Name] = shape
	}
	return d
}

// DiscoveredShapes answers the written-down shapes.
func (d *FixedDiscovery) DiscoveredShapes(context.Context) (map[string]machinetype.Shape, bool) {
	if !d.Landed {
		return nil, false
	}
	if d.Shapes == nil {
		return map[string]machinetype.Shape{}, true
	}
	return d.Shapes, true
}

// ResolveShape answers the on-demand shapes, refusing anything else the way
// Compute Engine refuses a machine it will not build.
func (d *FixedDiscovery) ResolveShape(_ context.Context, machineType string) (machinetype.Shape, error) {
	d.Calls++
	if shape, ok := d.Resolved[machineType]; ok {
		return shape, nil
	}
	return machinetype.Shape{}, fmt.Errorf(
		"%q is not a machine type Compute Engine will build: %w", machineType, machinetype.ErrNotFound)
}

// Compile-time check that the fake satisfies the discovery port.
var _ machinetype.Discovery = (*FixedDiscovery)(nil)
