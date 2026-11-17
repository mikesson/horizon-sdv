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

// Package cloudmonitoring reads the project's own quota telemetry out of Cloud
// Monitoring.
//
// It exists for the quota axes Compute Engine's region list structurally
// cannot report. That list carries a fixed flat array of metric names, so a
// pool selected by a dimension (CPU quota per VM family, which is what every
// machine family newer than c3 draws from) has no name in it, and the
// hyperdisk capacity pools are absent from it entirely.
//
// Everything here degrades to silence. A failed read, an unreadable point and
// a series that does not exist yet all produce no row, which placement
// judges as an unknown axis and passes advisorily. That direction is
// chosen because a series only starts existing once the project uses the
// family, so "no series" is the normal first answer for every family a fleet
// has not run yet, and reading it as a granted zero would make every zone
// offering those families unplaceable.
package cloudmonitoring
