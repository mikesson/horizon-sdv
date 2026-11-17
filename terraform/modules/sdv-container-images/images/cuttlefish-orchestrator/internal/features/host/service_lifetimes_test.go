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
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// fakeLifetimePolicy serves canned lifetime rows; absent keys read not-found,
// exactly as the Spanner repository reports them.
type fakeLifetimePolicy map[string]string

func (f fakeLifetimePolicy) Get(_ context.Context, key string) (*models.SystemConfig, error) {
	v, ok := f[key]
	if !ok {
		return nil, cspanner.ErrKeyNotFound
	}
	return &models.SystemConfig{Key: key, Value: v}, nil
}

// The stored lease differs from the compiled 10h on purpose: this goes red
// if Create stops reading the lifetimes.host_ttl_hours policy. Pool top-ups
// ride the same path, since the keeper's provisioner leaves TTLHours zero.
func TestCreateTakesPolicyHostTTL(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	require.NoError(t, db.SeedQuota(ctx, 100, 0))
	repo := NewHostRepository(db.Client)

	newSvc := func(policy fakeLifetimePolicy) *HostService {
		return NewHostService(
			HostServiceConfig{ProjectID: "test-project", HostOrchestratorPort: 2080},
			nil, nil, repo, slog.New(slog.DiscardHandler),
			WithTaskEnqueuer(&fakeEnqueuer{}),
			WithSystemConfig(policy),
		)
	}

	svc := newSvc(fakeLifetimePolicy{models.ConfigKeyHostTTLHours: "24"})
	h, err := svc.Create(ctx, host.CreateRequest{
		Name: "pol-default", Zone: "europe-west4-a", MachineType: "n1-standard-8", MaxDevices: 4,
	}, "u@example.com", host.OriginUser)
	require.NoError(t, err)
	assert.Equal(t, int64(24*3600), h.Lifecycle.TTLSeconds,
		"a create naming no TTL takes the stored policy")

	// An explicit request always wins over policy.
	h, err = svc.Create(ctx, host.CreateRequest{
		Name: "pol-explicit", Zone: "europe-west4-a", MachineType: "n1-standard-8",
		MaxDevices: 4, TTLHours: 3,
	}, "u@example.com", host.OriginUser)
	require.NoError(t, err)
	assert.Equal(t, int64(3*3600), h.Lifecycle.TTLSeconds)

	// Out of range clamps to the compiled default rather than out-leasing the
	// VM-side failsafe.
	svc = newSvc(fakeLifetimePolicy{models.ConfigKeyHostTTLHours: "999"})
	h, err = svc.Create(ctx, host.CreateRequest{
		Name: "pol-clamped", Zone: "europe-west4-a", MachineType: "n1-standard-8", MaxDevices: 4,
	}, "u@example.com", host.OriginUser)
	require.NoError(t, err)
	assert.Equal(t, host.DefaultHostTTLSeconds, h.Lifecycle.TTLSeconds)
}
