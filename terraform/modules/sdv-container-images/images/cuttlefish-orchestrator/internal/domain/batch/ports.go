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

package batch

import "context"

// ListFilter provides filtering options for listing batches. The table is
// bounded (rows are swept when emptied and TTL'd), so there is no pagination.
type ListFilter struct {
	// CreatedBy filters batches to one creator's; nil means all.
	CreatedBy *string
}

// Repository persists the batch policy rows (the registry). The settle
// markers (MarkReady/MarkFailed) are not part of this port. Only the
// batchsettle job writes them, through its own consumer-side Store. The
// batch feature's repository satisfies both.
type Repository interface {
	// Create inserts the batch policy row.
	Create(ctx context.Context, b *Batch) error

	// Get returns the batch policy row; ErrNotFound when absent.
	Get(ctx context.Context, id string) (*Batch, error)

	// List returns batch rows matching the filter, newest first.
	List(ctx context.Context, filter ListFilter) ([]*Batch, error)

	// Delete removes the batch policy row (idempotent).
	Delete(ctx context.Context, id string) error
}
