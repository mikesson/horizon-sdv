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

package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	legacyfeature "cloud-cuttlefish-orchestrator/internal/compat/legacy"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// wireLegacy assembles the legacy /v1 Cloud Orchestrator compatibility
// handler.
func wireLegacy(logger *slog.Logger, f *featureSet) *legacyfeature.COHandler {
	// The zone-token mode (hint vs literal pin) is the operator's
	// placement.v1_zone_is_hint policy; absent reads as hint. The docker-host
	// shape is the settings accessor itself, so the card and the create path
	// can never disagree.
	return legacyfeature.NewCOHandler(f.hostSvc, f.zonePolicy, f.iceComposer, logger).
		WithV1ZoneHintPolicy(func(ctx context.Context) (bool, error) {
			cfg, err := f.sysConfigRepo.Get(ctx, models.ConfigKeyV1ZoneIsHint)
			if err != nil {
				if errors.Is(err, spanner.ErrKeyNotFound) {
					return true, nil
				}
				return true, fmt.Errorf("reading v1 zone-token policy: %w", err)
			}
			return cfg.AsBool(), nil
		}).
		WithDockerHostPolicy(f.hostConfigSvc.GetDockerHost)
}
