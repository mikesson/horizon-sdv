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

package gcecatalogfeature

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

// TaskTypeReprobeSpotAdvice is the queue type this handler answers (canonical
// name in domain/task, where the producer can reach it without importing a
// feature).
const TaskTypeReprobeSpotAdvice = task.TypeCatalogReprobeSpotAdvice

// SpotAdviceProber probes the GCE capacity-advice API for one
// (zone, machine type) cell. The gce.SpotAdviceClient satisfies it, and it is
// the same prober the periodic sync drives, so a re-probe and a sync measure
// the same thing the same way.
type SpotAdviceProber interface {
	ProbeCell(ctx context.Context, region, zone, machineType string) (*gcecatalog.SpotAdvice, error)
}

// ReprobeAdviceHandler refreshes the one Spot capacity-advice cell a stockout
// has just proved empty.
//
// Ranking degrades to its quota and health signals when a cell is stale, so
// waiting out the sync interval after a capacity storm is not wrong, only
// slow. This shortens it to seconds for the cell that actually failed, and
// touches nothing else: one probe, one row.
type ReprobeAdviceHandler struct {
	prober SpotAdviceProber
	store  gcecatalog.SpotAdviceStore
	logger *slog.Logger
}

// NewReprobeAdviceHandler creates the stockout re-probe handler.
func NewReprobeAdviceHandler(
	prober SpotAdviceProber,
	store gcecatalog.SpotAdviceStore,
	logger *slog.Logger,
) *ReprobeAdviceHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReprobeAdviceHandler{prober: prober, store: store, logger: logger}
}

// TaskType implements task.Handler.
func (h *ReprobeAdviceHandler) TaskType() string { return TaskTypeReprobeSpotAdvice }

// Handle re-probes the cell named by the task's resource id ("<zone>/<machine
// type>") and upserts the result.
//
// A probe that answers nothing leaves the stored row alone rather than deleting
// it: the cell ages out under the table's own deletion policy, and removing it
// here would turn "the API had no recommendation" into "this cell was never
// measured", which ranks differently.
func (h *ReprobeAdviceHandler) Handle(ctx context.Context, t *task.Task) error {
	zone, machineType, ok := strings.Cut(t.ResourceID, "/")
	if !ok || zone == "" || machineType == "" {
		return fmt.Errorf("re-probe task carries %q, want <zone>/<machine type>", t.ResourceID)
	}
	// Bare metal is never offered as Spot capacity, and the advice API refuses
	// the request rather than answering it, so a stockout on a bare-metal shape
	// has no cell to refresh.
	if machinetype.IsBareMetalID(machineType) {
		return nil
	}
	advice, err := h.prober.ProbeCell(ctx, gcecatalog.RegionFromZone(zone), zone, machineType)
	if err != nil {
		return fmt.Errorf("re-probing spot advice for %s/%s: %w", zone, machineType, err)
	}
	if advice == nil {
		h.logger.Debug("spot-advice re-probe returned no recommendation",
			slog.String("zone", zone), slog.String("machine_type", machineType))
		return nil
	}
	if err := h.store.UpsertCells(ctx, []*gcecatalog.SpotAdvice{advice}); err != nil {
		return fmt.Errorf("storing the re-probed spot advice cell: %w", err)
	}
	h.logger.Info("spot-advice cell re-probed after a stockout",
		slog.String("zone", zone),
		slog.String("machine_type", machineType),
		slog.Float64("obtainability", advice.Obtainability))
	return nil
}
