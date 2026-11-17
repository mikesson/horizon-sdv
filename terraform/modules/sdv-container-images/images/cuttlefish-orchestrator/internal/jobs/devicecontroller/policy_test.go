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
	"errors"
	"log/slog"
	"testing"
)

func TestMayActStaysDetectOnlyOnAFailedRead(t *testing.T) {
	t.Parallel()
	svc := &Service{logger: slog.New(slog.DiscardHandler)}
	failing := func(context.Context) (bool, error) { return true, errors.New("spanner unavailable") }

	// Compiled default true as well: neither source may rescue a failed read
	// into acting.
	if svc.mayAct(t.Context(), failing, true, "orphan_cvds") {
		t.Error("a failed policy read enabled a destructive sweep")
	}
}

func TestMayActTakesTheRuntimeAnswer(t *testing.T) {
	t.Parallel()
	svc := &Service{logger: slog.New(slog.DiscardHandler)}
	on := func(context.Context) (bool, error) { return true, nil }

	if !svc.mayAct(t.Context(), on, false, "zombie_recreate") {
		t.Error("the runtime answer was ignored in favor of the compiled default")
	}
}

func TestMayActFallsBackToTheCompiledValue(t *testing.T) {
	t.Parallel()
	svc := &Service{logger: slog.New(slog.DiscardHandler)}

	if !svc.mayAct(t.Context(), nil, true, "orphan_cvds") {
		t.Error("with no policy wired the compiled default must stand")
	}
}
