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

import (
	"errors"
	"strings"
	"testing"
)

// env builds a LookupEnv over a fixed map, so the tests run in parallel.
func env(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := vars[k]
		return v, ok
	}
}

func twoProfiles() *File {
	return &File{
		CurrentProfile: "prod",
		Profiles: map[string]Config{
			"prod":    {Server: "https://prod.example.com", ImpersonateServiceAccount: "prod@sa", IAPAudience: "X"},
			"staging": {Server: "https://staging.example.com", AuthNone: true},
		},
	}
}

// A set CFCTL_AUTH_NONE=false turns off a profile's auth_none. The old loader
// could only switch auth-none on, so this came out true.
func TestResolve_AuthNoneEnvFalseBeatsProfile(t *testing.T) {
	t.Parallel()
	r, err := Resolve(twoProfiles(), Inputs{ProfileFlag: "staging", LookupEnv: env(map[string]string{EnvAuthNone: "false"})})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.AuthNone {
		t.Error("AuthNone = true, want CFCTL_AUTH_NONE=false to clear the profile's auth_none")
	}
	if r.Sources.AuthNone != SourceEnv {
		t.Errorf("AuthNone source = %s, want env", r.Sources.AuthNone)
	}
}

// A server from CFCTL_SERVER that is not the profile's takes none of the
// profile's auth. The old loader kept the audience X attached to any server.
func TestResolve_OtherServerDropsProfileAuth(t *testing.T) {
	t.Parallel()
	r, err := Resolve(twoProfiles(), Inputs{LookupEnv: env(map[string]string{EnvServer: "https://other.example.com"})})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.IAPAudience != "" || r.ImpersonateServiceAccount != "" {
		t.Errorf("audience = %q, impersonate = %q; want both empty for a foreign server", r.IAPAudience, r.ImpersonateServiceAccount)
	}
	if !r.DroppedProfileAuth {
		t.Error("DroppedProfileAuth = false, want the drop reported")
	}
	if r.Server != "https://other.example.com" || r.Sources.Server != SourceEnv {
		t.Errorf("server = %q (%s), want the env value", r.Server, r.Sources.Server)
	}
}

// The comparison ignores a trailing slash and case in scheme and host, so a
// cosmetic difference never silently drops the profile's auth.
func TestResolve_SameServerKeepsProfileAuth(t *testing.T) {
	t.Parallel()
	for _, server := range []string{"https://prod.example.com/", "HTTPS://Prod.Example.com"} {
		r, err := Resolve(twoProfiles(), Inputs{ServerFlag: server, ServerFlagSet: true, LookupEnv: env(nil)})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if r.IAPAudience != "X" || r.DroppedProfileAuth {
			t.Errorf("--server %s: audience = %q, dropped = %v; want the profile's auth kept", server, r.IAPAudience, r.DroppedProfileAuth)
		}
		if r.Sources.Server != SourceFlag {
			t.Errorf("--server %s: source = %s, want flag", server, r.Sources.Server)
		}
	}
}

// Env auth still applies on a foreign server, which makes CFCTL_SERVER plus
// CFCTL_IMPERSONATE a file-free CI mode.
func TestResolve_EnvAuthWithForeignServer(t *testing.T) {
	t.Parallel()
	r, err := Resolve(twoProfiles(), Inputs{LookupEnv: env(map[string]string{
		EnvServer: "https://ci.example.com", EnvImpersonate: "ci@sa", EnvCredentials: "/k.json",
	})})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.ImpersonateServiceAccount != "ci@sa" || r.CredentialsPath != "/k.json" || r.IAPAudience != "" {
		t.Errorf("got %+v, want only the env auth", r.Config)
	}
}

func TestResolve_ProfileSelection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		flag       string
		vars       map[string]string
		want       string
		wantSource Source
	}{
		{"current_profile by default", "", nil, "prod", SourceProfile},
		{"env beats current_profile", "", map[string]string{EnvProfile: "staging"}, "staging", SourceEnv},
		{"flag beats env", "prod", map[string]string{EnvProfile: "staging"}, "prod", SourceFlag},
		{"empty env means unset", "", map[string]string{EnvProfile: ""}, "prod", SourceProfile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r, err := Resolve(twoProfiles(), Inputs{ProfileFlag: tt.flag, LookupEnv: env(tt.vars)})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if r.Profile != tt.want || r.ProfileSource != tt.wantSource {
				t.Errorf("profile = %q (%s), want %q (%s)", r.Profile, r.ProfileSource, tt.want, tt.wantSource)
			}
			if r.Server != twoProfiles().Profiles[tt.want].Server {
				t.Errorf("server = %q, want profile %s's", r.Server, tt.want)
			}
		})
	}
}

func TestResolve_UnknownProfileIsNamed(t *testing.T) {
	t.Parallel()
	r, err := Resolve(twoProfiles(), Inputs{LookupEnv: env(map[string]string{EnvProfile: "qa"})})
	unknown, ok := errors.AsType[*UnknownProfileError](err)
	if !ok {
		t.Fatalf("err = %v, want *UnknownProfileError", err)
	}
	if !strings.Contains(unknown.Error(), `"qa"`) || !strings.Contains(unknown.Error(), EnvProfile) ||
		!strings.Contains(unknown.Error(), "prod, staging") {
		t.Errorf("error does not name the profile, its source and the known ones: %v", unknown)
	}
	if r == nil || r.Profile != "qa" {
		t.Errorf("Resolved = %+v, want the selected name kept for setup", r)
	}
}

// A set-and-empty variable clears the profile's value.
func TestResolve_SetAndEmptyClears(t *testing.T) {
	t.Parallel()
	r, err := Resolve(twoProfiles(), Inputs{LookupEnv: env(map[string]string{EnvAudience: "", EnvImpersonate: ""})})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.IAPAudience != "" || r.ImpersonateServiceAccount != "" {
		t.Errorf("got %+v, want the empty env values to clear the profile's", r.Config)
	}
	if r.Sources.IAPAudience != SourceEnv {
		t.Errorf("audience source = %s, want env", r.Sources.IAPAudience)
	}
}

func TestResolve_InvalidAuthNoneIsAnError(t *testing.T) {
	t.Parallel()
	_, err := Resolve(twoProfiles(), Inputs{LookupEnv: env(map[string]string{EnvAuthNone: "yes please"})})
	if err == nil || !strings.Contains(err.Error(), EnvAuthNone) {
		t.Errorf("err = %v, want an error naming %s", err, EnvAuthNone)
	}
}

func TestResolve_NoFileNoEnv(t *testing.T) {
	t.Parallel()
	r, err := Resolve(nil, Inputs{LookupEnv: env(nil)})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Profile != "" || r.Server != "" || r.Sources.Server != SourceUnset {
		t.Errorf("got %+v, want nothing selected and nothing set", r)
	}
}
