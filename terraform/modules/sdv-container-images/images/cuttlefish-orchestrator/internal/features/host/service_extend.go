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

package hostfeature

import (
	"context"
	"fmt"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// Extend adds hours to a reserved host's lease, from the later of now and its
// current expiry. The refusals (terminal, not reserved, protected, past the VM
// horizon) are host.Host.Extend's, checked against the row inside the
// transaction that writes it, so a concurrent pin or device raise is never
// overwritten by a stale read. The API's minimum and maximum tags bound one
// request; there is no cumulative cap.
func (s *HostService) Extend(ctx context.Context, id string, hours int64) (*host.Host, error) {
	var previous *time.Time
	updated, err := s.hostRepo.Mutate(ctx, id, func(h *host.Host) error {
		previous = h.Lifecycle.ExpiresAt
		return h.Extend(time.Duration(hours)*time.Hour, time.Now())
	})
	if err != nil {
		return nil, fmt.Errorf("extending host %s: %w", id, err)
	}
	audit.LogChange(ctx, s.auditLogger, audit.ActionHostUpdate, audit.ResourceHost, id,
		map[string]any{
			"extend_hours":        hours,
			"previous_expires_at": previous,
			"expires_at":          updated.Lifecycle.ExpiresAt,
		})
	return updated, nil
}
