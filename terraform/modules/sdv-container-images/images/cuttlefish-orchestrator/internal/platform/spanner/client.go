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

package spanner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"cloud.google.com/go/spanner"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

// Config holds Spanner client configuration
type Config struct {
	// ProjectID is the GCP project ID
	ProjectID string

	// InstanceID is the Spanner instance ID
	InstanceID string

	// DatabaseID is the Spanner database ID
	DatabaseID string

	// NumChannels is the gRPC connection pool size (each channel serves up to
	// 100 concurrent requests). 0 selects the client default of 4.
	NumChannels int

	// ListMaxStaleness bounds how stale a list/dashboard read may be. Bounded-
	// staleness reads can be served by non-leader replicas, offloading the leader
	// under heavy read traffic. 0 uses strong reads.
	ListMaxStaleness time.Duration

	// Emulator is the address of the Spanner emulator (for testing)
	Emulator string
}

// DefaultConfig returns production-ready default configuration.
//
// Since spanner v1.92 the client uses multiplexed sessions exclusively. The
// old session pool (and its Min/Max/HealthCheck tuning) no longer exists, so
// the only capacity setting left is the gRPC channel count.
func DefaultConfig() Config {
	return Config{
		ListMaxStaleness: 10 * time.Second,
	}
}

// Client is the platform adapter for Google Cloud Spanner.
//
// It encapsulates infrastructure concerns like connection pooling, session management,
// and OpenTelemetry instrumentation, exposing a clean interface to the domain repositories.
type Client struct {
	client   *spanner.Client
	config   Config
	database string

	mu     sync.RWMutex
	closed bool
}

// NewClient initializes the Spanner platform adapter with production-ready defaults.
//
// It configures the underlying gRPC connection pool with keepalives and
// OpenTelemetry instrumentation; sessions are multiplexed by the client.
func NewClient(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.ProjectID == "" {
		return nil, errors.New("project ID is required")
	}
	if cfg.InstanceID == "" {
		return nil, errors.New("instance ID is required")
	}
	if cfg.DatabaseID == "" {
		return nil, errors.New("database ID is required")
	}

	// Build database path
	database := fmt.Sprintf("projects/%s/instances/%s/databases/%s",
		cfg.ProjectID, cfg.InstanceID, cfg.DatabaseID)

	// gRPC connection pool (each channel serves up to 100 concurrent
	// requests). Sessions need no tuning: the client multiplexes them.
	numChannels := cfg.NumChannels
	if numChannels <= 0 {
		numChannels = 4
	}

	// Build client options
	var opts []option.ClientOption

	// Configure gRPC keepalive for long-lived connections
	keepaliveOpts := option.WithGRPCDialOption(
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	// Enable OpenTelemetry tracing
	opts = append(opts,
		keepaliveOpts,
		option.WithGRPCConnectionPool(numChannels),
		option.WithGRPCDialOption(grpc.WithStatsHandler(otelgrpc.NewClientHandler())),
	)

	// Use emulator if configured
	if cfg.Emulator != "" {
		opts = append(opts,
			option.WithEndpoint(cfg.Emulator),
			option.WithGRPCDialOption(grpc.WithInsecure()), //nolint:staticcheck // Required for emulator
			option.WithoutAuthentication(),
		)
	}

	// Create the Spanner client
	client, err := spanner.NewClient(ctx, database, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create Spanner client: %w", err)
	}

	return &Client{
		client:   client,
		config:   cfg,
		database: database,
	}, nil
}

// Single returns a read-only transaction for single-use queries
func (c *Client) Single() *spanner.ReadOnlyTransaction {
	return c.client.Single()
}

// StaleSingle returns a single-use read with bounded staleness, for
// list/dashboard reads that tolerate slightly stale data and so can be served by
// non-leader replicas. It falls back to a strong read when ListMaxStaleness is 0.
func (c *Client) StaleSingle() *spanner.ReadOnlyTransaction {
	if c.config.ListMaxStaleness <= 0 {
		return c.client.Single()
	}
	return c.client.Single().WithTimestampBound(spanner.MaxStaleness(c.config.ListMaxStaleness))
}

// ReadWriteTransaction executes a read-write transaction
func (c *Client) ReadWriteTransaction(ctx context.Context, f func(context.Context, *spanner.ReadWriteTransaction) error) (time.Time, error) {
	return c.client.ReadWriteTransaction(ctx, f)
}

// Apply applies mutations atomically
func (c *Client) Apply(ctx context.Context, mutations []*spanner.Mutation) (time.Time, error) {
	return c.client.Apply(ctx, mutations)
}

// Database returns the database path
func (c *Client) Database() string {
	return c.database
}

// Config returns the client configuration
func (c *Client) Config() Config {
	return c.config
}

// HealthCheck verifies the connection is working
func (c *Client) HealthCheck(ctx context.Context) error {
	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()
		return errors.New("client is closed")
	}
	c.mu.RUnlock()

	// Execute a simple query to verify connectivity
	iter := c.client.Single().Query(ctx, spanner.Statement{
		SQL: "SELECT 1",
	})
	defer iter.Stop()

	_, err := iter.Next()
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}

	return nil
}

// Close gracefully shuts down the client
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()

	c.client.Close()
}
