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

package config

import "testing"

func TestResolve_ProxyEmail(t *testing.T) {
	f := &File{
		CurrentProfile: "gke",
		Profiles: map[string]Config{
			"gke": {
				Server:     "https://cco.example.com",
				AuthNone:   true,
				ProxyEmail: "profile@example.com",
			},
		},
	}

	env := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) {
			v, ok := m[k]
			return v, ok
		}
	}

	t.Run("profile proxy_email resolves with profile source", func(t *testing.T) {
		r, err := Resolve(f, Inputs{LookupEnv: env(nil)})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if r.ProxyEmail != "profile@example.com" || r.Sources.ProxyEmail != SourceProfile {
			t.Errorf("ProxyEmail=%q (%s), want profile@example.com (profile)", r.ProxyEmail, r.Sources.ProxyEmail)
		}
	})

	t.Run("env CFCTL_PROXY_EMAIL beats profile", func(t *testing.T) {
		r, err := Resolve(f, Inputs{LookupEnv: env(map[string]string{EnvProxyEmail: "argo@example.com"})})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if r.ProxyEmail != "argo@example.com" || r.Sources.ProxyEmail != SourceEnv {
			t.Errorf("ProxyEmail=%q (%s), want argo@example.com (env)", r.ProxyEmail, r.Sources.ProxyEmail)
		}
	})

	t.Run("different server drops profile proxy_email", func(t *testing.T) {
		r, err := Resolve(f, Inputs{
			ServerFlag:    "https://other.example.com",
			ServerFlagSet: true,
			LookupEnv:     env(nil),
		})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if !r.DroppedProfileAuth || r.ProxyEmail != "" {
			t.Errorf("DroppedProfileAuth=%v ProxyEmail=%q, want true and empty", r.DroppedProfileAuth, r.ProxyEmail)
		}
	})
}
