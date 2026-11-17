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

package models

import (
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/build"
)

// BuildRow represents the Spanner table schema for builds.
type BuildRow struct {
	BuildID        string             `spanner:"build_id"`
	Name           string             `spanner:"name"`
	AndroidBuildID spanner.NullString `spanner:"android_build_id"`
	Target         string             `spanner:"target"`
	Branch         spanner.NullString `spanner:"branch"`
	Flavor         spanner.NullString `spanner:"flavor"`
	Source         string             `spanner:"source"`
	ArtifactSpecs  spanner.NullJSON   `spanner:"artifact_specs"`
	Fingerprint    spanner.NullString `spanner:"fingerprint"`
	CreatedBy      spanner.NullString `spanner:"created_by"`
	Description    spanner.NullString `spanner:"description"`
	IsDefault      bool               `spanner:"is_default"`
	Metadata       spanner.NullJSON   `spanner:"metadata"`
	CreatedAt      time.Time          `spanner:"created_at"`
	UpdatedAt      time.Time          `spanner:"updated_at"`
}

// ToDomain reads a build row. The artifact_specs column is hand-decoded rather
// than unmarshalled, because the driver hands JSON back as []any of
// map[string]any. The keys FromBuildDomain writes are read back by name here,
// so the two directions have to move together. An element of any other shape
// is skipped rather than failing the read.
func (r *BuildRow) ToDomain() *build.Build {
	b := &build.Build{
		ID:        r.BuildID,
		Name:      r.Name,
		Target:    r.Target,
		Source:    build.Source(r.Source),
		IsDefault: r.IsDefault,
		BuildID:   FromNullString(r.AndroidBuildID),
		Branch:    FromNullString(r.Branch),
		Flavor:    FromNullString(r.Flavor),
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}
	if r.ArtifactSpecs.Valid {
		// ArtifactSpecs is stored as JSON array
		if specs, ok := r.ArtifactSpecs.Value.([]any); ok {
			for _, spec := range specs {
				if specMap, ok := spec.(map[string]any); ok {
					specType, _ := specMap["type"].(string)
					specURL, _ := specMap["url"].(string)
					specRole, _ := specMap["role"].(string)
					b.ArtifactSpecs = append(b.ArtifactSpecs, build.ArtifactSpec{
						Type: build.ArtifactSpecType(specType),
						URL:  specURL,
						Role: build.ArtifactRole(specRole),
					})
				}
			}
		}
	}
	b.Fingerprint = FromNullString(r.Fingerprint)
	b.CreatedBy = FromNullStringPtr(r.CreatedBy)
	b.Description = FromNullString(r.Description)
	b.Metadata = FromNullJSONMap(r.Metadata)
	return b
}

// FromBuildDomain writes the build row and owns the artifact_specs wire shape:
// a JSON array of objects keyed "type", "url" and "role", which ToDomain reads
// back by name. A build with no specs leaves the column NULL rather than
// storing an empty array.
func FromBuildDomain(b *build.Build) *BuildRow {
	r := &BuildRow{
		BuildID:   b.ID,
		Name:      b.Name,
		Target:    b.Target,
		Source:    string(b.Source),
		IsDefault: b.IsDefault,
		CreatedAt: b.CreatedAt,
		UpdatedAt: b.UpdatedAt,
	}
	r.AndroidBuildID = ToNullString(b.BuildID)
	r.Branch = ToNullString(b.Branch)
	r.Flavor = ToNullString(b.Flavor)
	// Convert ArtifactSpecs to JSON-compatible format
	if len(b.ArtifactSpecs) > 0 {
		specs := make([]map[string]string, len(b.ArtifactSpecs))
		for i, spec := range b.ArtifactSpecs {
			specs[i] = map[string]string{
				"type": string(spec.Type),
				"url":  spec.URL,
				"role": string(spec.Role),
			}
		}
		r.ArtifactSpecs = spanner.NullJSON{Value: specs, Valid: true}
	}
	r.Fingerprint = ToNullString(b.Fingerprint)
	r.CreatedBy = ToNullStringPtr(b.CreatedBy)
	r.Description = ToNullString(b.Description)
	if b.Metadata != nil {
		r.Metadata = spanner.NullJSON{Value: b.Metadata, Valid: true}
	}
	return r
}
