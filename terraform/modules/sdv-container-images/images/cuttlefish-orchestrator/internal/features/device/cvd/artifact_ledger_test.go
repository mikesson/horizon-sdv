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

package cvd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"cloud-cuttlefish-orchestrator/internal/domain/build"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/device/devicetest"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
)

// oneBuild serves a single registered build record.
type oneBuild struct {
	nopBuilds
	b *build.Build
}

func (o oneBuild) Get(_ context.Context, id string) (*build.Build, error) {
	if o.b != nil && o.b.ID == id {
		return o.b, nil
	}
	return nil, build.ErrNotFound
}

// recordingLedger captures Record calls.
type recordingLedger struct {
	records []string
}

func (r *recordingLedger) Record(_ context.Context, hostID, fetchKey, buildID string, inc int64) error {
	r.records = append(r.records, hostID+"|"+fetchKey+"|"+buildID)
	return nil
}

// Only a pinned (build-id-keyed) registered build earns a ledger row: a
// tracking build's cached artifacts may already be stale, and an
// unregistered reference may be either. Dropping either exclusion, or the
// write itself, goes red here.
func TestRecordArtifactCachePinnedOnly(t *testing.T) {
	t.Parallel()
	pinned := &build.Build{ID: "reg-1", Source: build.SourceAB, BuildID: "12345678", Target: "aosp_cf_x86_64_only_phone-userdebug"}
	tracking := &build.Build{ID: "reg-2", Source: build.SourceAB, Branch: "aosp-android-latest-release", Target: "aosp_cf_x86_64_only_phone-userdebug"}

	cases := []struct {
		name    string
		builds  oneBuild
		buildID string
		want    []string
	}{
		{
			"pinned build records the shipped key",
			oneBuild{b: pinned},
			"reg-1",
			[]string{"h|@ab/12345678/aosp_cf_x86_64_only_phone-userdebug|reg-1"},
		},
		{"tracking build records nothing", oneBuild{b: tracking}, "reg-2", nil},
		{"unregistered reference records nothing", oneBuild{}, "12345678", nil},
		{"no build records nothing", oneBuild{}, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hostRepo := hosttest.NewFakeRepo()
			hostRepo.Hosts["h"] = &host.Host{ID: "h", Status: host.StatusRunning, Incarnation: 2}
			ledger := &recordingLedger{}
			act := NewActuator(devicetest.NewFakeRepo(), hostRepo, tc.builds, nil, nil,
				WithArtifactLedger(ledger))

			act.recordArtifactCache(t.Context(), &device.Device{ID: "d", HostID: "h", BuildID: tc.buildID})
			assert.Equal(t, tc.want, ledger.records)
		})
	}
}
