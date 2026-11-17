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

package hostfeature_test

import (
	"log/slog"
	"testing"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	hostfeature "cloud-cuttlefish-orchestrator/internal/features/host"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// hostCreateEntries reads back every host.create entry, strongly. Strongly on
// purpose: a stale read would tolerate an entry that had not committed yet,
// which is the exact difference these tests exist to detect.
func hostCreateEntries(t *testing.T, db *spannertest.TestDB) []audit.Entry {
	t.Helper()
	action := audit.ActionHostCreate
	entries, err := spannerplatform.NewAuditRepository(db.Client).
		Query(t.Context(), audit.ListFilter{Action: &action})
	if err != nil {
		t.Fatalf("reading host-create entries: %v", err)
	}
	return entries
}

// auditFactsStatement reads the generated columns directly, and never
// through the metadata map: the map is what the writer sent, the columns are
// what the demand signal scans, and a metadata key renamed on one side alone
// leaves the map right and the columns empty.
func auditFactsStatement() spanner.Statement {
	return spanner.Statement{
		SQL: `SELECT origin, max_devices, density, gpu_required, fits_default_host
		      FROM audit_logs WHERE action = 'host.create'`,
	}
}

// createService builds the service the way the composition root does for the
// create path, with NO audit sink: the only route an entry has to the table
// here is the create transaction, so an implementation that fell back to the
// buffered sink writes nothing and every assertion below goes red.
func createService(t *testing.T, db *spannertest.TestDB) *hostfeature.HostService {
	t.Helper()
	repo := hostfeature.NewHostRepository(db.Client)
	tq := spannerplatform.NewTaskQueue(db.Client,
		spannerplatform.WithShards(task.DefaultShards), spannerplatform.WithHolder("test"))
	return hostfeature.NewHostService(hostfeature.HostServiceConfig{}, nil, nil, repo, slog.Default(),
		hostfeature.WithTaskEnqueuer(tq),
		hostfeature.WithHostAuditMutator(spannerplatform.NewAuditRepository(db.Client)))
}

// The entry commits WITH the host row, so it is durable the instant Create
// returns.
//
// This is what separates the transactional write from the buffered sink, and
// nothing else does: the sink batches to a five-second timer, so under it this
// read finds nothing while every happy-path assertion about the host itself
// still passes. It is also the property the demand signal depends on: the
// keeper reads these entries back a cycle later, and a create that has not
// landed in the table by then is demand that never happened.
func TestCreateCommitsItsAuditEntryWithTheHost(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	if err := db.SeedQuota(ctx, 5, 0); err != nil {
		t.Fatalf("seed quota: %v", err)
	}
	svc := createService(t, db)

	h, err := svc.Create(ctx, host.CreateRequest{
		Name: "auto", Zone: "us-central1-a", MachineType: "n1-standard-8",
		MaxDevices: 4, Resolved: &host.ResolvedFacts{Density: 4, FitsDefaultHost: true},
	}, "someone@example.com", host.OriginAuto)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	entries := hostCreateEntries(t, db)
	if len(entries) != 1 {
		t.Fatalf("host-create entries = %d, want 1 immediately after Create returned", len(entries))
	}
	entry := entries[0]
	if entry.ResourceID == nil || *entry.ResourceID != h.ID {
		t.Errorf("entry resource = %v, want the created host %s", entry.ResourceID, h.ID)
	}
	if entry.ActorEmail != "someone@example.com" {
		t.Errorf("entry actor = %q, want someone@example.com", entry.ActorEmail)
	}
}

// A create that does not commit leaves no entry. Without this the trail can
// claim hosts that never existed, and the demand signal counts misses that
// were never served.
func TestAFailedCreateLeavesNoAuditEntry(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	// Room for exactly one host, so the second create aborts inside the same
	// transaction that would have written its entry.
	if err := db.SeedQuota(ctx, 1, 0); err != nil {
		t.Fatalf("seed quota: %v", err)
	}
	svc := createService(t, db)

	req := host.CreateRequest{
		Name: "auto", Zone: "us-central1-a", MachineType: "n1-standard-8",
		MaxDevices: 4, Resolved: &host.ResolvedFacts{Density: 4, FitsDefaultHost: true},
	}
	if _, err := svc.Create(ctx, req, "someone@example.com", host.OriginAuto); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := svc.Create(ctx, req, "someone@example.com", host.OriginAuto); err == nil {
		t.Fatal("second Create must fail on the exhausted quota")
	}

	if entries := hostCreateEntries(t, db); len(entries) != 1 {
		t.Errorf("host-create entries = %d, want 1; the refused create must leave none", len(entries))
	}
}

// Every fact the demand signal reads, at its generated column rather than
// inside the JSON: reading the metadata map would pass while the columns the
// query actually scans stayed empty, which is the failure with no other
// symptom.
//
// The booleans matter here more than the integers did. SAFE_CAST over
// JSON_VALUE has to turn a JSON `true` into a SQL TRUE, and a mis-typed column
// would read NULL for every row while the entry itself looked perfect.
func TestCreateStampsTheFleetFactsOnItsGeneratedColumns(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	if err := db.SeedQuota(ctx, 5, 0); err != nil {
		t.Fatalf("seed quota: %v", err)
	}
	svc := createService(t, db)

	if _, err := svc.Create(ctx, host.CreateRequest{
		Name: "auto", Zone: "us-central1-a", MachineType: "n1-standard-8",
		MaxDevices: 8,
		// A create that outgrew the default template and required a GPU: both
		// booleans non-zero, so neither can pass by accident.
		Resolved: &host.ResolvedFacts{Density: 4, GPURequired: true, FitsDefaultHost: false},
	}, "someone@example.com", host.OriginAuto); err != nil {
		t.Fatalf("Create: %v", err)
	}

	iter := db.Client.Single().Query(ctx, auditFactsStatement())
	defer iter.Stop()
	row, err := iter.Next()
	if err != nil {
		t.Fatalf("reading the stamped columns: %v", err)
	}
	var origin string
	var maxDevices, density int64
	var gpuRequired, fitsDefaultHost spanner.NullBool
	if err := row.Columns(&origin, &maxDevices, &density, &gpuRequired, &fitsDefaultHost); err != nil {
		t.Fatalf("scanning the stamped columns: %v", err)
	}
	if origin != string(host.OriginAuto) {
		t.Errorf("origin column = %q, want auto", origin)
	}
	if maxDevices != 8 {
		t.Errorf("max_devices column = %d, want 8", maxDevices)
	}
	if density != 4 {
		t.Errorf("density column = %d, want 4", density)
	}
	if !gpuRequired.Valid || !gpuRequired.Bool {
		t.Errorf("gpu_required column = %v, want TRUE; a NULL here means the cast never evaluated", gpuRequired)
	}
	// Stamped false, not omitted. The false arm is a judgement the signal acts
	// on; leaving it out would read as a create nobody judged.
	if !fitsDefaultHost.Valid || fitsDefaultHost.Bool {
		t.Errorf("fits_default_host column = %v, want FALSE and not NULL", fitsDefaultHost)
	}
}

// A create nobody judged leaves EVERY judged column NULL, not zero and not
// false: the signal treats an unjudgeable miss as a miss, and a stamped zero
// would make a user's create read as a host grown past its packing while a
// stamped false would mark it demand the fleet cannot serve.
//
// This is what the one-struct-or-nothing shape buys. Loose fields let a caller
// set the density and leave the booleans at their zero values, which is not an
// absence but a claim.
func TestAnUnjudgedCreateLeavesTheFleetFactsNull(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	if err := db.SeedQuota(ctx, 5, 0); err != nil {
		t.Fatalf("seed quota: %v", err)
	}
	svc := createService(t, db)

	if _, err := svc.Create(ctx, host.CreateRequest{
		Name: "mine", Zone: "us-central1-a", MachineType: "n1-standard-8", MaxDevices: 4,
	}, "someone@example.com", host.OriginUser); err != nil {
		t.Fatalf("Create: %v", err)
	}

	iter := db.Client.Single().Query(ctx, auditFactsStatement())
	defer iter.Stop()
	row, err := iter.Next()
	if err != nil {
		t.Fatalf("reading the stamped columns: %v", err)
	}
	var origin string
	var maxDevices int64
	var density spanner.NullInt64
	var gpuRequired, fitsDefaultHost spanner.NullBool
	if err := row.Columns(&origin, &maxDevices, &density, &gpuRequired, &fitsDefaultHost); err != nil {
		t.Fatalf("scanning the stamped columns: %v", err)
	}
	if origin != string(host.OriginUser) {
		t.Errorf("origin column = %q, want user", origin)
	}
	if density.Valid {
		t.Errorf("density column = %d, want NULL for a create no density judged", density.Int64)
	}
	if gpuRequired.Valid {
		t.Errorf("gpu_required column = %v, want NULL; nobody judged this create's GPU need", gpuRequired.Bool)
	}
	if fitsDefaultHost.Valid {
		t.Errorf("fits_default_host column = %v, want NULL; a false here reads as unservable demand", fitsDefaultHost.Bool)
	}
}

// The shape a create WANTED rides the entry only when it differs from the one
// it got. The host row keeps only the shape it ended on, so without this the
// entry cannot be told from an ordinary create on that shape, and an operator
// asking how often the fleet fell back has nothing to count.
//
// Read out of the metadata JSON rather than a generated column because there
// is none: absence is the ordinary case here, and a column of mostly-NULL is
// not worth a schema change to answer a question the entry already answers.
func TestCreateStampsThePreferredShapeOnlyWhenItFellBack(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	if err := db.SeedQuota(ctx, 5, 0); err != nil {
		t.Fatalf("seed quota: %v", err)
	}
	svc := createService(t, db)

	for _, tc := range []struct {
		name      string
		hostName  string
		preferred string
		want      spanner.NullString
	}{
		{
			name: "a create that fell back", hostName: "fellback",
			preferred: "n2-standard-8",
			want:      spanner.NullString{StringVal: "n2-standard-8", Valid: true},
		},
		{
			name: "a create that got the shape it preferred", hostName: "preferred",
			preferred: "",
			want:      spanner.NullString{},
		},
	} {
		if _, err := svc.Create(ctx, host.CreateRequest{
			Name: tc.hostName, Zone: "us-central1-a", MachineType: "n1-standard-8",
			MaxDevices: 4,
			Resolved: &host.ResolvedFacts{
				Density: 4, FitsDefaultHost: true, PreferredType: tc.preferred,
			},
		}, "someone@example.com", host.OriginAuto); err != nil {
			t.Fatalf("%s: Create: %v", tc.name, err)
		}

		stmt := spanner.Statement{
			SQL: `SELECT JSON_VALUE(metadata, '$.preferred_type')
			      FROM audit_logs
			      WHERE action = 'host.create' AND JSON_VALUE(metadata, '$.name') = @name`,
			Params: map[string]any{"name": tc.hostName},
		}
		iter := db.Client.Single().Query(ctx, stmt)
		row, err := iter.Next()
		if err != nil {
			iter.Stop()
			t.Fatalf("%s: reading the entry: %v", tc.name, err)
		}
		var got spanner.NullString
		if err := row.Columns(&got); err != nil {
			iter.Stop()
			t.Fatalf("%s: scanning preferred_type: %v", tc.name, err)
		}
		iter.Stop()
		if got != tc.want {
			t.Errorf("%s: preferred_type = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The pool a create was judged into rides the entry, and its generated column,
// only when it is not the default one, exactly what the host row stores, and
// exactly what an entry written before pools were selectable carries. The demand
// signal reads an unnamed pool as the default, so stamping every create would
// only make the index wider.
//
// It is what lets the signal attribute a miss to a pool at all: without it, a
// GPU pool's demand reads as the default pool's, the default pool is bought for,
// and the pool that was actually short never moves.
func TestCreateStampsThePoolOnlyWhenItIsNotTheDefault(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	if err := db.SeedQuota(ctx, 5, 0); err != nil {
		t.Fatalf("seed quota: %v", err)
	}
	svc := createService(t, db)

	for _, tc := range []struct {
		name     string
		hostName string
		pool     string
		want     spanner.NullString
	}{
		{
			name: "a create routed into the gpu pool", hostName: "accelerated",
			pool: host.PoolGPU,
			want: spanner.NullString{StringVal: host.PoolGPU, Valid: true},
		},
		{
			name:     "a create in the pool every unstamped machine belongs to",
			hostName: "ordinary", pool: "",
			want: spanner.NullString{},
		},
	} {
		if _, err := svc.Create(ctx, host.CreateRequest{
			Name: tc.hostName, Zone: "us-central1-a", MachineType: "n1-standard-8",
			MaxDevices: 4, Pool: tc.pool,
			Resolved: &host.ResolvedFacts{Density: 4, FitsDefaultHost: true},
		}, "someone@example.com", host.OriginAuto); err != nil {
			t.Fatalf("%s: Create: %v", tc.name, err)
		}

		stmt := spanner.Statement{
			SQL: `SELECT pool FROM audit_logs
			      WHERE action = 'host.create' AND JSON_VALUE(metadata, '$.name') = @name`,
			Params: map[string]any{"name": tc.hostName},
		}
		iter := db.Client.Single().Query(ctx, stmt)
		row, err := iter.Next()
		if err != nil {
			iter.Stop()
			t.Fatalf("%s: reading the entry: %v", tc.name, err)
		}
		var got spanner.NullString
		if err := row.Columns(&got); err != nil {
			iter.Stop()
			t.Fatalf("%s: scanning pool: %v", tc.name, err)
		}
		iter.Stop()
		if got != tc.want {
			t.Errorf("%s: pool column = %v, want %v", tc.name, got, tc.want)
		}
	}
}
