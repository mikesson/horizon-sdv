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

package hostfeature_test

import (
	"testing"

	hostfeature "cloud-cuttlefish-orchestrator/internal/features/host"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// TestQuotaHighWaterMark pins the P4 usage watermark: the host-create quota
// increment records the peak, and releases never lower it.
func TestQuotaHighWaterMark(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	if err := db.SeedQuota(ctx, 10, 0); err != nil {
		t.Fatalf("seed quota: %v", err)
	}
	hostRepo := hostfeature.NewHostRepository(db.Client)

	// A fresh quota row has no watermark.
	q, err := db.ReadQuota(ctx)
	if err != nil {
		t.Fatalf("get quota: %v", err)
	}
	if q.HighWaterAt != nil {
		t.Fatalf("fresh quota has watermark %d@%v", q.HighWaterUsedHosts, q.HighWaterAt)
	}

	h1 := newTestHost("wm-host-1")
	h2 := newTestHost("wm-host-2")
	if err := hostRepo.CreateWithQuotaIncrement(ctx, h1); err != nil {
		t.Fatalf("create host 1: %v", err)
	}
	if err := hostRepo.CreateWithQuotaIncrement(ctx, h2); err != nil {
		t.Fatalf("create host 2: %v", err)
	}

	q, err = db.ReadQuota(ctx)
	if err != nil {
		t.Fatalf("get quota after creates: %v", err)
	}
	if q.HighWaterUsedHosts != 2 || q.HighWaterAt == nil {
		t.Fatalf("watermark = %d@%v, want 2@non-nil", q.HighWaterUsedHosts, q.HighWaterAt)
	}

	// Releasing quota lowers usage but never the watermark.
	if err := hostRepo.DeleteWithQuotaDecrement(ctx, h2.ID); err != nil {
		t.Fatalf("delete host 2: %v", err)
	}
	q, err = db.ReadQuota(ctx)
	if err != nil {
		t.Fatalf("get quota after delete: %v", err)
	}
	if q.UsedHosts != 1 {
		t.Errorf("used = %d, want 1", q.UsedHosts)
	}
	if q.HighWaterUsedHosts != 2 {
		t.Errorf("watermark = %d, want 2 (releases must not lower it)", q.HighWaterUsedHosts)
	}
}
