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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/build"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
)

// buildShelf is a registry holding one default build. It counts reads, so a
// case can tell a skipped resolution from one that looked and agreed.
type buildShelf struct {
	builds map[string]*build.Build
	reads  int
}

func (s *buildShelf) Get(_ context.Context, id string) (*build.Build, error) {
	s.reads++
	if b, ok := s.builds[id]; ok {
		return b, nil
	}
	return nil, &build.NotFoundError{ID: id}
}

func (s *buildShelf) List(_ context.Context, f build.ListFilter) ([]*build.Build, error) {
	s.reads++
	var out []*build.Build
	for _, b := range s.builds {
		if f.IsDefault != nil && b.IsDefault != *f.IsDefault {
			continue
		}
		out = append(out, b)
	}
	return out, nil
}

func (s *buildShelf) Count(_ context.Context, _ build.ListFilter) (int64, error) {
	s.reads++
	return int64(len(s.builds)), nil
}

func defaultShelf() *buildShelf {
	return &buildShelf{builds: map[string]*build.Build{
		"b-default": {ID: "b-default", Name: "default", Source: build.SourceAB, Branch: "main", Target: "phone", IsDefault: true},
		"b-core":    {ID: "b-core", Name: "core", Source: build.SourceURL},
	}}
}

// sdvPairConfig is an env_config whose two instances each name a build, one by
// registered id and one by URL, the way an SDV Core and IVI pair arrives.
func sdvPairConfig() map[string]any {
	return map[string]any{"instances": []any{
		map[string]any{"name": "core", "@build": "b-core"},
		map[string]any{"name": "ivi", "disk": map[string]any{"default_build": "gs://b/ivi-img.zip"}},
	}}
}

// A group that names a build for every instance gets no device-level build.
// The payload fills each instance's gaps from the device-level build, so the
// platform default would merge its host package and disk fields into a pair
// that chose its own.
func TestResolveBuildLeavesAGroupThatNamesItsBuildsAlone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		run  func(context.Context, *Resolver, *device.CreateRequest, *device.Resolution) error
	}{
		{"resolve", func(ctx context.Context, r *Resolver, req *device.CreateRequest, res *device.Resolution) error {
			return r.ResolveBuild(ctx, device.BuildRequirement{}, "u@example.com", req, res)
		}},
		{"plan", func(ctx context.Context, r *Resolver, req *device.CreateRequest, res *device.Resolution) error {
			return r.PlanBuild(ctx, device.BuildRequirement{}, req, res)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			shelf := defaultShelf()
			r := NewResolver(ResolverDeps{Builds: shelf})
			req := device.CreateRequest{EnvConfig: sdvPairConfig()}
			res := &device.Resolution{}

			require.NoError(t, tc.run(t.Context(), r, &req, res))
			assert.Empty(t, req.BuildID, "the platform default must not become the pair's device-level build")
			assert.Empty(t, req.BuildTarget)
			assert.Empty(t, res.BuildID)
			assert.Equal(t, device.BuildActionUse, res.BuildAction)
			assert.Zero(t, shelf.reads, "the default build is not even looked up")
		})
	}
}

// One instance without a build still needs the device-level one to fill it, so
// the default resolves exactly as it always has.
func TestResolveBuildStillDefaultsAGroupWithAnUnbuiltInstance(t *testing.T) {
	t.Parallel()
	r := NewResolver(ResolverDeps{Builds: defaultShelf()})
	cfg := sdvPairConfig()
	cfg["instances"] = append(cfg["instances"].([]any), map[string]any{"name": "cluster"})
	req := device.CreateRequest{EnvConfig: cfg}
	res := &device.Resolution{}

	require.NoError(t, r.ResolveBuild(t.Context(), device.BuildRequirement{}, "u@example.com", &req, res))
	assert.Equal(t, "b-default", req.BuildID)
	assert.Equal(t, "b-default", res.BuildID)
}

// An explicit requirements.build wins even when every instance names one: the
// caller asked for that build to fill the gaps.
func TestResolveBuildHonoursAnExplicitRequirementOverInstanceBuilds(t *testing.T) {
	t.Parallel()
	r := NewResolver(ResolverDeps{Builds: defaultShelf()})
	req := device.CreateRequest{EnvConfig: sdvPairConfig()}
	res := &device.Resolution{}

	require.NoError(t, r.ResolveBuild(t.Context(), device.BuildRequirement{Registered: "b-core"},
		"u@example.com", &req, res))
	assert.Equal(t, "b-core", req.BuildID)
}
