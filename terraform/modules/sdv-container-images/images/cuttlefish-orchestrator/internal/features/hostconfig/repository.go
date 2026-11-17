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

package hostconfigfeature

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/grpc/codes"

	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// tableHostImageConfigs is the table name repeated across the reads and
// writes (goconst).
const tableHostImageConfigs = "host_image_configs"

// Repository implements hostconfig.Repository using Cloud Spanner.
type Repository struct {
	client *spannerplatform.Client
}

// NewRepository creates a new host image config repository.
func NewRepository(client *spannerplatform.Client) *Repository {
	return &Repository{client: client}
}

// Get retrieves a host image config by machine family.
func (r *Repository) Get(ctx context.Context, machineFamily string) (*hostconfig.HostImageConfig, error) {
	row, err := r.client.Single().ReadRow(
		ctx,
		tableHostImageConfigs,
		spanner.Key{machineFamily},
		models.HostImageConfigColumns(),
	)
	if err != nil {
		if spanner.ErrCode(err) == codes.NotFound {
			return nil, &hostconfig.NotFoundError{MachineFamily: machineFamily}
		}
		return nil, fmt.Errorf("getting host image config: %w", err)
	}

	var configRow models.HostImageConfigRow
	if err := row.ToStruct(&configRow); err != nil {
		return nil, fmt.Errorf("scanning host image config row: %w", err)
	}

	return configRow.ToDomain(), nil
}

// buildHostImageConfigFilter renders filter as a WHERE clause and its
// parameters. List and Count share it so paginated totals always agree with
// the returned rows.
func buildHostImageConfigFilter(filter hostconfig.ListFilter) (whereClause string, params map[string]any) {
	whereClause = ` WHERE 1=1`
	params = make(map[string]any)

	if filter.EnabledOnly {
		whereClause += ` AND enabled = true`
	}

	return whereClause, params
}

// List retrieves all host image configs with optional filtering.
func (r *Repository) List(ctx context.Context, filter hostconfig.ListFilter) ([]*hostconfig.HostImageConfig, error) {
	where, params := buildHostImageConfigFilter(filter)
	sql := `SELECT ` + strings.Join(models.HostImageConfigColumns(), ", ") + ` FROM host_image_configs` + where

	sql += ` ORDER BY machine_family ASC`

	if filter.Limit > 0 {
		sql += fmt.Sprintf(` LIMIT %d`, filter.Limit)
	}
	if filter.Offset > 0 {
		sql += fmt.Sprintf(` OFFSET %d`, filter.Offset)
	}

	stmt := spanner.Statement{SQL: sql, Params: params}
	return spannerplatform.QueryAll(r.client.Single().Query(ctx, stmt), "host image configs", (*models.HostImageConfigRow).ToDomain)
}

// Create creates a new host image config.
func (r *Repository) Create(ctx context.Context, config *hostconfig.HostImageConfig) error {
	now := time.Now()
	config.CreatedAt = now
	config.UpdatedAt = now

	configRow := models.FromHostImageConfigDomain(config)
	mutation, err := spanner.InsertStruct(tableHostImageConfigs, configRow)
	if err != nil {
		return fmt.Errorf("creating insert mutation: %w", err)
	}

	_, err = r.client.Apply(ctx, []*spanner.Mutation{mutation})
	if err != nil {
		if spanner.ErrCode(err) == codes.AlreadyExists {
			return fmt.Errorf("host image config already exists for machine family: %s", config.MachineFamily)
		}
		return fmt.Errorf("creating host image config: %w", err)
	}

	return nil
}

// Update updates an existing host image config.
func (r *Repository) Update(ctx context.Context, config *hostconfig.HostImageConfig) error {
	config.UpdatedAt = time.Now()

	configRow := models.FromHostImageConfigDomain(config)
	mutation, err := spanner.UpdateStruct(tableHostImageConfigs, configRow)
	if err != nil {
		return fmt.Errorf("creating update mutation: %w", err)
	}

	_, err = r.client.Apply(ctx, []*spanner.Mutation{mutation})
	if err != nil {
		if spanner.ErrCode(err) == codes.NotFound {
			return &hostconfig.NotFoundError{MachineFamily: config.MachineFamily}
		}
		return fmt.Errorf("updating host image config: %w", err)
	}

	return nil
}

// Delete removes a host image config by machine family.
func (r *Repository) Delete(ctx context.Context, machineFamily string) error {
	mutation := spanner.Delete(tableHostImageConfigs, spanner.Key{machineFamily})

	_, err := r.client.Apply(ctx, []*spanner.Mutation{mutation})
	if err != nil {
		return fmt.Errorf("deleting host image config: %w", err)
	}

	return nil
}

// Count returns the total number of configs matching the filter.
func (r *Repository) Count(ctx context.Context, filter hostconfig.ListFilter) (int64, error) {
	where, params := buildHostImageConfigFilter(filter)
	stmt := spanner.Statement{SQL: `SELECT COUNT(*) FROM host_image_configs` + where, Params: params}
	return spannerplatform.QueryCount(r.client.Single().Query(ctx, stmt), "host image configs")
}

// Ensure Repository implements hostconfig.Repository.
var _ hostconfig.Repository = (*Repository)(nil)
