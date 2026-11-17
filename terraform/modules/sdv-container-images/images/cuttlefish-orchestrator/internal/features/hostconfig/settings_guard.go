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

package hostconfigfeature

import (
	"context"
	"fmt"

	"cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// The system_config keys behind each settings resource. A resource's entity
// tag hashes exactly these, so a write to any of them changes it.
var (
	keysZonePin               = []string{models.ConfigKeyZonePin}
	keysQuotaMargin           = []string{models.ConfigKeyQuotaMarginPercent}
	keysV1ZoneHint            = []string{models.ConfigKeyV1ZoneIsHint}
	keysSpotAutoDefault       = []string{models.ConfigKeySpotAutoDefault}
	keysDefaultGPUMachineType = []string{models.ConfigKeyDefaultGPUMachineType}
	keysDockerHost            = []string{models.ConfigKeyDockerMachineType, models.ConfigKeyDockerTTLHours}
	keysSpotMinUptime         = []string{models.ConfigKeySpotMinUptimeSingle, models.ConfigKeySpotMinUptimeBatch}
	keysDefaultUpstreamBuild  = []string{models.ConfigKeyDefaultUpstreamBranch, models.ConfigKeyDefaultUpstreamTarget}
	keysArtifactBuckets       = []string{models.ConfigKeyBugreportBucket, models.ConfigKeyRecordingBucket, models.ConfigKeyCaptureOnError}
	keysLifetimes             = []string{
		models.ConfigKeyDeviceTTLHours, models.ConfigKeyExtendHours,
		models.ConfigKeyExtendCount, models.ConfigKeyHostTTLHours,
	}
	keysSweeps = []string{
		models.ConfigKeyDeleteOrphanVMs, models.ConfigKeyOrphanCVDs,
		models.ConfigKeyZombieRecreate, models.ConfigKeyReaperDryRun,
	}
	// Every fleet write shares one tag, so a client chaining several of them
	// carries each write's tag into the next.
	keysFleet = []string{
		models.ConfigKeyFleetAutoscaleMode, models.ConfigKeyFleetDemandHorizonSec,
		models.ConfigKeyFleetDrainGraceSec, models.ConfigKeyFleetPoolBounds,
		models.ConfigKeyFleetPoolSchedules, models.ConfigKeyFleetPoolLadders,
	}
)

// Guard is the precondition one settings write runs under: the keys behind
// the resource and the If-Match the caller sent (empty for none). After the
// write, ETag holds the resource's new entity tag.
type Guard struct {
	Keys    []string
	IfMatch string
	ETag    string
}

// Guarded returns a view of the service whose settings writes check g inside
// the write transaction and record the new entity tag in g.
func (s *Service) Guarded(g *Guard) *Service {
	guarded := *s
	guarded.settings = guardedStore{SettingsStore: s.settings, guard: g}
	return &guarded
}

// SettingETag returns the entity tag of the stored values behind keys.
func (s *Service) SettingETag(ctx context.Context, keys []string) (string, error) {
	etag, err := s.settings.Fingerprint(ctx, keys)
	if err != nil {
		return "", fmt.Errorf("reading the setting's entity tag: %w", err)
	}
	return etag, nil
}

// guardedStore routes every write through SetManyIfMatch under its guard.
type guardedStore struct {
	SettingsStore
	guard *Guard
}

// Set writes one key under the guard.
func (g guardedStore) Set(ctx context.Context, key, value, description string) error {
	return g.SetMany(ctx, []spanner.ConfigEntry{{Key: key, Value: value, Description: description}})
}

// SetMany writes a group of keys under the guard and records the new tag.
func (g guardedStore) SetMany(ctx context.Context, entries []spanner.ConfigEntry) error {
	etag, err := g.SetManyIfMatch(ctx, g.guard.Keys, g.guard.IfMatch, entries)
	if err != nil {
		return err
	}
	g.guard.ETag = etag
	// A second write in the same request checks against what the first stored.
	if g.guard.IfMatch != "" {
		g.guard.IfMatch = etag
	}
	return nil
}
