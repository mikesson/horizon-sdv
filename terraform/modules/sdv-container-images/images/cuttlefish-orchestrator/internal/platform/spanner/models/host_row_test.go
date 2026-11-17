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
	"testing"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

func TestFromHostDomain_ErrorRetryableRoundTrip(t *testing.T) {
	t.Parallel()
	for _, retryable := range []bool{false, true} {
		h := &host.Host{ID: "h1", Name: "host", Zone: "europe-west4-a", ErrorRetryable: retryable}
		r := FromHostDomain(h)
		// false must round-trip as a stored false, not SQL NULL.
		if !r.ErrorRetryable.Valid {
			t.Errorf("ErrorRetryable=%v: row value is NULL, want Valid", retryable)
		}
		if r.ErrorRetryable.Bool != retryable {
			t.Errorf("ErrorRetryable=%v: row value = %v", retryable, r.ErrorRetryable.Bool)
		}
		if got := r.ToDomain().ErrorRetryable; got != retryable {
			t.Errorf("ErrorRetryable=%v: round-trip = %v", retryable, got)
		}
	}
}

// A row naming no pool belongs to the default pool, not to none: an unstamped
// host read as an empty name is a host every pool's arithmetic leaves out.
//
// The write direction is the exact complement, which is what makes the column
// stamped-once rather than rewritten: every update writes every column, so a
// default pool that stored its own name would backfill each row the column
// predates the first time a reconciler touched the host.
func TestHostRowPoolReadsUnstampedAsTheDefault(t *testing.T) {
	t.Parallel()
	if got := new(HostRow).ToDomain().Pool; got != host.PoolDefault {
		t.Errorf("unstamped pool reads as %q, want %q", got, host.PoolDefault)
	}
	stamped := &HostRow{Pool: spanner.NullString{StringVal: "gpu", Valid: true}}
	if got := stamped.ToDomain().Pool; got != "gpu" {
		t.Errorf("stored pool reads as %q, want gpu", got)
	}
	if row := FromHostDomain(new(host.Host)); row.Pool.Valid {
		t.Errorf("an unset pool writes %q, want NULL", row.Pool.StringVal)
	}
	if row := FromHostDomain(new(HostRow).ToDomain()); row.Pool.Valid {
		t.Errorf("an unstamped row rewrites its pool as %q, want the NULL it was read from", row.Pool.StringVal)
	}
	if row := FromHostDomain(&host.Host{Pool: "gpu"}); !row.Pool.Valid || row.Pool.StringVal != "gpu" {
		t.Errorf("pool writes %+v, want gpu", row.Pool)
	}
}
