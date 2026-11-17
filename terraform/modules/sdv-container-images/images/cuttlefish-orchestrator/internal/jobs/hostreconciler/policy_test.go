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

package hostreconciler

import (
	"context"
	"errors"
	"log/slog"
	"testing"
)

// TestMayDeleteOrphansStaysDetectOnlyOnAFailedRead: a Spanner blip must never
// be the reason this loop starts deleting VMs.
func TestMayDeleteOrphansStaysDetectOnlyOnAFailedRead(t *testing.T) {
	t.Parallel()
	// Compiled default true as well: neither source may rescue a failed read
	// into deleting.
	svc := &Service{config: Config{DeleteOrphanVMs: true}, logger: slog.New(slog.DiscardHandler)}
	svc.deletePolicy = func(context.Context) (bool, error) {
		return true, errors.New("spanner unavailable")
	}

	if svc.mayDeleteOrphans(t.Context()) {
		t.Error("a failed policy read enabled deletion")
	}
}

func TestMayDeleteOrphansTakesTheRuntimeAnswer(t *testing.T) {
	t.Parallel()
	svc := &Service{config: Config{DeleteOrphanVMs: false}, logger: slog.New(slog.DiscardHandler)}
	svc.deletePolicy = func(context.Context) (bool, error) { return true, nil }

	if !svc.mayDeleteOrphans(t.Context()) {
		t.Error("the runtime answer was ignored in favor of the compiled default")
	}
}

func TestMayDeleteOrphansFallsBackToTheCompiledValue(t *testing.T) {
	t.Parallel()
	svc := &Service{config: Config{DeleteOrphanVMs: true}, logger: slog.New(slog.DiscardHandler)}

	if !svc.mayDeleteOrphans(t.Context()) {
		t.Error("with no policy wired the compiled Config value must stand")
	}
}
