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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	computebeta "google.golang.org/api/compute/v0.beta"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
)

// spotAdviceBaseURL is the beta compute endpoint the capacity-advice probe
// posts to. The pinned client libraries predate the advice.capacity method
// (the v1 GAPIC exposes only advice.calendarMode, and the beta discovery
// client carries the request/response types without a call binding), so the
// probe issues the one REST call itself over the shared authenticated
// transport, keeping the official beta types on the wire.
const spotAdviceBaseURL = "https://compute.googleapis.com/compute/beta"

// SpotAdviceClient probes the GCE advice.capacity API for per-cell Spot
// capacity advice.
type SpotAdviceClient struct {
	hc      *http.Client
	project string
	baseURL string
}

// NewSpotAdviceClient creates a capacity-advice prober. hc must already be
// authenticated for the Compute API (the orchestrator's shared GCE transport).
func NewSpotAdviceClient(hc *http.Client, project string) *SpotAdviceClient {
	return &SpotAdviceClient{hc: hc, project: project, baseURL: spotAdviceBaseURL}
}

// ProbeCell asks for single-VM Spot advice in one (zone, machine type) cell:
// a size-1, single-zone request, so the obtainability score measures exactly
// "would one Spot host create succeed here now". It returns nil when the API
// offers no recommendation for the cell.
func (c *SpotAdviceClient) ProbeCell(ctx context.Context, region, zone, machineType string) (*gcecatalog.SpotAdvice, error) {
	reqBody := &computebeta.CapacityAdviceRequest{
		InstanceProperties: &computebeta.CapacityAdviceRequestInstanceProperties{
			Scheduling: &computebeta.CapacityAdviceRequestInstancePropertiesScheduling{
				ProvisioningModel: "SPOT",
			},
		},
		InstanceFlexibilityPolicy: &computebeta.CapacityAdviceRequestInstanceFlexibilityPolicy{
			InstanceSelections: map[string]computebeta.CapacityAdviceRequestInstanceFlexibilityPolicyInstanceSelection{
				"cell": {MachineTypes: []string{machineType}},
			},
		},
		DistributionPolicy: &computebeta.CapacityAdviceRequestDistributionPolicy{
			TargetShape: "ANY_SINGLE_ZONE",
			Zones: []*computebeta.CapacityAdviceRequestDistributionPolicyZoneConfiguration{
				{Zone: "zones/" + zone},
			},
		},
		Size: 1,
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("encoding capacity-advice request: %w", err)
	}

	url := fmt.Sprintf("%s/projects/%s/regions/%s/advice/capacity", c.baseURL, c.project, region)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("building capacity-advice request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("probing capacity advice for %s/%s: %w", zone, machineType, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("capacity advice for %s/%s: HTTP %d: %s",
			zone, machineType, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var adviceResp computebeta.CapacityAdviceResponse
	if err := json.NewDecoder(resp.Body).Decode(&adviceResp); err != nil {
		return nil, fmt.Errorf("decoding capacity advice for %s/%s: %w", zone, machineType, err)
	}
	if len(adviceResp.Recommendations) == 0 || adviceResp.Recommendations[0].Scores == nil {
		return nil, nil //nolint:nilnil // no recommendation for the cell is a valid outcome, not an error.
	}
	scores := adviceResp.Recommendations[0].Scores
	uptime, err := parseUptimeSeconds(scores.EstimatedUptime)
	if err != nil {
		return nil, fmt.Errorf("capacity advice for %s/%s: %w", zone, machineType, err)
	}
	return &gcecatalog.SpotAdvice{
		Zone:              zone,
		MachineType:       machineType,
		Obtainability:     scores.Obtainability,
		UptimeTierSeconds: uptime,
	}, nil
}

// parseUptimeSeconds parses the API's estimated-uptime duration string
// ("3600s"). Empty reads as 0 = unknown (ranking treats an unknown tier as
// unfiltered).
func parseUptimeSeconds(v string) (int64, error) {
	if v == "" {
		return 0, nil
	}
	seconds, err := strconv.ParseInt(strings.TrimSuffix(v, "s"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unparseable estimated uptime %q: %w", v, err)
	}
	return seconds, nil
}
