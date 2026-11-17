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

package gce

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	computebeta "google.golang.org/api/compute/v0.beta"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
)

// adviceServer fakes the beta advice.capacity endpoint, capturing the request
// and returning the scripted response.
func adviceServer(t *testing.T, status int, resp *computebeta.CapacityAdviceResponse) (*SpotAdviceClient, *http.Request, *computebeta.CapacityAdviceRequest) {
	t.Helper()
	var gotReq http.Request
	var gotBody computebeta.CapacityAdviceRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = *r
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.WriteHeader(status)
		if resp != nil {
			assert.NoError(t, json.NewEncoder(w).Encode(resp))
		}
	}))
	t.Cleanup(srv.Close)
	c := NewSpotAdviceClient(srv.Client(), "test-project")
	c.baseURL = srv.URL
	return c, &gotReq, &gotBody
}

func TestSpotAdviceProbeCell(t *testing.T) {
	t.Parallel()
	c, gotReq, gotBody := adviceServer(t, http.StatusOK, &computebeta.CapacityAdviceResponse{
		Recommendations: []*computebeta.CapacityAdviceResponseRecommendation{{
			Scores: &computebeta.CapacityAdviceResponseRecommendationScores{
				Obtainability:   0.9,
				EstimatedUptime: "3600s",
			},
		}},
	})

	advice, err := c.ProbeCell(t.Context(), "europe-west4", "europe-west4-a", "n1-standard-8")
	require.NoError(t, err)
	require.NotNil(t, advice)
	assert.Equal(t, "europe-west4-a", advice.Zone)
	assert.Equal(t, "n1-standard-8", advice.MachineType)
	assert.InDelta(t, 0.9, advice.Obtainability, 1e-9)
	assert.Equal(t, int64(gcecatalog.SpotUptimeTierHour), advice.UptimeTierSeconds)

	assert.Equal(t, "/projects/test-project/regions/europe-west4/advice/capacity", gotReq.URL.Path)
	assert.Equal(t, "SPOT", gotBody.InstanceProperties.Scheduling.ProvisioningModel)
	assert.Equal(t, "ANY_SINGLE_ZONE", gotBody.DistributionPolicy.TargetShape)
	require.Len(t, gotBody.DistributionPolicy.Zones, 1)
	assert.Equal(t, "zones/europe-west4-a", gotBody.DistributionPolicy.Zones[0].Zone)
	assert.Equal(t, int64(1), gotBody.Size, "the cell probe measures a single-VM create")
	require.Len(t, gotBody.InstanceFlexibilityPolicy.InstanceSelections, 1)
	for _, sel := range gotBody.InstanceFlexibilityPolicy.InstanceSelections {
		assert.Equal(t, []string{"n1-standard-8"}, sel.MachineTypes)
	}
}

func TestSpotAdviceProbeCellNoRecommendation(t *testing.T) {
	t.Parallel()
	c, _, _ := adviceServer(t, http.StatusOK, &computebeta.CapacityAdviceResponse{})

	advice, err := c.ProbeCell(t.Context(), "europe-west4", "europe-west4-a", "n1-standard-8")
	require.NoError(t, err)
	assert.Nil(t, advice, "no recommendation means no cell, not an error")
}

func TestSpotAdviceProbeCellHTTPError(t *testing.T) {
	t.Parallel()
	c, _, _ := adviceServer(t, http.StatusForbidden, nil)

	_, err := c.ProbeCell(t.Context(), "europe-west4", "europe-west4-a", "n1-standard-8")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 403")
}

func TestParseUptimeSeconds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want int64
	}{
		{"3600s", 3600},
		{"600s", 600},
		{"60s", 60},
		{"", 0},
	} {
		got, err := parseUptimeSeconds(tc.in)
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.want, got, tc.in)
	}
	_, err := parseUptimeSeconds("an hour")
	require.Error(t, err, "unparseable uptime must surface, not read as a tier")
}
