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
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// Each reclamation direction opens ONE statement however many pools it is
// answering for, bounded by the deepest pool's gap so a single read still holds
// every pool's own candidates.
//
// Asserted here, at the read, rather than only at the keeper's port: the port
// counts calls to these two methods, so the listing this is about could be moved
// inside a per-pool loop (the obvious way to give each pool its own bound)
// with the port's count still exactly one and a read per pool opening every
// thirty seconds forever. Nothing else would notice: the emulator tests that
// exercise the real SQL assert renewals and reclamations, never statements.
func TestFleetListingsOpenOneStatementWhateverThePoolCount(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo()
	svc, _ := newTestService(t, repo, nil)
	// Three pools with work to do, the deepest short by three.
	gaps := map[string]int64{host.PoolDefault: 2, "gpu": 1, "arm": 3}

	if _, err := svc.RescueFleetHosts(t.Context(), host.ProvisionLead, gaps); err != nil {
		t.Fatalf("RescueFleetHosts: %v", err)
	}
	if len(repo.rescuableLists) != 1 || repo.rescuableLists[0] != 3 {
		t.Errorf("the renewal opened %v for %d pools, want one listing bounded by the deepest gap of 3",
			repo.rescuableLists, len(gaps))
	}

	if _, err := svc.ShortenSurplusHosts(t.Context(), host.ProvisionLead, gaps, time.Minute, nil); err != nil {
		t.Fatalf("ShortenSurplusHosts: %v", err)
	}
	if len(repo.shortenableLists) != 1 || repo.shortenableLists[0] != 3 {
		t.Errorf("the reclamation opened %v for %d pools, want one listing bounded by the deepest gap of 3",
			repo.shortenableLists, len(gaps))
	}
}
