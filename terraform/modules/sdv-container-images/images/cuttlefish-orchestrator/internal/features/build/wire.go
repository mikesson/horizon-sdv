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
	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

// Deps carries the build slice's optional collaborators; nil fields disable
// the corresponding capability.
type Deps struct {
	AuditLogger audit.Sink
	// Uploads + StagingBucket enable staged-artifact upload sessions
	// (StartUpload); leaving them unset makes the endpoint report staging
	// unavailable.
	Uploads       UploadSessionStore
	StagingBucket string
}

// Slice is the build feature's wired product set: the HTTP handler plus the
// two cross-feature products the device slice consumes: the repository (its
// domain build.Reader) and the service (its tracking-build registrar).
type Slice struct {
	Handler    *BuildHandler
	Service    *BuildService
	Repository *BuildRepository
}

// Wire creates the build feature: repository, service, handler.
func Wire(client *spannerplatform.Client, deps Deps) (Slice, error) {
	repo := NewBuildRepository(client)
	opts := []BuildServiceOption{WithBuildAuditLogger(deps.AuditLogger)}
	if deps.Uploads != nil {
		opts = append(opts, WithUploadStaging(deps.Uploads, deps.StagingBucket))
	}
	svc, err := NewBuildService(repo, opts...)
	if err != nil {
		return Slice{}, err
	}
	return Slice{Handler: NewBuildHandler(svc), Service: svc, Repository: repo}, nil
}
