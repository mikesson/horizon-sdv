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
	"strings"
	"time"

	"cloud.google.com/go/spanner"
)

// SystemConfigRow represents the Spanner table schema for system
// configuration, which stores platform-wide key/value settings such as the
// default host image project.
type SystemConfigRow struct {
	ConfigKey   string             `spanner:"config_key"`
	ConfigValue string             `spanner:"config_value"`
	Description spanner.NullString `spanner:"description"`
	CreatedAt   time.Time          `spanner:"created_at"`
	UpdatedAt   time.Time          `spanner:"updated_at"`
}

// SystemConfig represents a parsed system configuration entry.
type SystemConfig struct {
	Key         string
	Value       string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ToDomain converts a SystemConfigRow to a SystemConfig.
func (r *SystemConfigRow) ToDomain() *SystemConfig {
	c := &SystemConfig{
		Key:       r.ConfigKey,
		Value:     r.ConfigValue,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}
	if r.Description.Valid {
		c.Description = r.Description.StringVal
	}
	return c
}

// AsString returns the config value as a string.
func (c *SystemConfig) AsString() string {
	return c.Value
}

// AsBool reports whether the config value is truthy ("true"/"1",
// case-insensitive). Any other value, the empty string included, is false,
// so an unseeded or malformed flag reads as off.
func (c *SystemConfig) AsBool() bool {
	switch strings.ToLower(strings.TrimSpace(c.Value)) {
	case "true", "1":
		return true
	default:
		return false
	}
}
