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

// RecommendationWindow is how far back the standby recommendation looks.
//
// It spans days where the demand horizon spans minutes, which is why there is
// a second window. The target answers "how much capacity is wanted right
// now", and this answers "what should the standing intent be", which is a
// question about a week of behavior. Reading the recommendation over the
// horizon would recommend a number from whatever the last hour happened to do.
const RecommendationWindow = 7 * 24 * time.Hour

// RecommendationBucket is how wide one point of the series is. An hour over a
// week is 168 points: enough for a burst to be visibly a burst, few enough that
// the whole series is one small response.
const RecommendationBucket = time.Hour

// MinRecommendationMisses is how many servable misses the window must hold
// before a peak drawn from it is offered as a number to set.
//
// It is a judgement call, stated once here so the console keeps no copy of
// its own. Below it a single incident IS the peak: three misses that happened
// to arrive together would recommend standing three hosts for a week on the
// evidence of one afternoon. The failure this prevents is
// specific and one-directional: a quiet window recommending 0 reads as
// "measured, and the answer is none", which is the one way this feature does
// harm, because 0 is also an ordinary value to set on purpose.
const MinRecommendationMisses = 5

// StandbyRecommendation is the observation window's whole output: the number to
// set a pool's standby buffer to, with enough of its derivation that nobody has
// to take it on faith.
//
// Every field it publishes is one an operator needs to judge the number rather
// than to recompute it: the interval says when, the split says how much of the
// window a buffer could ever have answered, and the series is what the chart
// draws, carried on the same response so the picture and the sentence cannot
// come from two different reads.
type StandbyRecommendation struct {
	// Standby is the peak of concurrent SERVABLE misses, which is the statistic
	// the target itself is composed from over a shorter window. Recommending
	// anything else would suggest a number the autoscaler would then not pursue.
	Standby int64 `json:"standby" doc:"Standing capacity the window suggests: the peak of concurrent servable misses"`

	// Sufficient reports whether the window held enough servable misses for the
	// peak to have a shape. False makes Standby a figure to look at rather than
	// one to set, and a console MUST NOT render it as a recommendation of zero.
	Sufficient bool `json:"sufficient" doc:"Whether the window held enough servable misses for the peak to mean anything; false means this is not a recommendation"`

	// PeakFrom and PeakTo bound the run that reached the peak. Zero when there
	// is no peak to place.
	PeakFrom time.Time `json:"peak_from,omitzero" doc:"First miss of the run that reached the peak"`
	PeakTo   time.Time `json:"peak_to,omitzero" doc:"Last miss of the run that reached the peak"`

	// Servable and Unservable partition the whole window, as DemandSplit does
	// over the horizon: the unservable half is real demand that no standing
	// buffer answers, so it explains the gap between misses and the suggestion.
	Servable   int64 `json:"servable" doc:"Misses in the window a standby host could have absorbed"`
	Unservable int64 `json:"unservable" doc:"Misses in the window no standing capacity could have absorbed"`

	// Series is the concurrency the chart draws, one point per
	// RecommendationBucket. Its maximum is Standby by construction.
	Series []DemandSample `json:"series" doc:"Concurrent servable misses over the window, one point per hour"`

	// From and To bound the whole window, so the chart's axis needs no clock of
	// its own.
	From time.Time `json:"from" doc:"Start of the observation window"`
	To   time.Time `json:"to" doc:"End of the observation window (the server's now)"`

	// Partial reports that the read hit its sample bound, so the tail of the
	// window is missing and every count here is a floor.
	Partial bool `json:"partial,omitzero" doc:"The window held more misses than one read returns, so these counts are lower bounds"`
}

// Recommend derives the standing-capacity recommendation from one read of the
// observation window.
//
// It is the same arithmetic ComposeStatus performs (PeakConcurrent over the
// SERVABLE times, one provisioning leg wide) over a longer window, returning
// its derivation instead of only its result. Sharing the statistic is what stops
// the page recommending a number the keeper would never have reached.
//
// now is passed rather than read so the window the series is bucketed over is
// the same instant the caller queried with; a second clock read here would drop
// or invent a trailing bucket.
func Recommend(window DemandWindow, now time.Time) StandbyRecommendation {
	from := now.Add(-RecommendationWindow)
	servable := window.ServableTimes()
	peak, peakFrom, peakTo := PeakWindow(servable, host.ProvisioningLeg)
	split := window.Split()
	return StandbyRecommendation{
		Standby:    peak,
		Sufficient: split.Servable >= MinRecommendationMisses,
		PeakFrom:   peakFrom,
		PeakTo:     peakTo,
		Servable:   split.Servable,
		Unservable: split.Unservable,
		Series: ConcurrencySeries(servable, host.ProvisioningLeg, from,
			int(RecommendationWindow/RecommendationBucket), RecommendationBucket),
		From:    from,
		To:      now,
		Partial: window.Partial,
	}
}

// KeeperActivity is what the autoscaler actually DID over a window, counted
// from the audit log.
//
// From the trail and never from the keeper's own counters, which are in-process
// atomics on the leader instance alone: a console asking the keeper would get
// nothing on every other serving instance and a number up to a cycle stale on
// that one. The trail is also what survives the reclamation: the host rows
// these describe are gone by the time anyone reads them, which is exactly why
// the entries exist.
type KeeperActivity struct {
	Provisions int64 `json:"provisions" doc:"Standby hosts the keeper bought in the window"`
	Rescues    int64 `json:"rescues" doc:"Draining fleet hosts whose lease was renewed instead of replaced"`
	Shortens   int64 `json:"shortens" doc:"Surplus fleet hosts given back by shortening their lease"`
	Collapses  int64 `json:"collapses" doc:"Empty dedicated hosts reclaimed because their group had ended"`
}
