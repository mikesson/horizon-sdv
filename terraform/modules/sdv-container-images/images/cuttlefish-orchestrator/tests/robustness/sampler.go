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

package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	gspanner "cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"cloud-cuttlefish-orchestrator/internal/jobs/leaderelection"
)

// The conservation, capacity and quota invariants are maintained in the same
// Spanner transactions as the rows they summarize (hostCountersMutations for
// the device counters, CreateWithQuotaIncrement / DeleteWithQuotaDecrement /
// MarkHostLost for used_hosts; the reconciler's quota healer is left unwired
// in this composition). They must therefore hold at EVERY timestamp, and not
// only at quiescence. A checker that only reads the fixpoint lets a transient
// violation pass: an over-capacity admission masked by a later delete, a
// clamped double-decrement re-inflated by a later create.
// The sampler evaluates them on a globally consistent read-only snapshot
// every sampleInterval, through both the workload and quiesce windows.
const sampleInterval = 5 * time.Second

// maxSampleViolations bounds what one systemic failure can append; the
// sample count still reports the full extent.
const maxSampleViolations = 20

func (d *driver) sampleLoop(ctx context.Context) {
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		found, err := d.sampleOnce(ctx)
		d.mu.Lock()
		d.samples++
		if err != nil && ctx.Err() == nil {
			// A failed sample is lost coverage, not a violation; count it so
			// a run whose every sample failed cannot read as "sampled clean".
			d.samplesFailed++
		}
		for _, v := range found {
			if len(d.transientDrift) < maxSampleViolations {
				d.transientDrift = append(d.transientDrift, v)
			}
			d.transientSeen++
		}
		d.mu.Unlock()
	}
}

// sampleOnce evaluates the always-invariants at one consistent timestamp.
//
// The live devices come back as rows rather than as a counting subquery: the
// counter is denominated in CVD instances, and a device's desired instance
// count lives inside its opaque desired_spec, which nothing reads into in SQL.
// Both statements run inside the one read-only transaction, so the counters
// and the devices they summarize are still read at a single timestamp.
func (d *driver) sampleOnce(ctx context.Context) ([]string, error) {
	txn := d.raw.ReadOnlyTransaction()
	defer txn.Close()

	var found []string
	at := time.Now().Format(time.TimeOnly)

	instancesByHost, pinnedByHost, err := sampleLiveDevices(ctx, txn)
	if err != nil {
		return nil, err
	}

	election, err := d.sampleElection(ctx, txn, at)
	if err != nil {
		return nil, err
	}
	found = append(found, election...)

	iter := txn.Query(ctx, gspanner.Statement{
		SQL: `SELECT host_id, current_device_count,
		             COALESCE(protected_device_count, 0), max_devices
		      FROM hosts WHERE status != 'deleted'`,
	})
	var liveHosts int64
	err = iter.Do(func(row *gspanner.Row) error {
		var id string
		var count, protected, maxDev int64
		if err := row.Columns(&id, &count, &protected, &maxDev); err != nil {
			return err
		}
		liveHosts++
		if count != instancesByHost[id] {
			found = append(found, fmt.Sprintf(
				"[%s] host %s: current_device_count=%d, instances desired by live devices=%d",
				at, id, count, instancesByHost[id]))
		}
		if protected != pinnedByHost[id] {
			found = append(found, fmt.Sprintf(
				"[%s] host %s: protected_device_count=%d, pinned live devices=%d", at, id, protected, pinnedByHost[id]))
		}
		if count > maxDev {
			found = append(found, fmt.Sprintf(
				"[%s] host %s: current_device_count=%d > max_devices=%d", at, id, count, maxDev))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("sampling hosts: %w", err)
	}

	row, err := txn.Query(ctx, gspanner.Statement{
		SQL: `SELECT used_hosts FROM quotas WHERE scope='platform' AND scope_id='global'`,
	}).Next()
	if errors.Is(err, iterator.Done) {
		return found, nil // seeded row missing would be caught at quiesce
	}
	if err != nil {
		return nil, fmt.Errorf("sampling quota: %w", err)
	}
	var used int64
	if err := row.Columns(&used); err != nil {
		return nil, fmt.Errorf("scanning quota sample: %w", err)
	}
	if used != liveHosts {
		found = append(found, fmt.Sprintf(
			"[%s] quotas.used_hosts=%d, live hosts=%d", at, used, liveHosts))
	}
	return found, nil
}

// sampleElection reads the leader leases at the snapshot's timestamp: one
// election, and who is running it.
//
// Sampled rather than left to quiesce for two reasons. A second election is an
// always-invariant violation: a job wired to its own lease would be a second
// row for as long as it ran, and a row a later run's reset would erase. And the
// handover a kill forces is only visible while the run is happening: by check
// time every instance has been terminated and released, so the quiesce read
// cannot tell a lease that moved from one nobody ever held.
func (d *driver) sampleElection(ctx context.Context, txn *gspanner.ReadOnlyTransaction, at string) ([]string, error) {
	var found []string
	err := txn.Query(ctx, gspanner.Statement{
		SQL: `SELECT lease_name, COALESCE(holder, '') FROM leader_leases`,
	}).Do(func(row *gspanner.Row) error {
		var name, holder string
		if err := row.Columns(&name, &holder); err != nil {
			return err
		}
		if name != leaderelection.BackgroundPlaneLease {
			found = append(found, fmt.Sprintf(
				"[%s] leader_leases holds %q: the background plane elects on one lease", at, name))
			return nil
		}
		if holder != "" {
			d.mu.Lock()
			d.leaseHolders[holder] = true
			d.mu.Unlock()
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("sampling the election: %w", err)
	}
	return found, nil
}

// sampleLiveDevices folds the snapshot's live devices into what each host's
// two counters must equal, in that order: the instances its devices desire
// (the same charge admission makes), then how many of those devices are
// pinned.
func sampleLiveDevices(ctx context.Context, txn *gspanner.ReadOnlyTransaction) (map[string]int64, map[string]int64, error) {
	instances, pinned := make(map[string]int64), make(map[string]int64)
	err := txn.Query(ctx, gspanner.Statement{
		SQL: `SELECT host_id, COALESCE(desired_spec, ''), pinned_at IS NOT NULL
		      FROM devices WHERE status != 'deleted'`,
	}).Do(func(row *gspanner.Row) error {
		var hostID, spec string
		var isPinned bool
		if err := row.Columns(&hostID, &spec, &isPinned); err != nil {
			return err
		}
		instances[hostID] += deviceSlots(desiredInstances(spec))
		if isPinned {
			pinned[hostID]++
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("sampling devices: %w", err)
	}
	return instances, pinned, nil
}
