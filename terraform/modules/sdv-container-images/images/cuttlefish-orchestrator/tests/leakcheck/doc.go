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

// Package leakcheck asserts that a subsystem leaves no goroutine behind once
// it has been stopped.
//
// It wraps the runtime's "goroutineleak" pprof profile, which exists only
// under GOEXPERIMENT=goroutineleakprofile. Everything except this file is
// behind the matching build tag, so the default build sees an empty package
// and the helpers cannot be reached from code that would not work anyway.
//
// Callers are the control plane's goroutine owners: telemetry.Runner (the
// periodic loop every reconciler delegates to), and the three services that
// spawn goroutines of their own beyond it.
package leakcheck
