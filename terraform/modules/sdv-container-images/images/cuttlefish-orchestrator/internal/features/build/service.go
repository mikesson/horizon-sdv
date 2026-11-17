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
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	buildDomain "cloud-cuttlefish-orchestrator/internal/domain/build"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// attrTarget is the build-target key repeated across SQL parameters and
// audit metadata (goconst).
const attrTarget = "target"

// BuildServiceOption configures a BuildService.
type BuildServiceOption func(*BuildService)

// WithBuildAuditLogger configures an audit sink for the BuildService.
func WithBuildAuditLogger(al audit.Sink) BuildServiceOption {
	return func(s *BuildService) {
		s.auditLogger = al
	}
}

// UploadSessionStore mints resumable-upload sessions for staged build
// artifacts. Satisfied by the GCS platform client; declared here because the
// build slice is its only consumer.
type UploadSessionStore interface {
	ObjectExists(ctx context.Context, bucket, name string) (bool, error)
	StartResumableUpload(ctx context.Context, bucket, name, contentType string, sizeBytes int64) (string, error)
}

// WithUploadStaging configures artifact staging: the session store and the
// bucket staged objects land in. Without it, StartUpload reports staging
// unavailable.
func WithUploadStaging(store UploadSessionStore, bucket string) BuildServiceOption {
	return func(s *BuildService) {
		s.uploads = store
		s.stagingBucket = bucket
	}
}

// BuildService implements the business logic for the build artifact vertical slice.
//
// It manages the registry of Cuttlefish build artifacts (images): registration
// from Android Build or GCS, updates, and retrieval logic.
type BuildService struct {
	repo          buildDomain.Repository
	auditLogger   audit.Sink
	uploads       UploadSessionStore
	stagingBucket string
}

// NewBuildService creates a new build service.
func NewBuildService(
	repo buildDomain.Repository,
	opts ...BuildServiceOption,
) (*BuildService, error) {
	if repo == nil {
		return nil, errors.New("build repository is required")
	}

	s := &BuildService{
		repo: repo,
	}

	for _, opt := range opts {
		opt(s)
	}

	return s, nil
}

// Get retrieves a build by ID
func (s *BuildService) Get(ctx context.Context, id string) (*buildDomain.Build, error) {
	return s.repo.Get(ctx, id)
}

// GetByBuildID retrieves a build by Android build ID
func (s *BuildService) GetByBuildID(ctx context.Context, buildID string) (*buildDomain.Build, error) {
	return s.repo.GetByBuildID(ctx, buildID)
}

// List retrieves builds with optional filtering
func (s *BuildService) List(ctx context.Context, filter buildDomain.ListFilter) ([]*buildDomain.Build, error) {
	return s.repo.List(ctx, filter)
}

// Count returns the number of builds matching the filter, ignoring pagination.
func (s *BuildService) Count(ctx context.Context, filter buildDomain.ListFilter) (int64, error) {
	count, err := s.repo.Count(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("counting builds: %w", err)
	}
	return count, nil
}

// Register ingests a new build artifact into the system.
//
// It supports both Android Build references and URL artifact sets.
// The operation is idempotent: registering an existing BuildID returns the existing record.
func (s *BuildService) Register(ctx context.Context, req buildDomain.CreateRequest) (*buildDomain.Build, error) {
	if req.BuildID != "" {
		existing, err := s.repo.GetByBuildID(ctx, req.BuildID)
		if err == nil {
			return existing, nil
		}
		// Only a confirmed not-found may fall through to creation; treating a
		// transient lookup failure as not-found would register duplicates.
		if !errors.Is(err, buildDomain.ErrNotFound) {
			return nil, fmt.Errorf("checking for existing build %q: %w", req.BuildID, err)
		}
	}

	// URL-source builds carry no Android build ID, so their idempotency key
	// is the artifact fingerprint: caller-supplied (content digests) when
	// present, derived from the spec set otherwise.
	fingerprint := req.Fingerprint
	if req.Source == buildDomain.SourceURL {
		if fingerprint == "" {
			fingerprint = buildDomain.SpecsFingerprint(req.ArtifactSpecs)
		}
		existing, err := s.repo.GetByFingerprint(ctx, fingerprint)
		if err == nil {
			return existing, nil
		}
		// Only a confirmed not-found may fall through to creation; treating a
		// transient lookup failure as not-found would register duplicates.
		if !errors.Is(err, buildDomain.ErrNotFound) {
			return nil, fmt.Errorf("checking for existing build fingerprint %q: %w", fingerprint, err)
		}
	}

	b := &buildDomain.Build{
		ID:            uuid.New().String(),
		Name:          req.Name,
		BuildID:       req.BuildID,
		Target:        req.Target,
		Branch:        req.Branch,
		Flavor:        req.Flavor,
		Source:        req.Source,
		ArtifactSpecs: req.ArtifactSpecs,
		Fingerprint:   fingerprint,
		CreatedBy:     toPtr(req.CreatedBy),
		Description:   req.Description,
		IsDefault:     req.IsDefault,
		Metadata:      req.Metadata,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	if err := b.Validate(); err != nil {
		return nil, err
	}

	if err := s.repo.Create(ctx, b); err != nil {
		return nil, fmt.Errorf("failed to register build: %w", err)
	}

	if s.auditLogger != nil {
		// The actor comes from the request, not ctx: registration also runs
		// for background callers (batch admission) that carry no principal.
		metadata := map[string]any{"kind": "build_register", "name": b.Name, attrTarget: b.Target}
		if b.Branch != "" {
			metadata["branch"] = b.Branch
		}
		if b.IsDefault {
			metadata["is_default"] = true
		}
		s.auditLogger.LogAsync(audit.New(audit.ActionConfigChange, audit.ResourceSystem).
			WithActor(req.CreatedBy).
			WithResource(b.ID).
			WithMetadata(metadata).
			Build())
	}

	return b, nil
}

// StagedUpload describes a minted (or short-circuited) artifact upload.
type StagedUpload struct {
	ObjectURL     string
	SessionURL    string
	AlreadyExists bool
}

// StartUpload stages a build-artifact upload. Objects are content-addressed
// under staging/sha256/<hex>/<basename>, so an already-staged artifact
// short-circuits (the push client skips the transfer entirely) and re-pushes
// of identical content are idempotent. Otherwise a GCS resumable-upload
// session is minted for the client to PUT the bytes to directly. The bytes
// never transit this service, whose ingress caps request bodies well below
// image size.
func (s *BuildService) StartUpload(ctx context.Context, fileName string, sizeBytes int64, sha256Hex, contentType, actor string) (*StagedUpload, error) {
	if s.uploads == nil || s.stagingBucket == "" {
		return nil, &web.AppError{
			Type:    "service_unavailable",
			Message: "artifact staging is not configured on this deployment",
			Code:    http.StatusServiceUnavailable,
		}
	}
	if strings.ContainsAny(fileName, "/\\") {
		return nil, &buildDomain.ValidationError{Message: "file_name must be a bare filename without path separators"}
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	object := "staging/sha256/" + sha256Hex + "/" + fileName
	objectURL := "gs://" + s.stagingBucket + "/" + object

	exists, err := s.uploads.ObjectExists(ctx, s.stagingBucket, object)
	if err != nil {
		return nil, fmt.Errorf("checking staged artifact %s: %w", objectURL, err)
	}
	if exists {
		return &StagedUpload{ObjectURL: objectURL, AlreadyExists: true}, nil
	}

	session, err := s.uploads.StartResumableUpload(ctx, s.stagingBucket, object, contentType, sizeBytes)
	if err != nil {
		return nil, fmt.Errorf("starting upload session for %s: %w", objectURL, err)
	}

	if s.auditLogger != nil {
		s.auditLogger.LogAsync(audit.New(audit.ActionConfigChange, audit.ResourceSystem).
			WithActor(actor).
			WithResource(object).
			WithMetadata(map[string]any{
				"kind":       "build_upload_start",
				"file_name":  fileName,
				"size_bytes": sizeBytes,
				"object_url": objectURL,
			}).
			Build())
	}

	return &StagedUpload{ObjectURL: objectURL, SessionURL: session}, nil
}

// Update modifies an existing build's mutable fields. Fields left nil in the
// request are preserved.
func (s *BuildService) Update(ctx context.Context, id string, req buildDomain.UpdateRequest) (*buildDomain.Build, error) {
	b, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if req.Name != nil {
		b.Name = *req.Name
	}
	if req.Description != nil {
		b.Description = *req.Description
	}
	if req.IsDefault != nil {
		b.IsDefault = *req.IsDefault
	}
	if req.ArtifactSpecs != nil {
		b.ArtifactSpecs = req.ArtifactSpecs
		// The fingerprint is Register's dedup key for the spec set; carrying
		// the old one across a spec replacement would match re-registrations
		// of a set this row no longer holds.
		if b.Source == buildDomain.SourceURL {
			b.Fingerprint = buildDomain.SpecsFingerprint(req.ArtifactSpecs)
		}
	}

	if err := b.Validate(); err != nil {
		return nil, err
	}

	if err := s.repo.Update(ctx, b); err != nil {
		return nil, fmt.Errorf("failed to update build: %w", err)
	}

	audit.LogChange(ctx, s.auditLogger, audit.ActionConfigChange, audit.ResourceSystem, b.ID, nil)

	return b, nil
}

// Delete removes a build
func (s *BuildService) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

// toPtr returns a pointer to s, or nil when s is empty.
func toPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
