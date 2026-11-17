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

// Package idempotency holds the cross-cutting Idempotency-Key logic shared by
// the resource features (device, host) that make their Create safe to retry:
// the stored record, request fingerprinting, and the lookup/conflict flow.
// The write side (building the key-insert mutation) stays in the features
// because it is storage-specific.
package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// Record is the stored mapping for an Idempotency-Key: the resource it created
// and a fingerprint of the original request (to detect key reuse).
type Record struct {
	ResourceID  string
	Fingerprint string
}

// ErrConflict is returned when an Idempotency-Key was already used for a
// different request.
var ErrConflict = errors.New("idempotency key already used with a different request")

// Store resolves previously recorded Idempotency-Keys. The Spanner
// IdempotencyRepository satisfies it.
type Store interface {
	Lookup(ctx context.Context, key string) (record *Record, found bool, err error)
}

// KeyedStore is a Store that can also mint the key-insert mutation for a
// create transaction. It is generic over the storage mutation type M so this
// package stays storage-agnostic; the feature IdempotencyStore interfaces
// satisfy it with the Spanner mutation type.
type KeyedStore[M any] interface {
	Store
	NewKeyMutation(key, resourceID, fingerprint string) M
}

// Prepared is PrepareMutation's outcome: either the previously created
// resource (Found, so the caller short-circuits) or the key-insert mutations
// to fold into the create transaction.
type Prepared[R, M any] struct {
	Mutations []M
	Existing  R
	Found     bool
}

// PrepareMutation is the whole Idempotency-Key create flow the resource
// features share: resolve the key against the request's fingerprint, and
// either return the previously created resource or the key-insert mutations
// for resourceID. Reusing a key with a different request returns ErrConflict.
// The caller supplies the fingerprint, usually Fingerprint of the request it
// is committing, but a resolution path that rewrites the request (declarative
// create) carries the ORIGINAL request's fingerprint through instead, so the
// stored key records what the user sent.
func PrepareMutation[R, M any](ctx context.Context, store KeyedStore[M], key, fingerprint, resourceID string, load func(context.Context, string) (R, error)) (Prepared[R, M], error) {
	existing, found, err := Resolve(ctx, store, key, fingerprint, load)
	if err != nil {
		return Prepared[R, M]{}, err
	}
	if found {
		return Prepared[R, M]{Existing: existing, Found: true}, nil
	}
	return Prepared[R, M]{Mutations: []M{store.NewKeyMutation(key, resourceID, fingerprint)}}, nil
}

// Fingerprint returns a stable hash of the request's JSON form. Every
// JSON-visible field is covered, so a new request field cannot silently
// escape conflict detection; fields tagged `json:"-"` (like the
// Idempotency-Key itself) are excluded by construction.
func Fingerprint(req any) (string, error) {
	canonical, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("fingerprinting request: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// Resolve implements the read side of Idempotency-Key handling: it returns the
// resource previously created under key (loaded via load), reporting false
// when the key is unused. Reusing a key with a different request fingerprint
// returns ErrConflict instead of the wrong resource.
func Resolve[T any](ctx context.Context, store Store, key, fingerprint string, load func(context.Context, string) (T, error)) (T, bool, error) {
	var zero T
	rec, found, err := store.Lookup(ctx, key)
	if err != nil {
		return zero, false, fmt.Errorf("idempotency lookup: %w", err)
	}
	if !found {
		return zero, false, nil
	}
	if rec.Fingerprint != fingerprint {
		return zero, false, ErrConflict
	}
	resource, err := load(ctx, rec.ResourceID)
	if err != nil {
		return zero, false, fmt.Errorf("loading idempotent resource: %w", err)
	}
	return resource, true, nil
}

// ResolveRace resolves a lost commit race: a concurrent request with the same
// key may have won the insert. An equivalent request (matching fingerprint)
// returns the winner's resource as the idempotent result; a different request
// reusing the key reports ErrConflict, exactly as the sequential path does,
// and never the wrong resource. Any other failure reports not-found with a nil
// error, so the caller falls back to its original commit error, which is the
// primary signal.
func ResolveRace[T any](ctx context.Context, store Store, key, fingerprint string, load func(context.Context, string) (T, error)) (T, bool, error) {
	var zero T
	resource, found, err := Resolve(ctx, store, key, fingerprint, load)
	if errors.Is(err, ErrConflict) {
		return zero, false, ErrConflict
	}
	if err != nil {
		return zero, false, nil //nolint:nilerr // the caller's commit error is the primary signal
	}
	return resource, found, nil
}
