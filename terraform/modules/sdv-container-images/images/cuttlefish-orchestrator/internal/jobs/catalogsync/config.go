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

package catalogsync

import (
	"time"
)

// Config holds configuration for the catalog sync service.
type Config struct {
	// ProjectID is the GCP project ID.
	ProjectID string

	// SpotAdviceSyncInterval is how often to probe Spot capacity advice.
	// Consulted only when the spot-advice dependencies are wired
	// (WithSpotAdvice); 0 disables the loop.
	SpotAdviceSyncInterval time.Duration
}

// Validate checks if the configuration is valid.
func (c *Config) Validate() error {
	if c.ProjectID == "" {
		return &ConfigError{Field: "ProjectID", Message: "required"}
	}
	if c.SpotAdviceSyncInterval <= 0 {
		return &ConfigError{Field: "SpotAdviceSyncInterval", Message: "must be positive"}
	}
	return nil
}

// ConfigError represents a configuration validation error.
type ConfigError struct {
	Field   string
	Message string
}

// Error implements the error interface.
func (e *ConfigError) Error() string {
	return "catalogsync config: " + e.Field + " " + e.Message
}
