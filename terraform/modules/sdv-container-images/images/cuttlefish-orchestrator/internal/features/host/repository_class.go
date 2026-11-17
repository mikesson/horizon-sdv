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

package hostfeature

import (
	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// Parameter names for the class expression.
const (
	paramClass         = "class"
	paramSystemOrigins = "system_origins"
)

// hostClassExpr derives a host's class in SQL, and is a second copy of
// host.DeriveClass on purpose, because the class has no column. It is
// derived from three columns, so filtering on it in Go would mean fetching
// every row and paging over the remainder, and the keeper's inventory count
// cannot be a Go filter at all.
//
// The arms are the Go function's arms in the same order, for the same reasons:
// batch first so an unstamped batch host stays batch settlement's, and the
// exclusive arm conjoined with a system origin so a host a USER fenced is not
// mistaken for one the orchestrator dedicated.
//
// NULL is what makes the two copies non-obvious to keep in step, so state it.
// `origin IN UNNEST(...)` evaluates to NULL, never to FALSE, when origin is
// NULL, so an unstamped row takes neither IN arm and falls to ELSE. That is the
// same reserved fail-safe the Go default arm gives, reached by a different
// route, and it is why neither IN test may be rewritten as a negation.
//
// The membership itself is not duplicated: systemOrigins is derived from
// Origin.System(), so the only thing that can drift is the shape of the arms,
// which the agreement test pins across the whole grid.
const hostClassExpr = `CASE
	        WHEN batch_id IS NOT NULL THEN 'batch'
	        WHEN exclusive AND origin IN UNNEST(@system_origins) THEN 'dedicated'
	        WHEN origin IN UNNEST(@system_origins) THEN 'fleet'
	        ELSE 'reserved'
	      END`

// systemOrigins is the origin list both IN tests above read, taken from the
// domain rather than spelled out: a new system-created origin becomes visible
// to SQL by satisfying Origin.System() and nothing else.
func systemOrigins() []string {
	out := make([]string, 0, len(host.Origins))
	for _, o := range host.Origins {
		if o.System() {
			out = append(out, string(o))
		}
	}
	return out
}

// classFilter renders the class predicate and its parameters, for a query that
// filters on one class. There is no index behind it. The expression spans
// three columns and none of the queries that use it are index-served on their
// other terms either, which is affordable because this is a single-tenant
// fleet bounded by the host quota.
func classFilter(c host.Class) (string, map[string]any) {
	return ` AND (` + hostClassExpr + `) = @` + paramClass,
		map[string]any{
			paramClass:         string(c),
			paramSystemOrigins: systemOrigins(),
		}
}
