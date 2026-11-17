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

package catalogsync

import (
	"context"
	"fmt"
	"log/slog"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// SpotAdviceProber probes the GCE capacity-advice API for one
// (zone, machine type) cell. Satisfied by gce.SpotAdviceClient.
type SpotAdviceProber interface {
	ProbeCell(ctx context.Context, region, zone, machineType string) (*gcecatalog.SpotAdvice, error)
}

// MachineTypeLister is the slice of the machine-type catalog the spot-advice
// sync probes: the admin-enabled shapes.
type MachineTypeLister interface {
	List(ctx context.Context, filter machinetype.ListFilter) ([]*machinetype.MachineType, error)
}

// EnabledZones lists the zones an operator has admitted for host creation.
// They are the other axis of the probed cell set.
type EnabledZones interface {
	ListEnabledZones(ctx context.Context) ([]string, error)
}

// WithSpotAdvice enables the Spot capacity-advice sync: every interval, each
// enabled-zone × enabled-machine-type cell is probed and the results land in
// spot_advice for ranking to read. All four dependencies are required.
func WithSpotAdvice(prober SpotAdviceProber, store gcecatalog.SpotAdviceStore, machineTypes MachineTypeLister, zones EnabledZones) Option {
	return func(s *Service) {
		s.spotAdviceProber = prober
		s.spotAdviceStore = store
		s.spotAdviceMachineTypes = machineTypes
		s.spotAdviceZones = zones
	}
}

// spotAdviceConfigured reports whether the spot-advice sync can run.
func (s *Service) spotAdviceConfigured() bool {
	return s.spotAdviceProber != nil && s.spotAdviceStore != nil &&
		s.spotAdviceMachineTypes != nil && s.spotAdviceZones != nil &&
		s.config.SpotAdviceSyncInterval > 0
}

// MaxProbedShapes bounds how many machine types one sync probes.
//
// The cell set is enabled zones × enabled machine types, and the second factor
// stopped being small when curation moved to families. A family is enabled
// once and contributes every shape Compute Engine offers in it, so seven
// families are of the order of a hundred admissible shapes, where a
// hand-entered catalog held fifteen. At three enabled zones that is ~255
// probes of a rate-limited beta API every sync interval instead of ~45, and
// the interval is minutes.
//
// The catalog's display order is the operator's preference order, so the
// shapes taken first are the ones provisioning is most likely to bind (the
// default, its family's neighbors, the ladder's rungs). A shape past the
// ceiling simply carries no advice cell, which ranking already reads as no
// signal rather than as no capacity.
const MaxProbedShapes = 24

// syncSpotAdvice probes Spot capacity advice for the active cell set (enabled
// zones × the head of the enabled machine types, in catalog order) and upserts
// the results. A failed probe skips its cell and leaves the rest of the batch
// alone. The stale row ages out under the table's deletion policy.
func (s *Service) syncSpotAdvice(ctx context.Context) error {
	s.logger.Debug("starting spot-advice sync")

	zones, err := s.spotAdviceZones.ListEnabledZones(ctx)
	if err != nil {
		s.logger.Error("failed to list enabled zones for spot advice", slog.Any("error", err))
		return fmt.Errorf("listing enabled zones: %w", err)
	}
	enabled := true
	types, err := s.spotAdviceMachineTypes.List(ctx,
		machinetype.ListFilter{IsEnabled: &enabled, Limit: MaxProbedShapes})
	if err != nil {
		s.logger.Error("failed to list machine types for spot advice", slog.Any("error", err))
		return fmt.Errorf("listing machine types: %w", err)
	}

	cells := s.probeSpotCells(ctx, zones, types)
	if err := s.spotAdviceStore.UpsertCells(ctx, cells); err != nil {
		s.logger.Error("failed to store spot advice", slog.Any("error", err))
		return fmt.Errorf("storing spot advice: %w", err)
	}
	s.logger.Debug("spot-advice sync complete",
		slog.Int("cells", len(cells)),
		slog.Int("zones", len(zones)),
		slog.Int("machine_types", len(types)))
	return nil
}

// probeSpotCells probes every (zone, machine type) cell, dropping cells whose
// probe failed or returned no recommendation. Bare-metal shapes are skipped:
// GCE never offers them as Spot VMs (the advice API 400s on them), and they
// stay in the catalog regardless, because Cuttlefish on C4A/Axion exists only
// in bare-metal form.
func (s *Service) probeSpotCells(ctx context.Context, zones []string, types []*machinetype.MachineType) []*gcecatalog.SpotAdvice {
	var cells []*gcecatalog.SpotAdvice
	for _, zone := range zones {
		region := gcecatalog.RegionFromZone(zone)
		for _, mt := range types {
			if mt.IsBareMetal() {
				continue
			}
			advice, err := s.spotAdviceProber.ProbeCell(ctx, region, zone, mt.ID)
			if err != nil {
				s.logger.Warn("spot-advice probe failed",
					slog.String("zone", zone),
					slog.String("machine_type", mt.ID),
					slog.Any("error", err))
				continue
			}
			if advice == nil {
				continue
			}
			cells = append(cells, advice)
		}
	}
	return cells
}
