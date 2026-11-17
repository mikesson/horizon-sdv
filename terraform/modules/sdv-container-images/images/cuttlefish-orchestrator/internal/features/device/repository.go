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

package devicefeature

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"cloud.google.com/go/spanner"
	"github.com/google/uuid"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"

	deviceDomain "cloud-cuttlefish-orchestrator/internal/domain/device"
	hostDomain "cloud-cuttlefish-orchestrator/internal/domain/host"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// deviceColumnsSQL is the explicit projection for device queries; SELECT *
// breaks on additive migrations because ToStruct rejects unknown columns.
var deviceColumnsSQL = strings.Join(deviceColumns(), ", ")

// Column names repeated across queries and mutations (goconst).
const (
	colName            = "name"
	colStatus          = "status"
	colHostID          = "host_id"
	colDeviceID        = "device_id"
	colCreatedAtDev    = "created_at"
	colUpdatedAtDev    = "updated_at"
	colHealthStatus    = "health_status"
	colCVDStatus       = "cvd_status"
	colInstances       = "instances"
	colLastHealthCheck = "last_health_check"
	colLifecycleMode   = "lifecycle_mode"
	colSnapshotID      = "snapshot_id"
	colHostDevCount    = "current_device_count"
	paramLimit         = "limit"
	paramDeviceID      = "deviceID"
)

func deviceColumns() []string {
	return []string{
		colDeviceID, colHostID, colName, "cvd_id", colStatus,
		"build_id", "build_target",
		"adb_port", "webrtc_port", "gpu_mode",
		"created_by", "error_message", "started_at", "stopped_at",
		"tags", colCreatedAtDev, colUpdatedAtDev,
		colHealthStatus, colLastHealthCheck, "consecutive_fails",
		"last_failure_reason", "last_healthy_at", colCVDStatus, "boot_observation",
		colInstances,
		colLifecycleMode, "ttl_seconds", "expires_at",
		"pinned_at", "pinned_by", "pinned_reason", "pinned_reason_detail",
		"pinned_until", "extend_count",
		"desired_spec",
		"recreate_count", "recreate_window_start",
		"restart_count", "restart_window_start",
		"bugreport_captured_at",
		"batch_id",
	}
}

// DeviceRepository implements deviceDomain.Repository using Cloud Spanner.
//
// Hosts-table column ownership: this repository is the authoritative writer
// of the hosts table's device-derived columns (current_device_count,
// protected_device_count, lifecycle_mode, and the monotone expires_at raise)
// via hostCountersMutations, so a device write and its host-row
// adjustment always share one commit. The host repository owns every other
// hosts column (status, quota coupling, tombstones); see the matching note
// on HostRepository in features/host.
type DeviceRepository struct {
	client *spannerplatform.Client
}

// NewDeviceRepository creates a new device repository
func NewDeviceRepository(client *spannerplatform.Client) *DeviceRepository {
	return &DeviceRepository{client: client}
}

// Get retrieves a device by ID
func (r *DeviceRepository) Get(ctx context.Context, id string) (*deviceDomain.Device, error) {
	stmt := spanner.Statement{
		SQL: `SELECT ` + deviceColumnsSQL + ` FROM devices WHERE device_id = @id`,
		Params: map[string]any{
			"id": id,
		},
	}

	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return nil, deviceDomain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get device: %w", err)
	}

	var dRow models.DeviceRow
	if err := row.ToStruct(&dRow); err != nil {
		return nil, fmt.Errorf("failed to scan device row: %w", err)
	}

	return dRow.ToDomain(), nil
}

// buildDeviceFilter renders filter as a WHERE clause and its parameters.
// List and Count share it so paginated totals always agree with the returned
// rows. (Pagination and the Strong read choice are List-only concerns and
// stay there.)
func buildDeviceFilter(filter deviceDomain.ListFilter) (whereClause string, params map[string]any) {
	whereClause = ` WHERE 1=1`
	params = make(map[string]any)

	if filter.HostID != nil {
		whereClause += ` AND host_id = @host_id`
		params[colHostID] = *filter.HostID
	}

	if filter.Status != nil {
		whereClause += ` AND status = @status`
		params["status"] = string(*filter.Status)
	} else if !filter.IncludeDeleted {
		whereClause += ` AND status != 'deleted'`
	}

	if filter.HealthStatus != nil {
		whereClause += ` AND health_status = @health_status`
		params["health_status"] = string(*filter.HealthStatus)
	}

	if filter.CreatedBy != nil {
		whereClause += ` AND created_by = @created_by`
		params["created_by"] = *filter.CreatedBy
	}

	if filter.Name != nil {
		// live_name is NULL on a deleted row, so this also excludes deleted
		// devices. The IS NOT NULL is the null-filtered index's requirement.
		whereClause += ` AND live_name IS NOT NULL AND live_name = @name`
		params[colName] = *filter.Name
	}

	if filter.BuildID != nil {
		whereClause += ` AND build_id = @build_id`
		params["build_id"] = *filter.BuildID
	}

	if filter.BatchID != nil {
		whereClause += ` AND batch_id = @batch_id`
		params["batch_id"] = *filter.BatchID
	}

	if filter.BugreportUncaptured {
		whereClause += ` AND bugreport_captured_at IS NULL`
	}

	return whereClause, params
}

// deviceListSource returns the statement hint and FROM clause for List and
// Count. A name lookup is forced onto idx_devices_live_name. The emulator
// cannot decide whether a null-filtered index may answer a query, so its check
// is switched off; real Spanner still enforces it.
func deviceListSource(filter deviceDomain.ListFilter) (hint, from string) {
	if filter.Name == nil {
		return "", ` FROM devices`
	}
	return `@{spanner_emulator.disable_query_null_filtered_index_check=true} `,
		` FROM devices@{FORCE_INDEX=idx_devices_live_name}`
}

// List retrieves devices matching the filter
func (r *DeviceRepository) List(ctx context.Context, filter deviceDomain.ListFilter) ([]*deviceDomain.Device, error) {
	where, params := buildDeviceFilter(filter)
	hint, from := deviceListSource(filter)
	if filter.AfterID != "" {
		// Spanner has no row-value comparison, so the keyset predicate is
		// spelled out over the two sort columns.
		where += ` AND (created_at < @after_created_at OR (created_at = @after_created_at AND device_id < @after_id))`
		params["after_created_at"] = filter.AfterCreatedAt
		params["after_id"] = filter.AfterID
	}
	// device_id breaks the tie. A batch creates its devices in one loop, so
	// rows routinely share a created_at, and a keyset over an incomplete order
	// would skip the rest of a tied run.
	sql := hint + `SELECT ` + deviceColumnsSQL + from + where +
		` ORDER BY created_at DESC, device_id DESC`

	if filter.Limit > 0 {
		sql += fmt.Sprintf(` LIMIT %d`, filter.Limit)
	}
	if filter.Offset > 0 {
		sql += fmt.Sprintf(` OFFSET %d`, filter.Offset)
	}

	stmt := spanner.Statement{SQL: sql, Params: params}
	// Bounded-staleness read by default: list/dashboard polling can be served by
	// non-leader replicas, offloading the leader under heavy read traffic.
	// Strong opts into read-your-writes for callers that just wrote the rows
	// (a stale read can miss a row committed within the staleness bound).
	tx := r.client.StaleSingle()
	if filter.Strong {
		tx = r.client.Single()
	}
	return spannerplatform.QueryAll(tx.Query(ctx, stmt), "devices", (*models.DeviceRow).ToDomain)
}

// Count returns the number of devices matching the filter, ignoring
// pagination. It applies the same predicates as List.
func (r *DeviceRepository) Count(ctx context.Context, filter deviceDomain.ListFilter) (int64, error) {
	where, params := buildDeviceFilter(filter)
	hint, from := deviceListSource(filter)
	stmt := spanner.Statement{SQL: hint + `SELECT COUNT(*)` + from + where, Params: params}
	return spannerplatform.QueryCount(r.client.StaleSingle().Query(ctx, stmt), "devices")
}

// CountUndeleted returns how many device rows a sweep can still reach: every
// row that is not already deleted or on its way there.
//
// This is a WIDER predicate than CountLiveByHost below, and it is named for the
// predicate rather than for "live" so the two cannot be mistaken for each
// other. That one excludes error rows because it gates a destructive host-wide
// actuation and an error row holds evidence with no tenant. This one only sizes
// the reaper's per-cycle batch against the population its listings are drawn
// from, using the same NOT-IN set ListExpired and ListLapsedPins apply, so an
// error row belongs in it. It holds slots and it expires like any other.
//
// The read is bounded-stale. It sizes a batch bound, and a count seconds out of
// date changes that bound by nothing that matters.
func (r *DeviceRepository) CountUndeleted(ctx context.Context) (int64, error) {
	stmt := spanner.Statement{
		SQL: `SELECT COUNT(*) FROM devices WHERE status NOT IN ('deleted', 'deleting')`,
	}
	return spannerplatform.QueryCount(r.client.StaleSingle().Query(ctx, stmt), "undeleted devices")
}

// CountLiveByHost returns how many devices on a host still back (or are
// acquiring) a CVD, meaning every state except error/deleting/deleted. A failed
// create leaves an error row with no CVD, so those are excluded. They hold the
// poison evidence and back no tenant, so a host-wide reset destroys nothing a
// user is using.
//
// The read is strong on purpose. This count is the gate before destructive
// host-wide actuations (the remediation `cvd reset`), so it must see a device
// admitted an instant ago, and a bounded-staleness read would silently widen
// that window with the shared staleness setting. It runs once per detector cycle
// and per remediation, so the strong read costs nothing that matters.
func (r *DeviceRepository) CountLiveByHost(ctx context.Context, hostID string) (int64, error) {
	stmt := spanner.Statement{
		SQL: `SELECT COUNT(*) FROM devices
		      WHERE host_id = @host_id AND status NOT IN ('error', 'deleting', 'deleted')`,
		Params: map[string]any{colHostID: hostID},
	}
	return spannerplatform.QueryCount(r.client.Single().Query(ctx, stmt),
		"live devices on host "+hostID)
}

// CountLiveInstancesByHost returns how many instance slots the live devices on
// a host desire: the same population CountLiveByHost counts, denominated the
// way max_devices and current_device_count are. The two are not
// interchangeable, because one live device can want eight slots, and picking
// the wrong one silently under-gates whatever it guards.
//
// It reads whole rows rather than a SUM because the desired count lives inside
// the opaque desired_spec JSON, which no query can reach into; the row model is
// the single decoder for it, so both this and the accounting read the same
// value. Called once per max_devices change, so the cost is irrelevant.
//
// The read is strong for the same reason CountLiveByHost takes one. It gates a
// capacity decision that must see a device admitted an instant ago.
func (r *DeviceRepository) CountLiveInstancesByHost(ctx context.Context, hostID string) (int64, error) {
	stmt := spanner.Statement{
		SQL: `SELECT ` + deviceColumnsSQL + ` FROM devices
		      WHERE host_id = @host_id AND status NOT IN ('error', 'deleting', 'deleted')`,
		Params: map[string]any{colHostID: hostID},
	}
	devices, err := spannerplatform.QueryAll(r.client.Single().Query(ctx, stmt),
		"live devices on host "+hostID, (*models.DeviceRow).ToDomain)
	if err != nil {
		return 0, err
	}
	var slots int64
	for _, d := range devices {
		slots += int64(d.DesiredInstanceCount())
	}
	return slots, nil
}

// ListByHost retrieves all devices on a host
func (r *DeviceRepository) ListByHost(ctx context.Context, hostID string) ([]*deviceDomain.Device, error) {
	return r.List(ctx, deviceDomain.ListFilter{HostID: &hostID})
}

// Create stores a new device
func (r *DeviceRepository) Create(ctx context.Context, d *deviceDomain.Device) error {
	if d.ID == "" {
		d.ID = uuid.New().String()
	}
	now := time.Now()
	d.CreatedAt = now
	d.UpdatedAt = now

	if err := d.Validate(); err != nil {
		return fmt.Errorf("invalid device: %w", err)
	}

	dRow := models.FromDeviceDomain(d)
	mutation, err := spanner.InsertStruct("devices", dRow)
	if err != nil {
		return fmt.Errorf("failed to create insert mutation: %w", err)
	}

	_, err = r.client.Apply(ctx, []*spanner.Mutation{mutation})
	if err != nil {
		return fmt.Errorf("failed to create device: %w", err)
	}

	return nil
}

// Mutate applies fn to the device inside a read-write transaction. The row is
// re-read under transaction locks, fn performs its checks and mutations, and
// the full row is written back in the same commit, so a concurrent writer can
// never be silently overwritten. An error from fn aborts the transaction and
// is returned wrapped (sentinel matching via errors.Is still works).
//
// A pinned-state change made by fn adjusts the host's protected device count
// in the same commit, so the counter cannot drift from the pin state it
// mirrors.
func (r *DeviceRepository) Mutate(ctx context.Context, id string, fn func(*deviceDomain.Device) error) (*deviceDomain.Device, error) {
	var result *deviceDomain.Device
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		result = nil // Reset: the transaction body may run more than once.
		d, err := readDeviceForUpdate(ctx, txn, id)
		if err != nil {
			return err
		}
		// A tombstone is immutable. Its delete transition already settled the
		// host counters, so a lifecycle write landing after it (a pin racing
		// the delete) would adjust them with no compensating transition ever
		// coming. The robustness harness caught exactly that leak.
		if d.Status == deviceDomain.StatusDeleted {
			return deviceDomain.ErrNotFound
		}
		pinnedBefore := d.Lifecycle.IsPinned()
		expiryBefore := d.Lifecycle.ExpiresAt

		if err := fn(d); err != nil {
			return err
		}

		d.UpdatedAt = time.Now()
		if err := d.Validate(); err != nil {
			return fmt.Errorf("invalid device: %w", err)
		}
		mutation, err := spanner.UpdateStruct("devices", models.FromDeviceDomain(d))
		if err != nil {
			return fmt.Errorf("building device update: %w", err)
		}
		muts := []*spanner.Mutation{mutation}

		hostMuts, err := hostCountersForLifecycleChange(ctx, txn, d, pinnedBefore, expiryBefore)
		if err != nil {
			return err
		}
		muts = append(muts, hostMuts...)

		result = d
		return txn.BufferWrite(muts)
	})
	if err != nil {
		return nil, fmt.Errorf("mutating device %s: %w", id, err)
	}
	return result, nil
}

// hostCountersForLifecycleChange returns the host-side mutations a device
// lifecycle change implies, given the pin state and expiry the device carried
// before it. A pin flip moves the protected count, and a raised device expiry
// (extend, unpin) is a new promise the host lease must outlive. Nothing to
// adjust yields no mutations.
func hostCountersForLifecycleChange(ctx context.Context, txn *spanner.ReadWriteTransaction, d *deviceDomain.Device, pinnedBefore bool, expiryBefore *time.Time) ([]*spanner.Mutation, error) {
	var promise *time.Time
	if after := d.Lifecycle.ExpiresAt; after != nil && (expiryBefore == nil || after.After(*expiryBefore)) {
		promise = after
	}
	pinnedAfter := d.Lifecycle.IsPinned()
	if pinnedAfter == pinnedBefore && promise == nil {
		return nil, nil
	}
	var delta int64
	switch {
	case pinnedAfter && !pinnedBefore:
		delta = 1
	case !pinnedAfter && pinnedBefore:
		delta = -1
	}
	// A raise from an unpinned device is an extend and must fit the VM. The
	// raise an unpin carries restores time already granted, so it skips the
	// check.
	return hostCountersMutations(ctx, txn, d.HostID, 0, delta, devicePromise{expiry: promise, fitVM: !pinnedBefore})
}

// readDeviceForUpdate loads a device by ID inside txn, acquiring the row locks
// that make the enclosing read-modify-write safe.
func readDeviceForUpdate(ctx context.Context, txn *spanner.ReadWriteTransaction, id string) (*deviceDomain.Device, error) {
	iter := txn.Query(ctx, spanner.Statement{
		SQL:    `SELECT ` + deviceColumnsSQL + ` FROM devices WHERE device_id = @id`,
		Params: map[string]any{"id": id},
	})
	defer iter.Stop()

	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return nil, deviceDomain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("reading device %s: %w", id, err)
	}
	var dRow models.DeviceRow
	if err := row.ToStruct(&dRow); err != nil {
		return nil, fmt.Errorf("scanning device %s: %w", id, err)
	}
	return dRow.ToDomain(), nil
}

// maxErrorMessageLen matches the devices.error_message STRING(1000) column. An
// oversize value (e.g. a full HO stderr dump) otherwise fails the whole status
// write with FailedPrecondition, stranding the device in its prior state.
const maxErrorMessageLen = 1000

// truncateErrorMessage caps an error to fit the error_message column
// (STRING(1000), measured in runes).
func truncateErrorMessage(msg string) string {
	return spannerplatform.TruncateRunes(msg, maxErrorMessageLen)
}

// UpdateStatus updates only the device status (plus an optional error
// message), validating the transition against the state machine inside a
// transaction. Re-writing the current status is allowed so callers can
// refresh the error message on repeated failures.
func (r *DeviceRepository) UpdateStatus(ctx context.Context, id string, status deviceDomain.Status, errorMsg string) error {
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		d, err := readDeviceForUpdate(ctx, txn, id)
		if err != nil {
			return err
		}
		if d.Status != status && !d.Status.CanTransitionTo(status) {
			return fmt.Errorf("device %s cannot transition from %s to %s: %w",
				id, d.Status, status, deviceDomain.ErrInvalidState)
		}

		cols := []string{colHostID, colDeviceID, colStatus, colUpdatedAtDev}
		vals := []any{d.HostID, id, string(status), time.Now()}
		if errorMsg != "" {
			cols = append(cols, "error_message")
			vals = append(vals, truncateErrorMessage(errorMsg))
		}
		switch status {
		case deviceDomain.StatusRunning:
			cols = append(cols, "started_at")
			vals = append(vals, time.Now())
		case deviceDomain.StatusStopped:
			cols = append(cols, "stopped_at")
			vals = append(vals, time.Now())
		case deviceDomain.StatusPending, deviceDomain.StatusCreating, deviceDomain.StatusStarting,
			deviceDomain.StatusStopping, deviceDomain.StatusDeleting, deviceDomain.StatusDeleted,
			deviceDomain.StatusError:
			// No timestamp column of their own; updated_at already carries the write.
		}
		muts := []*spanner.Mutation{spanner.Update("devices", cols, vals)}

		// A device leaving error for a live status re-enters the population the
		// host-lease guards count, so its host must again outlive its promise.
		// Every lease-lowering write (the fleet keeper's shorten, the collapse)
		// treats error rows as non-tenants, which is correct at the instant it
		// commits. The device controller can later heal exactly such a row to
		// running from the CVD observation alone, and without this raise the
		// healed device sits on a host already run down below the device's own
		// expiry plus the teardown grace. Same monotone raise as a device
		// commit; error is the only live-set re-entry (deleting/deleted never
		// come back).
		if d.Status == deviceDomain.StatusError && status != deviceDomain.StatusError &&
			status != deviceDomain.StatusDeleting && status != deviceDomain.StatusDeleted &&
			d.Lifecycle.ExpiresAt != nil {
			hostMuts, err := hostCountersMutations(ctx, txn, d.HostID, 0, 0, devicePromise{expiry: d.Lifecycle.ExpiresAt})
			if err != nil {
				return err
			}
			muts = append(muts, hostMuts...)
		}
		return txn.BufferWrite(muts)
	})
	if err != nil {
		return fmt.Errorf("failed to update device status: %w", err)
	}
	return nil
}

// RecordBootObservation writes the boot-progress observation and the CVD
// status enum it was sampled with as one mutation. The derivation reads
// both, and written separately it can see a fresh enum against a stale count
// and settle wrongly. Best-effort last-write-wins like BatchUpdateHealth:
// observation data, no CAS. A row deleted underneath (NotFound) is not an
// error, because the sampler's device is gone and the observation with it.
func (r *DeviceRepository) RecordBootObservation(ctx context.Context, hostID, deviceID, cvdStatus string, obs *deviceDomain.BootObservation) error {
	b, err := json.Marshal(obs)
	if err != nil {
		return fmt.Errorf("encoding boot observation for device %s: %w", deviceID, err)
	}
	_, err = r.client.Apply(ctx, []*spanner.Mutation{spanner.Update("devices",
		[]string{colHostID, colDeviceID, "boot_observation", colCVDStatus, colUpdatedAtDev},
		[]any{hostID, deviceID, string(b), models.ToNullString(cvdStatus), time.Now()},
	)})
	if spanner.ErrCode(err) == codes.NotFound {
		return nil
	}
	if err != nil {
		return fmt.Errorf("recording boot observation for device %s: %w", deviceID, err)
	}
	return nil
}

// ListRecentBuildIDs returns the distinct registry build ids among device
// creates since the cutoff, which is the warm plane's notion of a hot build.
// Tombstones count on purpose: a CI loop's devices are usually deleted by
// the time the next one starts, and those are exactly the builds worth
// keeping warm.
func (r *DeviceRepository) ListRecentBuildIDs(ctx context.Context, since time.Time) ([]string, error) {
	iter := r.client.Single().Query(ctx, spanner.Statement{
		SQL: `SELECT DISTINCT build_id FROM devices
		      WHERE created_at >= @since AND build_id IS NOT NULL AND build_id != ''
		      ORDER BY build_id`,
		Params: map[string]any{"since": since},
	})
	defer iter.Stop()
	var ids []string
	if err := iter.Do(func(row *spanner.Row) error {
		var id string
		if err := row.Column(0, &id); err != nil {
			return err
		}
		ids = append(ids, id)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("listing recent build ids: %w", err)
	}
	return ids, nil
}

// CountByStatus returns device count per status
func (r *DeviceRepository) CountByStatus(ctx context.Context) (map[deviceDomain.Status]int64, error) {
	stmt := spanner.Statement{
		SQL: `SELECT status, COUNT(*) as count FROM devices GROUP BY status`,
	}

	counts := make(map[deviceDomain.Status]int64)
	if err := r.client.Single().Query(ctx, stmt).Do(func(row *spanner.Row) error {
		var status string
		var count int64
		if err := row.Columns(&status, &count); err != nil {
			return err
		}
		counts[deviceDomain.Status(status)] = count
		return nil
	}); err != nil {
		return nil, fmt.Errorf("counting devices by status: %w", err)
	}

	return counts, nil
}

// BatchUpdateHealth applies one health-check cycle's results in a single
// commit. TimestampOnly updates (steady-state healthy devices) advance only
// the check/healthy timestamps, so the hot path writes two cheap timestamp
// cells instead of the full health column set.
func (r *DeviceRepository) BatchUpdateHealth(ctx context.Context, updates []deviceDomain.HealthUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	now := time.Now()
	muts := make([]*spanner.Mutation, 0, len(updates))
	for _, u := range updates {
		// An enriched boot observation forces the full write path. The
		// timestamp-only dedup would drop the sample, and the observation
		// changes on every enriched sample by construction (it carries a log
		// size), so there is nothing for that dedup to save here. A fresh
		// instance array forces it for the plainer reason that the dedup would
		// otherwise discard it, which is why the caller sends one only when it
		// differs from the stored array, keeping the steady state on the cheap
		// two-cell path.
		if u.TimestampOnly && u.BootObs == nil && u.Instances == nil {
			muts = append(muts, spanner.Update("devices",
				[]string{colHostID, colDeviceID, colLastHealthCheck, "last_healthy_at"},
				[]any{u.HostID, u.DeviceID, now, now}))
			continue
		}
		cols := []string{
			colHostID, colDeviceID, colHealthStatus, "consecutive_fails",
			colLastHealthCheck, "last_failure_reason", colCVDStatus, colUpdatedAtDev,
		}
		vals := []any{
			u.HostID, u.DeviceID, string(u.Status), u.ConsecutiveFails,
			now, models.ToNullString(u.FailReason), models.ToNullString(u.CVDStatus), now,
		}
		// Written beside cvd_status in the same mutation, because the derivation
		// must never see a fresh enum against a stale count. Nil leaves the
		// stored observation in place, since most cycles do not sample it.
		if u.BootObs != nil {
			if b, err := json.Marshal(u.BootObs); err == nil {
				cols = append(cols, "boot_observation")
				vals = append(vals, string(b))
			}
		}
		// Same rule as the observation above, for the same reason. cvd_status is
		// the fold OVER this array, so a fresh enum stored against a stale array
		// is exactly the incoherent pair the derivation must never read. Nil
		// leaves the stored array alone.
		if u.Instances != nil {
			if b, err := json.Marshal(u.Instances); err == nil {
				cols = append(cols, colInstances)
				vals = append(vals, string(b))
			}
		}
		if u.Status == deviceDomain.HealthStatusHealthy {
			cols = append(cols, "last_healthy_at")
			vals = append(vals, now)
		}
		muts = append(muts, spanner.Update("devices", cols, vals))
	}
	if _, err := r.client.Apply(ctx, muts); err != nil {
		return fmt.Errorf("batch updating device health: %w", err)
	}
	return nil
}

// ListNeedingHealthCheck returns devices that need a health check,
// never-checked devices first. GoogleSQL ASC already sorts NULLs first; an
// explicit NULLS FIRST is rejected by the Spanner emulator and would blind
// the entire emulator suite to this query. Do not re-add it.
//
// Stopped devices are included. Their health cannot degrade, but the host is
// the only place a CVD still running under a stopped row is visible. Without
// the visit that drift is never observed and the row claims stopped forever
// while the device serves.
func (r *DeviceRepository) ListNeedingHealthCheck(ctx context.Context, olderThan time.Duration) ([]*deviceDomain.Device, error) {
	cutoff := time.Now().Add(-olderThan)

	stmt := spanner.Statement{
		SQL: `SELECT ` + deviceColumnsSQL + ` FROM devices
              WHERE status IN ('running', 'starting', 'stopped')
              AND (last_health_check IS NULL OR last_health_check < @cutoff)
              ORDER BY last_health_check ASC
              LIMIT 100`,
		Params: map[string]any{
			"cutoff": cutoff,
		},
	}

	return spannerplatform.QueryAll(r.client.Single().Query(ctx, stmt),
		"devices needing health check", (*models.DeviceRow).ToDomain)
}

// Both device sweep listings below are salted-shuffled rather than ordered by
// the column they filter on. The reaper runs on every instance, and one order
// would put every instance on the same rows. See spannerplatform.SweepSalt for
// what the lost oldest-first service costs. ListPinnedActive further down is a
// dashboard lens and not a sweep, so it keeps its order.

// ListLapsedPins returns pinned devices whose pinned_until has passed. The
// reaper unpins them (restoring a fresh ephemeral TTL) so a pin with an
// expiry actually expires. Otherwise the device and its host stay protected
// forever.
func (r *DeviceRepository) ListLapsedPins(ctx context.Context, limit int) ([]*deviceDomain.Device, error) {
	stmt := spanner.Statement{
		SQL: `SELECT ` + deviceColumnsSQL + ` FROM devices
              WHERE lifecycle_mode = 'pinned'
              AND pinned_until IS NOT NULL
              AND pinned_until < @now
              AND status NOT IN ('deleted', 'deleting')
              ORDER BY FARM_FINGERPRINT(CONCAT(device_id, @salt))
              LIMIT @limit`,
		Params: map[string]any{
			"now":                          time.Now(),
			paramLimit:                     int64(limit),
			spannerplatform.SweepSaltParam: spannerplatform.SweepSalt(),
		},
	}

	return spannerplatform.QueryAll(r.client.Single().Query(ctx, stmt),
		"lapsed pins", (*models.DeviceRow).ToDomain)
}

// ListExpired returns devices that have expired
func (r *DeviceRepository) ListExpired(ctx context.Context, limit int) ([]*deviceDomain.Device, error) {
	now := time.Now()

	stmt := spanner.Statement{
		SQL: `SELECT ` + deviceColumnsSQL + ` FROM devices
              WHERE lifecycle_mode = 'ephemeral'
              AND expires_at IS NOT NULL
              AND expires_at < @now
              AND status NOT IN ('deleted', 'deleting')
              ORDER BY FARM_FINGERPRINT(CONCAT(device_id, @salt))
              LIMIT @limit`,
		Params: map[string]any{
			"now":                          now,
			paramLimit:                     int64(limit),
			spannerplatform.SweepSaltParam: spannerplatform.SweepSalt(),
		},
	}

	return spannerplatform.QueryAll(r.client.Single().Query(ctx, stmt),
		"expired devices", (*models.DeviceRow).ToDomain)
}

// ListPinnedActive returns live pinned devices, oldest pin first, for the
// reclaimable-capacity lens.
func (r *DeviceRepository) ListPinnedActive(ctx context.Context, limit int) ([]*deviceDomain.Device, error) {
	stmt := spanner.Statement{
		SQL: `SELECT ` + deviceColumnsSQL + ` FROM devices
              WHERE lifecycle_mode = 'pinned'
              AND status NOT IN ('deleted', 'deleting')
              ORDER BY pinned_at ASC
              LIMIT @limit`,
		Params: map[string]any{
			paramLimit: int64(limit),
		},
	}

	return spannerplatform.QueryAll(r.client.Single().Query(ctx, stmt),
		"pinned devices", (*models.DeviceRow).ToDomain)
}

// CountOutcomesSince returns how many devices were created since the cutoff
// and how many of those sit in error, for the provisioning-success lens.
func (r *DeviceRepository) CountOutcomesSince(ctx context.Context, since time.Time) (total, errored int64, err error) {
	stmt := spanner.Statement{
		SQL: `SELECT COUNT(*), COUNTIF(status = 'error')
		      FROM devices WHERE created_at >= @since`,
		Params: map[string]any{"since": since},
	}

	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if err != nil {
		return 0, 0, fmt.Errorf("failed to count device outcomes: %w", err)
	}
	if err := row.Columns(&total, &errored); err != nil {
		return 0, 0, fmt.Errorf("failed to scan device outcomes: %w", err)
	}
	return total, errored, nil
}

// CreateWithHostIncrement atomically creates a device and increments the host's device count.
// This ensures consistency between device creation and host capacity tracking.
// Returns ErrCapacityExceeded if the host has no capacity for new devices, and
// a host InvalidStateError when checkHostExclusivity refuses the device.
//
// The capacity read-check-increment is write-skew-prone. It is safe only under
// Spanner's default SERIALIZABLE isolation, which validates the read at
// commit. Under repeatable read two concurrent creates would both see a free
// slot and overfill the host. See
// docs/development/architecture-decisions.md (ADR-1) before touching
// isolation anywhere.
func (r *DeviceRepository) CreateWithHostIncrement(ctx context.Context, d *deviceDomain.Device, extra ...*spanner.Mutation) error {
	if d.ID == "" {
		d.ID = uuid.New().String()
	}
	now := time.Now()
	d.CreatedAt = now
	d.UpdatedAt = now

	if err := d.Validate(); err != nil {
		return fmt.Errorf("invalid device: %w", err)
	}

	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		// Read host to verify capacity, status, exclusivity, and the poison fence.
		row, err := txn.ReadRow(ctx, "hosts", spanner.Key{d.HostID},
			[]string{colHostID, colHostDevCount, "max_devices", "status", "degraded_at", "exclusive"})
		if err != nil {
			if spanner.ErrCode(err) == codes.NotFound {
				return fmt.Errorf("host not found: %s", d.HostID)
			}
			return fmt.Errorf("failed to read host: %w", err)
		}

		var hostID string
		var currentCount, maxDevices int64
		var status string
		var degradedAt spanner.NullTime
		var exclusive bool
		if err := row.Columns(&hostID, &currentCount, &maxDevices, &status, &degradedAt, &exclusive); err != nil {
			return fmt.Errorf("failed to read host columns: %w", err)
		}

		// Fence the poison condition atomically with the count increment: server
		// placement already skips degraded hosts, but an explicit host_id must not
		// pile fresh creates (and error rows) onto a host awaiting remediation.
		if degradedAt.Valid {
			return &hostDomain.DegradedError{HostID: hostID}
		}

		// Hosts still coming up may accept devices. The CVD-create task waits
		// for the host via the not-ready reschedule, and declarative creates
		// pack onto provisioning hosts on purpose, which is the same
		// eligibility set automatic placement selects from.
		if !slices.Contains(hostDomain.PlacementEligibleStatuses, status) {
			return fmt.Errorf("host %s cannot accept devices (status: %s)", hostID, status)
		}
		// max_devices bounds INSTANCES, so the whole group has to fit. A device
		// running four Android VMs takes four of the host's slots. Comparing the
		// count alone let an N-instance device onto a host with one slot free.
		slots := int64(d.DesiredInstanceCount())
		if currentCount+slots > maxDevices {
			return hostDomain.ErrCapacityExceeded
		}
		// Read in this transaction so two creates racing onto one empty
		// exclusive host cannot both see it empty.
		occ := hostOccupancy{HostID: hostID, Exclusive: exclusive, Slots: currentCount}
		if err := checkHostExclusivityInTxn(ctx, txn, occ, int(slots)); err != nil {
			return err
		}

		// Create device mutation
		dRow := models.FromDeviceDomain(d)
		deviceMut, err := spanner.InsertStruct("devices", dRow)
		if err != nil {
			return fmt.Errorf("failed to create device mutation: %w", err)
		}

		// The host-side effects of this create (the counter increment, the
		// protection flip for a device born pinned, and the lease raise for
		// the device's TTL promise) go through the same builder every other
		// device write uses, so the protection and lease rules cannot drift
		// between create, pin, unpin, and delete.
		protectedDelta := int64(0)
		if d.Lifecycle.IsPinned() {
			protectedDelta = 1
		}
		hostMuts, err := hostCountersMutations(ctx, txn, d.HostID, slots, protectedDelta, devicePromise{expiry: d.Lifecycle.ExpiresAt, fitVM: true})
		if err != nil {
			return err
		}

		// Buffer the device, the host adjustments, and any extra mutations
		// (e.g. the CVD-creation task enqueue) for one atomic commit.
		muts := append([]*spanner.Mutation{deviceMut}, hostMuts...)
		return txn.BufferWrite(append(muts, extra...))
	})
	if err != nil {
		return fmt.Errorf("failed to create device atomically: %w", err)
	}

	return nil
}

// MarkDeletedWithHostDecrement marks a device deleted and decrements its host's
// current_device_count in one transaction. It is idempotent: if the device is
// already deleted (or gone) it changes nothing and reports transitioned=false,
// so a repeated delete cannot double-decrement the counter. A true return means
// this call performed the transition. Extra mutations (e.g. the CVD-cleanup
// task enqueue) commit with the transition and are dropped when it no-ops, so
// follow-up work is queued exactly once.
func (r *DeviceRepository) MarkDeletedWithHostDecrement(ctx context.Context, hostID, deviceID string, extra ...*spanner.Mutation) (transitioned bool, err error) {
	_, err = r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		transitioned = false // Reset: the transaction body may run more than once.
		devMut, outcome, err := deviceDeleteMutation(ctx, txn, hostID, deviceID)
		if err != nil || !outcome.transitioned {
			return err
		}
		var protectedDelta int64
		if outcome.wasPinned {
			protectedDelta = -1 // Deleting a pinned device releases its host protection.
		}
		hostMuts, err := hostCountersMutations(ctx, txn, hostID, -outcome.slots, protectedDelta, devicePromise{})
		if err != nil {
			return err
		}
		snapMuts, err := snapshotDeleteMutations(ctx, txn, deviceID)
		if err != nil {
			return err
		}
		transitioned = true
		muts := append([]*spanner.Mutation{devMut}, hostMuts...)
		muts = append(muts, snapMuts...)
		return txn.BufferWrite(append(muts, extra...))
	})
	if err != nil {
		return false, fmt.Errorf("marking device %s deleted: %w", deviceID, err)
	}
	return transitioned, nil
}

// BatchMarkDeletedWithHostDecrement marks each device deleted and decrements its
// host's current_device_count, all in one transaction. It returns the devices it
// actually transitioned (skipping any already deleted or gone), so callers can
// apply follow-up adjustments (e.g. protected counts) exactly once. Per-device
// extra mutations (keyed by device ID, e.g. CVD-cleanup task enqueues) commit
// with their device's transition and are dropped when it no-ops. The batch is
// bounded by the reaper batch size, well within Spanner's per-commit limits.
func (r *DeviceRepository) BatchMarkDeletedWithHostDecrement(ctx context.Context, devices []*deviceDomain.Device, extraFor map[string]*spanner.Mutation) ([]*deviceDomain.Device, error) {
	if len(devices) == 0 {
		return nil, nil
	}
	var transitioned []*deviceDomain.Device
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		transitioned = nil // Reset: the transaction body may run more than once.
		var muts []*spanner.Mutation
		hostDelta := make(map[string]int64)
		hostProtectedDelta := make(map[string]int64)
		for _, d := range devices {
			devMut, outcome, err := deviceDeleteMutation(ctx, txn, d.HostID, d.ID)
			if err != nil {
				return err
			}
			if !outcome.transitioned {
				continue
			}
			muts = append(muts, devMut)
			if extra := extraFor[d.ID]; extra != nil {
				muts = append(muts, extra)
			}
			snapMuts, err := snapshotDeleteMutations(ctx, txn, d.ID)
			if err != nil {
				return err
			}
			muts = append(muts, snapMuts...)
			hostDelta[d.HostID] -= outcome.slots // Aggregate per host: buffered writes are not read-visible.
			if outcome.wasPinned {
				hostProtectedDelta[d.HostID]--
			}
			transitioned = append(transitioned, d)
		}
		for hostID, delta := range hostDelta {
			hostMuts, err := hostCountersMutations(ctx, txn, hostID, delta, hostProtectedDelta[hostID], devicePromise{})
			if err != nil {
				return err
			}
			muts = append(muts, hostMuts...)
		}
		if len(muts) == 0 {
			return nil
		}
		return txn.BufferWrite(muts)
	})
	if err != nil {
		return nil, fmt.Errorf("batch marking devices deleted: %w", err)
	}
	return transitioned, nil
}

// deleteOutcome reports what a deviceDeleteMutation found: whether this call
// performs the transition, whether the device held host protection, and how
// many instance slots its host is owed back.
type deleteOutcome struct {
	transitioned bool
	wasPinned    bool
	slots        int64
}

// deviceDeleteMutation reads a device within txn and, unless it is already
// deleted or gone, returns the mutation that marks it deleted.
// outcome.transitioned is false (with a nil mutation) when there is nothing to
// do.
//
// It reads the whole row rather than the two columns the transition needs. The
// refund has to be the desired instance count the create was charged, that
// count lives in the row's opaque desired_spec, and the row model is what
// decodes it. Reconstructing the device is what lets both ends of the
// accounting call DesiredInstanceCount.
func deviceDeleteMutation(ctx context.Context, txn *spanner.ReadWriteTransaction, hostID, deviceID string) (*spanner.Mutation, deleteOutcome, error) {
	row, err := txn.ReadRow(ctx, "devices", spanner.Key{hostID, deviceID}, deviceColumns())
	if spanner.ErrCode(err) == codes.NotFound {
		return nil, deleteOutcome{}, nil // Already gone (e.g. host cascade-deleted).
	}
	if err != nil {
		return nil, deleteOutcome{}, fmt.Errorf("reading device %s: %w", deviceID, err)
	}
	var dRow models.DeviceRow
	if err := row.ToStruct(&dRow); err != nil {
		return nil, deleteOutcome{}, fmt.Errorf("scanning device %s: %w", deviceID, err)
	}
	d := dRow.ToDomain()
	if d.Status == deviceDomain.StatusDeleted {
		return nil, deleteOutcome{}, nil // Idempotent: already deleted, do not decrement again.
	}
	outcome := deleteOutcome{
		transitioned: true,
		wasPinned:    d.Lifecycle.IsPinned(),
		slots:        int64(d.DesiredInstanceCount()),
	}
	// tombstoned_at arms the schema's row deletion policy. The tombstone
	// lives long enough for its cleanup task and operator inspection, then
	// Spanner purges it with no scan needed.
	now := time.Now()
	return spanner.Update("devices",
		[]string{colHostID, colDeviceID, colStatus, colUpdatedAtDev, "tombstoned_at"},
		[]any{hostID, deviceID, string(deviceDomain.StatusDeleted), now, now}), outcome, nil
}

// hostCountersMutations builds the host-row adjustment that accompanies a
// device write. It reads the host's counters within txn and returns one
// mutation adjusting current_device_count by deviceDelta and
// protected_device_count by protectedDelta (each floored at 0). deviceDelta is
// denominated in CVD instances and never device rows, because a device charges
// its host one slot per Android VM it runs. The floor is why a create and its
// delete must pass the same number, since an under-refund is never recovered.
//
// A protected count change also recomputes the host lifecycle: any protected
// device parks the host in protected mode; dropping to zero restarts the
// ephemeral TTL clock. promise.expiry, when non-nil, is the device's (new)
// expiry, and an ephemeral host's expires_at is raised to at least
// expiry+grace, so a host always outlives every promise made to its devices.
// The raise is monotone and nothing here ever shortens a host's life. Returns
// no mutations when the host row is gone. Emitting a single mutation keeps a
// delete-of-pinned-device commit to one write per host row.
func hostCountersMutations(ctx context.Context, txn *spanner.ReadWriteTransaction, hostID string, deviceDelta, protectedDelta int64, promise devicePromise) ([]*spanner.Mutation, error) {
	row, err := txn.ReadRow(ctx, "hosts", spanner.Key{hostID},
		[]string{colHostDevCount, "protected_device_count", "ttl_seconds", colLifecycleMode, "expires_at", "vm_terminates_at"})
	if spanner.ErrCode(err) == codes.NotFound {
		return nil, nil // Host gone; nothing to adjust.
	}
	if err != nil {
		return nil, fmt.Errorf("reading host %s counters: %w", hostID, err)
	}
	var deviceCount, protectedCount, ttlSeconds spanner.NullInt64
	var mode spanner.NullString
	var expiresAt, terminatesAt spanner.NullTime
	if err := row.Columns(&deviceCount, &protectedCount, &ttlSeconds, &mode, &expiresAt, &terminatesAt); err != nil {
		return nil, fmt.Errorf("reading host %s counters: %w", hostID, err)
	}
	if err := promise.checkHorizon(hostID, terminatesAt); err != nil {
		return nil, err
	}

	cols := []string{colHostID, colUpdatedAtDev}
	vals := []any{hostID, time.Now()}
	if deviceDelta != 0 {
		cols = append(cols, colHostDevCount)
		vals = append(vals, max(deviceCount.Int64+deviceDelta, 0))
	}

	// Resolve the post-write lifecycle before applying the lease raise, so an
	// unpin (protected -> ephemeral) both restores the TTL-based expiry and
	// honors the device's own remaining promise in the same commit.
	finalMode := mode.StringVal
	finalExpiry := expiresAt
	if protectedDelta != 0 {
		newProtected := max(protectedCount.Int64+protectedDelta, 0)
		if newProtected > 0 {
			finalMode = string(hostDomain.LifecycleModeProtected)
			finalExpiry = spanner.NullTime{}
		} else {
			finalMode = string(hostDomain.LifecycleModeEphemeral)
			ttl := ttlSeconds.Int64
			if ttl <= 0 {
				ttl = hostDomain.DefaultHostTTLSeconds
			}
			finalExpiry = spanner.NullTime{Time: time.Now().Add(time.Duration(ttl) * time.Second), Valid: true}
		}
		cols = append(cols, "protected_device_count", colLifecycleMode, "expires_at")
		vals = append(vals, newProtected, finalMode, finalExpiry)
	}

	if promise.expiry != nil && finalMode == string(hostDomain.LifecycleModeEphemeral) {
		promised := promise.expiry.Add(time.Duration(hostDomain.GracePeriodSeconds) * time.Second)
		if !finalExpiry.Valid || finalExpiry.Time.Before(promised) {
			raised := spanner.NullTime{Time: promised, Valid: true}
			if protectedDelta != 0 {
				vals[len(vals)-1] = raised // expires_at is the last protection column
			} else {
				cols = append(cols, "expires_at")
				vals = append(vals, raised)
			}
		}
	}
	return []*spanner.Mutation{spanner.Update("hosts", cols, vals)}, nil
}

// devicePromise is the device expiry a write asks its host's lease to cover.
// The zero value promises nothing.
type devicePromise struct {
	expiry *time.Time
	// fitVM refuses the write, with nothing written, when expiry plus the grace
	// passes the VM's stop time (hostDomain.CheckVMHorizon). Create and extend
	// set it. The heal back to live and unpin leave it off, because they
	// restore time the user was already granted.
	fitVM bool
}

func (p devicePromise) checkHorizon(hostID string, terminatesAt spanner.NullTime) error {
	if !p.fitVM || p.expiry == nil {
		return nil
	}
	return hostDomain.CheckVMHorizon(hostID, *p.expiry, models.FromNullTimePtr(terminatesAt))
}
