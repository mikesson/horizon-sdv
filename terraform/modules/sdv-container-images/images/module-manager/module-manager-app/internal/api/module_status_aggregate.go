// Copyright (c) 2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"strings"
)

// mergeManagedApplicationStatuses combines sync/health/operation from parent + child Argo CD Applications.
func mergeManagedApplicationStatuses(statuses []StatusResponse) (syncStatus, healthStatus, operationPhase string) {
	if len(statuses) == 0 {
		return "", "", ""
	}
	syncs := make([]string, 0, len(statuses))
	healths := make([]string, 0, len(statuses))
	ops := make([]string, 0, len(statuses))
	for _, s := range statuses {
		if t := strings.TrimSpace(s.SyncStatus); t != "" {
			syncs = append(syncs, t)
		}
		if t := strings.TrimSpace(s.HealthStatus); t != "" {
			healths = append(healths, t)
		}
		if t := strings.TrimSpace(s.OperationPhase); t != "" {
			ops = append(ops, t)
		}
	}
	return mergeSyncStatuses(syncs), mergeHealthStatuses(healths), mergeOperationPhases(ops)
}

func mergeSyncStatuses(syncs []string) string {
	if len(syncs) == 0 {
		return ""
	}
	allSynced := true
	for _, s := range syncs {
		if s != "Synced" {
			allSynced = false
			break
		}
	}
	if allSynced {
		return "Synced"
	}
	for _, s := range syncs {
		if s == "OutOfSync" {
			return "OutOfSync"
		}
	}
	return "Unknown"
}

// healthRank maps Argo CD health.status to severity (higher = worse for readiness).
var healthRank = map[string]int{
	"Degraded":    5,
	"Missing":     4,
	"Progressing": 3,
	"Unknown":     2,
	"Suspended":   3,
	"Healthy":     1,
}

func mergeHealthStatuses(healths []string) string {
	if len(healths) == 0 {
		return ""
	}
	best := ""
	bestR := -1
	for _, h := range healths {
		r, ok := healthRank[h]
		if !ok {
			r = 2 // unknown string → treat like Unknown
		}
		if r > bestR {
			bestR, best = r, h
		}
	}
	return best
}

func mergeOperationPhases(ops []string) string {
	for _, p := range ops {
		if p == "Running" {
			return "Running"
		}
	}
	for _, p := range ops {
		if p == "Pending" {
			return "Pending"
		}
	}
	return ""
}
