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

import "time"

// Entry represents an audit log entry
type Entry struct {
	ID           string         `json:"id"`
	Timestamp    time.Time      `json:"timestamp"`
	ActorEmail   string         `json:"actor_email,omitempty"`
	Action       Action         `json:"action"`
	ResourceType ResourceType   `json:"resource_type"`
	ResourceID   *string        `json:"resource_id,omitempty"`
	RequestID    *string        `json:"request_id,omitempty"`
	Changes      map[string]any `json:"changes,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// Builder provides a fluent interface for creating audit entries
type Builder struct {
	entry Entry
}

// New creates a new audit entry builder
func New(action Action, resourceType ResourceType) *Builder {
	return &Builder{
		entry: Entry{
			Timestamp:    time.Now(),
			Action:       action,
			ResourceType: resourceType,
		},
	}
}

// WithActor sets the actor's verified email.
func (b *Builder) WithActor(email string) *Builder {
	b.entry.ActorEmail = email
	return b
}

// WithResource sets the resource ID
func (b *Builder) WithResource(id string) *Builder {
	if id != "" {
		b.entry.ResourceID = &id
	}
	return b
}

// WithMetadata sets additional metadata
func (b *Builder) WithMetadata(metadata map[string]any) *Builder {
	b.entry.Metadata = metadata
	return b
}

// Build creates the final entry
func (b *Builder) Build() Entry {
	return b.entry
}
