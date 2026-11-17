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

package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/auth/authtest"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
)

// runSetup drives the wizard with scripted stdin under a temporary config
// directory, with no profile selected.
func runSetup(t *testing.T, c *SetupCmd, input string) (string, error) {
	t.Helper()
	t.Setenv(config.DirEnv, t.TempDir())
	c.in = strings.NewReader(input)
	return captureStdout(t, func() error { return c.Run(t.Context(), &CLI{}) })
}

// savedProfile reloads the config file and returns the named profile.
func savedProfile(t *testing.T, name string) (config.Config, *config.File) {
	t.Helper()
	f, err := config.Load()
	if err != nil {
		t.Fatalf("reloading saved config: %v", err)
	}
	p, ok := f.Profiles[name]
	if !ok {
		t.Fatalf("profile %q not saved; have %v", name, f.Names())
	}
	return p, f
}

// The key-file branch must produce a config that authenticates end to end with
// a self-signed JWT from the key and no Google round trip.
func TestSetupWizardKeyFileFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.URL.Path != "/v2/me" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"email":"cli-sa@p.iam.gserviceaccount.com"}`)
	}))
	t.Cleanup(srv.Close)
	keyPath, _ := authtest.WriteKeyFile(t)

	// Server URL, audience (Enter), method 2, key path.
	input := srv.URL + "\n\n2\n" + keyPath + "\n"
	out, err := runSetup(t, &SetupCmd{}, input)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if !strings.Contains(out, "Authentication successful!") {
		t.Errorf("wizard did not verify the saved config:\n%s", out)
	}

	p, f := savedProfile(t, defaultProfileName)
	if p.Server != srv.URL || p.CredentialsPath == "" || p.ImpersonateServiceAccount != "" {
		t.Errorf("saved profile = %+v, want server + key file with impersonation cleared", p)
	}
	if f.CurrentProfile != defaultProfileName {
		t.Errorf("current_profile = %q, want the first profile made current", f.CurrentProfile)
	}
}

// The no-argument wizard defaults to impersonation. ADC is pinned to a test key
// so the wizard skips the gcloud offer. The connection test may fail, but setup
// must still complete and save the identity.
func TestSetupWizardImpersonationDefaultFlow(t *testing.T) {
	keyPath, _ := authtest.WriteKeyFile(t)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", keyPath)

	const sa = "cco-cli@p.iam.gserviceaccount.com"
	// Server URL, audience (Enter), method (Enter for impersonation), SA.
	input := "https://cco.example.run.app\n\n\n" + sa + "\n"
	if _, err := runSetup(t, &SetupCmd{}, input); err != nil {
		t.Fatalf("setup: %v", err)
	}

	p, _ := savedProfile(t, defaultProfileName)
	if p.ImpersonateServiceAccount != sa || p.CredentialsPath != "" {
		t.Errorf("saved profile = %+v, want impersonation of %s with the key path cleared", p, sa)
	}
	if p.Server != "https://cco.example.run.app" {
		t.Errorf("saved server = %q", p.Server)
	}
}

// A key-file argument skips the method question.
func TestSetupWizardKeyFileArgumentSkipsMethodQuestion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"email":"ci@p.iam.gserviceaccount.com"}`)
	}))
	t.Cleanup(srv.Close)
	keyPath, _ := authtest.WriteKeyFile(t)

	// Only the server URL and audience prompts remain. A method prompt would
	// find the reader empty and fail the run.
	out, err := runSetup(t, &SetupCmd{KeyFile: keyPath}, srv.URL+"\n\n")
	if err != nil {
		t.Fatalf("setup with a key-file argument: %v", err)
	}
	if !strings.Contains(out, "Authentication successful!") {
		t.Errorf("key-file argument flow did not verify:\n%s", out)
	}
}

// A bad SA email is prompted for again and never saved.
func TestSetupWizardRejectsNonEmailServiceAccount(t *testing.T) {
	keyPath, _ := authtest.WriteKeyFile(t)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", keyPath)

	input := "https://cco.example.run.app\n\n1\nnot-an-email\ncco-cli@p.iam.gserviceaccount.com\n"
	if _, err := runSetup(t, &SetupCmd{}, input); err != nil {
		t.Fatalf("setup: %v", err)
	}
	p, _ := savedProfile(t, defaultProfileName)
	if p.ImpersonateServiceAccount != "cco-cli@p.iam.gserviceaccount.com" {
		t.Errorf("saved SA = %q, want the re-prompted valid email", p.ImpersonateServiceAccount)
	}
}

// Setup configures an identity, so it must clear auth none. Otherwise the
// chosen identity is never used and every request goes out without a
// credential.
func TestSetupWizardClearsAuthNone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		fmt.Fprint(w, `{"email":"ci@p.iam.gserviceaccount.com"}`)
	}))
	t.Cleanup(srv.Close)
	keyPath, _ := authtest.WriteKeyFile(t)

	t.Setenv(config.DirEnv, t.TempDir())
	if err := config.Update(func(f *config.File) error {
		f.Set(defaultProfileName, config.Config{Server: srv.URL, AuthNone: true})
		return nil
	}); err != nil {
		t.Fatalf("seeding auth none: %v", err)
	}
	c := &SetupCmd{KeyFile: keyPath, in: strings.NewReader(srv.URL + "\n\n")}
	out, err := captureStdout(t, func() error { return c.Run(t.Context(), &CLI{}) })
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if p, _ := savedProfile(t, defaultProfileName); p.AuthNone {
		t.Error("setup left auth_none set")
	}
	if !strings.Contains(out, "Authentication successful!") {
		t.Errorf("setup did not authenticate with the chosen key:\n%s", out)
	}
}

// setup --profile writes the named profile, creating it, and leaves an
// existing current_profile where it was.
func TestSetupWizardWritesTheSelectedProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"email":"ci@p.iam.gserviceaccount.com"}`)
	}))
	t.Cleanup(srv.Close)
	keyPath, _ := authtest.WriteKeyFile(t)

	t.Setenv(config.DirEnv, t.TempDir())
	prod := config.Config{Server: "https://prod.example.com", ImpersonateServiceAccount: "prod@sa"}
	if err := config.Update(func(f *config.File) error {
		f.Set("prod", prod)
		f.CurrentProfile = "prod"
		return nil
	}); err != nil {
		t.Fatalf("seeding prod: %v", err)
	}
	c := &SetupCmd{KeyFile: keyPath, in: strings.NewReader(srv.URL + "\n\n")}
	if _, err := captureStdout(t, func() error { return c.Run(t.Context(), &CLI{profile: "staging"}) }); err != nil {
		t.Fatalf("setup --profile staging: %v", err)
	}
	staging, f := savedProfile(t, "staging")
	if staging.Server != srv.URL || staging.CredentialsPath == "" {
		t.Errorf("staging = %+v, want the wizard's answers", staging)
	}
	if f.CurrentProfile != "prod" || f.Profiles["prod"] != prod {
		t.Errorf("setup --profile staging moved or changed prod: %+v", f)
	}
}

// Setup always saves a credential, so it refuses plain http to a remote host.
func TestSetupWizardRejectsRemoteHTTP(t *testing.T) {
	keyPath, _ := authtest.WriteKeyFile(t)
	_, err := runSetup(t, &SetupCmd{KeyFile: keyPath}, "http://cco.example.com\n\n")
	if err == nil || !strings.Contains(err.Error(), "uses http") {
		t.Fatalf("err = %v, want the plain-http refusal", err)
	}
}
