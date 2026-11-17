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

package gcecatalogfeature

import (
	"fmt"
	"slices"
	"strings"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
)

// Scoring weights. Terms are normalized to 0-100 across the surviving
// candidates before these apply, so the weights are unit-free: they alone
// decide relative influence. Proximity is absent without a location hint, a
// tie-break with one, and dominant for interactive intent (WebRTC latency is
// bound to the host zone). When a Spot ranking has fresh capacity advice, the
// obtainability term activates with its own weight set (every set sums to 1,
// keeping scores in 0-100).
const (
	weightHeadroom = 0.6
	weightHealth   = 0.3
	weightGeo      = 0.1

	weightHeadroomGeoDominant = 0.3
	weightHealthGeoDominant   = 0.1
	weightGeoDominant         = 0.6

	weightHeadroomSpot             = 0.4
	weightHealthSpot               = 0.2
	weightObtainability            = 0.3
	weightGeoSpot                  = 0.1
	weightHeadroomSpotGeoDominant  = 0.15
	weightHealthSpotGeoDominant    = 0.05
	weightObtainabilityGeoDominant = 0.2
)

// scoreCandidates normalizes each term to 0-100 across the candidates, applies
// the weights, and returns the candidates best first with their reason traces.
// The obtainability term participates only in Spot rankings where at least one
// candidate carries fresh advice. Otherwise the weights revert to the
// standard sets and the term contributes nothing.
func scoreCandidates(candidates []*rankedZone, opts gcecatalog.RankOptions, spot bool) []gcecatalog.ZoneCandidate {
	headroomN := normalize(candidates, func(c *rankedZone) (float64, bool) { return c.headroomRaw, true })
	healthN := normalize(candidates, func(c *rankedZone) (float64, bool) { return c.healthRaw, true })
	geoN := normalize(candidates, func(c *rankedZone) (float64, bool) { return c.geoRaw, c.geoKnown })
	obtainN := normalize(candidates, func(c *rankedZone) (float64, bool) { return c.obtainRaw, c.obtainKnown })

	obtainActive := spot && slices.ContainsFunc(candidates, func(c *rankedZone) bool { return c.obtainKnown })
	wHeadroom, wHealth, wGeo := weightHeadroom, weightHealth, weightGeo
	var wObtain float64
	if obtainActive {
		wHeadroom, wHealth, wGeo, wObtain = weightHeadroomSpot, weightHealthSpot, weightGeoSpot, weightObtainability
	}
	switch {
	case opts.PreferredRegion == "":
		wGeo = 0
	case opts.GeoDominant && obtainActive:
		wHeadroom, wHealth, wGeo, wObtain = weightHeadroomSpotGeoDominant, weightHealthSpotGeoDominant,
			weightGeoDominant, weightObtainabilityGeoDominant
	case opts.GeoDominant:
		wHeadroom, wHealth, wGeo = weightHeadroomGeoDominant, weightHealthGeoDominant, weightGeoDominant
	}

	out := make([]gcecatalog.ZoneCandidate, 0, len(candidates))
	for i, c := range candidates {
		reasons := []string{fmt.Sprintf("headroom %d/100 (%s)", int(headroomN[i]), headroomDetail(c))}
		if c.failures > 0 {
			reasons = append(reasons, fmt.Sprintf("health %d/100 (%d recent failures)", int(healthN[i]), c.failures))
		} else {
			reasons = append(reasons, fmt.Sprintf("health %d/100", int(healthN[i])))
		}
		if obtainActive && c.obtainKnown {
			reasons = append(reasons, fmt.Sprintf("spot obtainability %d/100 (%.2f, uptime tier %s)",
				int(obtainN[i]), c.obtainRaw, uptimeTierLabel(c.uptimeTier)))
		}
		if wGeo > 0 {
			reasons = append(reasons, fmt.Sprintf("proximity %d/100 (%s vs preferred %s)",
				int(geoN[i]), c.zone.Region, opts.PreferredRegion))
		}
		reasons = append(reasons, c.notes...)
		out = append(out, gcecatalog.ZoneCandidate{
			Zone:    c.zone.Zone,
			Region:  c.zone.Region,
			Score:   wHeadroom*headroomN[i] + wHealth*healthN[i] + wGeo*geoN[i] + wObtain*obtainN[i],
			Reasons: reasons,
		})
	}
	slices.SortStableFunc(out, func(a, b gcecatalog.ZoneCandidate) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		}
		return strings.Compare(a.Zone, b.Zone)
	})
	return out
}

// headroomDetail describes the binding headroom metric, or why none applied.
func headroomDetail(c *rankedZone) string {
	if c.headroom != "" {
		return c.headroom
	}
	return "no quota data"
}

// uptimeTierLabel renders an estimated-uptime tier for the reason trace.
func uptimeTierLabel(seconds int64) string {
	switch seconds {
	case gcecatalog.SpotUptimeTierHour:
		return "60m"
	case gcecatalog.SpotUptimeTier10Min:
		return "10m"
	case gcecatalog.SpotUptimeTierMinute:
		return "1m"
	case 0:
		return "unknown"
	}
	return fmt.Sprintf("%ds", seconds)
}

// normalize scales a raw term to 0-100 across the candidates (min-max), so
// weights stay unit-free: a raw-unit change can shift no ordering. Candidates
// without a known value sit at the neutral midpoint, and so does every
// candidate when the values are all equal.
func normalize(candidates []*rankedZone, term func(*rankedZone) (float64, bool)) []float64 {
	lo, hi := 0.0, 0.0
	seen := false
	for _, c := range candidates {
		v, ok := term(c)
		if !ok {
			continue
		}
		if !seen || v < lo {
			lo = v
		}
		if !seen || v > hi {
			hi = v
		}
		seen = true
	}
	out := make([]float64, len(candidates))
	for i, c := range candidates {
		v, ok := term(c)
		if !ok || hi == lo {
			out[i] = 50
			continue
		}
		out[i] = 100 * (v - lo) / (hi - lo)
	}
	return out
}
