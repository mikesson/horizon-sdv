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

// Package models holds the Spanner row structs and the conversions between
// them and the domain entities. The `spanner:` column tags live here and
// nowhere else, so domain types carry no driver tags while repositories still
// accept and return them.
//
// Every conversion runs through the nullable.go helpers, which trade NULL for
// the Go zero value in both directions: a NULL column reads as "" or 0, and an
// empty string writes back as NULL. A field that has to tell "unset" from
// "zero" is a pointer on the domain entity.
package models
