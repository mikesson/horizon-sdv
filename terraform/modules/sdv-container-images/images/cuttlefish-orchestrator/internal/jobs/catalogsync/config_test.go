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
	"errors"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		ProjectID:              "test-project",
		SpotAdviceSyncInterval: 15 * time.Minute,
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mutate    func(*Config)
		wantField string
	}{
		{"valid", func(c *Config) {}, ""},
		{"missing project", func(c *Config) { c.ProjectID = "" }, "ProjectID"},
		// A zero interval would panic time.NewTicker in the probe loop.
		{"zero probe interval", func(c *Config) { c.SpotAdviceSyncInterval = 0 }, "SpotAdviceSyncInterval"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if tt.wantField == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			var cfgErr *ConfigError
			if !errors.As(err, &cfgErr) || cfgErr.Field != tt.wantField {
				t.Errorf("Validate() = %v, want ConfigError for field %s", err, tt.wantField)
			}
		})
	}
}
