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

// Package spannertest provides test utilities for integration tests that
// use the Cloud Spanner emulator.
//
// # Prerequisites
//
// Start the Spanner emulator and point the environment at it:
//
//	gcloud emulators spanner start
//	export SPANNER_EMULATOR_HOST=localhost:9010
//
// Without SPANNER_EMULATOR_HOST the emulator-backed tests SKIP, unless
// CCO_REQUIRE_EMULATOR is set (as CI does), in which case they FAIL: the
// emulator suite is the only executed verification of the persistence layer's
// invariants, and a silent skip would report green while verifying nothing.
//
// # Usage
//
// Each test creates its own throwaway database with the full production
// schema (parsed from migrations/spanner/complete_schema.sql, so the test
// schema can never drift from the deployed one):
//
//	func TestDeviceRepository(t *testing.T) {
//	    db := spannertest.MustNewTestDB(t) // skips/fails without emulator; cleans up itself
//	    repo := devicefeature.NewDeviceRepository(db.Client)
//
//	    // Run your tests...
//	}
//
// Databases are named uniquely, so parallel tests and packages never collide.
package spannertest
