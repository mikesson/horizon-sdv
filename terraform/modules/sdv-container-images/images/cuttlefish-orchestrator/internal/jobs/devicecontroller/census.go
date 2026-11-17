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

package devicecontroller

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// CVD census bounds. A group must be seen unclaimed across the grace
// window before it counts as an orphan: device rows are created before their
// groups, so a group mid-create only looks unclaimed when the listing raced
// the row read. Persistence across cycles is what distinguishes a leak.
// Sweeps (when enabled) are capped per host per cycle.
const (
	censusGrace               = 10 * time.Minute
	maxOrphanCVDSweepsPerHost = 2
)

// pruneCensus drops first-seen entries for hosts not visited this cycle. No
// fresh listing means the grace clock has no evidence to run against, and a
// group re-earns its grace when its host is next seen. Without this, entries
// for deleted hosts accumulated forever.
func (s *Service) pruneCensus(visited map[string]bool) {
	s.censusMu.Lock()
	defer s.censusMu.Unlock()
	for key := range s.censusFirstSeen {
		hostID, _, ok := strings.Cut(key, "/")
		if ok && !visited[hostID] {
			delete(s.censusFirstSeen, key)
		}
	}
}

// censusHost diffs the host's listed CVD groups against its device rows.
// This is the drift that create/cleanup self-races cannot see. A group no row
// claims leaks host RAM invisibly until the whole VM is torn down: an
// out-of-band `cvd` use, or a crashed create whose task never redelivered.
// Tombstoned device rows still claim their group (its cleanup task owns the
// teardown); once the tombstone is purged, a group still present is a leak.
// Detection always logs and gauges; deletion needs SweepOrphanCVDs and only
// fires after the group has stayed unclaimed across censusGrace.
func (s *Service) censusHost(ctx context.Context, hostID string, client hoclient.Client, cvds []*hoclient.CVDHealthStatus) {
	rows, err := s.deviceRepo.List(ctx, device.ListFilter{HostID: &hostID, IncludeDeleted: true})
	if err != nil {
		s.logger.Warn("census: failed to list device rows",
			slog.String("host_id", hostID), slog.Any("error", err))
		return
	}
	expected := make(map[string]bool, len(rows)+1)
	// The warm plane's throwaway prefetch group is expected by name: no
	// device row ever claims it, and the plane owns its cleanup. Counting
	// it would make every warm cycle an "anomaly" on a gauge documented as
	// any-nonzero-is-wrong.
	expected[device.WarmGroupName] = true
	for _, d := range rows {
		expected[device.CVDGroupName(d.ID)] = true
		if group, _, ok := strings.Cut(d.CVDID, "/"); ok && group != "" {
			expected[group] = true
		}
	}
	listed := make(map[string]bool, len(cvds))
	for _, c := range cvds {
		if c.Group != "" {
			listed[c.Group] = true
		}
	}

	orphans := s.orphanGroups(hostID, listed, expected)

	maySweep := s.mayAct(ctx, s.sweepPolicy, s.config.SweepOrphanCVDs, "orphan_cvds")
	swept := 0
	for _, group := range orphans {
		s.cycleOrphans.Add(1)
		s.logger.Warn("orphan CVD group detected: no device row claims it",
			slog.String("host_id", hostID),
			slog.String("group", group),
			slog.Bool("sweep_enabled", maySweep),
		)
		if !maySweep || swept >= maxOrphanCVDSweepsPerHost {
			continue
		}
		if err := client.DeleteCVD(ctx, group); err != nil {
			s.logger.Error("failed to sweep orphan CVD group",
				slog.String("host_id", hostID), slog.String("group", group), slog.Any("error", err))
			continue
		}
		swept++
		s.orphanCVDsSwept.Add(1)
		s.logger.Warn("orphan CVD group swept",
			slog.String("host_id", hostID), slog.String("group", group))
	}
}

// orphanGroups updates the first-seen ledger for one host's listing and
// returns the groups that have stayed unclaimed past the grace window.
func (s *Service) orphanGroups(hostID string, listed, expected map[string]bool) []string {
	now := time.Now()
	var orphans []string
	s.censusMu.Lock()
	defer s.censusMu.Unlock()
	// Drop tracking for groups that vanished or became claimed.
	for key := range s.censusFirstSeen {
		group, found := strings.CutPrefix(key, hostID+"/")
		if !found {
			continue
		}
		if !listed[group] || expected[group] {
			delete(s.censusFirstSeen, key)
		}
	}
	for group := range listed {
		if expected[group] {
			continue
		}
		key := hostID + "/" + group
		first, seen := s.censusFirstSeen[key]
		if !seen {
			s.censusFirstSeen[key] = now
			continue
		}
		if now.Sub(first) >= censusGrace {
			orphans = append(orphans, group)
		}
	}
	return orphans
}
