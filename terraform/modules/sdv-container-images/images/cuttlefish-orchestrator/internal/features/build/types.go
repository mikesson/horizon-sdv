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
	"fmt"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/build"
)

// ListBuildsInput is the input for listing builds.
type ListBuildsInput struct {
	Limit     int    `query:"limit" minimum:"1" maximum:"100" default:"20" doc:"Maximum number of builds to return"`
	Offset    int    `query:"offset" minimum:"0" default:"0" doc:"Number of builds to skip"`
	PageToken string `query:"page_token" doc:"Opaque token from a previous page's next_page_token; cannot be combined with a non-zero offset"`
	Target    string `query:"target" doc:"Filter by build target"`
	Branch    string `query:"branch" doc:"Filter by branch"`
	Source    string `query:"source" enum:"android_build,url" doc:"Filter by build source"`
}

// ListBuildsOutput is the output for listing builds.
type ListBuildsOutput struct {
	Body struct {
		Items  []*build.Build `json:"items" doc:"List of builds"`
		Limit  int            `json:"limit" doc:"Requested limit"`
		Offset int            `json:"offset" doc:"Requested offset"`
		Total  int            `json:"total,omitempty" doc:"Total number of items"`
		// NextPageToken continues after this page even when rows are created
		// or deleted between requests, which an offset cannot.
		NextPageToken string `json:"next_page_token" doc:"Token for the next page; empty on the last page"`
	}
}

// GetBuildInput is the input for getting a build.
type GetBuildInput struct {
	ID string `path:"id" format:"uuid" doc:"Build ID"`
}

// GetBuildOutput is the output for getting a build.
type GetBuildOutput struct {
	Body *build.Build
}

// ArtifactSpecInput represents a single artifact specification in API requests.
type ArtifactSpecInput struct {
	Type string `json:"type" required:"true" enum:"gcs,https" doc:"Artifact spec type (gcs for gs:// URLs, https for HTTPS URLs)"`
	URL  string `json:"url" required:"true" minLength:"10" maxLength:"2048" doc:"Full URL to the artifact"`
	Role string `json:"role" required:"true" enum:"host_package,default_build,kernel_build,boot_build,bootloader_build,system_image_build" doc:"Semantic role of the artifact (maps to env_config field)"`
}

// CreateBuildInput is the input for creating a build.
type CreateBuildInput struct {
	Body struct {
		Name    string `json:"name" required:"true" minLength:"1" maxLength:"255" doc:"User-friendly display name"`
		BuildID string `json:"build_id,omitempty" doc:"Android Build ID from ci.android.com"`
		Target  string `json:"target,omitempty" doc:"Build target (e.g., aosp_cf_x86_64_phone-userdebug). Required for android_build source."`
		Branch  string `json:"branch,omitempty" doc:"Branch name (e.g., aosp-android-latest-release)"`
		Flavor  string `json:"flavor,omitempty" doc:"Build flavor (e.g., userdebug, user)"`
		Source  string `json:"source" required:"true" enum:"android_build,url" doc:"Build source (android_build for CI, url for GCS/HTTPS)"`
		// ArtifactSpecs for 'url' source type. Each specifies type (gcs/https) and URL.
		ArtifactSpecs []ArtifactSpecInput `json:"artifact_specs,omitempty" doc:"Artifact specifications for URL-based builds. Required when source is 'url'."`
		Fingerprint   string              `json:"fingerprint,omitempty" doc:"Build fingerprint"`
		Description   string              `json:"description,omitempty" maxLength:"1000" doc:"Build description"`
		IsDefault     bool                `json:"is_default,omitempty" doc:"Mark as default build for target"`
		Metadata      map[string]any      `json:"metadata,omitempty" doc:"Additional build metadata"`
	}
}

// artifactSpecErrors validates each spec's URL prefix against its type. This is
// the cross-field rule JSON Schema cannot express, shared by the create and
// update resolvers so they cannot drift.
func artifactSpecErrors(specs []ArtifactSpecInput) []error {
	var errs []error
	for idx, spec := range specs {
		if build.ArtifactSpecType(spec.Type) == build.ArtifactSpecTypeGCS && !strings.HasPrefix(spec.URL, "gs://") {
			errs = append(errs, &huma.ErrorDetail{
				Location: fmt.Sprintf("body.artifact_specs[%d].url", idx),
				Message:  "GCS artifact URL must start with gs://",
			})
		}
		if build.ArtifactSpecType(spec.Type) == build.ArtifactSpecTypeHTTPS && !strings.HasPrefix(spec.URL, "https://") {
			errs = append(errs, &huma.ErrorDetail{
				Location: fmt.Sprintf("body.artifact_specs[%d].url", idx),
				Message:  "HTTPS artifact URL must start with https://",
			})
		}
	}
	return errs
}

// Resolve validates the cross-field source/artifact rules JSON Schema cannot
// express, mirroring build.Build.Validate so they fail as 422s at the API edge
// instead of surfacing as 500s from the domain.
func (i *CreateBuildInput) Resolve(_ huma.Context) []error {
	var errs []error
	switch build.Source(i.Body.Source) {
	case build.SourceAB:
		if i.Body.BuildID == "" && i.Body.Branch == "" {
			errs = append(errs, &huma.ErrorDetail{
				Location: "body.build_id",
				Message:  "either build_id or branch is required for android_build source",
			})
		}
		if i.Body.Target == "" {
			errs = append(errs, &huma.ErrorDetail{
				Location: "body.target",
				Message:  "target is required for android_build source",
			})
		}
	case build.SourceURL:
		if len(i.Body.ArtifactSpecs) == 0 {
			errs = append(errs, &huma.ErrorDetail{
				Location: "body.artifact_specs",
				Message:  "at least one artifact spec is required for url source",
			})
		}
		errs = append(errs, artifactSpecErrors(i.Body.ArtifactSpecs)...)
	}
	return errs
}

// CreateBuildOutput is the output for creating a build.
type CreateBuildOutput struct {
	Body *build.Build
}

// UpdateBuildInput is the input for updating a build's mutable fields. Omitted
// fields are left unchanged.
type UpdateBuildInput struct {
	ID   string `path:"id" format:"uuid" doc:"Build ID"`
	Body struct {
		Name          *string             `json:"name,omitempty" minLength:"1" maxLength:"255" doc:"User-friendly display name"`
		Description   *string             `json:"description,omitempty" maxLength:"1000" doc:"Build description"`
		IsDefault     *bool               `json:"is_default,omitempty" doc:"Mark as default build for target"`
		ArtifactSpecs []ArtifactSpecInput `json:"artifact_specs,omitempty" doc:"Artifact specifications (replaces existing when provided)"`
	}
}

// Resolve validates replacement artifact specs the way CreateBuildInput does,
// so a bad PUT fails as a 422 at the API edge instead of a 500 when the
// service re-runs build.Build.Validate. An explicitly empty array is rejected
// outright: replacing a URL build's specs with nothing is invalid, and
// clearing an android_build build's specs is meaningless.
func (i *UpdateBuildInput) Resolve(_ huma.Context) []error {
	if i.Body.ArtifactSpecs == nil {
		return nil
	}
	var errs []error
	if len(i.Body.ArtifactSpecs) == 0 {
		errs = append(errs, &huma.ErrorDetail{
			Location: "body.artifact_specs",
			Message:  "at least one artifact spec is required when replacing artifact specs",
		})
	}
	return append(errs, artifactSpecErrors(i.Body.ArtifactSpecs)...)
}

// UpdateBuildOutput is the output for updating a build.
type UpdateBuildOutput struct {
	Body *build.Build
}

// DeleteBuildInput is the input for deleting a build.
type DeleteBuildInput struct {
	ID string `path:"id" format:"uuid" doc:"Build ID"`
}

// DeleteBuildOutput is the output for deleting a build.
type DeleteBuildOutput struct{}

// StartBuildUploadInput requests a staged-artifact upload session.
type StartBuildUploadInput struct {
	Body struct {
		FileName    string `json:"file_name" minLength:"1" maxLength:"255" doc:"Artifact basename (no path separators), e.g. aosp_cf_x86_64_phone-img-1234.zip"`
		SizeBytes   int64  `json:"size_bytes" minimum:"1" doc:"Exact artifact size in bytes"`
		SHA256      string `json:"sha256" pattern:"^[a-f0-9]{64}$" doc:"Lowercase hex SHA-256 of the artifact content"`
		ContentType string `json:"content_type,omitempty" doc:"MIME type; defaults to application/octet-stream"`
	}
}

// StartBuildUploadOutput carries the staged object location and, unless the
// object is already present, the resumable session URL to PUT the bytes to.
type StartBuildUploadOutput struct {
	Body struct {
		ObjectURL     string `json:"object_url" doc:"gs:// URL of the staged artifact"`
		SessionURL    string `json:"session_url,omitempty" doc:"GCS resumable-upload session URL; absent when the object already exists"`
		AlreadyExists bool   `json:"already_exists" doc:"Whether the content-addressed object is already staged"`
	}
}
