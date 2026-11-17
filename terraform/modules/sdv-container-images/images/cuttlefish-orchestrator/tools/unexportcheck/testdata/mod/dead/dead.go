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

// Package dead declares the shapes the checker must classify. Each exported
// name here is named for the verdict it should receive.
package dead

// DeadType is referenced by nothing.
type DeadType struct{ Field string }

// DeadConst is referenced by nothing and is not positional.
const DeadConst = "dead"

// DeadVar is referenced by nothing.
var DeadVar = 42

// LiveType is referenced from another package, so it is not dead.
type LiveType struct{ Field string }

// LiveConst is referenced from another package.
const LiveConst = "live"

// PackageLocalType is referenced only inside this package: reportable as
// over-exported, never as dead.
type PackageLocalType struct{}

func useLocal() PackageLocalType { return PackageLocalType{} }

var _ = useLocal

// Colour values are positional: deleting one renumbers those after it, so the
// checker must flag them as iota rather than as a plain delete.
type Colour int

const (
	// ColourRed is referenced from another package.
	ColourRed Colour = iota
	// ColourGreen is referenced by nothing but cannot simply be removed.
	ColourGreen
	// ColourBlue is referenced by nothing but cannot simply be removed.
	ColourBlue
)

// Weekday values are grouped but not positional, so removing one is safe and
// the checker must not label them iota.
type Weekday string

const (
	// WeekdayMonday is referenced by nothing.
	WeekdayMonday Weekday = "monday"
	// WeekdayTuesday is referenced by nothing.
	WeekdayTuesday Weekday = "tuesday"
)

// TestOnlyType is referenced only from a _test.go file. Tests are real
// references: it must not be reported as dead.
type TestOnlyType struct{}
