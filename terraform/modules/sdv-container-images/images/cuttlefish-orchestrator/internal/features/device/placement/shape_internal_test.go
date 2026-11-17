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

package placement

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
)

func TestShapeFromEnvConfig(t *testing.T) {
	t.Parallel()
	single := SingleInstanceShape()

	for _, tc := range []struct {
		name   string
		config map[string]any
		want   GroupShape
	}{
		{
			// Every config-less path (simple creates, batch members, pool
			// hosts) still occupies one slot and still costs one instance.
			name:   "no config at all",
			config: nil,
			want:   single,
		},
		{
			name:   "an instances key holding nothing",
			config: map[string]any{"instances": []any{}},
			want:   single,
		},
		{
			name: "instances that declare no vm section take the defaults",
			config: map[string]any{"instances": []any{
				map[string]any{"name": "phone"},
				map[string]any{"name": "watch"},
			}},
			want: GroupShape{
				Instances: 2,
				MemoryMB:  2 * device.DefaultInstanceMemoryMB,
				VCPUs:     device.DefaultInstanceVCPUs,
			},
		},
		{
			// Memory sums and vCPUs take the widest single guest. That is the
			// whole asymmetry the rule rests on, in one case.
			name: "a phone beside a watch",
			config: map[string]any{"instances": []any{
				map[string]any{"vm": map[string]any{"memory_mb": 8192.0, "cpus": 8.0}},
				map[string]any{"vm": map[string]any{"memory_mb": 2048.0, "cpus": 2.0}},
			}},
			want: GroupShape{Instances: 2, MemoryMB: 10240, VCPUs: 8},
		},
		{
			// A zero is a caller saying nothing, not an instance that needs
			// nothing; sizing a host from it would provision one that cannot
			// hold what the create then asks the host for.
			name: "zero and negative values fall back",
			config: map[string]any{"instances": []any{
				map[string]any{"vm": map[string]any{"memory_mb": 0.0, "cpus": -4.0}},
			}},
			want: single,
		},
		{
			name: "a malformed vm section falls back",
			config: map[string]any{"instances": []any{
				map[string]any{"vm": "not an object"},
			}},
			want: single,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, ShapeFromEnvConfig(tc.config))
		})
	}
}

// The numbers arrive as decoded JSON on every production path, so the shape
// has to read what a real body carries rather than what a Go literal does.
func TestShapeFromDecodedJSON(t *testing.T) {
	t.Parallel()
	var config map[string]any
	require.NoError(t, json.Unmarshal([]byte(
		`{"instances":[{"vm":{"cpus":4,"memory_mb":4096}},{"vm":{"cpus":2,"memory_mb":2048}}]}`,
	), &config))

	assert.Equal(t, GroupShape{Instances: 2, MemoryMB: 6144, VCPUs: 4}, ShapeFromEnvConfig(config))
}

// The demand rounds UP into the catalog's whole-gigabyte vocabulary: a shape
// one megabyte short of the group is not a shape that holds it.
func TestGroupShapeHostMemoryGB(t *testing.T) {
	t.Parallel()
	assert.Equal(t, int64(12), GroupShape{MemoryMB: 8192}.hostMemoryGB())
	assert.Equal(t, int64(20), GroupShape{MemoryMB: 16384}.hostMemoryGB())
	assert.Equal(t, int64(5), GroupShape{MemoryMB: 1}.hostMemoryGB(),
		"a single megabyte over four gigabytes still needs a fifth")
}
