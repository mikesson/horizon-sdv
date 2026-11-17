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

package buildfeature

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/spanner"
	"github.com/google/uuid"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"

	"cloud-cuttlefish-orchestrator/internal/domain/build"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// buildColumnsSQL is the explicit projection for build queries; SELECT *
// breaks on additive migrations because ToStruct rejects unknown columns.
var buildColumnsSQL = strings.Join(buildColumns(), ", ")

// BuildRepository implements build.Repository using Cloud Spanner
type BuildRepository struct {
	client *spannerplatform.Client
}

// NewBuildRepository creates a new build repository
func NewBuildRepository(client *spannerplatform.Client) *BuildRepository {
	return &BuildRepository{client: client}
}

// Get retrieves a build by ID
func (r *BuildRepository) Get(ctx context.Context, id string) (*build.Build, error) {
	row, err := r.client.Single().ReadRow(ctx, "builds", spanner.Key{id}, buildColumns())
	if err != nil {
		if spanner.ErrCode(err) == codes.NotFound {
			return nil, &build.NotFoundError{ID: id}
		}
		return nil, fmt.Errorf("failed to get build: %w", err)
	}

	var bRow models.BuildRow
	if err := row.ToStruct(&bRow); err != nil {
		return nil, fmt.Errorf("failed to scan build row: %w", err)
	}

	return bRow.ToDomain(), nil
}

// GetByBuildID retrieves a build by Android build ID
func (r *BuildRepository) GetByBuildID(ctx context.Context, buildID string) (*build.Build, error) {
	stmt := spanner.Statement{
		SQL: `SELECT ` + buildColumnsSQL + ` FROM builds WHERE android_build_id = @build_id`,
		Params: map[string]any{
			"build_id": buildID,
		},
	}

	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return nil, &build.NotFoundError{ID: buildID}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get build by build ID: %w", err)
	}

	var bRow models.BuildRow
	if err := row.ToStruct(&bRow); err != nil {
		return nil, fmt.Errorf("failed to scan build row: %w", err)
	}

	return bRow.ToDomain(), nil
}

// GetByFingerprint retrieves a build by its artifact fingerprint.
func (r *BuildRepository) GetByFingerprint(ctx context.Context, fingerprint string) (*build.Build, error) {
	stmt := spanner.Statement{
		SQL: `SELECT ` + buildColumnsSQL + ` FROM builds WHERE fingerprint = @fingerprint`,
		Params: map[string]any{
			"fingerprint": fingerprint,
		},
	}

	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return nil, &build.NotFoundError{ID: fingerprint}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get build by fingerprint: %w", err)
	}

	var bRow models.BuildRow
	if err := row.ToStruct(&bRow); err != nil {
		return nil, fmt.Errorf("failed to scan build row: %w", err)
	}

	return bRow.ToDomain(), nil
}

// buildBuildFilter renders filter as a WHERE clause and its parameters.
// List and Count share it so paginated totals always agree with the returned
// rows.
func buildBuildFilter(filter build.ListFilter) (whereClause string, params map[string]any) {
	whereClause = ` WHERE 1=1`
	params = make(map[string]any)

	if filter.Target != nil {
		whereClause += ` AND target = @target`
		params[attrTarget] = *filter.Target
	}

	if filter.Branch != nil {
		whereClause += ` AND branch = @branch`
		params["branch"] = *filter.Branch
	}

	if filter.Source != nil {
		whereClause += ` AND source = @source`
		params["source"] = string(*filter.Source)
	}

	if filter.IsDefault != nil {
		whereClause += ` AND is_default = @is_default`
		params["is_default"] = *filter.IsDefault
	}

	return whereClause, params
}

// List retrieves builds matching the filter
func (r *BuildRepository) List(ctx context.Context, filter build.ListFilter) ([]*build.Build, error) {
	where, params := buildBuildFilter(filter)
	if filter.AfterID != "" {
		// Spanner has no row-value comparison, so the keyset predicate is
		// spelled out over the two sort columns.
		where += ` AND (created_at < @after_created_at OR (created_at = @after_created_at AND build_id < @after_id))`
		params["after_created_at"] = filter.AfterCreatedAt
		params["after_id"] = filter.AfterID
	}
	// build_id breaks the tie, since created_at alone is not a total order.
	sql := `SELECT ` + buildColumnsSQL + ` FROM builds` + where +
		` ORDER BY created_at DESC, build_id DESC`

	if filter.Limit > 0 {
		sql += fmt.Sprintf(` LIMIT %d`, filter.Limit)
	}
	if filter.Offset > 0 {
		sql += fmt.Sprintf(` OFFSET %d`, filter.Offset)
	}

	stmt := spanner.Statement{SQL: sql, Params: params}
	return spannerplatform.QueryAll(r.client.Single().Query(ctx, stmt), "builds", (*models.BuildRow).ToDomain)
}

// Count returns the number of builds matching the filter, ignoring
// pagination. It applies the same predicates as List.
func (r *BuildRepository) Count(ctx context.Context, filter build.ListFilter) (int64, error) {
	where, params := buildBuildFilter(filter)
	stmt := spanner.Statement{SQL: `SELECT COUNT(*) FROM builds` + where, Params: params}
	return spannerplatform.QueryCount(r.client.Single().Query(ctx, stmt), "builds")
}

// Create stores a new build
func (r *BuildRepository) Create(ctx context.Context, b *build.Build) error {
	if b.ID == "" {
		b.ID = uuid.New().String()
	}
	now := time.Now()
	b.CreatedAt = now
	b.UpdatedAt = now

	if err := b.Validate(); err != nil {
		return fmt.Errorf("invalid build: %w", err)
	}

	bRow := models.FromBuildDomain(b)
	mutation, err := spanner.InsertStruct("builds", bRow)
	if err != nil {
		return fmt.Errorf("failed to create insert mutation: %w", err)
	}

	_, err = r.client.Apply(ctx, []*spanner.Mutation{mutation})
	if err != nil {
		return fmt.Errorf("failed to create build: %w", err)
	}

	return nil
}

// Update modifies an existing build
func (r *BuildRepository) Update(ctx context.Context, b *build.Build) error {
	b.UpdatedAt = time.Now()

	if err := b.Validate(); err != nil {
		return fmt.Errorf("invalid build: %w", err)
	}

	bRow := models.FromBuildDomain(b)
	mutation, err := spanner.UpdateStruct("builds", bRow)
	if err != nil {
		return fmt.Errorf("failed to create update mutation: %w", err)
	}

	_, err = r.client.Apply(ctx, []*spanner.Mutation{mutation})
	if err != nil {
		return fmt.Errorf("failed to update build: %w", err)
	}

	return nil
}

// Delete removes a build
func (r *BuildRepository) Delete(ctx context.Context, id string) error {
	mutation := spanner.Delete("builds", spanner.Key{id})

	_, err := r.client.Apply(ctx, []*spanner.Mutation{mutation})
	if err != nil {
		return fmt.Errorf("failed to delete build: %w", err)
	}

	return nil
}

func buildColumns() []string {
	return []string{
		"build_id", "name", "android_build_id", "target", "branch",
		"flavor", "source", "artifact_specs",
		"fingerprint", "created_by", "description", "is_default", "metadata",
		"created_at", "updated_at",
	}
}
