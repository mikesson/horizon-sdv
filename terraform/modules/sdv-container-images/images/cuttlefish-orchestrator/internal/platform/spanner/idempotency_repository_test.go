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

package spanner_test

import (
	"testing"

	"cloud.google.com/go/spanner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

func TestIdempotencyRepository(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	repo := cspanner.NewIdempotencyRepository(db.Client)
	ctx := t.Context()

	// An unused key is not found.
	_, found, err := repo.Lookup(ctx, "k1")
	require.NoError(t, err)
	assert.False(t, found)

	// Store then look up round-trips the device id + fingerprint.
	_, err = db.Client.Apply(ctx, []*spanner.Mutation{repo.NewKeyMutation("k1", "dev-1", "fp-1")})
	require.NoError(t, err)
	rec, found, err := repo.Lookup(ctx, "k1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "dev-1", rec.ResourceID)
	assert.Equal(t, "fp-1", rec.Fingerprint)

	// A duplicate key insert fails on the primary key, which is what makes the
	// concurrent-create race safe.
	_, err = db.Client.Apply(ctx, []*spanner.Mutation{repo.NewKeyMutation("k1", "dev-2", "fp-2")})
	require.Error(t, err)
	assert.Equal(t, codes.AlreadyExists, spanner.ErrCode(err))
}
