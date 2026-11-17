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
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/spanner"
	"google.golang.org/api/option"

	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/domain/idempotency"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/domain/quota"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/idempotencytest"
)

const testHostID = "11111111-2222-3333-4444-555555555555"

// atomicHostRepo extends the shared host fake with the AtomicHostRepository
// operations, mirroring the transactional contract of the Spanner repository:
// extra mutations "commit" only when the enclosing operation succeeds, tracked
// in committedExtras.
type atomicHostRepo struct {
	*hosttest.FakeRepo
	quotaErr        error // returned by CreateWithQuotaIncrement when set
	committedExtras int
	quotaDeleted    []string
	liveSnapshots   int // rows SnapshotsLostMutations pretends the host holds
	// The per-pool bound each reclamation listing was opened with, one entry
	// per statement. The count is what the fleet's statement budget is about
	// and the bound is what makes a single statement enough for every pool.
	rescuableLists   []int
	shortenableLists []int
	// gceUpdates are the observation writes BatchUpdateGCEState applied.
	gceUpdates []host.GCEStateUpdate
	mutateErr  error // returned by Mutate when set
}

func newAtomicHostRepo(seed ...*host.Host) *atomicHostRepo {
	return &atomicHostRepo{FakeRepo: hosttest.NewFakeRepo(seed...)}
}

func (f *atomicHostRepo) CreateWithQuotaIncrement(_ context.Context, h *host.Host, extra ...*spanner.Mutation) error {
	if f.quotaErr != nil {
		return f.quotaErr
	}
	if f.CreateErr != nil {
		return f.CreateErr
	}
	f.Hosts[h.ID] = h
	f.committedExtras += len(extra)
	return nil
}

func (f *atomicHostRepo) Mutate(_ context.Context, id string, fn func(*host.Host) error, extra ...*spanner.Mutation) (*host.Host, error) {
	if f.mutateErr != nil {
		return nil, f.mutateErr
	}
	h, ok := f.Hosts[id]
	if !ok {
		return nil, host.ErrNotFound
	}
	working := *h
	if err := fn(&working); err != nil {
		return nil, fmt.Errorf("mutating host %s: %w", id, err)
	}
	working.UpdatedAt = time.Now()
	f.Hosts[id] = &working
	f.committedExtras += len(extra)
	return &working, nil
}

func (f *atomicHostRepo) DeleteWithQuotaDecrement(_ context.Context, id string) error {
	delete(f.Hosts, id)
	f.quotaDeleted = append(f.quotaDeleted, id)
	return nil
}

// SnapshotsLostMutations returns one placeholder mutation per fake "live
// snapshot", so tests can assert the retry path collects and commits them.
func (f *atomicHostRepo) SnapshotsLostMutations(context.Context, string) ([]*spanner.Mutation, error) {
	muts := make([]*spanner.Mutation, f.liveSnapshots)
	for i := range muts {
		muts[i] = spanner.Delete("snapshots", spanner.Key{"fake", strconv.Itoa(i)})
	}
	return muts, nil
}

// ListRescuable serves no candidates. The selection is real SQL over three
// columns and a subquery, so it is exercised against the emulator; a fake that
// answered here would only be asserting its own fixture. What it does record is
// the read itself, which is the one thing the emulator tests cannot see.
func (f *atomicHostRepo) ListRescuable(_ context.Context, _ time.Duration, perPool int) ([]*host.Host, error) {
	f.rescuableLists = append(f.rescuableLists, perPool)
	return nil, nil
}

// ExtendLease mirrors the real refusals (a protected host's NULL lease, a
// target that would LOWER the lease, and one past the age ceiling), because a
// fake that raised unconditionally would let a service bug through in exactly
// the direction provision_only may never move.
func (f *atomicHostRepo) ExtendLease(ctx context.Context, id string, target time.Time, extra ...*spanner.Mutation) (bool, error) {
	h, ok := f.Hosts[id]
	if !ok {
		return false, host.ErrNotFound
	}
	if h.Lifecycle.ExpiresAt == nil || !target.After(*h.Lifecycle.ExpiresAt) ||
		target.After(h.CreatedAt.Add(host.MaxFleetRescueAge)) {
		return false, nil
	}
	if _, err := f.Mutate(ctx, id, func(m *host.Host) error {
		m.Lifecycle.ExpiresAt = &target
		return nil
	}, extra...); err != nil {
		return false, err
	}
	return true, nil
}

// ListShortenable and ListCollapsible serve no candidates, for the reason
// ListRescuable does not: the selection is real SQL, and a fake answering here
// would assert its own fixture.
func (f *atomicHostRepo) ListShortenable(_ context.Context, _ time.Duration, perPool int) ([]*host.Host, error) {
	f.shortenableLists = append(f.shortenableLists, perPool)
	return nil, nil
}

func (f *atomicHostRepo) ListCollapsible(context.Context, time.Duration, int) ([]*host.Host, error) {
	return nil, nil
}

// ShortenLease mirrors the two refusals a fake can see: a protected host, and a
// target that would not actually lower the lease. It cannot mirror the third
// and most important one, because the live-device guard is a read of another
// table under the write's own transaction, which is why the promise invariant
// is asserted against the emulator and not here.
func (f *atomicHostRepo) ShortenLease(ctx context.Context, id string, target time.Time, extra ...*spanner.Mutation) (bool, error) {
	h, ok := f.Hosts[id]
	if !ok {
		return false, host.ErrNotFound
	}
	if h.Lifecycle.IsProtected() ||
		(h.Lifecycle.ExpiresAt != nil && !target.Before(*h.Lifecycle.ExpiresAt)) {
		return false, nil
	}
	if _, err := f.Mutate(ctx, id, func(m *host.Host) error {
		m.Lifecycle.ExpiresAt = &target
		return nil
	}, extra...); err != nil {
		return false, err
	}
	return true, nil
}

// BatchUpdateGCEState mirrors the CAS on PrevStatus and records every update
// it applied. The device settle that rides a stopped update is SQL over
// another table, so it is asserted against the emulator and not here.
func (f *atomicHostRepo) BatchUpdateGCEState(_ context.Context, updates []host.GCEStateUpdate) (int64, error) {
	var applied int64
	for _, u := range updates {
		h, ok := f.Hosts[u.ID]
		if !ok || h.Status != u.PrevStatus {
			continue
		}
		working := *h
		working.Status = u.Status
		working.VMStatus = u.VMStatus
		working.IPAddress = u.IP
		working.InternalIP = u.InternalIP
		working.VMTerminatesAt = u.VMTerminatesAt
		f.Hosts[u.ID] = &working
		f.gceUpdates = append(f.gceUpdates, u)
		applied++
	}
	return applied, nil
}

// ctxAwareHostRepo refuses Mutate on an already-canceled context, modeling a
// real Spanner write that fails on a dead context. It backs the test that the
// terminal mark*Failed writes detach from the exhausted attempt's (often
// already-canceled) context.
type ctxAwareHostRepo struct {
	*atomicHostRepo
}

func (f *ctxAwareHostRepo) Mutate(ctx context.Context, id string, fn func(*host.Host) error, extra ...*spanner.Mutation) (*host.Host, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("refusing write on dead context: %w", err)
	}
	return f.atomicHostRepo.Mutate(ctx, id, fn, extra...)
}

func TestMarkDeprovisionStalledDetachesFromDeadContext(t *testing.T) {
	t.Parallel()
	seed := &host.Host{ID: testHostID, Name: "h", Zone: "z", MaxDevices: 4, Status: host.StatusDeleting, DesiredState: host.DesiredAbsent}
	repo := &ctxAwareHostRepo{newAtomicHostRepo(seed)}
	svc := &HostService{hostRepo: repo, logger: slog.Default()}

	// The exhausted teardown failed because its own context died; the Stalled
	// mark must still land (detached write). The host stays "deleting" so
	// host.reconcile keeps re-driving the idempotent delete.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	svc.markDeprovisionStalled(ctx, testHostID, errors.New("boom"))

	got := repo.Hosts[testHostID]
	if got.Status != host.StatusDeleting {
		t.Errorf("status = %q, want deleting", got.Status)
	}
	if got.StalledReason == "" {
		t.Error("the Stalled mark must land despite the dead context")
	}
}

// fakeEnqueuer records enqueued tasks and returns a throwaway mutation. Note
// the service calls EnqueueMutation before the transaction, so this records
// attempts; committedExtras on the repo counts what actually committed.
type fakeEnqueuer struct {
	enqueued []*task.Task
	wakes    int
}

// Wake is what the producer calls once the transaction carrying the mutation
// committed, so the count separates "a mutation was built" from "the commit
// that carried it succeeded".
func (f *fakeEnqueuer) Wake() { f.wakes++ }

func (f *fakeEnqueuer) EnqueueMutation(_ context.Context, t *task.Task) *spanner.Mutation {
	f.enqueued = append(f.enqueued, t)
	return spanner.Delete("tasks", spanner.Key{t.ID})
}

func newTestService(t *testing.T, repo *atomicHostRepo, client *compute.InstancesClient, opts ...HostServiceOption) (*HostService, *fakeEnqueuer) {
	t.Helper()
	enq := &fakeEnqueuer{}
	opts = append([]HostServiceOption{WithTaskEnqueuer(enq), WithImageResolver(staticImages{})}, opts...)
	svc := NewHostService(
		HostServiceConfig{ProjectID: "test-project", HostOrchestratorPort: 2080},
		client, nil, repo, slog.New(slog.DiscardHandler), opts...)
	return svc, enq
}

// fakeGCE serves the narrow slice of the GCE Instances REST API the service
// uses (start/stop/get), so the concrete *compute.InstancesClient can be
// exercised without an interface refactor.
type fakeGCE struct {
	mu       sync.Mutex
	requests []string
	failAll  bool
	// instanceJSON is returned for GET instance requests.
	instanceJSON string
}

func (f *fakeGCE) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		fail := f.failAll
		f.mu.Unlock()
		if fail {
			http.Error(w, `{"error":{"code":500,"message":"boom"}}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/start"), strings.HasSuffix(r.URL.Path, "/stop"):
			fmt.Fprint(w, `{"name":"op-1","status":"DONE"}`)
		case r.Method == http.MethodGet:
			fmt.Fprint(w, f.instanceJSON)
		default:
			fmt.Fprint(w, `{"name":"op-1","status":"DONE"}`)
		}
	})
}

func (f *fakeGCE) calls(suffix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, req := range f.requests {
		if strings.HasSuffix(req, suffix) {
			n++
		}
	}
	return n
}

// newFakeGCEClient returns an InstancesClient wired to an in-process fake GCE.
func newFakeGCEClient(t *testing.T, fake *fakeGCE) *compute.InstancesClient {
	t.Helper()
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	client, err := compute.NewInstancesRESTClient(t.Context(),
		option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("creating fake GCE client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestHostServiceCreate(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo()
	svc, enq := newTestService(t, repo, nil)

	h, err := svc.Create(t.Context(), host.CreateRequest{
		Name: "My Host", Zone: "europe-west4-a", MachineType: "n1-standard-8", MaxDevices: 4,
	}, "user@example.com", host.OriginUser)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if h.Status != host.StatusPending {
		t.Errorf("status = %q, want pending", h.Status)
	}
	if !strings.HasPrefix(h.GCEInstanceName, "cf-my-host-") {
		t.Errorf("GCE instance name = %q, want cf-my-host-* prefix", h.GCEInstanceName)
	}
	if h.ProvisioningModel != host.ProvisioningModelStandard {
		t.Errorf("provisioning model = %q, want STANDARD default", h.ProvisioningModel)
	}
	if h.CreatedBy == nil || *h.CreatedBy != "user@example.com" {
		t.Errorf("created_by = %v, want user@example.com", h.CreatedBy)
	}
	if _, ok := repo.Hosts[h.ID]; !ok {
		t.Error("host not stored in repository")
	}
	if len(enq.enqueued) != 1 || enq.enqueued[0].Type != TaskTypeProvisionHost || enq.enqueued[0].ResourceID != h.ID {
		t.Errorf("enqueued = %+v, want one %s task for %s", enq.enqueued, TaskTypeProvisionHost, h.ID)
	}
	if repo.committedExtras != 1 {
		t.Errorf("committed extras = %d, want 1 (the provision enqueue)", repo.committedExtras)
	}
	if h.Lifecycle.TTLSeconds != host.DefaultHostTTLSeconds {
		t.Errorf("ttl = %d, want default %d", h.Lifecycle.TTLSeconds, host.DefaultHostTTLSeconds)
	}
	// The origin reaches the row it was passed for. buildHost assembles the row
	// from the request, and the origin is the one field that arrives beside the
	// request rather than inside it. Dropping it there writes NULL, and the
	// host silently becomes reserved.
	if h.Origin != host.OriginUser {
		t.Errorf("origin = %q, want %q", h.Origin, host.OriginUser)
	}
}

// No host may be created without a pool, whichever path created it. Every
// create (the /v2 and /v1 creates, the declarative miss, the keeper's top-up, a
// batch's host) assembles its row in buildHost, so the origins those paths pass
// are the coverage: a stamp moved out of there into one caller leaves the others
// creating hosts in no named pool, which is a host no pool's arithmetic counts.
//
// What the row model then stores is a different question with a different
// answer, and it is asserted where it is decided: on the row's own round trip
// and against the emulator.
func TestCreateStampsThePoolOnEveryOrigin(t *testing.T) {
	t.Parallel()
	for _, origin := range host.Origins {
		t.Run(string(origin), func(t *testing.T) {
			t.Parallel()
			repo := newAtomicHostRepo()
			svc, _ := newTestService(t, repo, nil)

			h, err := svc.Create(t.Context(), host.CreateRequest{
				Name: "stamped", Zone: "europe-west4-a", MachineType: "n1-standard-8", MaxDevices: 4,
			}, "user@example.com", origin)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if h.Pool != host.PoolDefault {
				t.Errorf("pool = %q, want %q", h.Pool, host.PoolDefault)
			}
		})
	}
}

// The stamp is the pool the RESOLUTION judged, not a constant. A create routed
// into one pool and stamped into another is placed against one population and
// counted against the other, and neither pool's numbers then describe what
// happened. Restore the constant and this row goes red while the one above
// stays green.
func TestCreateStampsTheResolvedPool(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo()
	svc, _ := newTestService(t, repo, nil)

	h, err := svc.Create(t.Context(), host.CreateRequest{
		Name: "accelerated", Zone: "europe-west4-a", MachineType: "n1-standard-8",
		MaxDevices: 4, Pool: host.PoolGPU,
	}, "user@example.com", host.OriginAuto)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if h.Pool != host.PoolGPU {
		t.Errorf("pool = %q, want %q", h.Pool, host.PoolGPU)
	}
}

// TestCreateHostHandlerStampsUserOrigin covers the native /v2 create's own
// choice of provenance, which the service test above cannot: it passes the
// origin in, so it would agree with a handler that stamped anything at all.
//
// A host somebody asked for must be reserved, the class the fleet leaves
// alone. Stamp it 'auto' or 'pool' here and every host created through the API
// becomes managed inventory: counted toward the standby floor it was never
// meant to serve, and eligible to have its lease shortened underneath its
// owner.
func TestCreateHostHandlerStampsUserOrigin(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo()
	svc, _ := newTestService(t, repo, nil)
	handler := NewHostHandler(svc)

	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Email: "u@example.com"})
	input := &CreateHostInput{}
	input.Body.Name = "mine"
	input.Body.Zone = "europe-west4-a"
	input.Body.MachineType = "n1-standard-8"
	input.Body.MaxDevices = 4

	out, err := handler.CreateHost(ctx, input)
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	if out.Body.Origin != host.OriginUser {
		t.Errorf("origin = %q, want %q", out.Body.Origin, host.OriginUser)
	}
	if got := host.DeriveClass(out.Body.Origin, out.Body.Exclusive, out.Body.BatchID); got != host.ClassReserved {
		t.Errorf("an API-created host derives class %q, want %q", got, host.ClassReserved)
	}
}

// A user fencing their host from placement must not thereby hand it to the
// fleet. `exclusive` is the field they set to protect an in-flight test, and a
// class arm keyed on it alone derives 'dedicated', whose rule is "reclaim it
// when empty", which is exactly what an idle host between two test runs is.
func TestCreateHostHandlerExclusiveStaysReserved(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo()
	svc, _ := newTestService(t, repo, nil)
	handler := NewHostHandler(svc)

	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Email: "u@example.com"})
	input := &CreateHostInput{}
	input.Body.Name = "fenced"
	input.Body.Zone = "europe-west4-a"
	input.Body.MachineType = "n1-standard-8"
	input.Body.MaxDevices = 4
	input.Body.Exclusive = true

	out, err := handler.CreateHost(ctx, input)
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	if !out.Body.Exclusive {
		t.Fatal("the create did not carry exclusive through; the assertion below would be vacuous")
	}
	if got := host.DeriveClass(out.Body.Origin, out.Body.Exclusive, out.Body.BatchID); got != host.ClassReserved {
		t.Errorf("a user's fenced host derives class %q, want %q", got, host.ClassReserved)
	}
}

func TestHostServiceCreateTTLHours(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t, newAtomicHostRepo(), nil)

	h, err := svc.Create(t.Context(), host.CreateRequest{
		Name: "long-host", Zone: "europe-west4-a", MachineType: "n1-standard-8",
		MaxDevices: 4, TTLHours: 48,
	}, "user@example.com", host.OriginUser)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if h.Lifecycle.TTLSeconds != 48*3600 {
		t.Errorf("ttl = %d, want %d", h.Lifecycle.TTLSeconds, 48*3600)
	}
	if h.Lifecycle.ExpiresAt == nil {
		t.Fatal("expires_at not set")
	}
	if remaining := time.Until(*h.Lifecycle.ExpiresAt); remaining < 47*time.Hour {
		t.Errorf("expires_at only %v away, want ~48h", remaining)
	}
}

func TestHostServiceCreateInvalidProvisioningModel(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t, newAtomicHostRepo(), nil)
	_, err := svc.Create(t.Context(), host.CreateRequest{
		Name: "h", Zone: "z", ProvisioningModel: "WEIRD",
	}, "u", host.OriginUser)
	if err == nil || !strings.Contains(err.Error(), "provisioning model") {
		t.Errorf("err = %v, want invalid provisioning model", err)
	}
}

// A UUID-shaped name passes the transport pattern, so the service is the only
// thing between it and a stored host. The /v1 path has no pattern at all.
func TestHostServiceCreateRejectsIDShapedName(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo()
	svc, enq := newTestService(t, repo, nil)
	_, err := svc.Create(t.Context(), host.CreateRequest{
		Name: "abcdef01-2345-6789-abcd-ef0123456789", Zone: "z",
	}, "u", host.OriginUser)
	if _, ok := errors.AsType[*host.IDShapedNameError](err); !ok {
		t.Fatalf("err = %v, want an IDShapedNameError", err)
	}
	if len(repo.Hosts) != 0 || len(enq.enqueued) != 0 {
		t.Errorf("stored %d hosts and enqueued %d tasks, want none", len(repo.Hosts), len(enq.enqueued))
	}
}

func TestHostServiceCreateRequiresTaskEnqueuer(t *testing.T) {
	t.Parallel()
	svc := NewHostService(HostServiceConfig{}, nil, nil, newAtomicHostRepo(), slog.New(slog.DiscardHandler))
	_, err := svc.Create(t.Context(), host.CreateRequest{Name: "h", Zone: "z"}, "u", host.OriginUser)
	if err == nil || !strings.Contains(err.Error(), "enqueuer") {
		t.Errorf("err = %v, want missing-enqueuer error", err)
	}
}

func TestHostServiceCreateIdempotency(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo()
	svc, enq := newTestService(t, repo, nil, WithHostIdempotencyStore(idempotencytest.NewFakeStore()))

	req := host.CreateRequest{Name: "idem", Zone: "z1", MaxDevices: 2, IdempotencyKey: "k1"}
	h1, err := svc.Create(t.Context(), req, "u", host.OriginUser)
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	h2, err := svc.Create(t.Context(), req, "u", host.OriginUser)
	if err != nil {
		t.Fatalf("retried Create: %v", err)
	}
	if h1.ID != h2.ID {
		t.Errorf("retry created a new host: %s != %s", h1.ID, h2.ID)
	}
	if len(repo.Hosts) != 1 {
		t.Errorf("hosts stored = %d, want 1", len(repo.Hosts))
	}
	if len(enq.enqueued) != 1 {
		t.Errorf("tasks enqueued = %d, want 1 (no second provision)", len(enq.enqueued))
	}

	// Same key, different request -> conflict, not the wrong host.
	_, err = svc.Create(t.Context(), host.CreateRequest{Name: "other", Zone: "z1", IdempotencyKey: "k1"}, "u", host.OriginUser)
	if !errors.Is(err, idempotency.ErrConflict) {
		t.Errorf("err = %v, want idempotency.ErrConflict", err)
	}
}

// TestHostServiceCreateRaceRecovery covers the lost-commit-race path: the key
// lookup misses, the create commit fails because a concurrent request with
// the same key won the insert, and the recovery lookup resolves the key.
func TestHostServiceCreateRaceRecovery(t *testing.T) {
	t.Parallel()
	winnerReq := host.CreateRequest{Name: "idem", Zone: "z1", MaxDevices: 2, IdempotencyKey: "k1"}
	fp, err := idempotency.Fingerprint(winnerReq)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	winner := &host.Host{ID: "winner", Name: "idem", Zone: "z1", Status: host.StatusRunning}

	// An equivalent request that lost the race gets the winner's host.
	repo := newAtomicHostRepo(winner)
	repo.CreateErr = errors.New("spanner: row already exists")
	svc, _ := newTestService(t, repo, nil, WithHostIdempotencyStore(idempotencytest.NewRaceStore("winner", fp)))
	got, err := svc.Create(t.Context(), winnerReq, "u", host.OriginUser)
	if err != nil {
		t.Fatalf("Create after lost race: %v", err)
	}
	if got.ID != "winner" {
		t.Errorf("host = %s, want the race winner's host", got.ID)
	}

	// A different request reusing the key gets the sequential path's
	// conflict, and never the winner's host.
	repo = newAtomicHostRepo(winner)
	repo.CreateErr = errors.New("spanner: row already exists")
	svc, _ = newTestService(t, repo, nil, WithHostIdempotencyStore(idempotencytest.NewRaceStore("winner", fp)))
	_, err = svc.Create(t.Context(), host.CreateRequest{Name: "other", Zone: "z1", IdempotencyKey: "k1"}, "u", host.OriginUser)
	if !errors.Is(err, idempotency.ErrConflict) {
		t.Errorf("err = %v, want idempotency.ErrConflict", err)
	}
}

// The wake follows the COMMIT, not the mutation. A create the quota rejected
// still built an enqueue mutation, and waking on that would send the pool
// looking for work that was never written. That is harmless but wrong, and the
// same mistake on the delete path would keep an idle pool spinning on every
// refused verb. The wake is only correct after the transaction reports success.
func TestHostServiceWakesOnlyOnACommittedEnqueue(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo()
	svc, enq := newTestService(t, repo, nil)

	if _, err := svc.Create(t.Context(),
		host.CreateRequest{Name: "committed", Zone: "z1", MaxDevices: 1}, "u", host.OriginUser); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if enq.wakes != 1 {
		t.Errorf("wakes after a committed create = %d, want 1", enq.wakes)
	}

	repo.quotaErr = &quota.ExceededError{Requested: 1, Available: 0, MaxHosts: 1}
	if _, err := svc.Create(t.Context(),
		host.CreateRequest{Name: "refused", Zone: "z1", MaxDevices: 1}, "u", host.OriginUser); err == nil {
		t.Fatal("Create: want the quota rejection")
	}
	if len(enq.enqueued) != 2 {
		t.Fatalf("mutations built = %d, want 2 (the refused create built one too)", len(enq.enqueued))
	}
	if enq.wakes != 1 {
		t.Errorf("wakes = %d, want still 1: a rejected commit enqueued nothing", enq.wakes)
	}
}

func TestHostServiceCreateQuotaExceeded(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo()
	repo.quotaErr = &quota.ExceededError{Requested: 1, Available: 0, MaxHosts: 3}
	svc, _ := newTestService(t, repo, nil)

	_, err := svc.Create(t.Context(), host.CreateRequest{Name: "h", Zone: "z"}, "u", host.OriginUser)
	var exceeded *quota.ExceededError
	if !errors.As(err, &exceeded) {
		t.Fatalf("err = %v, want *quota.ExceededError passed through unwrapped", err)
	}
	if exceeded.MaxHosts != 3 {
		t.Errorf("MaxHosts = %d, want 3", exceeded.MaxHosts)
	}
}

func TestHostServiceDelete(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo(&host.Host{ID: testHostID, Name: "web", Zone: "z1", MaxDevices: 4, Status: host.StatusRunning})
	svc, enq := newTestService(t, repo, nil)

	if err := svc.Delete(t.Context(), testHostID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := repo.Hosts[testHostID].Status; got != host.StatusDeleting {
		t.Errorf("status = %q, want deleting", got)
	}
	if repo.committedExtras != 1 {
		t.Fatalf("committed extras = %d, want 1 deprovision enqueue", repo.committedExtras)
	}
	if enq.enqueued[0].Type != TaskTypeDeprovisionHost {
		t.Errorf("task type = %q, want %s", enq.enqueued[0].Type, TaskTypeDeprovisionHost)
	}

	// Second delete is an idempotent no-op: no error, no second committed enqueue.
	if err := svc.Delete(t.Context(), testHostID); err != nil {
		t.Fatalf("repeat Delete: %v", err)
	}
	if repo.committedExtras != 1 {
		t.Errorf("committed extras after repeat = %d, want still 1", repo.committedExtras)
	}
}

func TestHostServiceDeleteInvalidState(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo(&host.Host{ID: testHostID, Name: "web", Zone: "z1", MaxDevices: 4, Status: host.StatusDeleted})
	svc, _ := newTestService(t, repo, nil)

	err := svc.Delete(t.Context(), testHostID)
	if !errors.Is(err, host.ErrInvalidState) {
		t.Errorf("err = %v, want host.ErrInvalidState", err)
	}
	if repo.committedExtras != 0 {
		t.Errorf("committed extras = %d, want 0", repo.committedExtras)
	}
}

func TestHostServiceRetryNotRetryable(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo(&host.Host{ID: testHostID, Name: "web", Zone: "z1", MaxDevices: 4, Status: host.StatusRunning})
	svc, _ := newTestService(t, repo, nil)

	_, err := svc.Retry(t.Context(), testHostID, false)
	var notRetryable *host.NotRetryableError
	if !errors.As(err, &notRetryable) {
		t.Fatalf("err = %v, want *host.NotRetryableError", err)
	}
	if notRetryable.HostID != testHostID {
		t.Errorf("HostID = %q, want %q", notRetryable.HostID, testHostID)
	}
	if repo.committedExtras != 0 {
		t.Errorf("committed extras = %d, want 0 (no provision enqueued)", repo.committedExtras)
	}
}

// The re-provision that bumps the incarnation destroys the disk, so
// RetryDelayed must fold the snapshot lost-marks into the same commit as the
// bump. A retry that forgot them would list restorable snapshots whose bytes
// are gone.
func TestRetryMarksSnapshotsLostInTheSameCommit(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo(&host.Host{
		ID: testHostID, Name: "web", Zone: "z1", MaxDevices: 4,
		Status: host.StatusError, ErrorRetryable: true,
	})
	repo.liveSnapshots = 2
	svc, _ := newTestService(t, repo, nil)

	if _, err := svc.Retry(t.Context(), testHostID, false); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if repo.committedExtras != 4 {
		t.Errorf("committed extras = %d, want 4 (provision enqueue + 2 lost marks + artifact-cache delete)",
			repo.committedExtras)
	}
}

func TestHostServiceRetryClearsErrorAndEnqueues(t *testing.T) {
	t.Parallel()
	now := time.Now()
	repo := newAtomicHostRepo(&host.Host{
		ID: testHostID, Name: "web", Zone: "z1", MaxDevices: 4,
		Status:          host.StatusError,
		ErrorRetryable:  true,
		ErrorMessage:    "zone exhausted",
		ErrorType:       "resource_exhausted",
		ErrorCode:       "ZONE_RESOURCE_POOL_EXHAUSTED",
		ErrorSuggestion: "try another zone",
		ErrorOccurredAt: &now,
		GCEInstanceName: "cf-web-old",
	})
	svc, enq := newTestService(t, repo, nil)

	h, err := svc.Retry(t.Context(), testHostID, false)
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if h.Status != host.StatusProvisioning {
		t.Errorf("status = %q, want provisioning", h.Status)
	}
	if h.ErrorMessage != "" || h.ErrorType != "" || h.ErrorCode != "" || h.ErrorSuggestion != "" ||
		h.ErrorRetryable || h.ErrorOccurredAt != nil {
		t.Errorf("error state not cleared: %+v", h)
	}
	if h.GCEInstanceName == "cf-web-old" || !strings.HasPrefix(h.GCEInstanceName, "cf-web-") {
		t.Errorf("GCE instance name = %q, want a fresh cf-web-* name", h.GCEInstanceName)
	}
	if len(enq.enqueued) != 1 || enq.enqueued[0].Type != TaskTypeProvisionHost {
		t.Errorf("enqueued = %+v, want one provision task", enq.enqueued)
	}
	if repo.committedExtras != 2 {
		t.Errorf("committed extras = %d, want 2 (provision enqueue + artifact-cache delete)", repo.committedExtras)
	}
}

// The refusal that exhausted the old incarnation must not fence the new one
// from placement: a retry is a fresh bet on capacity, and GCE has not answered
// it yet.
func TestHostServiceRetryClearsProvisionBlocked(t *testing.T) {
	t.Parallel()
	seed := &host.Host{
		ID: testHostID, Name: "web", Zone: "z1", MaxDevices: 4,
		Status: host.StatusError, ErrorRetryable: true, GCEInstanceName: "cf-web-old",
	}
	seed.MarkProvisionBlocked("zone exhausted")
	svc, _ := newTestService(t, newAtomicHostRepo(seed), nil)

	h, err := svc.Retry(t.Context(), testHostID, false)
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if h.ProvisionBlockedAt != nil || h.ProvisionBlockedReason != "" {
		t.Errorf("provision-blocked condition not cleared: %+v", h)
	}
}

func TestHostServiceRetrySpotFallsBackToStandard(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo(&host.Host{
		ID: testHostID, Name: "spot", Zone: "z1", MaxDevices: 4,
		Status:            host.StatusPreempted,
		ProvisioningModel: host.ProvisioningModelSpot,
	})
	svc, _ := newTestService(t, repo, nil)

	h, err := svc.Retry(t.Context(), testHostID, true)
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if h.ProvisioningModel != host.ProvisioningModelStandard {
		t.Errorf("provisioning model = %q, want STANDARD after fallback", h.ProvisioningModel)
	}
}

func TestHostServiceStartStopStateValidation(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo(
		&host.Host{ID: "running-host", Name: "r", Zone: "z1", MaxDevices: 4, Status: host.StatusRunning},
		&host.Host{ID: "stopped-host", Name: "s", Zone: "z1", MaxDevices: 4, Status: host.StatusStopped},
	)
	// nil GCE client: the state check must reject before any GCE call.
	svc, _ := newTestService(t, repo, nil)

	if err := svc.Start(t.Context(), "running-host"); !errors.Is(err, host.ErrInvalidState) {
		t.Errorf("Start(running) err = %v, want host.ErrInvalidState", err)
	}
	if err := svc.Stop(t.Context(), "stopped-host"); !errors.Is(err, host.ErrInvalidState) {
		t.Errorf("Stop(stopped) err = %v, want host.ErrInvalidState", err)
	}
}

func TestHostServiceStartStopGCEOrdering(t *testing.T) {
	t.Parallel()

	t.Run("StartHappyPath", func(t *testing.T) {
		t.Parallel()
		fake := &fakeGCE{}
		repo := newAtomicHostRepo(&host.Host{
			ID: testHostID, Name: "web", Zone: "z1", MaxDevices: 4,
			Status: host.StatusStopped, GCEInstanceName: "cf-web-abc",
		})
		svc, _ := newTestService(t, repo, newFakeGCEClient(t, fake))

		if err := svc.Start(t.Context(), testHostID); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if got := fake.calls("/start"); got != 1 {
			t.Errorf("GCE start calls = %d, want 1", got)
		}
		if got := repo.Hosts[testHostID].Status; got != host.StatusStarting {
			t.Errorf("status = %q, want starting", got)
		}
	})

	t.Run("StartGCEFailureLeavesStateUntouched", func(t *testing.T) {
		t.Parallel()
		fake := &fakeGCE{failAll: true}
		repo := newAtomicHostRepo(&host.Host{
			ID: testHostID, Name: "web", Zone: "z1", MaxDevices: 4,
			Status: host.StatusStopped, GCEInstanceName: "cf-web-abc",
		})
		svc, _ := newTestService(t, repo, newFakeGCEClient(t, fake))

		if err := svc.Start(t.Context(), testHostID); err == nil {
			t.Fatal("Start should surface the GCE failure")
		}
		// The transition commits only after GCE accepts the call.
		if got := repo.Hosts[testHostID].Status; got != host.StatusStopped {
			t.Errorf("status = %q, want stopped (unchanged)", got)
		}
	})

	t.Run("StopHappyPath", func(t *testing.T) {
		t.Parallel()
		fake := &fakeGCE{}
		repo := newAtomicHostRepo(&host.Host{
			ID: testHostID, Name: "web", Zone: "z1", MaxDevices: 4,
			Status: host.StatusRunning, GCEInstanceName: "cf-web-abc",
		})
		svc, _ := newTestService(t, repo, newFakeGCEClient(t, fake))

		if err := svc.Stop(t.Context(), testHostID); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if got := fake.calls("/stop"); got != 1 {
			t.Errorf("GCE stop calls = %d, want 1", got)
		}
		if got := repo.Hosts[testHostID].Status; got != host.StatusStopping {
			t.Errorf("status = %q, want stopping", got)
		}
	})

	t.Run("StopGCEFailureLeavesStateUntouched", func(t *testing.T) {
		t.Parallel()
		fake := &fakeGCE{failAll: true}
		repo := newAtomicHostRepo(&host.Host{
			ID: testHostID, Name: "web", Zone: "z1", MaxDevices: 4,
			Status: host.StatusRunning, GCEInstanceName: "cf-web-abc",
		})
		svc, _ := newTestService(t, repo, newFakeGCEClient(t, fake))

		if err := svc.Stop(t.Context(), testHostID); err == nil {
			t.Fatal("Stop should surface the GCE failure")
		}
		if got := repo.Hosts[testHostID].Status; got != host.StatusRunning {
			t.Errorf("status = %q, want running (unchanged)", got)
		}
	})
}

func TestHostServiceSyncDoesNotClobberInvalidTransition(t *testing.T) {
	t.Parallel()
	// A deleting host whose VM still reports RUNNING must not be resurrected;
	// observed IP drift is still recorded.
	fake := &fakeGCE{instanceJSON: `{
		"name": "cf-web-abc",
		"status": "RUNNING",
		"networkInterfaces": [{"networkIP": "10.0.0.9", "accessConfigs": [{"natIP": "34.9.9.9"}]}]
	}`}
	repo := newAtomicHostRepo(&host.Host{
		ID: testHostID, Name: "web", Zone: "z1", MaxDevices: 4,
		Status: host.StatusDeleting, GCEInstanceName: "cf-web-abc",
	})
	svc, _ := newTestService(t, repo, newFakeGCEClient(t, fake))

	if err := svc.Sync(t.Context(), testHostID); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	h := repo.Hosts[testHostID]
	if h.Status != host.StatusDeleting {
		t.Errorf("status = %q, want deleting (never resurrected by GCE state)", h.Status)
	}
	if h.InternalIP != "10.0.0.9" || h.IPAddress != "34.9.9.9" {
		t.Errorf("IPs = (%q, %q), want observed drift applied", h.InternalIP, h.IPAddress)
	}
}

// buildInstanceCfg and buildInstanceHost are the shared inputs for the
// TestBuildInstance* table below.
func buildInstanceCfg() HostServiceConfig {
	return HostServiceConfig{
		ProjectID:   "proj",
		Network:     "net",
		Subnetwork:  "subnet",
		NetworkTags: []string{"cuttlefish"},
		Labels:      map[string]string{"env": "test"},
	}
}

func buildInstanceHost() *host.Host {
	owner := "owner@example.com"
	return &host.Host{
		ID:                "9f8e7d6c-1111-2222-3333-444455556666",
		Name:              "web",
		GCEInstanceName:   "cf-web-abc",
		Zone:              "us-central1-a",
		Incarnation:       2,
		ProvisioningModel: host.ProvisioningModelStandard,
		CreatedBy:         &owner,
	}
}

// TestBuildInstanceCarriesIdentity pins the labels the orphan sweep and the
// Cloud Console join on. Dropping any of them leaves a VM traceable only by
// parsing its name.
func TestBuildInstanceCarriesIdentity(t *testing.T) {
	t.Parallel()

	cfg := buildInstanceCfg()
	inst := buildInstance(cfg, buildInstanceHost(), machineTypeConfig{machineType: "n1-standard-8"}, "img")

	want := map[string]string{
		"env":              "test",
		"cco-host-id":      "9f8e7d6c-1111-2222-3333-444455556666",
		"cco-incarnation":  "2",
		"cco-provisioning": "standard",
	}
	disk := inst.GetDisks()[0].GetInitializeParams()
	for k, v := range want {
		if got := inst.GetLabels()[k]; got != v {
			t.Errorf("label %s = %q, want %q", k, got, v)
		}
		if got := disk.GetLabels()[k]; got != v {
			t.Errorf("boot disk label %s = %q, want %q", k, got, v)
		}
	}

	// The config's label map is shared across every provision; building an
	// instance must not write through to it.
	if _, leaked := cfg.Labels["cco-host-id"]; leaked {
		t.Error("buildInstance mutated the shared config label map")
	}

	desc := inst.GetDescription()
	for _, want := range []string{`"web"`, "9f8e7d6c-1111-2222-3333-444455556666", "owner@example.com"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description %q missing %q", desc, want)
		}
	}
}

// TestBuildInstanceDescriptionWithoutOwner covers the /v1 and reconciler
// paths, where no principal is recorded.
func TestBuildInstanceDescriptionWithoutOwner(t *testing.T) {
	t.Parallel()

	h := buildInstanceHost()
	h.CreatedBy = nil
	desc := buildInstance(buildInstanceCfg(), h, machineTypeConfig{machineType: "n1-standard-8"}, "img").GetDescription()
	if strings.Contains(desc, "owner") {
		t.Errorf("description %q names an owner that does not exist", desc)
	}
	if !strings.Contains(desc, "9f8e7d6c-1111-2222-3333-444455556666") {
		t.Errorf("description %q lost the host id", desc)
	}
}

func TestBuildInstanceCoreShape(t *testing.T) {
	t.Parallel()
	inst := buildInstance(buildInstanceCfg(), buildInstanceHost(), machineTypeConfig{machineType: "n1-standard-8"}, "projects/p/global/images/family/f")
	if got := inst.GetName(); got != "cf-web-abc" {
		t.Errorf("name = %q", got)
	}
	if got := inst.GetMachineType(); got != "zones/us-central1-a/machineTypes/n1-standard-8" {
		t.Errorf("machine type = %q", got)
	}
	disk := inst.GetDisks()[0]
	if !disk.GetBoot() || !disk.GetAutoDelete() {
		t.Error("boot disk must be boot+autodelete")
	}
	if got := disk.GetInitializeParams().GetDiskSizeGb(); got != host.DefaultBootDiskSizeGB {
		t.Errorf("disk size = %d, want %d default", got, host.DefaultBootDiskSizeGB)
	}
	if got := disk.GetInitializeParams().GetSourceImage(); got != "projects/p/global/images/family/f" {
		t.Errorf("source image = %q", got)
	}
	ni := inst.GetNetworkInterfaces()[0]
	if got := ni.GetNetwork(); got != "projects/proj/global/networks/net" {
		t.Errorf("network = %q", got)
	}
	if got := ni.GetSubnetwork(); got != "projects/proj/regions/us-central1/subnetworks/subnet" {
		t.Errorf("subnetwork = %q", got)
	}
	if len(ni.GetAccessConfigs()) != 0 {
		t.Error("no external IP without UseExternalIP")
	}
	if inst.GetLabels()["env"] != "test" || inst.GetTags().GetItems()[0] != "cuttlefish" {
		t.Error("labels/tags not applied from config")
	}
}

// TestBuildInstanceShapeSpec pins the create-time shape settings: an explicit
// boot-disk size overrides the compiled default, and min_cpu_platform reaches
// the instance verbatim, but only when requested, because GCE treats the
// field's presence itself as a constraint.
func TestBuildInstanceShapeSpec(t *testing.T) {
	t.Parallel()

	h := buildInstanceHost()
	h.BootDiskSizeGB = 750
	h.MinCPUPlatform = "Intel Cascade Lake"
	inst := buildInstance(buildInstanceCfg(), h, machineTypeConfig{machineType: "n1-standard-8"}, "img")
	if got := inst.GetDisks()[0].GetInitializeParams().GetDiskSizeGb(); got != 750 {
		t.Errorf("disk size = %d, want the requested 750, not the default", got)
	}
	if got := inst.GetMinCpuPlatform(); got != "Intel Cascade Lake" {
		t.Errorf("minCpuPlatform = %q, want it passed verbatim", got)
	}

	plain := buildInstance(buildInstanceCfg(), buildInstanceHost(), machineTypeConfig{machineType: "n1-standard-8"}, "img")
	if plain.MinCpuPlatform != nil {
		t.Errorf("minCpuPlatform = %q without a request; must stay unset", plain.GetMinCpuPlatform())
	}
}

func TestBuildInstanceStandardDefaults(t *testing.T) {
	t.Parallel()
	inst := buildInstance(buildInstanceCfg(), buildInstanceHost(), machineTypeConfig{machineType: "n1-standard-8"}, "img")
	if got := inst.GetScheduling().GetOnHostMaintenance(); got != "TERMINATE" {
		t.Errorf("onHostMaintenance = %q, want TERMINATE", got)
	}
	if inst.GetScheduling().ProvisioningModel != nil {
		t.Error("standard hosts must not set a provisioning model")
	}
	// The failsafe for a control plane that is not there to reap: standard
	// hosts stop rather than vanish, so the bound is recoverable.
	if got := inst.GetScheduling().GetMaxRunDuration().GetSeconds(); got != hostMaxRunDurationSeconds {
		t.Errorf("maxRunDuration = %ds, want %ds (the VM outlives a dead control plane without it)", got, hostMaxRunDurationSeconds)
	}
	if got := inst.GetScheduling().GetInstanceTerminationAction(); got != "STOP" {
		t.Errorf("termination action = %q, want STOP", got)
	}
	if len(inst.GetGuestAccelerators()) != 0 {
		t.Error("no accelerators without a GPU")
	}
	if len(inst.GetServiceAccounts()) != 0 {
		t.Error("no service account when unconfigured")
	}
}

// TestBuildInstanceMarksVarEmpty pins the startup script that keeps crosvm
// unsandboxed on the host. Without it, assemble_cvd's environmental default
// puts virtio-fs on the bus for any launch that did not pin the flag, which
// every relaunch after a stop is, and crosvm cannot suspend virtio-fs, so the
// device silently stops being snapshottable.
func TestBuildInstanceMarksVarEmpty(t *testing.T) {
	t.Parallel()
	inst := buildInstance(buildInstanceCfg(), buildInstanceHost(), machineTypeConfig{machineType: "n1-standard-8"}, "img")

	items := inst.GetMetadata().GetItems()
	var script string
	for _, item := range items {
		if item.GetKey() == "startup-script" {
			script = item.GetValue()
		}
	}
	if script == "" {
		t.Fatalf("no startup-script metadata item among %d item(s)", len(items))
	}
	if !strings.HasPrefix(script, "#!") {
		t.Errorf("startup script needs an interpreter line, got %q", script)
	}
	// What assemble_cvd reads is whether the directory is EMPTY, so the script
	// has to leave something INSIDE it; creating the directory alone is the
	// mutation this catches.
	if !strings.Contains(script, "/var/empty/") {
		t.Errorf("startup script %q puts nothing inside /var/empty", script)
	}
}

func TestBuildInstanceFullNetworkPathsPassThrough(t *testing.T) {
	t.Parallel()
	cfg := buildInstanceCfg()
	cfg.Network = "projects/other/global/networks/shared"
	cfg.Subnetwork = "projects/other/regions/us-central1/subnetworks/shared-sub"
	inst := buildInstance(cfg, buildInstanceHost(), machineTypeConfig{machineType: "n1-standard-8"}, "img")
	ni := inst.GetNetworkInterfaces()[0]
	if ni.GetNetwork() != cfg.Network || ni.GetSubnetwork() != cfg.Subnetwork {
		t.Errorf("full paths must pass through unchanged: %q %q", ni.GetNetwork(), ni.GetSubnetwork())
	}
}

func TestBuildInstanceGPUAccelerators(t *testing.T) {
	t.Parallel()
	inst := buildInstance(buildInstanceCfg(), buildInstanceHost(), machineTypeConfig{machineType: "n1-standard-8", gpuType: "nvidia-tesla-t4", gpuCount: 2}, "img")
	acc := inst.GetGuestAccelerators()
	if len(acc) != 1 {
		t.Fatalf("accelerators = %d, want 1", len(acc))
	}
	if got := acc[0].GetAcceleratorType(); got != "zones/us-central1-a/acceleratorTypes/nvidia-tesla-t4" {
		t.Errorf("accelerator type = %q", got)
	}
	if got := acc[0].GetAcceleratorCount(); got != 2 {
		t.Errorf("accelerator count = %d, want 2", got)
	}
}

func TestResolveGPUAttachment(t *testing.T) {
	t.Parallel()
	const t4 = "nvidia-tesla-t4"
	tests := []struct {
		name           string
		mode           string
		defaultGPUType string
		reqType        string
		reqCount       int64
		wantType       string
		wantCount      int64
	}{
		// Attachable (N1): count is honored; zero means CPU-only, never forced.
		{"attachable omitted is cpu-only", machinetype.GPUModeAttachable, t4, "", 0, "", 0},
		{"attachable explicit type+count", machinetype.GPUModeAttachable, t4, t4, 2, t4, 2},
		{"attachable count without type uses default", machinetype.GPUModeAttachable, t4, "", 1, t4, 1},
		// Integrated (G2/G4): the GPU is part of the machine type; never attach.
		{"integrated never attaches (no request)", machinetype.GPUModeIntegrated, "", "", 0, "", 0},
		{"integrated never attaches (with request)", machinetype.GPUModeIntegrated, "", "nvidia-l4", 1, "", 0},
		// None (E2/C4A) and unseeded mode: never attach.
		{"none never attaches", machinetype.GPUModeNone, "", "", 0, "", 0},
		{"unseeded mode is safe cpu-only", "", t4, t4, 1, "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotType, gotCount := resolveGPUAttachment(tc.mode, tc.defaultGPUType, tc.reqType, tc.reqCount)
			if gotType != tc.wantType || gotCount != tc.wantCount {
				t.Errorf("resolveGPUAttachment(%q,%q,%q,%d) = (%q,%d), want (%q,%d)",
					tc.mode, tc.defaultGPUType, tc.reqType, tc.reqCount, gotType, gotCount, tc.wantType, tc.wantCount)
			}
		})
	}
}

func TestBuildInstanceSpotScheduling(t *testing.T) {
	t.Parallel()
	h := buildInstanceHost()
	h.ProvisioningModel = host.ProvisioningModelSpot
	inst := buildInstance(buildInstanceCfg(), h, machineTypeConfig{machineType: "n1-standard-8"}, "img")
	sched := inst.GetScheduling()
	if got := sched.GetProvisioningModel(); got != "SPOT" {
		t.Errorf("provisioning model = %q, want SPOT", got)
	}
	// Spot shares the one termination-action field between preemption and the
	// duration bound, and the recovery path is written against DELETE.
	if got := sched.GetInstanceTerminationAction(); got != "DELETE" {
		t.Errorf("termination action = %q, want DELETE", got)
	}
	if got := sched.GetMaxRunDuration().GetSeconds(); got != hostMaxRunDurationSeconds {
		t.Errorf("Spot hosts need the failsafe too: maxRunDuration = %ds, want %ds", got, hostMaxRunDurationSeconds)
	}
}

func TestBuildInstanceExternalIP(t *testing.T) {
	t.Parallel()
	cfg := buildInstanceCfg()
	cfg.UseExternalIP = true
	inst := buildInstance(cfg, buildInstanceHost(), machineTypeConfig{machineType: "n1-standard-8"}, "img")
	acs := inst.GetNetworkInterfaces()[0].GetAccessConfigs()
	if len(acs) != 1 || acs[0].GetType() != "ONE_TO_ONE_NAT" {
		t.Errorf("access configs = %+v, want one ONE_TO_ONE_NAT", acs)
	}
}

func TestBuildInstanceServiceAccount(t *testing.T) {
	t.Parallel()
	cfg := buildInstanceCfg()
	cfg.ServiceAccount = "runner@proj.iam.gserviceaccount.com"
	inst := buildInstance(cfg, buildInstanceHost(), machineTypeConfig{machineType: "n1-standard-8"}, "img")
	sas := inst.GetServiceAccounts()
	if len(sas) != 1 || sas[0].GetEmail() != cfg.ServiceAccount {
		t.Fatalf("service accounts = %+v", sas)
	}
	if sas[0].GetScopes()[0] != "https://www.googleapis.com/auth/cloud-platform" {
		t.Errorf("scopes = %v", sas[0].GetScopes())
	}
}

func TestBuildInstanceDiskSize(t *testing.T) {
	t.Parallel()
	inst := buildInstance(buildInstanceCfg(), buildInstanceHost(), machineTypeConfig{machineType: "n1-standard-8"}, "img")
	if got := inst.GetDisks()[0].GetInitializeParams().GetDiskSizeGb(); got != host.DefaultBootDiskSizeGB {
		t.Errorf("disk size = %d, want %d", got, host.DefaultBootDiskSizeGB)
	}
}

// TestBuildInstanceEnablesNestedVirtualization is what makes a host able to
// run a device at all: Cuttlefish needs /dev/kvm, and the instance flag is the
// mechanism Google documents and a verified-working G2 relies on. The host
// image's enable-vmx license is the older path and does not cover every shape.
func TestBuildInstanceEnablesNestedVirtualization(t *testing.T) {
	t.Parallel()

	inst := buildInstance(buildInstanceCfg(), buildInstanceHost(),
		machineTypeConfig{machineType: "n1-standard-8"}, "img")

	if !inst.GetAdvancedMachineFeatures().GetEnableNestedVirtualization() {
		t.Error("nested virtualization is off; the host would boot without /dev/kvm")
	}
}

// Bare metal is the hardware, so there is nothing to nest and GCE has no
// business being asked.
func TestBuildInstanceLeavesBareMetalAlone(t *testing.T) {
	t.Parallel()

	inst := buildInstance(buildInstanceCfg(), buildInstanceHost(),
		machineTypeConfig{machineType: "c4a-highmem-96-metal"}, "img")

	if inst.GetAdvancedMachineFeatures().GetEnableNestedVirtualization() {
		t.Error("nested virtualization was requested on a bare-metal shape")
	}
}

// TestBuildInstanceHonorsTheCatalogDiskType: C4 and N4 reject pd-* outright
// ("pd-balanced disk type cannot be used by c4-standard-4 machine type"), so a
// shape that names a disk type must get it. Leaving it unset on the shapes
// that do not name one keeps the zonal default.
func TestBuildInstanceHonorsTheCatalogDiskType(t *testing.T) {
	t.Parallel()

	h := buildInstanceHost()
	withType := buildInstance(buildInstanceCfg(), h,
		machineTypeConfig{machineType: "c4-standard-8", bootDiskType: "hyperdisk-balanced"}, "img")
	got := withType.GetDisks()[0].GetInitializeParams().GetDiskType()
	if want := "zones/" + h.Zone + "/diskTypes/hyperdisk-balanced"; got != want {
		t.Errorf("disk type = %q, want %q", got, want)
	}

	plain := buildInstance(buildInstanceCfg(), h,
		machineTypeConfig{machineType: "n1-standard-8"}, "img")
	if dt := plain.GetDisks()[0].GetInitializeParams().GetDiskType(); dt != "" {
		t.Errorf("disk type = %q, want the zonal default (unset)", dt)
	}
}
