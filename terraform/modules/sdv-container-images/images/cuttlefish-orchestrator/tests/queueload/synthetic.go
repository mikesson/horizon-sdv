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

package main

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

const taskTypeSynthetic = "load.synthetic"

// outcome is a task's scripted fate, chosen deterministically from its ID so
// every delivery attempt of the same task agrees.
type outcome int

const (
	outcomeSucceed outcome = iota
	outcomeTransient
	outcomePoison
)

// outcomeFor buckets the FNV hash of the task ID into [poison | transient |
// succeed] bands. Deterministic in id.
func outcomeFor(id string, poisonRate, transientRate float64) outcome {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	const scale = 1 << 20
	v := float64(h.Sum32()%scale) / scale
	switch {
	case v < poisonRate:
		return outcomePoison
	case v < poisonRate+transientRate:
		return outcomeTransient
	default:
		return outcomeSucceed
	}
}

var (
	errPoison    = errors.New("synthetic poison task")
	errTransient = errors.New("synthetic transient failure")
)

// syntheticHandler simulates HO/GCE work with a configurable latency band and
// scripted failures. A successful attempt records its effect in the ledger,
// which is the ground truth for post-run at-least-once verification.
type syntheticHandler struct {
	cfg      Cfg
	ledger   *ledger
	holder   string
	startLat *hist // enqueue -> first handling attempt

	effectCount atomic.Int64
}

func (h *syntheticHandler) TaskType() string { return taskTypeSynthetic }

func (h *syntheticHandler) Handle(ctx context.Context, t *task.Task) error {
	switch outcomeFor(t.ID, h.cfg.PoisonRate, h.cfg.TransientRate) {
	case outcomePoison:
		return errPoison
	case outcomeTransient:
		if t.Attempts <= h.cfg.TransientN {
			return fmt.Errorf("%w (attempt %d)", errTransient, t.Attempts)
		}
	case outcomeSucceed:
	}

	// Start latency measured from the durable enqueue timestamp (the same
	// producer-clock anchor the deleted payload timestamp carried).
	if t.Attempts == 1 {
		h.startLat.observe(time.Since(t.CreatedAt))
	}

	// The simulated work: an idle wait, like a handler blocked on HO/GCE I/O.
	span := h.cfg.HandlerMax - h.cfg.HandlerMin
	d := h.cfg.HandlerMin
	if span > 0 {
		d += time.Duration(rand.Int64N(int64(span))) //nolint:gosec // load jitter
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("handler interrupted: %w", ctx.Err())
	case <-timer.C:
	}

	if err := h.ledger.effect(t.ID, h.holder, t.Attempts); err != nil {
		return fmt.Errorf("recording effect: %w", err)
	}

	// Event-triggered crash: instance 0 SIGKILLs itself right after its Nth
	// effect is durable but before Complete runs. That is the exact
	// crash-between-effect-and-ack window, with zero production failpoints.
	// The task stays leased until the sweeper reclaims it; redelivery must
	// absorb the duplicate.
	if h.cfg.KillAfterEffects > 0 && h.cfg.InstanceIndex == 0 &&
		h.effectCount.Add(1) == int64(h.cfg.KillAfterEffects) {
		fmt.Fprintf(os.Stderr, "self-kill after effect %d (task %s attempt %d)\n",
			h.cfg.KillAfterEffects, t.ID, t.Attempts)
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	}
	return nil
}
