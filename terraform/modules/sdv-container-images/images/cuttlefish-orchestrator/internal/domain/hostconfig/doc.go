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

// Package hostconfig defines the domain model for host image configuration.
//
// This package provides the entity and repository interface for managing
// GCE image family mappings per machine family. It enables dynamic
// configuration of which GCE image family to use for each machine type
// prefix (e.g., "n1", "g2", "c4a").
//
// # Key Types
//
//   - HostImageConfig: Entity representing the mapping between machine family
//     and GCE image family.
//   - Repository: Interface for persistence operations on host image configs.
//
// # Design Decisions
//
// The database is the single source of truth for image family mappings.
// There is no fallback to static configuration. If a machine family is
// not found in the database, an error is returned.
package hostconfig
