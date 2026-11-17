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

package spanner

import (
	"errors"
	"fmt"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"
)

// QueryAll drains iter, scanning each row into a fresh R and converting it
// with conv (typically a row model's ToDomain method expression). It stops the
// iterator and wraps errors with desc. Preferred over spanner.SelectAll here
// because every caller converts row models to domain values, which SelectAll
// would leave as a second loop.
func QueryAll[R, T any](iter *spanner.RowIterator, desc string, conv func(*R) T) ([]T, error) {
	defer iter.Stop()

	var results []T
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return results, nil
		}
		if err != nil {
			return nil, fmt.Errorf("iterating %s: %w", desc, err)
		}

		var r R
		if err := row.ToStruct(&r); err != nil {
			return nil, fmt.Errorf("scanning %s: %w", desc, err)
		}
		results = append(results, conv(&r))
	}
}

// QueryRow drains a single-row query, scanning it into a fresh R and converting
// it with conv. The twin of QueryCount for an aggregate that produces more than
// one value, where two QueryCount calls would read the values at two instants.
//
// A missing row is an error and never a zero value. The callers are aggregates
// with no GROUP BY, which always produce exactly one row, so an empty result
// means the read failed. It cannot mean the table was empty.
func QueryRow[R, T any](iter *spanner.RowIterator, desc string, conv func(*R) T) (T, error) {
	defer iter.Stop()

	var zero T
	row, err := iter.Next()
	if err != nil {
		return zero, fmt.Errorf("reading %s: %w", desc, err)
	}
	var r R
	if err := row.ToStruct(&r); err != nil {
		return zero, fmt.Errorf("scanning %s: %w", desc, err)
	}
	return conv(&r), nil
}

// QueryCount drains a single-row aggregate query (SELECT COUNT(*) ...) and
// returns its value, stopping the iterator and wrapping errors with desc.
func QueryCount(iter *spanner.RowIterator, desc string) (int64, error) {
	defer iter.Stop()

	row, err := iter.Next()
	if err != nil {
		return 0, fmt.Errorf("counting %s: %w", desc, err)
	}
	var count int64
	if err := row.Columns(&count); err != nil {
		return 0, fmt.Errorf("scanning %s count: %w", desc, err)
	}
	return count, nil
}
