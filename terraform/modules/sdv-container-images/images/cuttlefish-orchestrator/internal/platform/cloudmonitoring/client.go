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

package cloudmonitoring

import (
	"context"
	"errors"
	"fmt"

	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"google.golang.org/api/iterator"
)

// TimeSeries is the Cloud Monitoring read surface a quota reading is built
// from. It takes the request message whole so the filter and the interval are
// part of the contract a fake can assert on, and it returns drained pages
// because the generated iterator cannot be constructed outside its own package.
//
// There is no Cloud Monitoring emulator, so this interface is the only boundary
// at which the reader can be exercised at all.
type TimeSeries interface {
	List(ctx context.Context, req *monitoringpb.ListTimeSeriesRequest) ([]*monitoringpb.TimeSeries, error)
}

// metricClient adapts the generated Monitoring client to TimeSeries.
type metricClient struct{ metrics *monitoring.MetricClient }

// NewTimeSeries wraps a Monitoring metric client as the reader's read surface.
func NewTimeSeries(metrics *monitoring.MetricClient) TimeSeries {
	return &metricClient{metrics: metrics}
}

// List drains one time-series query.
func (c *metricClient) List(ctx context.Context, req *monitoringpb.ListTimeSeriesRequest) ([]*monitoringpb.TimeSeries, error) {
	var out []*monitoringpb.TimeSeries
	it := c.metrics.ListTimeSeries(ctx, req)
	for {
		series, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("listing time series %q: %w", req.GetFilter(), err)
		}
		out = append(out, series)
	}
}
