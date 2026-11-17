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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// servableMiss is a miss no axis rules out, the population a buffer answers.
func servableMiss(at time.Time) Miss {
	return Miss{
		At: at, GPURequired: new(false), FitsDefaultHost: new(true),
		MaxDevices: new(int64(1)), Density: new(int64(3)),
	}
}

// gpuMiss is real demand no standing buffer could ever have absorbed.
func gpuMiss(at time.Time) Miss {
	return Miss{
		At: at, GPURequired: new(true), FitsDefaultHost: new(true),
		MaxDevices: new(int64(1)), Density: new(int64(3)),
	}
}

// The peak's interval is what makes it answerable: "3 concurrent misses"
// invites the question of when, and a recommendation nobody can place in the
// week is one nobody can check.
func TestPeakWindowReportsTheIntervalThatProducedThePeak(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	// Two isolated arrivals, then a run of three inside one leg. The isolated
	// pair is spaced well past the leg so the peak run is unambiguous. Arrivals
	// a shade over two minutes apart are still concurrent, which is exactly the
	// half-open definition doing its job.
	times := []time.Time{
		base,
		base.Add(10 * time.Minute),
		base.Add(20 * time.Minute),
		base.Add(20*time.Minute + 30*time.Second),
		base.Add(21 * time.Minute),
	}

	peak, from, to := PeakWindow(times, host.ProvisioningLeg)
	assert.Equal(t, int64(3), peak)
	assert.Equal(t, base.Add(20*time.Minute), from)
	assert.Equal(t, base.Add(21*time.Minute), to)
}

// Ties go to the earliest run. Later runs of the same height are the same
// evidence, and taking the last would move the interval under a reader as the
// window slid forward without the number changing at all.
func TestPeakWindowTakesTheEarliestOfEqualRuns(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	times := []time.Time{
		base, base.Add(time.Second),
		base.Add(time.Hour), base.Add(time.Hour + time.Second),
	}

	peak, from, _ := PeakWindow(times, host.ProvisioningLeg)
	assert.Equal(t, int64(2), peak)
	assert.Equal(t, base, from, "the first run of the peak height, not the last")
}

// Nothing to place is not a peak of zero at some arbitrary instant.
func TestPeakWindowOnAnEmptyWindowLocatesNothing(t *testing.T) {
	t.Parallel()
	peak, from, to := PeakWindow(nil, host.ProvisioningLeg)
	assert.Zero(t, peak)
	assert.True(t, from.IsZero() && to.IsZero())
}

// The chart and the sentence must agree. The series buckets the same
// concurrency PeakWindow maximizes, so its highest point IS the reported peak.
// A bucket carrying an arrival COUNT instead would climb where concurrency does
// not and top out above the number the text states.
func TestConcurrencySeriesPeaksAtTheReportedPeak(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	times := make([]time.Time, 0, 9)
	// A steady drip, an hour apart: never concurrent, however many there are.
	for i := range 6 {
		times = append(times, from.Add(time.Duration(i)*time.Hour))
	}
	// Then a burst of three inside one leg.
	burst := from.Add(10 * time.Hour)
	times = append(times, burst, burst.Add(20*time.Second), burst.Add(40*time.Second))

	peak, _, _ := PeakWindow(times, host.ProvisioningLeg)
	series := ConcurrencySeries(times, host.ProvisioningLeg, from, 24, time.Hour)

	require.Len(t, series, 24)
	var highest int64
	for _, s := range series {
		highest = max(highest, s.Peak)
	}
	assert.Equal(t, peak, highest, "the chart's maximum is the peak the text reports")
	assert.Equal(t, int64(3), series[10].Peak, "the burst lands in its own hour")
	assert.Zero(t, series[7].Peak, "a quiet hour is drawn as quiet, not skipped")
}

// A dense series is what lets a quiet week look quiet. Dropping empty buckets
// would draw a line joining two distant spikes and imply demand between them.
func TestConcurrencySeriesIsDense(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	series := ConcurrencySeries(nil, host.ProvisioningLeg, from, 5, time.Hour)

	require.Len(t, series, 5)
	for i, s := range series {
		assert.Equal(t, from.Add(time.Duration(i)*time.Hour), s.At)
		assert.Zero(t, s.Peak)
	}
}

// The recommendation is the SAME statistic the target is composed from, over a
// longer window. Anything else would suggest a number the autoscaler would then
// not pursue.
func TestRecommendIsThePeakOfConcurrentServableMisses(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	burst := now.Add(-48 * time.Hour)
	window := DemandWindow{Misses: []Miss{
		servableMiss(burst),
		servableMiss(burst.Add(15 * time.Second)),
		servableMiss(burst.Add(30 * time.Second)),
		// Demand no buffer answers: it must not raise the suggestion.
		gpuMiss(burst.Add(45 * time.Second)),
		gpuMiss(burst.Add(50 * time.Second)),
		servableMiss(now.Add(-4 * time.Hour)),
		servableMiss(now.Add(-3 * time.Hour)),
	}}

	rec := Recommend(window, now)
	assert.Equal(t, int64(3), rec.Standby, "the GPU pair is real demand and still not a reason to buy standby")
	assert.True(t, rec.Sufficient)
	assert.Equal(t, int64(5), rec.Servable)
	assert.Equal(t, int64(2), rec.Unservable)
	assert.Equal(t, burst, rec.PeakFrom)
	assert.Equal(t, burst.Add(30*time.Second), rec.PeakTo)
	assert.Equal(t, now, rec.To)
	assert.Equal(t, now.Add(-RecommendationWindow), rec.From)
	assert.Len(t, rec.Series, int(RecommendationWindow/RecommendationBucket))
}

// The one way this feature does harm. A quiet window's peak is arithmetically
// fine and means nothing, and rendering it as a recommendation of 0 reads as
// "measured, and the answer is none", while 0 is also an ordinary value to set
// on purpose, so the two are indistinguishable after the fact.
func TestRecommendRefusesToRecommendFromTooLittleEvidence(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

	t.Run("an empty window", func(t *testing.T) {
		t.Parallel()
		rec := Recommend(DemandWindow{}, now)
		assert.Zero(t, rec.Standby)
		assert.False(t, rec.Sufficient, "0 from nothing is not a recommendation")
	})

	t.Run("one incident", func(t *testing.T) {
		t.Parallel()
		at := now.Add(-time.Hour)
		misses := make([]Miss, 0, MinRecommendationMisses-1)
		for i := range MinRecommendationMisses - 1 {
			misses = append(misses, servableMiss(at.Add(time.Duration(i)*time.Second)))
		}
		rec := Recommend(DemandWindow{Misses: misses}, now)
		assert.Equal(t, int64(MinRecommendationMisses-1), rec.Standby,
			"the arithmetic still runs; what is withheld is the recommendation, not the figure")
		assert.False(t, rec.Sufficient, "one afternoon is not a week's evidence")
	})

	t.Run("enough evidence", func(t *testing.T) {
		t.Parallel()
		misses := make([]Miss, 0, MinRecommendationMisses)
		for i := range MinRecommendationMisses {
			misses = append(misses, servableMiss(now.Add(-time.Duration(i+1)*time.Hour)))
		}
		rec := Recommend(DemandWindow{Misses: misses}, now)
		assert.True(t, rec.Sufficient)
	})
}

// A truncated read loses the tail, so every count derived from it is a floor.
// Carrying that through is what stops a partial week reading as a quiet one.
func TestRecommendCarriesThePartialRead(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	rec := Recommend(DemandWindow{Partial: true}, now)
	assert.True(t, rec.Partial)
}
