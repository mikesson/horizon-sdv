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

package spanner_test

import (
	"strconv"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// These tests own what ONLY the database can answer: that the generated columns
// evaluate from the metadata the create path writes, that the index guards are
// present, and that the horizon and the origin discriminator work. Which misses
// standing capacity could have absorbed is a pure rule and lives beside it, in
// hostconfig, and moving it there is what made it testable without an emulator.
//
// One guard has no automated form and is stated instead: removing the
// `spanner_emulator.disable_query_null_filtered_index_check` hint from either
// query makes its tests here fail with "not able to determine whether the null
// filtered index can be used", and removing the explicit null filter
// (`origin IS NOT NULL`, `gpu_degraded IS NOT NULL`) makes them fail against
// real Spanner while the emulator stays silent. That asymmetry is why both are
// written, and why the hint means "this form was run against production".

// hostCreateEntry builds the host-create entry the create path writes, so these
// tests exercise the same metadata keys the generated columns read. A key
// renamed on one side and not the other empties its column, which is exactly the
// failure no unit test can see.
func hostCreateEntry(at time.Time, origin host.Origin, maxDevices, density int64) audit.Entry {
	metadata := map[string]any{
		"name":         "auto",
		"zone":         "us-central1-a",
		"machine_type": "n1-standard-8",
		"origin":       string(origin),
		"max_devices":  maxDevices,
	}
	if density > 0 {
		metadata["density"] = density
		metadata["gpu_required"] = false
		metadata["fits_default_host"] = true
	}
	e := audit.New(audit.ActionHostCreate, audit.ResourceHost).
		WithResource(resourceID("host-", at)).
		WithMetadata(metadata).
		Build()
	e.Timestamp = at
	return e
}

// resourceID mints a distinct id for a seeded entry, bounded by the column.
//
// resource_id is STRING(36), because it holds a UUID, and a formatted
// timestamp does NOT have a fixed width: RFC3339Nano trims trailing zeros from
// the fractional second, so the same expression yields 25 to 35 characters
// depending on the instant it happens to be called at. Prefixed, that
// overflows the column for some values of time.Now() and not others, which is
// a test that fails a few runs in ten and passes on the next attempt. Unix
// nanoseconds are always 19 digits.
func resourceID(prefix string, at time.Time) string {
	return prefix + strconv.FormatInt(at.UnixNano(), 10)
}

func writeEntries(t *testing.T, repo *cspanner.AuditRepository, entries ...audit.Entry) {
	t.Helper()
	if err := repo.WriteBatch(t.Context(), entries); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}
}

// The signal reads AUDIT ENTRIES, and the entries it must see include ones
// whose hosts are long gone: the host row hard-deletes, so a query over hosts
// reports the least demand exactly when reclamation was working hardest. There
// is no host row behind any entry here, and the count must still be right.
func TestDemandReadsEntriesNotHosts(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewAuditRepository(db.Client)
	now := time.Now()

	writeEntries(t, repo,
		hostCreateEntry(now.Add(-10*time.Minute), host.OriginAuto, 4, 4),
		hostCreateEntry(now.Add(-9*time.Minute), host.OriginAuto, 4, 4),
		hostCreateEntry(now.Add(-8*time.Minute), host.OriginAuto, 4, 4),
	)

	window, err := repo.Demand(t.Context(), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("Demand: %v", err)
	}
	if window.Partial {
		t.Error("partial = true on three entries")
	}
	if len(window.Misses) != 3 {
		t.Fatalf("Demand returned %d misses, want 3", len(window.Misses))
	}
	// Ascending, because the peak pass walks forward and a descending slice
	// would make every gap negative and the peak read as the whole set.
	for i := 1; i < len(window.Misses); i++ {
		if window.Misses[i].At.Before(window.Misses[i-1].At) {
			t.Fatalf("Demand is not ascending: %v", window.Misses)
		}
	}
}

// Only a declarative auto-provision is a miss. A keeper top-up is the ANSWER
// to demand, and a batch plans against the whole-fleet view and never misses
// placement. A filter written as "system-created", or one that merely
// excludes users, sweeps both back in and inflates the target with the fleet's
// own activity.
func TestDemandCountsOnlyAutoProvisions(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewAuditRepository(db.Client)
	now := time.Now()

	writeEntries(t, repo,
		hostCreateEntry(now.Add(-5*time.Minute), host.OriginAuto, 4, 4),
		hostCreateEntry(now.Add(-4*time.Minute), host.OriginPool, 4, 4),
		hostCreateEntry(now.Add(-3*time.Minute), host.OriginBatch, 4, 0),
		hostCreateEntry(now.Add(-2*time.Minute), host.OriginUser, 8, 0),
	)

	window, err := repo.Demand(t.Context(), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("Demand: %v", err)
	}
	if len(window.Misses) != 1 {
		t.Errorf("Demand returned %d misses, want 1 (the auto create alone)", len(window.Misses))
	}
}

// Every stamped fact arrives, at the value the ENTRY carries. The generated
// columns are the only path from metadata to the signal, and a SAFE_CAST that
// never evaluates reads NULL for every row while the entries themselves look
// perfect. That is the failure with no other symptom.
func TestDemandCarriesTheStampedFacts(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewAuditRepository(db.Client)
	now := time.Now()

	// A create that required a GPU and outgrew the default template: both
	// booleans carry the non-zero value, so neither can pass by accident.
	entry := hostCreateEntry(now.Add(-5*time.Minute), host.OriginAuto, 8, 4)
	entry.Metadata["gpu_required"] = true
	entry.Metadata["fits_default_host"] = false
	writeEntries(t, repo, entry)

	window, err := repo.Demand(t.Context(), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("Demand: %v", err)
	}
	if len(window.Misses) != 1 {
		t.Fatalf("Demand returned %d misses, want 1", len(window.Misses))
	}
	m := window.Misses[0]
	switch {
	case m.MaxDevices == nil || *m.MaxDevices != 8:
		t.Errorf("max_devices = %v, want 8", m.MaxDevices)
	case m.Density == nil || *m.Density != 4:
		t.Errorf("density = %v, want 4", m.Density)
	case m.GPURequired == nil || !*m.GPURequired:
		t.Errorf("gpu_required = %v, want true. A nil here means the BOOL cast never evaluated", m.GPURequired)
	case m.FitsDefaultHost == nil || *m.FitsDefaultHost:
		t.Errorf("fits_default_host = %v, want false and not nil", m.FitsDefaultHost)
	}
}

// An entry written before these columns existed carries none of them, and every
// fact must come back nil rather than as a zero value. nil is "nobody judged
// this", which the rule treats as a miss; false would mean "judged and beyond
// the buffer", which silently deletes a horizon of demand.
func TestDemandLeavesUnstampedFactsAbsent(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewAuditRepository(db.Client)
	now := time.Now()

	bare := audit.New(audit.ActionHostCreate, audit.ResourceHost).
		WithResource("host-legacy").
		WithMetadata(map[string]any{"name": "auto", "origin": string(host.OriginAuto)}).
		Build()
	bare.Timestamp = now.Add(-5 * time.Minute)
	writeEntries(t, repo, bare)

	window, err := repo.Demand(t.Context(), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("Demand: %v", err)
	}
	if len(window.Misses) != 1 {
		t.Fatalf("Demand returned %d misses, want 1. An unjudgeable entry is still a miss", len(window.Misses))
	}
	m := window.Misses[0]
	if m.MaxDevices != nil || m.Density != nil || m.GPURequired != nil || m.FitsDefaultHost != nil {
		t.Errorf("unstamped entry came back judged: %+v", m)
	}
	if !m.Servable() {
		t.Error("an unjudgeable miss must still count toward the target")
	}
}

// The facts are read off the ENTRY, never re-derived from live settings. An
// operator lowering the packing policy must not retroactively reclassify a
// horizon of ordinary misses, because the target would drop for reasons that
// have nothing to do with demand.
//
// The mutation this catches has no other symptom: a join back to system_config
// is one clause away and passes every test that plants a single density.
func TestDemandFactsComeFromTheEntryNotTheLiveSetting(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewAuditRepository(db.Client)
	now := time.Now()

	// Judged at a density of 4, with a ceiling of 4: an ordinary miss.
	writeEntries(t, repo, hostCreateEntry(now.Add(-5*time.Minute), host.OriginAuto, 4, 4))

	// The operator halves the packing policy. Against the LIVE value the entry
	// above would look oversized (4 > 2) and stop being servable.
	if err := cspanner.NewSystemConfigRepository(db.Client).Set(t.Context(),
		"host.auto_max_devices", "2", "packing density"); err != nil {
		t.Fatalf("setting the density: %v", err)
	}

	window, err := repo.Demand(t.Context(), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("Demand: %v", err)
	}
	if len(window.Misses) != 1 {
		t.Fatalf("Demand returned %d misses, want 1", len(window.Misses))
	}
	if d := window.Misses[0].Density; d == nil || *d != 4 {
		t.Errorf("density = %v, want the 4 the entry was judged against", d)
	}
	if !window.Misses[0].Servable() {
		t.Error("the entry keeps the density it was judged against")
	}
}

// The horizon bounds what the signal can see, and the boundary is the query's
// own: an entry older than `since` is not read at all, so a stale burst cannot
// keep raising the target after it has passed.
func TestDemandHonoursTheHorizon(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewAuditRepository(db.Client)
	now := time.Now()

	writeEntries(t, repo,
		hostCreateEntry(now.Add(-90*time.Minute), host.OriginAuto, 4, 4),
		hostCreateEntry(now.Add(-30*time.Minute), host.OriginAuto, 4, 4),
	)

	window, err := repo.Demand(t.Context(), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("Demand: %v", err)
	}
	if len(window.Misses) != 1 {
		t.Errorf("Demand returned %d misses, want 1 (the 90-minute-old entry is outside a one-hour horizon)",
			len(window.Misses))
	}
}

// deviceCreateEntry builds the device-create entry the create path writes.
//
// A nil stamp is a create with no placement decision to judge, because it named
// its own host or wanted no GPU. It must stay out of BOTH terms of the ratio,
// the denominator included.
func deviceCreateEntry(at time.Time, degraded *bool) audit.Entry {
	metadata := map[string]any{"name": "cvd", "host_id": "host-1"}
	if degraded != nil {
		metadata["gpu_degraded"] = *degraded
	}
	e := audit.New(audit.ActionDeviceCreate, audit.ResourceDevice).
		WithResource(resourceID("device-", at)).
		WithMetadata(metadata).
		Build()
	e.Timestamp = at
	return e
}

// Both arms, from one scan. The false arm IS the denominator: an index holding
// only degradations would make the number undecidable, because forty
// degradations mean nothing without how many creates wanted a GPU at all.
func TestGPUDegradationCountsBothArms(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewAuditRepository(db.Client)
	now := time.Now()

	writeEntries(t, repo,
		deviceCreateEntry(now.Add(-5*time.Minute), new(true)),
		deviceCreateEntry(now.Add(-4*time.Minute), new(true)),
		deviceCreateEntry(now.Add(-3*time.Minute), new(false)),
		deviceCreateEntry(now.Add(-2*time.Minute), new(false)),
		deviceCreateEntry(now.Add(-time.Minute), new(false)),
	)

	got, err := repo.GPUDegradation(t.Context(), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("GPUDegradation: %v", err)
	}
	if want := (hostconfig.GPUDegradation{Degraded: 2, Wanted: 5}); got != want {
		t.Errorf("GPUDegradation = %+v, want %+v. A zero Degraded means the BOOL cast never evaluated",
			got, want)
	}
}

// A create that wanted no GPU is not a data point at all. It stamps nothing, the
// null-filtered index drops it, and it must not reach the denominator, which
// would silently turn the figure into a fraction of every create and answer a
// question nobody asked.
func TestGPUDegradationExcludesCreatesThatWantedNoGPU(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewAuditRepository(db.Client)
	now := time.Now()

	writeEntries(t, repo,
		deviceCreateEntry(now.Add(-3*time.Minute), new(true)),
		deviceCreateEntry(now.Add(-2*time.Minute), nil),
		deviceCreateEntry(now.Add(-time.Minute), nil),
		// A different action entirely, which stamps none of this either.
		hostCreateEntry(now.Add(-4*time.Minute), host.OriginAuto, 4, 4),
	)

	got, err := repo.GPUDegradation(t.Context(), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("GPUDegradation: %v", err)
	}
	if want := (hostconfig.GPUDegradation{Degraded: 1, Wanted: 1}); got != want {
		t.Errorf("GPUDegradation = %+v, want %+v", got, want)
	}
}

// The horizon bounds it the way it bounds the miss window, so a burst that has
// passed stops arguing for GPU capacity.
func TestGPUDegradationHonoursTheHorizon(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewAuditRepository(db.Client)
	now := time.Now()

	writeEntries(t, repo,
		deviceCreateEntry(now.Add(-90*time.Minute), new(true)),
		deviceCreateEntry(now.Add(-30*time.Minute), new(false)),
	)

	got, err := repo.GPUDegradation(t.Context(), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("GPUDegradation: %v", err)
	}
	if want := (hostconfig.GPUDegradation{Degraded: 0, Wanted: 1}); got != want {
		t.Errorf("GPUDegradation = %+v, want %+v (the 90-minute-old entry is outside the horizon)",
			got, want)
	}
}

// An empty window answers zero, not an error. COUNTIF over no rows is 0 rather
// than NULL. If it ever were NULL the int64 read would fail and take the whole
// operations page with it, which is a failure worth pinning rather than
// assuming.
func TestGPUDegradationOverAnEmptyWindow(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewAuditRepository(db.Client)

	got, err := repo.GPUDegradation(t.Context(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("GPUDegradation over an empty table: %v", err)
	}
	if got != (hostconfig.GPUDegradation{}) {
		t.Errorf("GPUDegradation = %+v, want zeroes", got)
	}
}

// What the query is for, end to end: six misses one provisioning leg apart are
// a steady stream one host wide, not six hosts' worth of demand. A raw count
// answers six here.
func TestDemandFeedsThePeakWithTheRightDimension(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewAuditRepository(db.Client)
	now := time.Now()
	leg := host.ProvisioningLeg

	entries := make([]audit.Entry, 0, 6)
	for i := range 6 {
		entries = append(entries, hostCreateEntry(now.Add(-time.Duration(i+1)*leg), host.OriginAuto, 4, 4))
	}
	writeEntries(t, repo, entries...)

	window, err := repo.Demand(t.Context(), now.Add(-2*time.Hour))
	if err != nil {
		t.Fatalf("Demand: %v", err)
	}
	if len(window.Misses) != 6 {
		t.Fatalf("Demand returned %d misses, want 6", len(window.Misses))
	}
	if peak := hostconfig.PeakConcurrent(window.ServableTimes(), leg); peak != 1 {
		t.Errorf("PeakConcurrent = %d, want 1. Six misses one leg apart never overlapped", peak)
	}
}
