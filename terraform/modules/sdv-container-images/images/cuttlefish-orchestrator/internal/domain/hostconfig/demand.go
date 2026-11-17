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

package hostconfig

import (
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// MaxDemandSample bounds how many misses the signal reads in one pass. A
// horizon can be set to a day, and an unbounded scan grows with it. Reaching
// the bound is reported, because a truncated tail lowers the peak, and a demand
// signal that reads low without saying so is worse than one that says it could
// not see the whole window.
//
// It bounds ALL misses, including the unservable ones, because servability is
// decided here rather than in the query. A window full of demand the fleet
// cannot serve could therefore under-sample the demand it can. At a one-hour
// horizon that needs 2.8 misses a second sustained, and Partial says so when it
// happens.
const MaxDemandSample = 10000

// Reason is an axis on which no standing buffer could have absorbed a miss.
//
// A named type rather than bare strings so that a switch over the vocabulary is
// exhaustive-checked: the counts on DemandSplit are that switch, and a fourth
// axis added without a case there would silently report zero for it forever.
type Reason string

// The axes. Independent, because a miss can fail more than one and each is a
// different thing to fix: buy GPU capacity, raise the default machine, or raise
// the packing density.
const (
	// ReasonGPURequired: the create required a GPU. The keeper provisions the
	// headless template, so no amount of standing capacity answers this.
	ReasonGPURequired Reason = "gpu_required"
	// ReasonNeedsLargerHost: the default template could not hold the create's
	// shape: its widest guest's vCPUs, or its guests' summed RAM.
	ReasonNeedsLargerHost Reason = "needs_larger_host"
	// ReasonOversizedGroup: the host took more slots than a standby host
	// offers, so the group would not have fit one.
	ReasonOversizedGroup Reason = "oversized_group"
)

// Miss is one placement miss: when a create found no capacity and provisioned,
// and what the resolver froze about it at that moment.
//
// Every fact is optional and nil means "not stamped". An entry written before a
// fact existed carries none of it, and a miss nobody can judge is still a miss,
// so an absent fact is never a reason to discard demand.
type Miss struct {
	At time.Time

	// Pool is the pool the create was judged into, empty when the entry carries
	// none, which reads as the default pool, the same rule a machine's
	// unstamped column follows. Demand nobody attributed is still demand, and
	// the pool every unattributed create would have landed in is the default.
	//
	// An entry written before the stamp existed carries none and counts as the
	// default pool's, which is where every create landed while that was true.
	Pool string

	// GPURequired is the create's hard GPU requirement, never a preference: a
	// preference degrades onto a CPU host, which standing capacity can serve.
	GPURequired *bool
	// FitsDefaultHost is whether the default template held this create's shape.
	FitsDefaultHost *bool
	// MaxDevices is the slot ceiling the provisioned host took, and Density what
	// a standby host would have offered. Only comparable to each other.
	MaxDevices *int64
	Density    *int64
}

// Reasons lists every axis on which standing capacity could not have absorbed
// this miss, in a stable order. Empty means servable.
//
// Servability is judged inside the miss's OWN pool, because that is the standing
// capacity that would have answered it. A required GPU is beyond a pool whose
// machines carry none (which is every pool but the GPU one) and it is beyond
// the fleet entirely while that pool is switched off, since the derivation then
// leaves such a create in the default pool. Enabling the pool moves those misses
// into it and they become demand it can buy for, without this rule changing.
//
// Per reason and never first-fail: a create that required a GPU AND outgrew the
// default machine is two different things to fix, and reporting only the first
// would make the second invisible until the first was solved.
func (m Miss) Reasons() []Reason {
	var reasons []Reason
	if m.GPURequired != nil && *m.GPURequired && !host.PoolRequiresGPU(host.NormalizePool(m.Pool)) {
		reasons = append(reasons, ReasonGPURequired)
	}
	if m.FitsDefaultHost != nil && !*m.FitsDefaultHost {
		reasons = append(reasons, ReasonNeedsLargerHost)
	}
	if m.MaxDevices != nil && m.Density != nil && *m.MaxDevices > *m.Density {
		reasons = append(reasons, ReasonOversizedGroup)
	}
	return reasons
}

// Servable reports whether a standing buffer could have absorbed this miss.
func (m Miss) Servable() bool { return len(m.Reasons()) == 0 }

// DemandWindow is one read of the demand horizon: every miss it saw, ascending,
// and whether the read hit its sample bound.
type DemandWindow struct {
	Misses  []Miss
	Partial bool
}

// ByPool splits one read of the window into the pools its misses belong to,
// each keeping the window's own ascending order and its partial flag.
//
// A split of the read rather than a read per pool: the misses arrive in one
// scan, and asking the audit table once per pool would make the statement count
// a function of how many pools an operator configured. The partial flag rides
// every part, because a truncated tail is a fact about the READ: no part of a
// window that hit its sample bound can claim to hold all of anything.
func (w DemandWindow) ByPool() map[string]DemandWindow {
	pools := make(map[string]DemandWindow)
	for _, m := range w.Misses {
		pool := host.NormalizePool(m.Pool)
		part := pools[pool]
		part.Misses = append(part.Misses, m)
		part.Partial = w.Partial
		pools[pool] = part
	}
	return pools
}

// ServableTimes are the moments standing capacity could have absorbed, which is
// the only population a target may be composed from.
//
// The rest is demand too. It is demand this fleet cannot answer, and buying
// hosts that cannot serve it would spend money to change nothing. Reread that
// before wiring DemandSplit.Unservable into a target.
func (w DemandWindow) ServableTimes() []time.Time {
	times := make([]time.Time, 0, len(w.Misses))
	for _, m := range w.Misses {
		if m.Servable() {
			times = append(times, m.At)
		}
	}
	return times
}

// DemandSplit attributes a window's misses: the ones standing capacity could
// have absorbed, and the ones it could not, by axis. It is what lets an operator
// tell demand the target ignores from a demand signal that has broken.
//
// COUNTS, where FleetStatus.Misses over the same window is a peak-concurrent
// figure. The two answer different questions (how much demand arrived, and how
// much of it was in flight at once) so they are never added, and a difference
// between them is not a discrepancy.
type DemandSplit struct {
	// Servable and Unservable partition the window: every miss is exactly one of
	// them, and the pair sums to everything the read returned.
	Servable   int64 `json:"servable" doc:"Misses a standby host could have absorbed"`
	Unservable int64 `json:"unservable" doc:"Misses no standing capacity could have absorbed, on any axis"`

	// The axes. A miss failing two is counted under both, so these do not sum to
	// Unservable, and that is on purpose: each names a separate thing to fix,
	// and a share of a population would hide the second one until the first was
	// solved.
	GPURequired     int64 `json:"gpu_required" doc:"Unservable because the create required a GPU"`
	NeedsLargerHost int64 `json:"needs_larger_host" doc:"Unservable because the default machine could not hold the create's shape"`
	OversizedGroup  int64 `json:"oversized_group" doc:"Unservable because the group took more slots than a standby host offers"`
}

// Split attributes every miss in the window.
//
// One pass over the same read the target is composed from, so the split an
// operator reads and the number the keeper acts on cannot describe two different
// moments. Counting the axes with their own queries would need a read per
// timestamp and re-create exactly that.
func (w DemandWindow) Split() DemandSplit {
	var split DemandSplit
	for _, m := range w.Misses {
		reasons := m.Reasons()
		if len(reasons) == 0 {
			split.Servable++
			continue
		}
		split.Unservable++
		for _, reason := range reasons {
			switch reason {
			case ReasonGPURequired:
				split.GPURequired++
			case ReasonNeedsLargerHost:
				split.NeedsLargerHost++
			case ReasonOversizedGroup:
				split.OversizedGroup++
			}
		}
	}
	return split
}

// GPUDegradation is how often a create that wanted a GPU was placed on a host
// without one, over a window: the figure that decides whether standing GPU stock
// is worth buying.
//
// Both terms, never a percentage. Forty degradations mean nothing without how
// many creates wanted a GPU at all, and the size of the denominator is itself
// how much confidence the number deserves. A rounded ratio throws that away.
type GPUDegradation struct {
	Degraded int64 `json:"degraded" doc:"Creates that wanted a GPU and were placed without one"`
	Wanted   int64 `json:"wanted" doc:"Creates that wanted a GPU at all, the population Degraded is out of"`
}

// PeakConcurrent reports the largest number of moments falling inside any
// half-open window [t, t+window), given them in ascending order. It is the
// demand signal: each moment is a create that found no capacity and provisioned
// a host, and the peak is how many were in flight at once.
//
// EXACT, not bucketed. A bucket-pair estimator (the usual shortcut) reports
// up to 2k for a steady arrival rate of k, which is a correct upper bound and
// twice the truth; against a target clamped at StandbyCap that difference is the
// clamp being hit by arithmetic rather than by demand. Demand is the one signal
// that can reach the cap at all, so the estimator's error would land squarely on
// it.
//
// HALF-OPEN, and that half of the definition decides the answer. The window is
// one host-provisioning leg wide, so a miss exactly one leg after another could
// have landed on the host the first one started, because it existed by then.
// Counting it as concurrent would double the answer for a steady stream.
//
// No conversion by density. Pending hosts are placement-eligible, so concurrent
// misses consolidate onto the host the first of them started, and the create
// entries this counts are therefore already approximately ceil(k / density).
// Dividing here would divide twice.
func PeakConcurrent(ascending []time.Time, window time.Duration) int64 {
	peak, _, _ := PeakWindow(ascending, window)
	return peak
}

// PeakWindow is PeakConcurrent with the interval that produced the peak: the
// first and last moment of the run that reached it.
//
// The interval is what makes a peak answerable. "3 concurrent misses" invites
// the question of when, and a recommendation an operator cannot place in the
// week is one they cannot check against anything they remember. Both times are
// zero when there is no peak to locate.
//
// Ties go to the EARLIEST run. Later runs of the same height are the same
// evidence, and reporting the last one would move the interval under a reader
// as the window slid forward without the recommendation changing at all.
func PeakWindow(ascending []time.Time, window time.Duration) (int64, time.Time, time.Time) {
	if window <= 0 {
		return 0, time.Time{}, time.Time{}
	}
	var from, to time.Time
	best, start := 0, 0
	for end := range ascending {
		for ascending[end].Sub(ascending[start]) >= window {
			start++
		}
		if run := end - start + 1; run > best {
			best, from, to = run, ascending[start], ascending[end]
		}
	}
	return int64(best), from, to
}

// DemandSample is one bucket of the concurrency series: the moment the bucket
// opens and the highest concurrency any miss inside it reached.
type DemandSample struct {
	At   time.Time `json:"at" doc:"When this bucket opens"`
	Peak int64     `json:"peak" doc:"Highest concurrent servable misses reached inside the bucket"`
}

// ConcurrencySeries buckets the same concurrency PeakWindow maximizes, so a
// chart drawn from it has the reported peak as its own highest point rather
// than a second, differently-derived number beside it.
//
// The bucket's value is the MAXIMUM concurrency any miss inside it reached, not
// the count of misses in it. A count would be a different statistic wearing the
// same axis: with buckets wider than the window it climbs where concurrency
// does not, and its maximum would sit above the peak the text states.
//
// A bucket holding no miss is 0, which is the truth about it. The series is
// dense so the chart can draw a quiet week as visibly quiet rather than as a
// line joining two distant spikes.
func ConcurrencySeries(ascending []time.Time, window time.Duration, from time.Time, buckets int, width time.Duration) []DemandSample {
	if buckets <= 0 || width <= 0 {
		return nil
	}
	series := make([]DemandSample, buckets)
	for i := range series {
		series[i] = DemandSample{At: from.Add(time.Duration(i) * width)}
	}
	if window <= 0 {
		return series
	}
	start := 0
	for end := range ascending {
		for ascending[end].Sub(ascending[start]) >= window {
			start++
		}
		bucket := int(ascending[end].Sub(from) / width)
		if bucket < 0 || bucket >= buckets {
			continue
		}
		series[bucket].Peak = max(series[bucket].Peak, int64(end-start+1))
	}
	return series
}
