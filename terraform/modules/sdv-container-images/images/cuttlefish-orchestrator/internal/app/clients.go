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
	"context"
	"fmt"
	"net/http"

	compute "cloud.google.com/go/compute/apiv1"
	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	gcsstorage "cloud.google.com/go/storage"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"google.golang.org/api/option"
	googlehttp "google.golang.org/api/transport/http"

	"cloud-cuttlefish-orchestrator/internal/config"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

// clients bundles the process-lifetime infrastructure clients. Run constructs
// them and owns their close order (see the defer sequence there); the wiring
// stages only consume them.
type clients struct {
	spanner *spanner.Client
	gceHTTP *http.Client
	gce     *gceClients
	storage *gcsstorage.Client
	// metrics reads the project's own quota telemetry, the axes Compute
	// Engine's region list cannot report.
	metrics *monitoring.MetricClient
}

// newSpannerClient maps the database configuration onto the platform client.
func newSpannerClient(ctx context.Context, cfg *config.Config) (*spanner.Client, error) {
	spannerCfg := spanner.DefaultConfig()
	spannerCfg.ProjectID = cfg.Database.Spanner.Project
	spannerCfg.InstanceID = cfg.Database.Spanner.Instance
	spannerCfg.DatabaseID = cfg.Database.Spanner.Database

	client, err := spanner.NewClient(ctx, spannerCfg)
	if err != nil {
		return nil, fmt.Errorf("creating Spanner client: %w", err)
	}
	return client, nil
}

// newGCEHTTPClient builds the authenticated, otel-instrumented HTTP client
// shared by the GCE REST clients and the Spot capacity-advice prober.
func newGCEHTTPClient(ctx context.Context) (*http.Client, error) {
	baseTransport, err := googlehttp.NewTransport(ctx, http.DefaultTransport, option.WithScopes(compute.DefaultAuthScopes()...))
	if err != nil {
		return nil, fmt.Errorf("creating Google HTTP transport: %w", err)
	}

	otelTransport := otelhttp.NewTransport(
		baseTransport,
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return "GCE " + r.Method + " " + r.URL.Path
		}),
	)
	return &http.Client{Transport: otelTransport}, nil
}

// gceClients bundles the Compute API clients the orchestrator uses; Close
// releases whatever was constructed.
type gceClients struct {
	instances        *compute.InstancesClient
	operations       *compute.ZoneOperationsClient
	zones            *compute.ZonesClient
	machineTypes     *compute.MachineTypesClient
	acceleratorTypes *compute.AcceleratorTypesClient
	regions          *compute.RegionsClient
	images           *compute.ImagesClient
}

func newGCEClients(ctx context.Context, httpClient *http.Client) (*gceClients, error) {
	opt := option.WithHTTPClient(httpClient)
	c := &gceClients{}
	var err error
	fail := func(name string) (*gceClients, error) {
		c.Close()
		return nil, fmt.Errorf("creating GCE %s client: %w", name, err)
	}
	if c.instances, err = compute.NewInstancesRESTClient(ctx, opt); err != nil {
		return fail("instances")
	}
	if c.operations, err = compute.NewZoneOperationsRESTClient(ctx, opt); err != nil {
		return fail("zone operations")
	}
	if c.zones, err = compute.NewZonesRESTClient(ctx, opt); err != nil {
		return fail("zones")
	}
	if c.machineTypes, err = compute.NewMachineTypesRESTClient(ctx, opt); err != nil {
		return fail("machine types")
	}
	if c.acceleratorTypes, err = compute.NewAcceleratorTypesRESTClient(ctx, opt); err != nil {
		return fail("accelerator types")
	}
	if c.regions, err = compute.NewRegionsRESTClient(ctx, opt); err != nil {
		return fail("regions")
	}
	if c.images, err = compute.NewImagesRESTClient(ctx, opt); err != nil {
		return fail("images")
	}
	return c, nil
}

// Close releases every constructed client.
func (c *gceClients) Close() {
	if c.instances != nil {
		_ = c.instances.Close()
	}
	if c.operations != nil {
		_ = c.operations.Close()
	}
	if c.zones != nil {
		_ = c.zones.Close()
	}
	if c.machineTypes != nil {
		_ = c.machineTypes.Close()
	}
	if c.acceleratorTypes != nil {
		_ = c.acceleratorTypes.Close()
	}
	if c.regions != nil {
		_ = c.regions.Close()
	}
	if c.images != nil {
		_ = c.images.Close()
	}
}
