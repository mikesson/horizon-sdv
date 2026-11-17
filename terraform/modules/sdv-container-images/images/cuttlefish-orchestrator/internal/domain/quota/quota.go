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

// Package quota provides host quota management for the platform.
//
// The quota system enforces a single global limit on how many host VMs can
// be provisioned. Device limits are handled separately by host capacity
// (Host.MaxDevices), not by this quota system.
package quota

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Scope represents where the quota applies.
type Scope string

// ScopePlatform represents the global system limit, the only scope.
const ScopePlatform Scope = "platform"

// GlobalScopeID is the scope_id used for platform-level quota.
const GlobalScopeID = "global"

// Quota represents a host quota with usage tracking.
type Quota struct {
	Scope     Scope     // always "platform"
	ScopeID   string    // always "global"
	MaxHosts  int64     // Maximum hosts allowed
	UsedHosts int64     // Current host count
	CreatedAt time.Time // When quota was created
	UpdatedAt time.Time // When quota was last modified

	// HighWaterUsedHosts is the peak used_hosts recorded within the rolling
	// watermark window (see HighWaterWindow); HighWaterAt is when it was set.
	// The watermark is maintained by the host-create quota increment, the only
	// path that raises used_hosts.
	HighWaterUsedHosts int64
	HighWaterAt        *time.Time
}

// HighWaterWindow is the rolling window for the usage watermark: a peak older
// than this is replaced rather than raised, so the gauge answers "how full did
// the pool get this week".
const HighWaterWindow = 7 * 24 * time.Hour

// Available returns remaining host capacity.
func (q *Quota) Available() int64 {
	if q.UsedHosts >= q.MaxHosts {
		return 0
	}
	return q.MaxHosts - q.UsedHosts
}

// CanProvision checks if n hosts can be provisioned.
func (q *Quota) CanProvision(n int64) bool {
	return q.UsedHosts+n <= q.MaxHosts
}

// Percentage returns usage as 0-100.
func (q *Quota) Percentage() float64 {
	if q.MaxHosts == 0 {
		return 0
	}
	return float64(q.UsedHosts) / float64(q.MaxHosts) * 100
}

// IsExhausted returns true if no capacity remains.
func (q *Quota) IsExhausted() bool {
	return q.UsedHosts >= q.MaxHosts
}

// Validate ensures the quota data is consistent.
func (q *Quota) Validate() error {
	if q.Scope == "" {
		return errors.New("scope is required")
	}
	if q.Scope != ScopePlatform {
		return fmt.Errorf("invalid scope: %s", q.Scope)
	}
	if q.ScopeID == "" {
		return errors.New("scope_id is required")
	}
	if q.MaxHosts < 0 {
		return errors.New("max_hosts cannot be negative")
	}
	if q.UsedHosts < 0 {
		return errors.New("used_hosts cannot be negative")
	}
	return nil
}

// ExceededError is returned when host provisioning would exceed the limit.
type ExceededError struct {
	Scope     Scope
	ScopeID   string
	Requested int64
	Available int64
	MaxHosts  int64
}

// Error implements the error interface.
func (e *ExceededError) Error() string {
	return fmt.Sprintf("host quota exceeded: requested %d, available %d/%d",
		e.Requested, e.Available, e.MaxHosts)
}

// NotFoundError is returned when a quota does not exist.
type NotFoundError struct {
	Scope   Scope
	ScopeID string
}

// Error implements the error interface.
func (e *NotFoundError) Error() string {
	return fmt.Sprintf("quota not found: %s/%s", e.Scope, e.ScopeID)
}

// Repository defines data access for quotas.
//
// used_hosts ownership: the counter is written by the HOST feature's atomic
// commits (CreateWithQuotaIncrement, DeleteWithQuotaDecrement, MarkHostLost),
// inline in the same transactions as the host rows, never through this port.
// Drift healing lives there too (ReconcileUsedHosts), because only the host
// repository can put the recount and the healing write into one serializable
// transaction. This port owns the limit (Set).
type Repository interface {
	// Get retrieves a quota by scope. Returns NotFoundError if not found.
	Get(ctx context.Context, scope Scope, scopeID string) (*Quota, error)

	// Set creates or updates a quota.
	Set(ctx context.Context, q *Quota) error
}

// Service defines business logic for quota management.
type Service interface {
	// SetLimit updates the max_hosts for a quota (creates if not exists).
	SetLimit(ctx context.Context, scope Scope, scopeID string, maxHosts int64) error

	// Get retrieves quota for a scope.
	Get(ctx context.Context, scope Scope, scopeID string) (*Quota, error)

	// ReconcileAll recalculates used_hosts for all quotas based on actual host counts.
	// This corrects any drift between quota counters and reality.
	// Returns the number of quotas that had drift corrected.
	ReconcileAll(ctx context.Context) (int, error)
}

// DefaultPlatformMaxHosts is the default limit for platform quota.
const DefaultPlatformMaxHosts = 100
