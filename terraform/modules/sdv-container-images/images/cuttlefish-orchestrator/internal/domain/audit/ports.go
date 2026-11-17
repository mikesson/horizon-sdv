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

package audit

import (
	"context"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/auth"
)

// Sink is the write port every audit producer consumes: fire-and-forget,
// never blocking the operation being audited. The platform audit logger
// satisfies it with bounded asynchronous batching. Entries are droppable
// under pressure, because audit is observability and no entry is guaranteed
// to survive.
type Sink interface {
	// LogAsync records an audit entry asynchronously.
	LogAsync(entry Entry)
}

// LogChange records a mutating action attributed to the request principal:
// the actor email is taken from ctx (empty for background callers with no
// principal). A nil sink disables auditing. Callers whose actor is not the
// ctx principal (system actors, an explicit createdBy carried into
// background work) build their entry directly instead.
func LogChange(ctx context.Context, sink Sink, action Action, resourceType ResourceType, resourceID string, metadata map[string]any) {
	if sink == nil {
		return
	}
	email := ""
	if principal, ok := auth.PrincipalFromContext(ctx); ok {
		email = principal.Email
	}
	sink.LogAsync(New(action, resourceType).
		WithActor(email).
		WithResource(resourceID).
		WithMetadata(metadata).
		Build())
}

// ListFilter provides filtering options for querying audit entries. It is
// shared by the storage repository's read side (Query/Count, consumed
// directly by the admin feature's AuditReader).
type ListFilter struct {
	ActorEmail   *string
	Action       *Action
	ResourceType *ResourceType
	ResourceID   *string
	// Kind matches the metadata "kind" tag writers stamp (system_config,
	// host_image_config, machine_type, zone). It is the discriminator that
	// separates settings changes from the other admin.config writes.
	Kind      *string
	StartTime *time.Time
	EndTime   *time.Time
	Limit     int
	Offset    int
}

// Repository is the persistence port the asynchronous sink drains to. Reads
// are not part of it. They go straight to the storage repository, bypassing
// the buffered write path.
type Repository interface {
	// WriteBatch stores multiple audit entries in one commit.
	WriteBatch(ctx context.Context, entries []Entry) error
}
