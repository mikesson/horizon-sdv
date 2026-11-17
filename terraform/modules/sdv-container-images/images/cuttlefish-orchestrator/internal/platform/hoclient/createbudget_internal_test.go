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

package hoclient

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
)

// The create ceiling is the one wait sized by what it was asked to do rather
// than by the client it came through.
func TestCreateBudgetScalesWithInstances(t *testing.T) {
	t.Parallel()
	c := &HostClient{}

	assert.Equal(t, device.TransientProgressWindow, c.waitCeiling(c.createBudget(1)),
		"a single-instance create keeps the window it always had")
	assert.Equal(t, device.TransientProgressWindow+7*device.PerInstanceProgressWindow,
		c.waitCeiling(c.createBudget(8)),
		"seven instances past the first each add their own window")

	// Every other verb is untouched: nothing else knows an instance count, and
	// nothing else should have grown one.
	assert.Equal(t, defaultMaxWait, c.waitCeiling(queuedBudget))
	assert.Equal(t, defaultMaxWait, c.waitCeiling(extendedBudget))
}

// An operator who widened MaxWait keeps it. The scaling raises a floor; it is
// not a second opinion about a configured ceiling.
func TestCreateBudgetNeverShortensAConfiguredMaxWait(t *testing.T) {
	t.Parallel()
	c := &HostClient{config: ClientConfig{MaxWait: time.Hour}}

	assert.Equal(t, time.Hour, c.waitCeiling(c.createBudget(1)))
	assert.Equal(t, time.Hour, c.waitCeiling(c.createBudget(8)),
		"the computed window is below the configured one here, so the configured one stands")
}

// The request-path tiers derive their bound from an envelope and must stay
// capped by the ceiling, which is what keeps a slow verb inside the connection
// deadline it was classified into. Adding a per-create ceiling must not have
// loosened them.
func TestRequestPathTiersStillCappedByTheCeiling(t *testing.T) {
	t.Parallel()
	c := &HostClient{config: ClientConfig{MaxWait: 5 * time.Second}}

	assert.Equal(t, 5*time.Second, c.waitLimit(extendedBudget),
		"a MaxWait below the derived envelope limit still wins")
}

// A config-less create request is the one-instance shape a simple create
// sends, and counts as one rather than as nothing. A zero would make the
// ceiling shorter than the base window.
func TestRequestInstances(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 1, requestInstances(&CreateCVDRequest{}))
	assert.Equal(t, 1, requestInstances(&CreateCVDRequest{
		EnvConfig: map[string]any{"instances": []any{}},
	}))
	assert.Equal(t, 3, requestInstances(&CreateCVDRequest{
		EnvConfig: map[string]any{"instances": []any{1, 2, 3}},
	}))
}
