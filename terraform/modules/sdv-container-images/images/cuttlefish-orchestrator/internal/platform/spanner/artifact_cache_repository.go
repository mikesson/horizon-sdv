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

package spanner

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/spanner"
)

// ArtifactCacheRepository records which pinned cvd fetch keys a host's
// download cache holds. It is the build-affinity ledger placement orders warm
// hosts first by. Advisory by construction: the host's own cache prune can
// evict artifacts under a row, and a stale row costs exactly the baseline
// fetch, so nothing here prunes. Rows die with their host row (interleave)
// or with its disk (ArtifactCacheLostMutation, folded into the commit that
// dooms the disk).
type ArtifactCacheRepository struct {
	client *Client
}

// NewArtifactCacheRepository creates the build-affinity ledger repository.
func NewArtifactCacheRepository(client *Client) *ArtifactCacheRepository {
	return &ArtifactCacheRepository{client: client}
}

// Record upserts a fetch key onto the host's ledger. Last-write-wins by
// design: concurrent creates of the same build on one host record the same
// fact, and a re-fetch after the host's prune refreshes the stamp.
func (r *ArtifactCacheRepository) Record(ctx context.Context, hostID, fetchKey, buildID string, hostIncarnation int64) error {
	_, err := r.client.Apply(ctx, []*spanner.Mutation{spanner.InsertOrUpdate("host_artifact_cache",
		[]string{"host_id", "fetch_key", "build_id", "host_incarnation", "fetched_at"},
		[]any{hostID, fetchKey, buildID, hostIncarnation, time.Now()})})
	if err != nil {
		return fmt.Errorf("recording artifact cache key %s on host %s: %w", fetchKey, hostID, err)
	}
	return nil
}

// ArtifactCacheLostMutation deletes every ledger row of a host, for callers
// folding the delete into the commit that dooms the host's disk, meaning the
// incarnation bump and the lost-VM tombstone. A key-prefix delete: no read,
// so it can ride any transaction.
func ArtifactCacheLostMutation(hostID string) *spanner.Mutation {
	return spanner.Delete("host_artifact_cache", spanner.Key{hostID}.AsPrefix())
}
