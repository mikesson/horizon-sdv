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

// Package idempotencytest provides an in-memory fake of the Spanner
// idempotency store for the resource features' create tests.
package idempotencytest

import (
	"context"
	"sync"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/idempotency"
)

// FakeStore simulates the Spanner idempotency store: NewKeyMutation records
// the mapping immediately (as the real insert would on commit) so a later
// Lookup resolves it. It satisfies the device and host feature
// IdempotencyStore interfaces.
type FakeStore struct {
	mu   sync.Mutex
	seen map[string]idempotency.Record
}

// NewFakeStore creates an empty fake store.
func NewFakeStore() *FakeStore {
	return &FakeStore{seen: make(map[string]idempotency.Record)}
}

// Lookup resolves a previously recorded key.
func (f *FakeStore) Lookup(_ context.Context, key string) (*idempotency.Record, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.seen[key]
	if !ok {
		return nil, false, nil
	}
	return &r, true, nil
}

// NewKeyMutation records the mapping and returns an inert mutation (test
// repositories ignore extra mutations).
func (f *FakeStore) NewKeyMutation(key, resourceID, fingerprint string) *spanner.Mutation {
	f.mu.Lock()
	f.seen[key] = idempotency.Record{ResourceID: resourceID, Fingerprint: fingerprint}
	f.mu.Unlock()
	return spanner.Delete("idempotency_keys", spanner.Key{key})
}

// RaceStore simulates losing the key-insert commit race: the first Lookup
// misses (the winner has not committed yet), every later Lookup resolves to
// the winner's record, and NewKeyMutation records nothing, because the loser's
// insert is exactly the write that fails to commit. It backs the tests of
// the post-commit-failure recovery path.
type RaceStore struct {
	mu     sync.Mutex
	looked bool
	winner idempotency.Record
}

// NewRaceStore creates a RaceStore whose race winner committed resourceID
// under fingerprint.
func NewRaceStore(resourceID, fingerprint string) *RaceStore {
	return &RaceStore{winner: idempotency.Record{ResourceID: resourceID, Fingerprint: fingerprint}}
}

// Lookup misses once, then resolves to the winner's record.
func (r *RaceStore) Lookup(_ context.Context, _ string) (*idempotency.Record, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.looked {
		r.looked = true
		return nil, false, nil
	}
	rec := r.winner
	return &rec, true, nil
}

// NewKeyMutation returns an inert mutation without recording anything: the
// loser's key insert never commits.
func (r *RaceStore) NewKeyMutation(key, _, _ string) *spanner.Mutation {
	return spanner.Delete("idempotency_keys", spanner.Key{key})
}
