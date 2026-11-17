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

package auth

import (
	"testing"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
)

func TestNewHTTPClient_ProxyEmailRejectsAllASCIIControlChars(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		email string
	}{
		{name: "tab", email: "user\t@example.com"},
		{name: "vertical_tab", email: "user\v@example.com"},
		{name: "escape", email: "user\x1b@example.com"},
		{name: "del", email: "user\x7f@example.com"},
		{name: "cr", email: "user\r@example.com"},
		{name: "lf", email: "user\n@example.com"},
		{name: "nul", email: "user\x00@example.com"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{
				AuthNone:   true,
				ProxyEmail: tc.email,
			}
			if _, err := NewHTTPClient(t.Context(), cfg, "https://orchestrator.example"); err == nil {
				t.Fatalf("NewHTTPClient(%q) succeeded, want control-character error", tc.email)
			}
		})
	}
}
