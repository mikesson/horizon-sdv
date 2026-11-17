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
	"maps"
	"strings"
	"time"

	"cloud.google.com/go/spanner"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"

	hostDomain "cloud-cuttlefish-orchestrator/internal/domain/host"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// hostColumnsSQL is the explicit projection for host queries; SELECT * breaks
// on additive migrations because ToStruct rejects unknown columns.
var hostColumnsSQL = strings.Join(hostColumns(), ", ")

// Column and parameter names repeated across queries and mutations (goconst).
const (
	colHostID   = "host_id"
	colStatus   = "status"
	colOrigin   = "origin"
	paramNow    = "now"
	paramLimit  = "limit"
	paramCutoff = "cutoff"
)

// HostRepository implements the persistence layer for Host entities using Cloud Spanner.
//
// It handles the mapping between the pure domain Host entity and the database schema,
// including complex queries for capacity planning and atomic updates.
//
// Hosts-table column ownership: the device-derived columns
// (current_device_count, protected_device_count, lifecycle_mode, and the
// monotone expires_at raise) are written by the DEVICE repository
// (hostCountersMutations in features/device), inside device-write commits;
// this repository owns the rest (status, quota coupling, tombstones) and
// only reads those counters (e.g. the capacity re-check in placement
// queries). The reverse edge exists too: MarkHostLost writes devices rows,
// failing a lost host's live devices over in the same commit, and
// BatchUpdateGCEState settles a newly stopped host's devices to stopped.
type HostRepository struct {
	client *spannerplatform.Client
}

// NewHostRepository creates a new host repository
func NewHostRepository(client *spannerplatform.Client) *HostRepository {
	return &HostRepository{client: client}
}

// Get retrieves a host by ID
func (r *HostRepository) Get(ctx context.Context, id string) (*hostDomain.Host, error) {
	row, err := r.client.Single().ReadRow(ctx, "hosts", spanner.Key{id}, hostColumns())
	if err != nil {
		if spanner.ErrCode(err) == codes.NotFound {
			return nil, hostDomain.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get host: %w", err)
	}

	var hRow models.HostRow
	if err := row.ToStruct(&hRow); err != nil {
		return nil, fmt.Errorf("failed to scan host row: %w", err)
	}

	return hRow.ToDomain(), nil
}

// GetByIDs retrieves the hosts with the given IDs in one query, keyed by ID.
// Missing hosts are simply absent from the map. It backs the device-list
// enrichment, so it uses the same bounded-staleness read as List.
func (r *HostRepository) GetByIDs(ctx context.Context, ids []string) (map[string]*hostDomain.Host, error) {
	hosts := make(map[string]*hostDomain.Host, len(ids))
	if len(ids) == 0 {
		return hosts, nil
	}

	stmt := spanner.Statement{
		SQL:    `SELECT ` + hostColumnsSQL + ` FROM hosts WHERE host_id IN UNNEST(@ids)`,
		Params: map[string]any{"ids": ids},
	}
	if err := r.client.StaleSingle().Query(ctx, stmt).Do(func(row *spanner.Row) error {
		var hRow models.HostRow
		if err := row.ToStruct(&hRow); err != nil {
			return err
		}
		h := hRow.ToDomain()
		hosts[h.ID] = h
		return nil
	}); err != nil {
		return nil, fmt.Errorf("getting hosts by IDs: %w", err)
	}

	return hosts, nil
}

// buildHostFilter renders filter as a WHERE clause and its parameters. List
// and Count share it so paginated totals always agree with the returned rows.
func buildHostFilter(filter hostDomain.ListFilter) (whereClause string, params map[string]any) {
	whereClause = ` WHERE 1=1`
	params = make(map[string]any)

	if filter.Zone != nil {
		whereClause += ` AND zone = @zone`
		params["zone"] = *filter.Zone
	}

	if filter.RequestedOrPhysicalZone != nil {
		whereClause += ` AND (requested_zone = @either_zone OR zone = @either_zone)`
		params["either_zone"] = *filter.RequestedOrPhysicalZone
	}

	if filter.Status != nil {
		whereClause += ` AND status = @status`
		params[colStatus] = string(*filter.Status)
	} else if !filter.IncludeDeleted {
		whereClause += ` AND status != 'deleted'`
	}

	if filter.CreatedBy != nil {
		whereClause += ` AND created_by = @created_by`
		params["created_by"] = *filter.CreatedBy
	}

	if filter.Name != nil {
		// live_name is NULL on a deleted row, so this also excludes deleted
		// hosts. The IS NOT NULL is the null-filtered index's requirement.
		whereClause += ` AND live_name IS NOT NULL AND live_name = @name`
		params["name"] = *filter.Name
	}

	if filter.Degraded != nil {
		if *filter.Degraded {
			whereClause += ` AND degraded_at IS NOT NULL`
		} else {
			whereClause += ` AND degraded_at IS NULL`
		}
	}

	if filter.Class != nil {
		predicate, classParams := classFilter(*filter.Class)
		whereClause += predicate
		maps.Copy(params, classParams)
	}

	return whereClause, params
}

// hostListSource returns the statement hint and FROM clause for List and
// Count. A name lookup is forced onto idx_hosts_live_name. The emulator cannot
// decide whether a null-filtered index may answer a query, so its check is
// switched off; real Spanner still enforces it.
func hostListSource(filter hostDomain.ListFilter) (hint, from string) {
	if filter.Name == nil {
		return "", ` FROM hosts`
	}
	return `@{spanner_emulator.disable_query_null_filtered_index_check=true} `,
		` FROM hosts@{FORCE_INDEX=idx_hosts_live_name}`
}

// List retrieves hosts matching the filter
func (r *HostRepository) List(ctx context.Context, filter hostDomain.ListFilter) ([]*hostDomain.Host, error) {
	where, params := buildHostFilter(filter)
	hint, from := hostListSource(filter)
	if filter.AfterID != "" {
		// Spanner has no row-value comparison, so the keyset predicate is
		// spelled out over the two sort columns.
		where += ` AND (created_at < @after_created_at OR (created_at = @after_created_at AND host_id < @after_id))`
		params["after_created_at"] = filter.AfterCreatedAt
		params["after_id"] = filter.AfterID
	}
	// host_id breaks the tie: created_at alone is not a total order, and a
	// keyset or offset page over an underdetermined order can return a row
	// twice and skip another.
	sql := hint + `SELECT ` + hostColumnsSQL + from + where +
		` ORDER BY created_at DESC, host_id DESC`

	if filter.Limit > 0 {
		sql += fmt.Sprintf(` LIMIT %d`, filter.Limit)
	}
	if filter.Offset > 0 {
		sql += fmt.Sprintf(` OFFSET %d`, filter.Offset)
	}

	stmt := spanner.Statement{SQL: sql, Params: params}
	// Bounded-staleness read: list/dashboard polling can be served by non-leader
	// replicas, offloading the leader under heavy read traffic.
	return spannerplatform.QueryAll(r.client.StaleSingle().Query(ctx, stmt), "hosts", (*models.HostRow).ToDomain)
}

// Count returns the number of hosts matching the filter, ignoring pagination.
// It applies the same predicates as List.
func (r *HostRepository) Count(ctx context.Context, filter hostDomain.ListFilter) (int64, error) {
	where, params := buildHostFilter(filter)
	hint, from := hostListSource(filter)
	stmt := spanner.Statement{SQL: hint + `SELECT COUNT(*)` + from + where, Params: params}
	return spannerplatform.QueryCount(r.client.StaleSingle().Query(ctx, stmt), "hosts")
}

// Create stores a host row without touching quota. Not part of the domain
// Repository port: production flows go through CreateWithQuotaIncrement,
// which admits against the quota in the same commit. Kept as fixture surface
// for repository tests that need rows without quota coupling.
func (r *HostRepository) Create(ctx context.Context, h *hostDomain.Host) error {
	if h.ID == "" {
		h.ID = uuid.New().String()
	}
	now := time.Now()
	h.CreatedAt = now
	h.UpdatedAt = now

	if err := h.Validate(); err != nil {
		return fmt.Errorf("invalid host: %w", err)
	}

	hRow := models.FromHostDomain(h)
	mutation, err := spanner.InsertStruct("hosts", hRow)
	if err != nil {
		return fmt.Errorf("failed to create insert mutation: %w", err)
	}

	_, err = r.client.Apply(ctx, []*spanner.Mutation{mutation})
	if err != nil {
		return fmt.Errorf("failed to create host: %w", err)
	}

	return nil
}

// Mutate applies fn to the host inside a read-write transaction. The row is
// re-read under transaction locks, fn performs its checks and mutations, and
// the full row plus any extra mutations (e.g. a task enqueue) are committed
// together. An error from fn aborts everything, including the extras. It
// replaces read-modify-write flows built on blind full-row updates, which
// silently overwrote concurrent writers.
func (r *HostRepository) Mutate(ctx context.Context, id string, fn func(*hostDomain.Host) error, extra ...*spanner.Mutation) (*hostDomain.Host, error) {
	return r.mutateTx(ctx, id, func(_ context.Context, _ *spanner.ReadWriteTransaction, h *hostDomain.Host) error {
		return fn(h)
	}, extra...)
}

// mutateTx is Mutate with the transaction handed to fn, for the writes whose
// guard is a fact about another table. Only the lease shorten needs it: its
// refusal turns on the host's live DEVICE rows, which must be read under the
// same locks as the write or the guard is reading a world that has already
// moved. Mutate is this with the handle dropped, rather than a second copy of
// the read-modify-write-validate-commit sequence.
func (r *HostRepository) mutateTx(ctx context.Context, id string, fn func(context.Context, *spanner.ReadWriteTransaction, *hostDomain.Host) error, extra ...*spanner.Mutation) (*hostDomain.Host, error) {
	var result *hostDomain.Host
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		result = nil // Reset: the transaction body may run more than once.
		row, err := txn.ReadRow(ctx, "hosts", spanner.Key{id}, hostColumns())
		if spanner.ErrCode(err) == codes.NotFound {
			return hostDomain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("reading host: %w", err)
		}
		var hRow models.HostRow
		if err := row.ToStruct(&hRow); err != nil {
			return fmt.Errorf("scanning host row: %w", err)
		}
		h := hRow.ToDomain()

		if err := fn(ctx, txn, h); err != nil {
			return err
		}

		h.UpdatedAt = time.Now()
		if err := h.Validate(); err != nil {
			return fmt.Errorf("invalid host: %w", err)
		}
		hostMut, err := spanner.UpdateStruct("hosts", models.FromHostDomain(h))
		if err != nil {
			return fmt.Errorf("building host update: %w", err)
		}
		result = h
		return txn.BufferWrite(append([]*spanner.Mutation{hostMut}, extra...))
	})
	if err != nil {
		return nil, fmt.Errorf("mutating host %s: %w", id, err)
	}
	return result, nil
}

// Delete removes a host row without releasing quota. Not part of the domain
// Repository port: production flows go through DeleteWithQuotaDecrement.
// Kept as fixture surface for repository tests.
func (r *HostRepository) Delete(ctx context.Context, id string) error {
	mutation := spanner.Delete("hosts", spanner.Key{id})

	_, err := r.client.Apply(ctx, []*spanner.Mutation{mutation})
	if err != nil {
		return fmt.Errorf("failed to delete host: %w", err)
	}

	return nil
}

// FindAvailableHosts finds hosts with capacity in a zone
func (r *HostRepository) FindAvailableHosts(ctx context.Context, zone string, requiredSlots int64) ([]*hostDomain.Host, error) {
	stmt := spanner.Statement{
		SQL: `SELECT ` + hostColumnsSQL + ` FROM hosts
              WHERE zone = @zone
              AND status = 'running'
              AND degraded_at IS NULL
              AND (max_devices - current_device_count) >= @required_slots
              ORDER BY (max_devices - current_device_count) DESC`,
		Params: map[string]any{
			"zone":           zone,
			"required_slots": requiredSlots,
		},
	}

	return spannerplatform.QueryAll(r.client.Single().Query(ctx, stmt),
		"available hosts", (*models.HostRow).ToDomain)
}

func hostColumns() []string {
	return []string{
		colHostID, "name", "zone", "requested_zone", "zone_pinned",
		"gce_instance_name", "gce_project",
		"machine_type", "gpu_type", "gpu_count",
		"min_cpu_platform", "boot_disk_size_gb", "boot_image", "provisioning_model",
		colStatus, "ip_address", "internal_ip", "host_orchestrator_port",
		"max_devices", "current_device_count", "exclusive", "foreign_cvds", "batch_id",
		colOrigin, "pool", "created_by", "error_message",
		"error_type", "error_code", "error_suggestion", "error_alternative_zones",
		"error_retryable", "error_occurred_at",
		"stopped_at", "vm_terminates_at", "preempted_at", "preempted_incarnation", "preemption_count", "last_preemption_id", "incarnation",
		"consecutive_create_fails", "last_create_fail_reason", "degraded_at", "degraded_reason",
		"provision_blocked_at", "provision_blocked_reason",
		"desired_state", "vm_status", "vm_observed_at", "stalled_reason", "ho_ready_at",
		"created_at", colUpdatedAt,
		"lifecycle_mode", "ttl_seconds", "expires_at", "protected_device_count",
	}
}
