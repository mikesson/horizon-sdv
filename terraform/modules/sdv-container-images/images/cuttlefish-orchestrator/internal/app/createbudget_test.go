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

package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/features/device/validation"
	"cloud-cuttlefish-orchestrator/internal/jobs/taskworker"
)

// createHandlerReserve is what a create task spends outside its operation wait:
// the host readiness leg, the build resolution, the POST that starts the
// operation, and the row commit after it. It is a rough floor, not a
// measurement, and its job is to keep the assertion below from passing on a
// zero margin.
const createHandlerReserve = 3 * time.Minute

// The create wait ceiling scales with instance count, and the task worker's
// HandleTimeout is what actually bounds the handler running that wait. Nothing
// links the two: MaxInstances lives in the device slice, the window in the
// domain, the timeout in internal/jobs, and only the composition root sees all
// three.
//
// Left unlinked, raising MaxInstances past ten would push the ceiling beyond
// the handler that has to hold it. The wait would then never expire on its own,
// because the handler would be canceled first, and the expiry is exactly what
// resumes an abandoned create, so the failure would be a create that stops
// resuming instead of anything that looks like a timeout.
func TestCreateWaitCeilingFitsTheTaskHandler(t *testing.T) {
	t.Parallel()
	handle := taskworker.DefaultConfig().HandleTimeout
	widest := device.CreateProgressWindow(validation.MaxInstances)

	assert.Less(t, widest+createHandlerReserve, handle,
		"a create of %d instances waits up to %v, which must leave the %v handler room for the rest of the task",
		validation.MaxInstances, widest, handle)
}

// The scaling is per instance PAST the first, so an ordinary single-instance
// create is bit-for-bit the window it always had. That is the one property
// that keeps this a group change instead of a fleet-wide loosening.
func TestCreateWaitCeilingUnchangedForOneInstance(t *testing.T) {
	t.Parallel()
	assert.Equal(t, device.TransientProgressWindow, device.CreateProgressWindow(1))
}
