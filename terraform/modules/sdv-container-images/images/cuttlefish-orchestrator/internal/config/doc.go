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

// Package config provides configuration management for the orchestrator.
//
// Configuration is env-only, and there is no config file. Load layers the
// CCO_* environment variables (the envBindings table in load.go, one row
// per deployment setting) over the code defaults in DefaultConfig, reads
// secrets from their mounted Secret Manager volumes, then validates.
// Operational tuning (timeouts, job intervals, pool sizes) stays in the
// code defaults; Terraform sets the env in production and a dotenv
// (.env.example) supplies it for local dev.
package config
