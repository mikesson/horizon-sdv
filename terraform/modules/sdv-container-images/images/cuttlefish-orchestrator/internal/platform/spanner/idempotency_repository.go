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
	"google.golang.org/grpc/codes"

	"cloud-cuttlefish-orchestrator/internal/domain/idempotency"
)

const (
	idempotencyKeysTable = "idempotency_keys"

	colResourceID         = "resource_id"
	colIdempotencyKey     = "idempotency_key"
	colRequestFingerprint = "request_fingerprint"
)

// IdempotencyRepository records which resource a client-supplied Idempotency-Key
// created, so retried create requests (devices, hosts) are safe.
type IdempotencyRepository struct {
	client *Client
}

// NewIdempotencyRepository creates an idempotency-key repository.
func NewIdempotencyRepository(client *Client) *IdempotencyRepository {
	return &IdempotencyRepository{client: client}
}

// Lookup returns the record stored for key and whether it was found.
func (r *IdempotencyRepository) Lookup(ctx context.Context, key string) (*idempotency.Record, bool, error) {
	row, err := r.client.Single().ReadRow(ctx, idempotencyKeysTable, spanner.Key{key},
		[]string{colResourceID, colRequestFingerprint})
	if err != nil {
		if spanner.ErrCode(err) == codes.NotFound {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("looking up idempotency key: %w", err)
	}
	var rec idempotency.Record
	if err := row.Columns(&rec.ResourceID, &rec.Fingerprint); err != nil {
		return nil, false, fmt.Errorf("scanning idempotency key: %w", err)
	}
	return &rec, true, nil
}

// NewKeyMutation builds the insert recording that key created resourceID. Fold
// it into the create transaction so it commits atomically; a duplicate key fails
// the commit (codes.AlreadyExists), which the caller resolves by reading the
// existing row.
func (r *IdempotencyRepository) NewKeyMutation(key, resourceID, fingerprint string) *spanner.Mutation {
	return spanner.InsertMap(idempotencyKeysTable, map[string]any{
		colIdempotencyKey:     key,
		colResourceID:         resourceID,
		colRequestFingerprint: fingerprint,
		ColCreatedAt:          time.Now(),
	})
}
