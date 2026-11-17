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

package hostconfigfeature

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// imageCacheTTL is how long a successful resolution is reused. Only successes
// are cached: a refusal is re-checked on the next call, so an operator's fix
// takes effect on the next create on every replica.
const imageCacheTTL = 90 * time.Second

// imageResolver runs hostconfig.ResolveImage against the stored rows, with a
// per-replica cache of successes and singleflight so a burst of creates for
// one family makes one lookup.
type imageResolver struct {
	repo    hostconfig.Repository
	source  hostconfig.ImageSource
	project string
	ttl     time.Duration

	cache sync.Map // cacheKey -> imageCacheEntry
	group singleflight.Group
}

type imageCacheEntry struct {
	image     *hostconfig.Image
	expiresAt time.Time
}

func newImageResolver(repo hostconfig.Repository, source hostconfig.ImageSource, project string) *imageResolver {
	return &imageResolver{repo: repo, source: source, project: project, ttl: imageCacheTTL}
}

func cacheKey(machineFamily string, arch machinetype.Architecture) string {
	return machineFamily + "/" + string(arch)
}

// resolve answers which image boots machineFamily on a machine of arch.
func (r *imageResolver) resolve(ctx context.Context, machineFamily string, arch machinetype.Architecture) (*hostconfig.Image, error) {
	key := cacheKey(machineFamily, arch)
	if v, ok := r.cache.Load(key); ok {
		entry := v.(imageCacheEntry)
		if time.Now().Before(entry.expiresAt) {
			return entry.image, nil
		}
		r.cache.Delete(key)
	}
	v, err, _ := r.group.Do(key, func() (any, error) {
		cfg, err := r.repo.Get(ctx, machineFamily)
		if _, missing := errors.AsType[*hostconfig.NotFoundError](err); missing {
			// No row is no_config, which ResolveImage reports for a nil row.
			cfg, err = nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading the host image config for %s: %w", machineFamily, err)
		}
		img, err := hostconfig.ResolveImage(ctx, r.source, cfg, machineFamily, arch, r.project)
		if err != nil {
			return nil, err
		}
		r.cache.Store(key, imageCacheEntry{image: img, expiresAt: time.Now().Add(r.ttl)})
		return img, nil
	})
	if err != nil {
		return nil, err //nolint:wrapcheck // Do returns the closure's own error, which already wraps what it read and keeps ResolveImage's refusal typed.
	}
	return v.(*hostconfig.Image), nil
}

// check resolves a row that is not necessarily stored yet, bypassing the
// cache: a write is judged on what it would store.
func (r *imageResolver) check(ctx context.Context, cfg *hostconfig.HostImageConfig, arch machinetype.Architecture) (*hostconfig.Image, error) {
	return hostconfig.ResolveImage(ctx, r.source, cfg, cfg.MachineFamily, arch, r.project)
}

// forget drops every cached resolution of a family whose row changed.
func (r *imageResolver) forget(machineFamily string) {
	prefix := machineFamily + "/"
	r.cache.Range(func(key, _ any) bool {
		if strings.HasPrefix(key.(string), prefix) {
			r.cache.Delete(key)
		}
		return true
	})
}
