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

package fakegce_test

import (
	"testing"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"

	"cloud-cuttlefish-orchestrator/internal/platform/gce"
	"cloud-cuttlefish-orchestrator/tests/harness/fakegce"
)

// TestStockoutZoneSurfacesAsClassifiableError pins the stockout injection's
// contract with a REAL compute client. The insert operation must fail fast
// (no client-internal retry loop) with an error the production classifier
// reads as resource_exhausted, which is the signal zone failover reacts to.
func TestStockoutZoneSurfacesAsClassifiableError(t *testing.T) {
	t.Parallel()
	srv := fakegce.New()
	t.Cleanup(srv.Close)
	srv.StockoutZone("z-a")

	ctx := t.Context()
	client, err := compute.NewInstancesRESTClient(ctx,
		option.WithEndpoint(srv.URL()), option.WithoutAuthentication())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	insert := func(name, zone string) error {
		op, ierr := client.Insert(ctx, &computepb.InsertInstanceRequest{
			Project: "p", Zone: zone,
			InstanceResource: &computepb.Instance{Name: &name},
		})
		if ierr != nil {
			return ierr
		}
		waitCtx, cancel := t.Context(), func() {}
		defer cancel()
		return op.Wait(waitCtx)
	}

	start := time.Now()
	err = insert("cf-stockout", "z-a")
	require.Error(t, err, "a stocked-out zone must fail the insert operation")
	assert.Less(t, time.Since(start), 10*time.Second,
		"the stockout must fail fast, not sit in a client retry loop")
	classified := gce.ClassifyError(err)
	assert.Equal(t, gce.ErrorTypeResourceExhausted, classified.Type,
		"classifier read %q from %v", classified.Type, err)

	// Another zone stays insertable, and the instance records its zone.
	require.NoError(t, insert("cf-ok", "z-b"))
	assert.Equal(t, "z-b", srv.InstanceZone("cf-ok"))

	// Restoring the zone lifts the stockout.
	srv.RestoreZone("z-a")
	require.NoError(t, insert("cf-recovered", "z-a"))
	assert.Equal(t, "z-a", srv.InstanceZone("cf-recovered"))

	created, _, _ := srv.Ledger()
	assert.NotContains(t, created, "cf-stockout",
		"a stocked-out insert must not create an instance")
}
