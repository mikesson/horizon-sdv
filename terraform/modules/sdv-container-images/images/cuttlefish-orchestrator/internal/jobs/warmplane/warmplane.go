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

// Package warmplane prefetches hot builds onto standby hosts, so a later real
// create of one of those builds pays the boot instead of the fetch.
//
// The prefetch premise, established from the upstream source: the cvd download
// cache lives at /var/tmp/cvd/<uid>/cache, survives `cvd rm` by construction,
// and a cache hit hardlinks into the new group directory. So a throwaway create
// with a hot build, deleted once running, leaves the host warm, so a later real
// create of that build pays the ~94s boot, not the ~155s fetch+boot.
//
// The warm walk is a state machine across cycles, never a wait inside one: each
// cycle does bounded quick HO calls (one listing, at most one create or delete
// per host), because a warm bake takes minutes and a keeper cycle has a seconds
// budget.
//
// A sibling of the keeper rather than a part of it, because warming is not
// conditional on what the keeper is doing about capacity. It runs over
// whatever inventory exists. The composition root builds this and hands it to
// the keeper, which drives it one step per cycle, so no job imports a job and
// there is still one loop, one lease and one cadence.
package warmplane

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/build"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// warmTargetCap bounds one cycle's warm walk; far above any sane floor.
const warmTargetCap = 32

// warmStateTTL is how long warm bookkeeping survives without the disk being
// seen again, and it is long on purpose.
//
// A host leaves the standby listing whenever it stops being idle inventory,
// while it holds a device and while its lease drains, and it comes back
// afterwards with the same disk and the same artifacts still on it. Forgetting
// in the meantime loses more than bookkeeping: the next walk re-bakes every
// key it already baked, and a bake in flight is what the reclamation side reads
// to avoid choosing a host that is about to become warm.
//
// A fleet host cannot outlive host.MaxFleetRescueAge, so at this value no fleet
// disk is ever forgotten while it exists. A longer-lived reserved host re-bakes
// about once a day onto a cache that is still warm, which costs a boot rather
// than a fetch.
const warmStateTTL = host.MaxFleetRescueAge

// Planner names the fetch keys worth prefetching, default build first. The
// device service satisfies it, wired by the composition root.
type Planner interface {
	WarmFetchKeys(ctx context.Context) ([]build.WarmKey, error)
}

// StandbyLister lists the idle standby inventory, which is the set of prefetch
// targets. It is a separate port from the keeper's deficit count on purpose:
// the statements behind them are allowed to diverge, and only the count
// narrows to hosts the keeper manages. The host feature's repository satisfies
// it.
type StandbyLister interface {
	ListStandby(ctx context.Context, lead time.Duration, limit int) ([]*host.Host, error)
}

// ArtifactRecorder is the same ledger boundary the create path writes through: a
// pinned warm the ledger cannot see is a warm placement cannot use. The
// platform artifact-cache repository satisfies it.
type ArtifactRecorder interface {
	Record(ctx context.Context, hostID, fetchKey, buildID string, hostIncarnation int64) error
}

// Plane is the prefetch state machine over the standby inventory.
type Plane struct {
	planner Planner
	hosts   StandbyLister
	ho      *hoclient.Factory
	ledger  ArtifactRecorder
	lead    time.Duration
	logger  *slog.Logger

	// Warm bookkeeping is in-memory on purpose: the keeper driving this plane is
	// leader-gated, pinned warmth is durable in the ledger, and the worst a
	// restart costs is one redundant bake per host, against an already-warm
	// cache.
	mu       sync.Mutex
	inFlight map[string]build.WarmKey // epoch -> the key baking there
	warmed   map[string]bool          // epoch|fetchKey -> done
	seen     map[string]time.Time     // epoch -> when the walk last found this disk

	totalWarms atomic.Int64
}

// New creates the warm plane. lead is the standby-inventory lease margin, taken
// once here rather than per call so the keeper's deficit count and this listing
// cannot come to disagree about what inventory is; non-positive defaults to
// host.ProvisionLead, the same constant the keeper's config defaults to. A nil
// logger defaults.
func New(planner Planner, hosts StandbyLister, ho *hoclient.Factory, ledger ArtifactRecorder,
	lead time.Duration, logger *slog.Logger,
) *Plane {
	if lead <= 0 {
		lead = host.ProvisionLead
	}
	if logger == nil {
		logger = slog.Default()
	}
	p := &Plane{
		planner:  planner,
		hosts:    hosts,
		ho:       ho,
		ledger:   ledger,
		lead:     lead,
		logger:   logger,
		inFlight: make(map[string]build.WarmKey),
		warmed:   make(map[string]bool),
		seen:     make(map[string]time.Time),
	}
	telemetry.RegisterCounters("warmplane", []telemetry.ObservedCounter{
		{Name: "cco.warmplane.warms", Description: "Prefetch bakes completed on standby hosts.", Value: &p.totalWarms},
	})
	return p
}

// hostEpoch keys per-disk warm state: an incarnation bump is a fresh, cold
// disk, so its warm history must not carry over.
func hostEpoch(h *host.Host) string {
	return fmt.Sprintf("%s|%d", h.ID, h.Incarnation)
}

// Advance advances the prefetch state machine one step per standby host: finish
// a bake that reached Running (delete the throwaway, ledger a pinned key),
// clear a stale husk, or start the next unwarmed key. It is best-effort
// throughout: warmth is an optimization, and a failed step retries next cycle.
func (p *Plane) Advance(ctx context.Context) {
	if p.planner == nil || p.hosts == nil || p.ho == nil {
		return
	}
	targets, err := p.hosts.ListStandby(ctx, p.lead, warmTargetCap)
	if err != nil {
		p.logger.Warn("warm walk skipped: listing standby inventory failed", slog.Any("error", err))
		return
	}
	if len(targets) == 0 {
		return
	}
	keys, err := p.planner.WarmFetchKeys(ctx)
	if err != nil {
		p.logger.Warn("warm walk skipped: planning fetch keys failed", slog.Any("error", err))
		return
	}
	if len(keys) == 0 {
		return
	}
	var pinned []build.WarmKey
	for _, k := range keys {
		if k.Pinned {
			pinned = append(pinned, k)
		}
	}
	p.trackState(targets, time.Now())
	for _, h := range targets {
		if h.Status != host.StatusRunning || h.HostOrchestratorURL() == "" {
			continue // a host still coming up warms once it serves
		}
		// Warm-all: every standby host gets the default build (keys[0]); the
		// hot pinned builds spread across the hosts.
		want := []build.WarmKey{keys[0]}
		if len(pinned) > 0 {
			want = append(want, pinnedKeyFor(h.ID, pinned))
		}
		p.warmHost(ctx, h, want)
	}
}

// pinnedKeyFor picks this host's pinned build by rendezvous hashing, so the
// assignment depends only on the host and stays the same whatever order this
// cycle's listing returns.
//
// Handing keys out by listing position is what this replaces, and the failure
// stops being subtle the moment hosts become long-lived: the listing is ordered
// by age, so every time the oldest host left it (an expiry, a lease draining, a
// device landing on it) every remaining host's index shifted by one, and every
// host was assigned a different key and baked it. Nothing evicts a host's
// fetched artifacts, so a host that survived long enough collected the whole
// pinned set onto one boot disk.
//
// Rendezvous rather than a modulus over a hash, for the other half of the
// property: changing the pinned set reassigns only the hosts whose own winner
// changed, instead of reshuffling every host the way a changed divisor does.
func pinnedKeyFor(hostID string, pinned []build.WarmKey) build.WarmKey {
	best, bestScore := pinned[0], uint64(0)
	for i, k := range pinned {
		h := fnv.New64a()
		// Separated, so ("ab", "c") and ("a", "bc") cannot score alike.
		_, _ = h.Write([]byte(hostID))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(k.FetchKey))
		if score := h.Sum64(); i == 0 || score > bestScore {
			best, bestScore = k, score
		}
	}
	return best
}

// BakeInFlight reports the disks currently mid-bake, host id to the incarnation
// being baked. The reclamation side reads it to avoid shortening the host
// closest to becoming warm: a bake writes no artifact-ledger rows until it
// finishes, so a host mid-bake reads as the coldest in the fleet and sorts first
// for destruction.
//
// Host id to incarnation rather than this package's internal epoch string: the
// consumer compares against a host row it already holds, and handing out the
// "host|incarnation" format would make every caller rebuild it with nothing
// binding the two spellings together.
//
// A copy, because the map behind it is mutated on every cycle.
func (p *Plane) BakeInFlight() map[string]int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]int64, len(p.inFlight))
	for epoch := range p.inFlight {
		hostID, incarnation, ok := splitEpoch(epoch)
		if !ok {
			continue
		}
		// Last writer wins on the impossible case of one host appearing under
		// two incarnations: trackState drops a superseded epoch on the walk that
		// observes it, so the pair cannot outlive a cycle.
		out[hostID] = incarnation
	}
	return out
}

// splitEpoch reverses hostEpoch. The parse cannot fail on a key this package
// wrote, so a failure means the key was not one of ours and is skipped rather
// than guessed at.
func splitEpoch(epoch string) (string, int64, bool) {
	hostID, ok := cutLast(epoch)
	if !ok {
		return "", 0, false
	}
	incarnation, err := strconv.ParseInt(epoch[len(hostID)+1:], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return hostID, incarnation, true
}

// warmHost advances one host's warm walk by a single step.
func (p *Plane) warmHost(ctx context.Context, h *host.Host, want []build.WarmKey) {
	client := p.ho.ClientForHost(h.HostOrchestratorURL())
	// ProbeCVDs bypasses the host breaker on purpose: a warm probe against a
	// booting HO must not poison the breaker real creates depend on.
	cvds, err := client.ProbeCVDs(ctx)
	if err != nil {
		// Logged because this is the walk's only silent stall point: an HO
		// that stops answering would otherwise freeze warming invisibly.
		p.logger.Warn("warm walk skipped a host: HO probe failed",
			slog.String("host_id", h.ID), slog.Any("error", err))
		return
	}
	epoch := hostEpoch(h)
	for _, c := range cvds {
		if c.Group != device.WarmGroupName {
			continue
		}
		switch c.Status {
		case device.CVDStateRunning:
			p.finishWarm(ctx, client, h, epoch)
			return
		case device.CVDStateStopped, device.CVDStateBootFailed,
			device.CVDStatePrepareFailed, device.CVDStateCancelled:
			// A quiescent husk: a bake that failed, or one left by a
			// crashed keeper. Only these explicitly-settled states may be
			// deleted; the walk restarts next cycle.
			if err := client.DeleteCVD(ctx, device.WarmGroupName); err != nil {
				p.logger.Warn("failed to clear stale warm group",
					slog.String("host_id", h.ID), slog.Any("error", err))
			}
			return
		default:
			// Preparing, Starting, Stopping, or any state this code does
			// not know. A loading group MUST NOT be deleted (`cvd rm` under
			// a live launch leaks its processes), so uncertainty always
			// reads as "still baking".
			return
		}
	}

	next, ok := p.nextUnwarmed(epoch, want)
	if !ok {
		return // fully warm for this cycle's plan
	}
	if _, err := client.CreateCVDOperation(ctx, &hoclient.CreateCVDRequest{
		EnvConfig: warmEnvConfig(next.FetchKey),
	}); err != nil {
		p.logger.Warn("failed to start warm create",
			slog.String("host_id", h.ID), slog.String("fetch_key", next.FetchKey), slog.Any("error", err))
		return
	}
	p.mu.Lock()
	p.inFlight[epoch] = next
	p.mu.Unlock()
	p.logger.Info("warm create started",
		slog.String("host_id", h.ID), slog.String("fetch_key", next.FetchKey))
}

// finishWarm tears down a bake that reached Running and records what it
// proved: the artifacts are on the host's disk. The ledger write happens for
// pinned keys only, through the same boundary the create path uses. A Running
// group with no in-flight record (keeper restart) is deleted unrecorded. The
// artifacts are still cached, so the re-warm the next cycle starts is a fetch
// hit.
func (p *Plane) finishWarm(ctx context.Context, client hoclient.Client, h *host.Host, epoch string) {
	if err := client.DeleteCVD(ctx, device.WarmGroupName); err != nil {
		p.logger.Warn("failed to delete finished warm group",
			slog.String("host_id", h.ID), slog.Any("error", err))
		return
	}
	p.mu.Lock()
	key, known := p.inFlight[epoch]
	delete(p.inFlight, epoch)
	if known {
		p.warmed[epoch+"|"+key.FetchKey] = true
	}
	p.mu.Unlock()
	if !known {
		p.logger.Info("adopted and cleared an unrecorded warm group",
			slog.String("host_id", h.ID))
		return
	}
	if key.Pinned && p.ledger != nil {
		if err := p.ledger.Record(ctx, h.ID, key.FetchKey, key.RegistryID, h.Incarnation); err != nil {
			p.logger.Warn("failed to ledger a pinned warm",
				slog.String("host_id", h.ID), slog.String("fetch_key", key.FetchKey), slog.Any("error", err))
		}
	}
	p.totalWarms.Add(1)
	p.logger.Info("host warmed",
		slog.String("host_id", h.ID),
		slog.String("fetch_key", key.FetchKey),
		slog.Bool("pinned", key.Pinned))
}

// nextUnwarmed picks the first wanted key this disk has not baked yet.
func (p *Plane) nextUnwarmed(epoch string, want []build.WarmKey) (build.WarmKey, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, k := range want {
		if !p.warmed[epoch+"|"+k.FetchKey] {
			return k, true
		}
	}
	return build.WarmKey{}, false
}

// trackState records the disks this walk found and drops bookkeeping for disks
// that are gone.
//
// Two rules, because two different things make an entry stale and only one of
// them is observable from here. A disk whose host appears under a DIFFERENT
// incarnation is definitely gone, because an incarnation bump is a fresh cold
// disk, so its entries go at once. A disk that has merely stopped appearing
// might be busy, draining, or deleted, and this walk cannot tell which; those
// age out.
//
// Keying the whole thing on standby-listing membership is the defect this
// replaces. Draining is exactly leaving that listing, so a host shortened and
// then renewed came back with no history and re-baked every key, and the
// re-bake registered as in flight, pushing the next reclamation onto a host
// that was actually colder. Narrowing instead to fleet membership would be the
// same mistake in another form: this plane warms any idle shareable host,
// including hosts a user created, and those would then be forgotten every cycle.
func (p *Plane) trackState(targets []*host.Host, now time.Time) {
	current := make(map[string]string, len(targets))
	for _, h := range targets {
		current[h.ID] = hostEpoch(h)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, epoch := range current {
		p.seen[epoch] = now
	}
	for epoch, last := range p.seen {
		hostID, ok := cutLast(epoch)
		superseded := ok && current[hostID] != "" && current[hostID] != epoch
		if superseded || now.Sub(last) > warmStateTTL {
			delete(p.seen, epoch)
			delete(p.inFlight, epoch)
		}
	}
	for key := range p.warmed {
		epoch, ok := cutLast(key)
		if !ok {
			delete(p.warmed, key)
			continue
		}
		if _, live := p.seen[epoch]; !live {
			delete(p.warmed, key)
		}
	}
}

// cutLast returns everything before the LAST separator: the disk epoch inside a
// "host|incarnation|fetchKey" bookkeeping key, and the host id inside a
// "host|incarnation" epoch. Both keys are built by appending, so both are read
// by taking the tail off.
//
// It splits from the right on purpose. Fetch keys contain no '|', but
// splitting that way keeps the code correct by the format's own rule instead
// of by an assumption about key contents.
func cutLast(s string) (string, bool) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '|' {
			return s[:i], true
		}
	}
	return "", false
}

// warmEnvConfig is the minimal canonical config for a throwaway prefetch:
// one instance, the wanted build, the reserved group name. It mirrors the
// create payload's simple mode minus the per-device settings a throwaway does
// not need.
func warmEnvConfig(fetchKey string) map[string]any {
	return map[string]any{
		"common": map[string]any{"group_name": device.WarmGroupName},
		"instances": []any{map[string]any{
			"name": "1",
			"vm": map[string]any{
				"memory_mb":        8192,
				"cpus":             8,
				"setupwizard_mode": "OPTIONAL",
			},
			"disk": map[string]any{"default_build": fetchKey},
		}},
	}
}
