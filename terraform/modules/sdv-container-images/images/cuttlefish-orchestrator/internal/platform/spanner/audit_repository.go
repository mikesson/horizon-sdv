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
	"slices"
	"time"

	"cloud.google.com/go/spanner"
	"github.com/google/uuid"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// auditLogColumns is the explicit projection for audit_logs reads; SELECT *
// breaks on additive migrations because ToStruct rejects unknown columns.
const auditLogColumns = `log_id, timestamp, actor_email, action, resource_type, resource_id, request_id, changes, metadata`

// paramSince names the window cutoff every windowed read here binds.
const paramSince = "since"

// AuditRepository persists and reads audit_logs: the write side satisfies
// audit.Repository (drained by the platform sink's batcher), the read side
// (Query/Count) satisfies the admin feature's AuditReader directly.
type AuditRepository struct {
	client *Client
}

// NewAuditRepository creates a new audit repository
func NewAuditRepository(client *Client) *AuditRepository {
	return &AuditRepository{client: client}
}

// Compile-time check that AuditRepository implements the write port.
var _ audit.Repository = (*AuditRepository)(nil)

// EntryMutation builds the insert for one entry, defaulting its ID and
// timestamp exactly as the batched path does.
//
// It exists so a caller can commit an entry in the SAME transaction as the row
// it describes. The buffered sink is the right default, because audit is
// observability and blocking an operation on its own trail is worse than
// losing the trail. It drops under pressure, though, and an entry that is the
// only durable record of something (a hard-deleted host's create) cannot be
// allowed to vanish exactly when the system is busiest.
func (r *AuditRepository) EntryMutation(entry audit.Entry) (*spanner.Mutation, error) {
	if entry.ID == "" {
		entry.ID = uuid.New().String()
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}
	mutation, err := spanner.InsertStruct("audit_logs", models.FromAuditDomain(entry))
	if err != nil {
		return nil, fmt.Errorf("failed to create audit insert mutation: %w", err)
	}
	return mutation, nil
}

// WriteBatch stores multiple audit entries
func (r *AuditRepository) WriteBatch(ctx context.Context, entries []audit.Entry) error {
	mutations := make([]*spanner.Mutation, 0, len(entries))
	for _, entry := range entries {
		mutation, err := r.EntryMutation(entry)
		if err != nil {
			return err
		}
		mutations = append(mutations, mutation)
	}

	_, err := r.client.Apply(ctx, mutations)
	if err != nil {
		return fmt.Errorf("failed to write audit entries batch: %w", err)
	}

	return nil
}

// buildAuditFilter renders filter as a WHERE clause and its parameters. Query
// and Count share it so paginated totals always agree with the returned rows.
func buildAuditFilter(filter audit.ListFilter) (whereClause string, params map[string]any) {
	whereClause = ` WHERE 1=1`
	params = make(map[string]any)

	if filter.ActorEmail != nil {
		whereClause += ` AND actor_email = @actor_email`
		params["actor_email"] = *filter.ActorEmail
	}

	if filter.Action != nil {
		whereClause += ` AND action = @action`
		params["action"] = string(*filter.Action)
	}

	if filter.ResourceType != nil {
		whereClause += ` AND resource_type = @resource_type`
		params["resource_type"] = string(*filter.ResourceType)
	}

	if filter.ResourceID != nil {
		whereClause += ` AND resource_id = @resource_id`
		params["resource_id"] = *filter.ResourceID
	}

	if filter.Kind != nil {
		whereClause += ` AND JSON_VALUE(metadata, '$.kind') = @kind`
		params["kind"] = *filter.Kind
	}

	if filter.StartTime != nil {
		whereClause += ` AND timestamp >= @start_time`
		params["start_time"] = *filter.StartTime
	}

	if filter.EndTime != nil {
		whereClause += ` AND timestamp <= @end_time`
		params["end_time"] = *filter.EndTime
	}

	return whereClause, params
}

// demandWindowSQL reads the moments a declarative create found no capacity and
// provisioned a host, with the facts the resolver froze about each. It is the
// fleet autoscaler's demand signal.
//
// It reads audit ENTRIES, never host rows. A host row hard-deletes, so a
// row-based query would lose exactly the misses whose hosts were reclaimed
// fastest: the signal would read lowest when the fleet had been busiest, and
// the autoscaler would answer a burst by provisioning nothing.
//
// origin = 'auto' is the whole discriminator, and it is doing three jobs at
// once. It selects the declarative path, which provisions only after placement
// found nothing. It excludes 'pool', because standing capacity the keeper
// created is the answer to demand, not evidence of it. And it excludes 'batch',
// because a batch plans against the whole-fleet view and never misses
// placement. A filter written as "system-created" would sweep both back in.
//
// It PROJECTS the stamped facts and judges none of them. Which misses standing
// capacity could have absorbed is hostconfig.Miss.Reasons, in the domain, for
// three reasons: the rule is then unit-testable without an emulator, one read
// at one timestamp serves both the target and the per-reason split the card
// shows, and the NULL handling that rule needs (an unstamped fact is not a
// verdict) is visible Go instead of three-way SQL.
// Every predicate here is an index KEY column, so nothing depends on a STORED
// one, and the columns are read for every row the scan returns.
//
// The index guards are required. Without FORCE_INDEX the optimizer picks this
// index only by INFERRING null-exclusion from the equality on origin, which
// the documentation does not guarantee, and optimizer versions become default
// about thirty days after release, so it can change with nobody touching the
// database. Declining it costs no error: the query silently post-filters a
// 90-day table every thirty seconds forever. With the directive, a missing
// null filter is a hard error instead, which is the inversion that makes the
// guard worth having. The emulator hint means "this form was run against the
// real database". The emulator answers "not able to determine" for every
// form, correct or broken, so setting the hint without doing that is exactly
// how a refused query shipped green once already.
//
// VERDICT, the reference deployment 2026-08-10, this statement with BOUND
// parameters through a scratch client (the CLI cannot bind, and a literal
// probe judges a different query): accepted, and the plan's scan_target is
// `idx_audit_host_create`. So the hint is licensed and FORCE_INDEX with the
// explicit null filter still reads the index and does not fall back to the
// base table.
//
// The projection has gained `pool` since that reading. It is a STORED column of
// the same index, so the plan should be the one above. But "should" is what the
// verdict exists to replace, and re-taking it against real Spanner is what
// licenses the hint again.
const demandWindowSQL = `@{spanner_emulator.disable_query_null_filtered_index_check=true}
	      SELECT timestamp, pool, max_devices, density, gpu_required, fits_default_host
	      FROM audit_logs@{FORCE_INDEX=idx_audit_host_create}
	      WHERE action = @action
	        AND origin IS NOT NULL AND origin = @origin
	        AND timestamp >= @since
	      ORDER BY timestamp DESC
	      LIMIT @limit`

// Demand returns the placement misses since the given time, ascending, with the
// facts each was stamped with, and reports whether the read hit its sample bound
// (the tail is then missing and any peak derived from it reads low).
//
// The query orders DESC because that is the index's own order, and the result
// is reversed here: an ascending scan of a descending index is a sort the
// optimizer is free to do differently on a different day, and this query runs
// every keeper cycle forever.
func (r *AuditRepository) Demand(ctx context.Context, since time.Time) (hostconfig.DemandWindow, error) {
	stmt := spanner.Statement{
		SQL: demandWindowSQL,
		Params: map[string]any{
			"action":   string(audit.ActionHostCreate),
			"origin":   string(host.OriginAuto),
			paramSince: since,
			"limit":    int64(hostconfig.MaxDemandSample),
		},
	}
	misses, err := QueryAll(r.client.StaleSingle().Query(ctx, stmt), "host-create misses",
		(*missRow).toDomain)
	if err != nil {
		return hostconfig.DemandWindow{}, err
	}
	slices.Reverse(misses)
	return hostconfig.DemandWindow{
		Misses:  misses,
		Partial: len(misses) >= hostconfig.MaxDemandSample,
	}, nil
}

// missRow is the projection Demand reads. Every stamped fact is nullable: the
// columns are generated from metadata keys that only the placement resolver
// writes, so an entry from any other create path, or from before a key
// existed, carries NULL and must stay a miss nobody can rule out.
type missRow struct {
	Timestamp time.Time `spanner:"timestamp"`
	// Pool is NULL on every create judged into the default pool, which is what
	// the create stamps there and what every entry written before pools were
	// selectable carries. The domain folds both to the default pool.
	Pool            spanner.NullString `spanner:"pool"`
	MaxDevices      spanner.NullInt64  `spanner:"max_devices"`
	Density         spanner.NullInt64  `spanner:"density"`
	GPURequired     spanner.NullBool   `spanner:"gpu_required"`
	FitsDefaultHost spanner.NullBool   `spanner:"fits_default_host"`
}

func (r *missRow) toDomain() hostconfig.Miss {
	return hostconfig.Miss{
		At:              r.Timestamp,
		Pool:            r.Pool.StringVal,
		MaxDevices:      nullInt64(r.MaxDevices),
		Density:         nullInt64(r.Density),
		GPURequired:     nullBool(r.GPURequired),
		FitsDefaultHost: nullBool(r.FitsDefaultHost),
	}
}

// gpuDegradationSQL counts creates that wanted a GPU and were placed without
// one, against how many wanted one at all.
//
// Two counts from ONE scan, not two queries. A numerator and a denominator read
// at different instants can produce a ratio above 1 with both reads correct, and
// a figure that occasionally reports 140% is a figure nobody trusts again.
//
// gpu_degraded is a KEY of this index, so NULL_FILTERED holds the index to
// exactly the creates that stamped it: both arms, and nothing else. That is the
// exact opposite of idx_audit_host_create, where the facts are STORED and a
// NULL keeps its row because an unjudged miss is still a miss. Here an unstamped
// create is not a data point at all, and dropping it is what keeps the scan the
// size of the population instead of the size of the retention window.
//
// The index guards are the demand window's, for the reasons written there, and
// so is the verdict: the reference deployment 2026-08-10, bound parameters
// through a scratch client, accepted with scan_target `idx_audit_gpu_degraded`.
const gpuDegradationSQL = `@{spanner_emulator.disable_query_null_filtered_index_check=true}
	      SELECT COUNTIF(gpu_degraded) AS degraded, COUNT(*) AS wanted
	      FROM audit_logs@{FORCE_INDEX=idx_audit_gpu_degraded}
	      WHERE action = @action
	        AND gpu_degraded IS NOT NULL
	        AND timestamp >= @since`

// GPUDegradation reports how often a create that wanted a GPU got a host
// without one since the given time, beside how many wanted one at all.
//
// Bounded-stale like the demand window, and for the same reason: it is read over
// a horizon measured in hours, so seconds of lag cannot change what it says.
func (r *AuditRepository) GPUDegradation(ctx context.Context, since time.Time) (hostconfig.GPUDegradation, error) {
	stmt := spanner.Statement{
		SQL: gpuDegradationSQL,
		Params: map[string]any{
			"action":   string(audit.ActionDeviceCreate),
			paramSince: since,
		},
	}
	return QueryRow(r.client.StaleSingle().Query(ctx, stmt), "GPU degradation",
		(*degradationRow).toDomain)
}

// degradationRow is the two-count projection GPUDegradation reads. Neither is
// nullable: COUNTIF and COUNT answer 0 over an empty scan rather than NULL.
type degradationRow struct {
	Degraded int64 `spanner:"degraded"`
	Wanted   int64 `spanner:"wanted"`
}

func (r *degradationRow) toDomain() hostconfig.GPUDegradation {
	return hostconfig.GPUDegradation{Degraded: r.Degraded, Wanted: r.Wanted}
}

// nullInt64 and nullBool carry SQL NULL into the domain as absence. The domain
// takes no dependency on the Spanner driver, so the optionality has to change
// representation at this boundary rather than travel as a driver type.
func nullInt64(v spanner.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

func nullBool(v spanner.NullBool) *bool {
	if !v.Valid {
		return nil
	}
	return &v.Bool
}

// keeperActivitySQL counts what the autoscaler actually did in a window, by
// lane, from the durable trail.
//
// ONE scan with four arms, not four queries. The lanes are drawn beside each
// other and read as one picture, where a cycle that rescued instead of
// provisioning is what a reader is looking for, and four reads at four instants
// can show a rescue whose provision has not happened yet, or the reverse, with
// every read correct.
//
// It carries no index directive, matching the Spot churn KPIs it sits beside.
// The two hinted queries in this file are the keeper's own hot path, read every
// thirty seconds over a null-filtered index that the optimizer will only choose
// when told to; this one is a page's 24-hour aggregate over an ordinary action
// predicate, and forcing an index built for a different question would be a
// guess dressed as a guarantee.
//
// The lanes discriminate on what the writers actually stamp: origin is the
// generated column over the create's metadata, and the reclaim key is what
// separates the two populations that reach the single lease-shortening action.
const keeperActivitySQL = `SELECT
	        COUNTIF(action = @create_action AND origin = @pool_origin) AS provisions,
	        COUNTIF(action = @extend_action) AS rescues,
	        COUNTIF(action = @shorten_action AND JSON_VALUE(metadata, '$.` + audit.MetaReclaim + `') = @surplus) AS shortens,
	        COUNTIF(action = @shorten_action AND JSON_VALUE(metadata, '$.` + audit.MetaReclaim + `') = @dedicated) AS collapses
	      FROM audit_logs
	      WHERE action IN UNNEST(@actions)
	        AND timestamp >= @since`

// KeeperActivity reports the autoscaler's own actuation since the given time:
// hosts bought, leases renewed, surplus given back, empty dedicated hosts
// collapsed.
//
// It reads the audit trail and never the keeper itself. The keeper's counters
// are in-process atomics on the leader instance, so a console asking for them
// would get nothing on every other serving instance and a number up to a cycle
// stale on that one. The trail also outlives what it describes:
// these host rows hard-delete, which is the whole reason the entries exist.
func (r *AuditRepository) KeeperActivity(ctx context.Context, since time.Time) (hostconfig.KeeperActivity, error) {
	stmt := spanner.Statement{
		SQL: keeperActivitySQL,
		Params: map[string]any{
			"create_action":  string(audit.ActionHostCreate),
			"extend_action":  string(audit.ActionHostLeaseExtend),
			"shorten_action": string(audit.ActionHostLeaseShorten),
			"actions": []string{
				string(audit.ActionHostCreate),
				string(audit.ActionHostLeaseExtend),
				string(audit.ActionHostLeaseShorten),
			},
			"pool_origin": string(host.OriginPool),
			"surplus":     audit.ReclaimFleetSurplus,
			"dedicated":   audit.ReclaimDedicatedEmpty,
			paramSince:    since,
		},
	}
	return QueryRow(r.client.StaleSingle().Query(ctx, stmt), "keeper activity",
		(*activityRow).toDomain)
}

// activityRow is the four-count projection KeeperActivity reads. None is
// nullable: COUNTIF answers 0 over an empty scan rather than NULL.
type activityRow struct {
	Provisions int64 `spanner:"provisions"`
	Rescues    int64 `spanner:"rescues"`
	Shortens   int64 `spanner:"shortens"`
	Collapses  int64 `spanner:"collapses"`
}

func (r *activityRow) toDomain() hostconfig.KeeperActivity {
	return hostconfig.KeeperActivity{
		Provisions: r.Provisions,
		Rescues:    r.Rescues,
		Shortens:   r.Shortens,
		Collapses:  r.Collapses,
	}
}

// Query retrieves audit entries matching the filter
func (r *AuditRepository) Query(ctx context.Context, filter audit.ListFilter) ([]audit.Entry, error) {
	whereClause, params := buildAuditFilter(filter)
	sql := `SELECT ` + auditLogColumns + ` FROM audit_logs` + whereClause + ` ORDER BY timestamp DESC`

	if filter.Limit > 0 {
		sql += fmt.Sprintf(` LIMIT %d`, filter.Limit)
	}
	if filter.Offset > 0 {
		sql += fmt.Sprintf(` OFFSET %d`, filter.Offset)
	}

	stmt := spanner.Statement{SQL: sql, Params: params}
	return QueryAll(r.client.StaleSingle().Query(ctx, stmt), "audit logs", (*models.AuditRow).ToDomain)
}

// Count returns the number of entries matching the filter
func (r *AuditRepository) Count(ctx context.Context, filter audit.ListFilter) (int64, error) {
	whereClause, params := buildAuditFilter(filter)

	stmt := spanner.Statement{SQL: `SELECT COUNT(*) FROM audit_logs` + whereClause, Params: params}
	return QueryCount(r.client.StaleSingle().Query(ctx, stmt), "audit logs")
}
