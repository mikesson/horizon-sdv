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

package warmplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gspanner "cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/build"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// testLead is the inventory lease margin the plane is built with; the fake
// lister ignores it, so its only job is to be a value the plane does not have
// to default.
const testLead = 10 * time.Minute

// fakeWarmHO is a Host Orchestrator with one group slot: creates register
// the warm group Starting, the test promotes it to Running, deletes clear it.
type fakeWarmHO struct {
	mu      sync.Mutex
	status  string // "" = no group
	creates []string
	deletes int
}

func (f *fakeWarmHO) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/cvds":
			if f.status == "" {
				fmt.Fprint(w, `{"cvds": []}`)
				return
			}
			fmt.Fprintf(w, `{"cvds":[{"group":%q,"name":"1","status":%q}]}`,
				device.WarmGroupName, f.status)
		case r.Method == http.MethodPost && r.URL.Path == "/cvds":
			var req struct {
				EnvConfig map[string]any `json:"env_config"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("warm create body: %v", err)
			}
			f.creates = append(f.creates, warmCreateKey(t, req.EnvConfig))
			f.status = device.CVDStateStarting
			fmt.Fprint(w, `{"name":"op-warm"}`)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, device.WarmGroupName):
			f.deletes++
			f.status = ""
			fmt.Fprint(w, `{"name":"op-rm","done":true}`)
		default:
			fmt.Fprint(w, `{}`)
		}
	})
}

func (f *fakeWarmHO) promote(status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

// warmCreateKey digs the default_build reference out of a warm env_config.
func warmCreateKey(t *testing.T, envConfig map[string]any) string {
	t.Helper()
	instances, _ := envConfig["instances"].([]any)
	if len(instances) != 1 {
		t.Fatalf("warm env_config has %d instances, want 1", len(instances))
	}
	inst, _ := instances[0].(map[string]any)
	disk, _ := inst["disk"].(map[string]any)
	key, _ := disk["default_build"].(string)
	if common, ok := envConfig["common"].(map[string]any); !ok || common["group_name"] != device.WarmGroupName {
		t.Fatalf("warm env_config does not name the reserved group: %v", envConfig)
	}
	return key
}

type fakePlanner struct{ keys []build.WarmKey }

func (f fakePlanner) WarmFetchKeys(context.Context) ([]build.WarmKey, error) {
	return f.keys, nil
}

type fakeStandbyLister struct{ hosts []*host.Host }

func (f fakeStandbyLister) ListStandby(context.Context, time.Duration, int) ([]*host.Host, error) {
	return f.hosts, nil
}

// swappableLister lets a test take a host out of the standby inventory and put
// it back: a lease draining and then being renewed, or a device landing on a
// standby host and later being deleted. Both are ordinary, and both are what
// pruning on listing membership treated as the host ceasing to exist.
type swappableLister struct{ hosts []*host.Host }

func (l *swappableLister) ListStandby(context.Context, time.Duration, int) ([]*host.Host, error) {
	return l.hosts, nil
}

// planeWith is planeFor over a listing the test can change between cycles.
func planeWith(db *spannertest.TestDB, keys []build.WarmKey, lister StandbyLister) *Plane {
	return New(
		fakePlanner{keys: keys},
		lister,
		hoclient.NewFactory(hoclient.DefaultClientConfig()),
		spannerplatform.NewArtifactCacheRepository(db.Client),
		testLead, nil,
	)
}

// bakeToCompletion drives one host's walk through a full bake: start it, promote
// the group to Running, and let the next cycle finish and ledger it.
func bakeToCompletion(ctx context.Context, p *Plane, ho *fakeWarmHO) {
	p.Advance(ctx)
	ho.promote(device.CVDStateRunning)
	p.Advance(ctx)
}

// planeFor builds a plane over a fake HO and the REAL artifact ledger. The
// keeper is absent by construction now: the walk needs a planner, a listing, an
// HO factory and a ledger, and nothing about holding a floor.
func planeFor(db *spannertest.TestDB, keys []build.WarmKey, hosts []*host.Host) *Plane {
	return New(
		fakePlanner{keys: keys},
		fakeStandbyLister{hosts: hosts},
		hoclient.NewFactory(hoclient.DefaultClientConfig()),
		spannerplatform.NewArtifactCacheRepository(db.Client),
		testLead, nil,
	)
}

// warmHostAt points a host row at a fake HO's address.
func warmHostAt(t *testing.T, srvURL, id string, incarnation int64) *host.Host {
	t.Helper()
	u, err := url.Parse(srvURL)
	if err != nil {
		t.Fatalf("parsing fake HO URL: %v", err)
	}
	port, err := strconv.ParseInt(u.Port(), 10, 64)
	if err != nil {
		t.Fatalf("parsing fake HO port: %v", err)
	}
	return &host.Host{
		ID: id, Status: host.StatusRunning, Incarnation: incarnation,
		IPAddress: "127.0.0.1", HostOrchestratorPort: port,
	}
}

// The full warm walk against a fake HO and the REAL ledger: the default
// (tracking) bake completes without a ledger row, the pinned bake completes
// WITH one, the row the affinity ordering matches on. Dropping the ledger
// write, the delete-on-running, or the state advance goes red here.
func TestWarmWalkBakesAndLedgersPinnedKeys(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	if err := db.SeedHost(ctx, "standby-h1", "standby-h1", "us-central1-a"); err != nil {
		t.Fatal(err)
	}

	ho := &fakeWarmHO{}
	srv := httptest.NewServer(ho.handler(t))
	t.Cleanup(srv.Close)

	const (
		trackingKey = "@ab/aosp-android-latest-release/aosp_cf_x86_64_only_phone-userdebug"
		pinnedKey   = "@ab/12345678/aosp_cf_x86_64_only_phone-userdebug"
	)
	target := warmHostAt(t, srv.URL, "standby-h1", 3)
	p := planeFor(db, []build.WarmKey{
		{FetchKey: trackingKey},
		{FetchKey: pinnedKey, RegistryID: "reg-1", Pinned: true},
	}, []*host.Host{target})

	// Bake 1: the default build. Start, observe it baking, promote, finish.
	p.Advance(ctx)
	if len(ho.creates) != 1 || ho.creates[0] != trackingKey {
		t.Fatalf("first bake creates = %v, want [%s]", ho.creates, trackingKey)
	}
	p.Advance(ctx) // still Starting: the walk must not touch it
	if ho.deletes != 0 {
		t.Fatal("a baking group was deleted")
	}
	ho.promote(device.CVDStateRunning)
	p.Advance(ctx)
	if ho.deletes != 1 {
		t.Fatalf("finished bake deletes = %d, want 1", ho.deletes)
	}
	if keys := ledgerKeys(t, db, "standby-h1"); len(keys) != 0 {
		t.Fatalf("a tracking bake was ledgered: %v", keys)
	}

	// Bake 2: the pinned build, spread onto the same host.
	p.Advance(ctx)
	if len(ho.creates) != 2 || ho.creates[1] != pinnedKey {
		t.Fatalf("second bake creates = %v, want the pinned key next", ho.creates)
	}
	ho.promote(device.CVDStateRunning)
	p.Advance(ctx)
	if ho.deletes != 2 {
		t.Fatalf("finished pinned bake deletes = %d, want 2", ho.deletes)
	}
	keys := ledgerKeys(t, db, "standby-h1")
	if len(keys) != 1 || keys[0] != pinnedKey {
		t.Fatalf("ledger after pinned bake = %v, want [%s]", keys, pinnedKey)
	}

	// Fully warm: another cycle starts nothing.
	p.Advance(ctx)
	if len(ho.creates) != 2 {
		t.Fatalf("a fully-warm host was re-baked: %v", ho.creates)
	}
}

// A group still LOADING (Preparing on the wire, the state the whole fetch
// runs under) must never be deleted: `cvd rm` under a live launch leaks its
// processes and poisons the host. This is the live 19:57 incident shape:
// the husk arm caught Preparing and tried exactly that delete.
func TestWarmWalkNeverDeletesALoadingGroup(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	if err := db.SeedHost(t.Context(), "standby-h3", "standby-h3", "us-central1-a"); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{device.CVDStatePreparing, device.CVDStateStarting, device.CVDStateUnknown} {
		ho := &fakeWarmHO{status: status}
		srv := httptest.NewServer(ho.handler(t))
		t.Cleanup(srv.Close)

		p := planeFor(db,
			[]build.WarmKey{{FetchKey: "@ab/1/t"}},
			[]*host.Host{warmHostAt(t, srv.URL, "standby-h3", 0)})

		p.Advance(t.Context())
		if ho.deletes != 0 {
			t.Errorf("status %q: deletes = %d, want 0 (a live launch must settle, never be removed)", status, ho.deletes)
		}
		if len(ho.creates) != 0 {
			t.Errorf("status %q: a second bake started under a live one", status)
		}
	}
}

// A quiescent non-Running warm group (a failed bake or a crashed keeper's
// leftover) is cleared so the walk can restart, never adopted as done.
func TestWarmWalkClearsStaleHusk(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	if err := db.SeedHost(t.Context(), "standby-h2", "standby-h2", "us-central1-a"); err != nil {
		t.Fatal(err)
	}
	ho := &fakeWarmHO{status: device.CVDStateBootFailed}
	srv := httptest.NewServer(ho.handler(t))
	t.Cleanup(srv.Close)

	p := planeFor(db,
		[]build.WarmKey{{FetchKey: "@ab/1/t"}},
		[]*host.Host{warmHostAt(t, srv.URL, "standby-h2", 0)})

	p.Advance(t.Context())
	if ho.deletes != 1 {
		t.Fatalf("husk deletes = %d, want 1", ho.deletes)
	}
	if keys := ledgerKeys(t, db, "standby-h2"); len(keys) != 0 {
		t.Fatalf("a husk was ledgered: %v", keys)
	}
}

// A host that leaves the standby inventory and comes back keeps its warm
// history, so the next cycle starts no bake.
//
// This is the defect the state tracking replaces. A lease draining is exactly
// leaving that listing, so a host shortened and then renewed returned as if its
// disk were empty and re-baked every key, and the re-bake registered as in
// flight, which is what the reclamation side reads to avoid choosing a host
// about to become warm, so the next shorten fell on an actually colder one.
// Under the drained sawtooth this design produces, that is the steady state.
//
// The assertion is that no CREATE is issued, not that a map is non-empty:
// bookkeeping that survives without changing what the walk does proves nothing.
func TestWarmStateSurvivesAHostLeavingTheInventory(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	for _, id := range []string{"drains", "stays"} {
		if err := db.SeedHost(ctx, id, id, "us-central1-a"); err != nil {
			t.Fatal(err)
		}
	}

	drains, stays := &fakeWarmHO{}, &fakeWarmHO{}
	srvA := httptest.NewServer(drains.handler(t))
	srvB := httptest.NewServer(stays.handler(t))
	t.Cleanup(srvA.Close)
	t.Cleanup(srvB.Close)

	drainer := warmHostAt(t, srvA.URL, "drains", 0)
	stayer := warmHostAt(t, srvB.URL, "stays", 0)
	lister := &swappableLister{hosts: []*host.Host{drainer, stayer}}
	p := planeWith(db, []build.WarmKey{{FetchKey: "@ab/1/t"}}, lister)

	bakeToCompletion(ctx, p, drains)
	if len(drains.creates) != 1 {
		t.Fatalf("creates after the first bake = %v, want one", drains.creates)
	}

	// The lease drains: the host leaves the inventory while its disk, and every
	// artifact on it, stays exactly where it was. Another host remains, so the
	// walk still runs and still has the chance to forget.
	lister.hosts = []*host.Host{stayer}
	p.Advance(ctx)

	// Renewed, and back.
	lister.hosts = []*host.Host{drainer, stayer}
	p.Advance(ctx)
	if len(drains.creates) != 1 {
		t.Errorf("creates after the host returned = %v, want the original one; a renewed host must not re-bake", drains.creates)
	}
}

// An incarnation bump is a fresh, cold disk, and its predecessor's warm history
// must not carry over. This is the other half of the tracking rule, and the one
// that has to stay precise: age alone would keep claiming the old disk's bakes
// for a day after the artifacts went with the VM.
func TestWarmStateDropsOnAnIncarnationBump(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	if err := db.SeedHost(ctx, "reprovisioned", "reprovisioned", "us-central1-a"); err != nil {
		t.Fatal(err)
	}
	ho := &fakeWarmHO{}
	srv := httptest.NewServer(ho.handler(t))
	t.Cleanup(srv.Close)

	lister := &swappableLister{hosts: []*host.Host{warmHostAt(t, srv.URL, "reprovisioned", 0)}}
	p := planeWith(db, []build.WarmKey{{FetchKey: "@ab/1/t"}}, lister)

	bakeToCompletion(ctx, p, ho)
	if len(ho.creates) != 1 {
		t.Fatalf("creates after the first bake = %v, want one", ho.creates)
	}

	lister.hosts = []*host.Host{warmHostAt(t, srv.URL, "reprovisioned", 1)}
	p.Advance(ctx)
	if len(ho.creates) != 2 {
		t.Errorf("creates on the new incarnation = %v, want a second; a fresh disk holds nothing", ho.creates)
	}
}

// A host keeps its pinned build when the listing around it changes.
//
// Handing keys out by listing position is what this replaces, and the scenario
// has to be the one that actually happens: the listing is ordered oldest-first,
// so a new host lands at the END and shifts nobody. What shifts everybody is a
// host LEAVING from the front, which under this design is routine (a lease
// draining, a device landing on it, a TTL expiring). Every remaining host then
// slides down one index and is assigned a different build, which it bakes onto a
// disk nothing ever evicts from. Under renewal, hosts live long enough to
// collect the whole pinned set that way.
//
// Two hosts and three pinned keys, so the survivor's index moves from 1 to 0 and
// a position-based assignment is guaranteed to hand it a different key. The
// extra create is what goes red.
func TestPinnedKeyDoesNotFollowTheListingPosition(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	for _, id := range []string{"older", "survivor"} {
		if err := db.SeedHost(ctx, id, id, "us-central1-a"); err != nil {
			t.Fatal(err)
		}
	}
	older, survivor := &fakeWarmHO{}, &fakeWarmHO{}
	srvA := httptest.NewServer(older.handler(t))
	srvB := httptest.NewServer(survivor.handler(t))
	t.Cleanup(srvA.Close)
	t.Cleanup(srvB.Close)

	keys := []build.WarmKey{
		{FetchKey: "@ab/aosp-android-latest-release/x-userdebug"},
		{FetchKey: "@ab/11111111/x-userdebug", RegistryID: "r1", Pinned: true},
		{FetchKey: "@ab/22222222/x-userdebug", RegistryID: "r2", Pinned: true},
		{FetchKey: "@ab/33333333/x-userdebug", RegistryID: "r3", Pinned: true},
	}
	first := warmHostAt(t, srvA.URL, "older", 0)
	second := warmHostAt(t, srvB.URL, "survivor", 0)
	lister := &swappableLister{hosts: []*host.Host{first, second}}
	p := planeWith(db, keys, lister)

	// Warm both fully: the default build, then each host's own pinned build.
	for range 2 {
		p.Advance(ctx)
		older.promote(device.CVDStateRunning)
		survivor.promote(device.CVDStateRunning)
		p.Advance(ctx)
	}
	if len(survivor.creates) != 2 {
		t.Fatalf("creates = %v, want the default build and one pinned build", survivor.creates)
	}
	p.Advance(ctx)
	if len(survivor.creates) != 2 {
		t.Fatalf("a fully-warm host re-baked: %v", survivor.creates)
	}

	// The older host leaves, drained or holding a device. The survivor slides
	// from index 1 to index 0, and its assignment must not move with it.
	lister.hosts = []*host.Host{second}
	p.Advance(ctx)
	if len(survivor.creates) != 2 {
		t.Errorf("creates after the host ahead of it left = %v, want the original two; the pinned build is the host's, not its position's",
			survivor.creates)
	}
}

// What the reclamation side reads to spare the host closest to becoming warm.
//
// The window is exactly the bake: nothing before it starts, the host while it
// runs, nothing once it finishes. That last transition is the one worth
// pinning: a bake that finished has ledger rows, so the host is no longer the
// coldest and no longer needs sparing; an exemption that outlived its bake
// would protect a host from reclamation on the strength of work that is
// already done.
//
// The incarnation is the disk's, not a placeholder. A bump is a fresh cold disk,
// so an exemption carrying the wrong one spares a host for artifacts that are
// gone, which is why the host here is at incarnation 3 and an accessor
// answering 0 goes red.
func TestBakeInFlightReportsTheDiskBeingBaked(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	if err := db.SeedHost(ctx, "baking-h", "baking-h", "us-central1-a"); err != nil {
		t.Fatal(err)
	}
	ho := &fakeWarmHO{}
	srv := httptest.NewServer(ho.handler(t))
	t.Cleanup(srv.Close)

	p := planeFor(db,
		[]build.WarmKey{{FetchKey: "@ab/1/t", RegistryID: "r1", Pinned: true}},
		[]*host.Host{warmHostAt(t, srv.URL, "baking-h", 3)})

	if got := p.BakeInFlight(); len(got) != 0 {
		t.Fatalf("BakeInFlight = %v before any bake, want empty", got)
	}

	p.Advance(ctx)
	got := p.BakeInFlight()
	if len(got) != 1 || got["baking-h"] != 3 {
		t.Fatalf("BakeInFlight = %v mid-bake, want baking-h at incarnation 3", got)
	}

	// A copy: the map behind this is rewritten every cycle, so handing out the
	// live one would let a consumer's read race the walk's write.
	got["baking-h"] = 99
	got["invented"] = 1
	if again := p.BakeInFlight(); again["baking-h"] != 3 || len(again) != 1 {
		t.Errorf("BakeInFlight = %v after the caller mutated its copy, want the plane's own state", again)
	}

	ho.promote(device.CVDStateRunning)
	p.Advance(ctx)
	if after := p.BakeInFlight(); len(after) != 0 {
		t.Errorf("BakeInFlight = %v after the bake finished, want empty; the host has ledger rows now", after)
	}
}

// ledgerKeys reads the affinity-ledger keys on a host.
func ledgerKeys(t *testing.T, db *spannertest.TestDB, hostID string) []string {
	t.Helper()
	iter := db.Client.Single().Query(t.Context(), gspanner.Statement{
		SQL:    `SELECT fetch_key FROM host_artifact_cache WHERE host_id = @host ORDER BY fetch_key`,
		Params: map[string]any{"host": hostID},
	})
	var keys []string
	if err := iter.Do(func(row *gspanner.Row) error {
		var k string
		if err := row.Column(0, &k); err != nil {
			return err
		}
		keys = append(keys, k)
		return nil
	}); err != nil {
		t.Fatalf("listing ledger keys: %v", err)
	}
	return keys
}
