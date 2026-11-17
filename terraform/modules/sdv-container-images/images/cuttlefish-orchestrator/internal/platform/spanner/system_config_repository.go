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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/grpc/codes"

	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// ErrKeyNotFound is returned when a system config key does not exist.
var ErrKeyNotFound = errors.New("config key not found")

// system_config column names, repeated across the reads and the upsert.
const (
	colConfigKey   = "config_key"
	colConfigValue = "config_value"
	colDescription = "description"
)

// SystemConfigRepository provides access to the system_config table.
// This table stores platform-wide configuration values.
type SystemConfigRepository struct {
	client *Client
}

// NewSystemConfigRepository creates a new system config repository.
func NewSystemConfigRepository(client *Client) *SystemConfigRepository {
	return &SystemConfigRepository{client: client}
}

// Get retrieves a configuration value by key.
func (r *SystemConfigRepository) Get(ctx context.Context, key string) (*models.SystemConfig, error) {
	row, err := r.client.Single().ReadRow(ctx, "system_config",
		spanner.Key{key},
		[]string{colConfigKey, colConfigValue, colDescription, ColCreatedAt, ColUpdatedAt},
	)
	if err != nil {
		if spanner.ErrCode(err) == codes.NotFound {
			return nil, fmt.Errorf("config key %q: %w", key, ErrKeyNotFound)
		}
		return nil, fmt.Errorf("getting config %q: %w", key, err)
	}

	var configRow models.SystemConfigRow
	if err := row.ToStruct(&configRow); err != nil {
		return nil, fmt.Errorf("scanning config row: %w", err)
	}

	return configRow.ToDomain(), nil
}

// GetAll retrieves all configuration values.
func (r *SystemConfigRepository) GetAll(ctx context.Context) ([]*models.SystemConfig, error) {
	stmt := spanner.Statement{SQL: `SELECT config_key, config_value, description, created_at, updated_at FROM system_config`}

	return QueryAll(r.client.StaleSingle().Query(ctx, stmt), "system configs", (*models.SystemConfigRow).ToDomain)
}

// ConfigEntry is one key's value and stored description, for a write that
// applies a whole group of them.
type ConfigEntry struct {
	Key         string
	Value       string
	Description string
}

// Set creates or updates a configuration value.
func (r *SystemConfigRepository) Set(ctx context.Context, key, value, description string) error {
	return r.SetMany(ctx, []ConfigEntry{{Key: key, Value: value, Description: description}})
}

// SetMany creates or updates several configuration values in ONE commit.
//
// It exists so the group applies atomically. The settings groups this serves
// are read back as one fact (an autoscaler mode beside the standing capacity it
// acts on, four lifetime bounds, a pair of Spot uptime tiers) so a group that
// half-applies stores a policy nobody wrote. The loudest case is the
// autoscaler's: `mode: on` committing while `standby: 0` does not gives the
// keeper a target of zero beside standing inventory, which it starts giving
// back within one cycle. Reversing the write order only changes which half is
// left behind.
//
// The existence checks and the writes share one transaction, so concurrent
// writers serialize.
func (r *SystemConfigRepository) SetMany(ctx context.Context, entries []ConfigEntry) error {
	if len(entries) == 0 {
		return nil
	}
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		return writeEntries(ctx, txn, entries)
	})
	if err != nil {
		return fmt.Errorf("setting config %s: %w", quotedKeys(entries), err)
	}

	return nil
}

// SetManyIfMatch is SetMany guarded by the fingerprint of a group of keys. A
// non-empty ifMatch that differs from the stored fingerprint fails with
// hostconfig.ErrSettingChanged and writes nothing. The check and the writes
// share one transaction. It returns the group's fingerprint after the write.
func (r *SystemConfigRepository) SetManyIfMatch(ctx context.Context, keys []string, ifMatch string, entries []ConfigEntry) (string, error) {
	var after string
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		stored, err := readValues(ctx, txn, keys)
		if err != nil {
			return err
		}
		if ifMatch != "" && ifMatch != fingerprint(keys, stored) {
			return hostconfig.ErrSettingChanged
		}
		if err := writeEntries(ctx, txn, entries); err != nil {
			return err
		}
		for _, e := range entries {
			stored[e.Key] = e.Value
		}
		after = fingerprint(keys, stored)
		return nil
	})
	if errors.Is(err, hostconfig.ErrSettingChanged) {
		return "", hostconfig.ErrSettingChanged
	}
	if err != nil {
		return "", fmt.Errorf("setting config %s: %w", quotedKeys(entries), err)
	}
	return after, nil
}

// Fingerprint returns the entity tag of a group of keys: a hash over each
// key's stored value, where an absent key differs from an empty one.
func (r *SystemConfigRepository) Fingerprint(ctx context.Context, keys []string) (string, error) {
	stored, err := readValues(ctx, r.client.Single(), keys)
	if err != nil {
		return "", err
	}
	return fingerprint(keys, stored), nil
}

// rowReader is the read half shared by read-only and read-write transactions.
type rowReader interface {
	Read(ctx context.Context, table string, keys spanner.KeySet, columns []string) *spanner.RowIterator
}

// readValues reads the stored values of keys; an absent key has no entry.
func readValues(ctx context.Context, txn rowReader, keys []string) (map[string]string, error) {
	keySet := make([]spanner.KeySet, len(keys))
	for i, k := range keys {
		keySet[i] = spanner.Key{k}
	}
	stored := make(map[string]string, len(keys))
	err := txn.Read(ctx, "system_config", spanner.KeySets(keySet...), []string{colConfigKey, colConfigValue}).
		Do(func(row *spanner.Row) error {
			var key, value string
			if err := row.Columns(&key, &value); err != nil {
				return fmt.Errorf("scanning config row: %w", err)
			}
			stored[key] = value
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", strings.Join(keys, ", "), err)
	}
	return stored, nil
}

// fingerprint hashes the stored values of keys in a fixed order.
func fingerprint(keys []string, stored map[string]string) string {
	h := sha256.New()
	for _, k := range slices.Sorted(slices.Values(keys)) {
		value, ok := stored[k]
		// Length prefixes keep any two different groups from hashing alike.
		fmt.Fprintf(h, "%d:%s|%t|%d:%s|", len(k), k, ok, len(value), value)
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// writeEntries buffers the upserts for entries inside txn. The existence
// check shares the transaction, so a transient read failure aborts the group
// instead of being mistaken for not-found and resetting created_at.
//
// Timestamps use time.Now() rather than spanner.CommitTimestamp: the
// system_config columns don't carry allow_commit_timestamp=true.
func writeEntries(ctx context.Context, txn *spanner.ReadWriteTransaction, entries []ConfigEntry) error {
	if len(entries) == 0 {
		return nil
	}
	now := time.Now()
	mutations := make([]*spanner.Mutation, 0, len(entries))
	for _, e := range entries {
		_, err := txn.ReadRow(ctx, "system_config", spanner.Key{e.Key}, []string{colConfigKey})
		switch {
		case err == nil:
			// Existing key: created_at stays untouched.
			mutations = append(mutations, spanner.UpdateMap("system_config", map[string]any{
				colConfigKey:   e.Key,
				colConfigValue: e.Value,
				colDescription: e.Description,
				ColUpdatedAt:   now,
			}))

		case spanner.ErrCode(err) == codes.NotFound:
			mutations = append(mutations, spanner.InsertMap("system_config", map[string]any{
				colConfigKey:   e.Key,
				colConfigValue: e.Value,
				colDescription: e.Description,
				ColCreatedAt:   now,
				ColUpdatedAt:   now,
			}))

		default:
			return fmt.Errorf("checking config %q: %w", e.Key, err)
		}
	}
	if err := txn.BufferWrite(mutations); err != nil {
		return fmt.Errorf("buffering config writes: %w", err)
	}
	return nil
}

// quotedKeys renders an entry group for an error message, so a single-key write
// still reads as the one key it was.
func quotedKeys(entries []ConfigEntry) string {
	quoted := make([]string, len(entries))
	for i, e := range entries {
		quoted[i] = strconv.Quote(e.Key)
	}
	return strings.Join(quoted, ", ")
}

// Delete removes a configuration value.
func (r *SystemConfigRepository) Delete(ctx context.Context, key string) error {
	mutation := spanner.Delete("system_config", spanner.Key{key})

	_, err := r.client.Apply(ctx, []*spanner.Mutation{mutation})
	if err != nil {
		return fmt.Errorf("deleting config %q: %w", key, err)
	}

	return nil
}
