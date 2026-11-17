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
	"errors"
	"fmt"
	"log/slog"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// SystemConfigStore is the slice of system_config the zone policy owns: one
// key, read whole and written whole. The Spanner SystemConfigRepository
// satisfies it.
type SystemConfigStore interface {
	Get(ctx context.Context, key string) (*models.SystemConfig, error)
	Set(ctx context.Context, key, value, description string) error
}

// ZonePolicy holds the operator's decision about which zones may take a host.
//
// It is the whole durable half of the zone catalog. What Compute Engine offers
// is answered from the in-process snapshot, which is cheaper, fresher and can
// say "not known", a distinction a missing row cannot make. What an operator
// decided exists nowhere else, so it earns a durable value, and one
// system_config key is the shape that fits: a small set, always read whole,
// with no per-zone metadata anything queries.
//
// Validation lives with the writer that has the snapshot to validate against
// (the handler), not here. This stores what it is given.
type ZonePolicy struct {
	settings SystemConfigStore
	logger   *slog.Logger
}

// NewZonePolicy creates the zone-enablement store.
func NewZonePolicy(settings SystemConfigStore, logger *slog.Logger) *ZonePolicy {
	return &ZonePolicy{settings: settings, logger: logger}
}

// ListEnabledZones returns the zones enabled for host creation, sorted. An
// absent key is no enabled zones, which is where a deployment starts.
func (p *ZonePolicy) ListEnabledZones(ctx context.Context) ([]string, error) {
	cfg, err := p.settings.Get(ctx, models.ConfigKeyZonesEnabled)
	if err != nil {
		if errors.Is(err, cspanner.ErrKeyNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading the enabled zones: %w", err)
	}
	zones, ok := gcecatalog.ParseEnabledZones(cfg.AsString())
	if !ok {
		p.logger.Warn("the stored enabled-zone set is unreadable; no zone is enabled for host creation",
			slog.String("key", models.ConfigKeyZonesEnabled))
		return nil, nil
	}
	return zones, nil
}

// SetEnabledZones replaces the enabled set whole.
func (p *ZonePolicy) SetEnabledZones(ctx context.Context, zones []string) error {
	encoded, err := gcecatalog.EnabledZonesJSON(zones)
	if err != nil {
		return err
	}
	if err := p.settings.Set(ctx, models.ConfigKeyZonesEnabled, encoded,
		"Zones enabled for host creation; placement never ranks outside this set"); err != nil {
		return fmt.Errorf("storing the enabled zones: %w", err)
	}
	return nil
}
