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
	"context"
	"log/slog"
	"sync"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// ARMCapability answers whether this deployment can build an arm64 host. The
// answer is yes when some machine family the catalog serves as arm resolves to
// an arm64 host image, through the same resolution a create runs. Both halves
// are operator acts (enabling the family in curation, and publishing an arm64
// image for it), so the answer is data, never a compile-time fact.
//
// Three consumers share the one answer. The pool derivation refuses an arm
// create the deployment cannot serve, the keeper declines to spend against the
// arm pool, and the autoscaler page states which of the two acts is still
// missing. All three read HERE so they cannot disagree.
//
// A row naming an image family is not enough. An x86 image on arm64 metal
// boots nothing, so the resolution checks the image's architecture.
type ARMCapability struct {
	images  ImageResolver
	catalog machinetype.Catalog
	logger  *slog.Logger

	mu      sync.Mutex
	ready   bool
	checked time.Time
	ttl     time.Duration
}

// NewARMCapability creates the probe. Nil dependencies read as not ready, so
// an arm create is refused instead of routed wrong.
func NewARMCapability(images ImageResolver, catalog machinetype.Catalog, logger *slog.Logger) *ARMCapability {
	if logger == nil {
		logger = slog.Default()
	}
	return &ARMCapability{images: images, catalog: catalog, logger: logger, ttl: time.Minute}
}

// Ready reports whether an arm64 host can be built here right now.
//
// A yes is cached for a minute. The autoscaler page polls every few seconds
// and every create asks, while the two operator acts behind the answer change
// on human timescales. A no is never cached, so publishing the image is seen
// on the next ask. An error reads as not ready. Refusing an arm create while a
// read fails is recoverable, while provisioning one that cannot boot wastes a
// machine.
func (c *ARMCapability) Ready(ctx context.Context) bool {
	c.mu.Lock()
	if c.ready && time.Since(c.checked) < c.ttl {
		c.mu.Unlock()
		return true
	}
	c.mu.Unlock()

	ready := c.probe(ctx)

	c.mu.Lock()
	c.ready = ready
	c.checked = time.Now()
	c.mu.Unlock()
	return ready
}

func (c *ARMCapability) probe(ctx context.Context) bool {
	if c.images == nil || c.catalog == nil {
		return false
	}
	shapes, err := c.catalog.List(ctx, machinetype.ListFilter{})
	if err != nil {
		c.logger.Warn("arm capability: catalog unavailable; reading as not ready", slog.Any("error", err))
		return false
	}
	seen := map[string]bool{}
	for _, shape := range shapes {
		if shape.Architecture != machinetype.ArchArm || seen[shape.Family] {
			continue
		}
		seen[shape.Family] = true
		if _, err := c.images.ResolveImage(ctx, shape.Family, machinetype.ArchArm); err == nil {
			return true
		}
	}
	return false
}
