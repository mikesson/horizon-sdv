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
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/filelock"
)

// profiles is a file with an IAP prod profile, current, and a network-mode
// staging profile.
func profiles() *config.File {
	return &config.File{
		CurrentProfile: "prod",
		Profiles: map[string]config.Config{
			"prod":    {Server: "https://prod.example.com", ImpersonateServiceAccount: "prod@sa", IAPAudience: "X"},
			"staging": {Server: "https://staging.example.com", AuthNone: true},
		},
	}
}

// parseWith parses args through the production parser over file.
func parseWith(t *testing.T, file *config.File, args ...string) (*CLI, error) {
	t.Helper()
	var c CLI
	parser, err := NewParser(&c, file, "test")
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	parser.Exit = func(int) {}
	_, err = parser.Parse(args)
	return &c, err
}

// runOnDisk runs one command the way main does: the config file is loaded
// from CFCTL_CONFIG_DIR first, so each call sees what the last one wrote.
func runOnDisk(t *testing.T, args ...string) (string, error) {
	t.Helper()
	file, loadErr := config.Load()
	var c CLI
	c.ConfigErr = loadErr
	parser, err := NewParser(&c, file, "test")
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	parser.Exit = func(int) { t.Fatal("command tried to exit") }
	kctx, err := parser.Parse(args)
	if err != nil {
		return "", err
	}
	kctx.BindTo(t.Context(), (*context.Context)(nil))
	return captureStdout(t, func() error { return kctx.Run() })
}

// --profile is parsed before the config is resolved, so it selects the
// profile. With the config resolved before parse, as main once did, the
// current profile would win.
func TestProfileFlagSelectsTheProfile(t *testing.T) {
	c, err := parseWith(t, profiles(), "--profile", "staging", "host", "list")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.Server != "https://staging.example.com" || !c.resolved.AuthNone || c.profile != "staging" {
		t.Errorf("server %q, auth none %v, profile %q; want staging's", c.Server, c.resolved.AuthNone, c.profile)
	}
	if c.resolved.ProfileSource != config.SourceFlag {
		t.Errorf("profile source = %s, want flag", c.resolved.ProfileSource)
	}
}

func TestProfileEnvSelectsTheProfile(t *testing.T) {
	t.Setenv(config.EnvProfile, "staging")
	c, err := parseWith(t, profiles(), "host", "list")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.profile != "staging" || c.resolved.ProfileSource != config.SourceEnv {
		t.Errorf("profile %q (%s), want staging from env", c.profile, c.resolved.ProfileSource)
	}
}

// The two cases the old loader got wrong, through the real parse path.
func TestProfileEnvOverridesThroughTheParser(t *testing.T) {
	t.Run("CFCTL_AUTH_NONE=false clears the profile's auth_none", func(t *testing.T) {
		t.Setenv(config.EnvAuthNone, "false")
		c, err := parseWith(t, profiles(), "--profile", "staging", "auth", "status")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if c.resolved.AuthNone {
			t.Error("AuthNone = true, want false")
		}
	})
	t.Run("CFCTL_SERVER elsewhere drops the profile audience", func(t *testing.T) {
		t.Setenv(config.EnvServer, "https://other.example.com")
		c, err := parseWith(t, profiles(), "host", "list")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if c.resolved.IAPAudience != "" || c.resolved.ImpersonateServiceAccount != "" {
			t.Errorf("audience %q, impersonate %q; want both empty", c.resolved.IAPAudience, c.resolved.ImpersonateServiceAccount)
		}
		if c.Server != "https://other.example.com" {
			t.Errorf("server = %q, want the env value", c.Server)
		}
	})
	t.Run("invalid CFCTL_AUTH_NONE fails the command", func(t *testing.T) {
		t.Setenv(config.EnvAuthNone, "maybe")
		if _, err := parseWith(t, profiles(), "host", "list"); err == nil || !strings.Contains(err.Error(), config.EnvAuthNone) {
			t.Errorf("err = %v, want the invalid value named", err)
		}
	})
}

func TestUnknownProfileFailsTargetCommandsOnly(t *testing.T) {
	if _, err := parseWith(t, profiles(), "--profile", "qa", "host", "list"); err == nil || !strings.Contains(err.Error(), `"qa"`) {
		t.Errorf("host list err = %v, want the unknown profile named", err)
	}
	for _, args := range [][]string{
		{"--profile", "qa", "config", "profiles"},
		{"--profile", "qa", "config", "create", "qa", "--server", "https://qa.example.com"},
		{"--profile", "qa", "setup"},
	} {
		if _, err := parseWith(t, profiles(), args...); err != nil {
			t.Errorf("%v: %v, want profile management to work while the profile is missing", args, err)
		}
	}
}

// create, use, rename and delete round-trip through the file, and set-*
// changes only the selected profile.
func TestProfileCommandsOnDisk(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.DirEnv, dir)
	keyPath := filepath.Join(dir, "key.json")
	if err := os.WriteFile(keyPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	steps := [][]string{
		{"--server", "https://prod.example.com", "config", "create", "prod", "--impersonate", "prod@sa"},
		{"--server", "http://staging.internal", "config", "create", "staging", "--auth", "none"},
		{"--server", "https://ci.example.com", "config", "create", "ci", "--credentials", keyPath, "--audience", "aud"},
		{"--profile", "staging", "config", "set-audience", "ignored-later"},
		{"config", "use", "staging"},
		{"config", "rename", "staging", "stage"},
		{"config", "delete", "ci"},
	}
	for _, args := range steps {
		if _, err := runOnDisk(t, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}

	checkProfilesAfterSteps(t)
	checkProfilesJSON(t)
}

func checkProfilesAfterSteps(t *testing.T) {
	t.Helper()
	f, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := f.Names(); !slices.Equal(got, []string{"prod", "stage"}) {
		t.Errorf("profiles = %v, want prod and stage", got)
	}
	if f.CurrentProfile != "stage" {
		t.Errorf("current_profile = %q, want the renamed current profile", f.CurrentProfile)
	}
	if p := f.Profiles["prod"]; p.ImpersonateServiceAccount != "prod@sa" || p.IAPAudience != "" {
		t.Errorf("prod = %+v, want it untouched by set-audience on staging", p)
	}
	if p := f.Profiles["stage"]; !p.AuthNone || p.IAPAudience != "ignored-later" || p.Server != "http://staging.internal" {
		t.Errorf("stage = %+v", p)
	}
}

func checkProfilesJSON(t *testing.T) {
	t.Helper()
	out, err := runOnDisk(t, "--json", "config", "profiles")
	if err != nil {
		t.Fatalf("config profiles: %v", err)
	}
	var rows []configProfileRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("config profiles --json: %v\n%s", err, out)
	}
	if len(rows) != 2 || rows[1].Name != "stage" || !rows[1].Current || rows[0].Current {
		t.Errorf("rows = %+v, want stage marked current", rows)
	}
}

// config create stores only what the command line says. CFCTL_SERVER is an
// ambient value and must never reach the file.
func TestConfigCreateNeedsTheServerFlag(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	t.Setenv(config.EnvServer, "https://ambient.example.com")
	if _, err := runOnDisk(t, "config", "create", "prod"); err == nil || !strings.Contains(err.Error(), "--server is required") {
		t.Fatalf("err = %v, want --server required", err)
	}
	if _, err := runOnDisk(t, "--server", "https://prod.example.com", "config", "create", "prod",
		"--impersonate", "a@sa", "--credentials", "/nope"); err == nil {
		t.Error("--impersonate with --credentials must be refused")
	}
	if _, err := runOnDisk(t, "--server", "https://p.example.com", "config", "create", "dup"); err != nil {
		t.Fatalf("create dup: %v", err)
	}
	if _, err := runOnDisk(t, "--server", "https://p.example.com", "config", "create", "dup"); err == nil {
		t.Error("creating an existing profile must be refused")
	}
}

// On a fresh install set-* writes a default profile and selects it, so the
// first-run path of set-server then set-impersonate works.
func TestSetCommandsWithoutAProfileWriteTheDefault(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	for _, args := range [][]string{
		{"config", "set-server", "https://prod.example.com"},
		{"config", "set-impersonate", "cli@sa"},
	} {
		if _, err := runOnDisk(t, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	f, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f.CurrentProfile != defaultProfileName {
		t.Errorf("current_profile = %q, want %q", f.CurrentProfile, defaultProfileName)
	}
	if p := f.Profiles[defaultProfileName]; p.Server != "https://prod.example.com" || p.ImpersonateServiceAccount != "cli@sa" {
		t.Errorf("default = %+v, want both set-* values", p)
	}
}

// A named profile that does not exist is never created by set-*.
func TestSetCommandsOnAnUnknownProfileFail(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	if _, err := runOnDisk(t, "--profile", "qa", "config", "set-audience", "x"); err == nil || !strings.Contains(err.Error(), `"qa"`) {
		t.Errorf("err = %v, want the unknown profile named", err)
	}
}

// config use, delete and rename take the name as an argument, so the error
// must not claim it came from --profile.
func TestProfileArgumentErrorsDoNotBlameTheFlag(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	for _, args := range [][]string{
		{"config", "use", "nope"},
		{"config", "delete", "nope"},
		{"config", "rename", "nope", "other"},
	} {
		_, err := runOnDisk(t, args...)
		if err == nil || !strings.Contains(err.Error(), `"nope"`) || strings.Contains(err.Error(), "--profile") {
			t.Errorf("%v: err = %v, want the name without --profile", args, err)
		}
	}
}

// The create line in the refusal must be one config create accepts. An old
// file could hold both identities, which config create refuses together.
func TestLegacyCreateLineParses(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.json")
	if err := os.WriteFile(keyPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	line := config.CreateCommand("default", config.Config{
		Server: "https://cco.example.com", ImpersonateServiceAccount: "cli@sa", CredentialsPath: keyPath, IAPAudience: "aud",
	})
	args := strings.Fields(line)
	if len(args) < 3 || args[0] != "cfctl" {
		t.Fatalf("line = %q", line)
	}
	// Kong reads global flags anywhere on the line.
	if _, err := parseWith(t, nil, args[1:]...); err != nil {
		t.Errorf("%q does not parse: %v", line, err)
	}
	if !strings.Contains(line, "--impersonate cli@sa") || strings.Contains(line, "--credentials") {
		t.Errorf("line = %q, want impersonation kept and the key file dropped", line)
	}
}

// config show names the source of every value.
func TestConfigShowReportsSources(t *testing.T) {
	t.Setenv(config.EnvImpersonate, "env@sa")
	var c CLI
	parser, err := NewParser(&c, profiles(), "test")
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	kctx, err := parser.Parse([]string{"--json", "--server", "https://prod.example.com/", "config", "show"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := captureStdout(t, func() error { return kctx.Run() })
	if err != nil {
		t.Fatalf("config show: %v", err)
	}
	var got configShowResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if got.Profile.Value != "prod" || got.Profile.Source != config.SourceProfile {
		t.Errorf("profile = %+v", got.Profile)
	}
	if got.Server.Source != config.SourceFlag || got.ImpersonateServiceAccount.Source != config.SourceEnv ||
		got.IAPAudience.Source != config.SourceProfile || got.CredentialsPath.Source != config.SourceUnset {
		t.Errorf("sources = server %s, impersonate %s, audience %s, credentials %s",
			got.Server.Source, got.ImpersonateServiceAccount.Source, got.IAPAudience.Source, got.CredentialsPath.Source)
	}
	if got.DroppedProfileAuth {
		t.Error("a trailing slash on the profile's own server dropped its auth")
	}
}

// The agent gets the profile on its command line, so a later 'config use'
// cannot change which credentials a running tunnel uses.
func TestAgentArgsCarryTheProfile(t *testing.T) {
	t.Parallel()
	args := agentArgs(connectRequest{server: testServer, profile: "staging", deviceID: "d1", instance: "1"})
	i := slices.Index(args, "--profile")
	if i < 0 || i+1 >= len(args) || args[i+1] != "staging" {
		t.Errorf("agent args %v lack --profile staging", args)
	}
	if slices.Index(args, "device") < i {
		t.Errorf("--profile must precede the subcommand: %v", args)
	}
	if slices.Contains(agentArgs(connectRequest{server: testServer, deviceID: "d1", instance: "1"}), "--profile") {
		t.Error("an unselected profile must not become --profile \"\"")
	}
}

// An old-format file fails target commands with the create line, but lets
// config create through so the user can follow it.
func TestLegacyConfigRefusesTargetCommands(t *testing.T) {
	legacy := &config.LegacyError{
		Path: filepath.Join(t.TempDir(), "config.yaml"),
		Old:  config.Config{Server: "https://cco.example.com", ImpersonateServiceAccount: "cli@sa"},
	}
	var c CLI
	c.ConfigErr = legacy
	parser, err := NewParser(&c, nil, "test")
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	parser.Exit = func(int) {}
	_, err = parser.Parse([]string{"host", "list"})
	if err == nil || !strings.Contains(err.Error(), "cfctl config create default --server https://cco.example.com --impersonate cli@sa") {
		t.Errorf("host list err = %v, want the create line", err)
	}

	var c2 CLI
	c2.ConfigErr = legacy
	parser, err = NewParser(&c2, nil, "test")
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	if _, err := parser.Parse([]string{"--server", "https://cco.example.com", "config", "create", "default"}); err != nil {
		t.Errorf("config create must run under a legacy file: %v", err)
	}
}

// device connections reads the config directory's agents. With the old file
// elsewhere it would list nothing, so it gets the refusal; pointed at the old
// directory it runs and sees the old agents.
func TestLegacyConfigAgentCommandsNeedTheOldDir(t *testing.T) {
	old := t.TempDir()
	legacy := &config.LegacyError{Path: filepath.Join(old, "config.yaml"), Old: config.Config{Server: testServer}}
	parse := func() error {
		var c CLI
		c.ConfigErr = legacy
		parser, err := NewParser(&c, nil, "test")
		if err != nil {
			t.Fatalf("NewParser: %v", err)
		}
		parser.Exit = func(int) {}
		_, err = parser.Parse([]string{"device", "connections"})
		return err
	}

	t.Setenv(config.DirEnv, t.TempDir())
	if err := parse(); err == nil || !strings.Contains(err.Error(), "old single-target format") {
		t.Errorf("err = %v, want the legacy refusal", err)
	}
	t.Setenv(config.DirEnv, old+string(filepath.Separator))
	if err := parse(); err != nil {
		t.Errorf("with %s at the old directory: %v", config.DirEnv, err)
	}
}

// The refusal lists agents the old binary started from the old directory,
// with the command that stops them, since the new binary looks elsewhere.
func TestLegacyConfigErrorListsOldAgents(t *testing.T) {
	old := t.TempDir()
	store, err := openConnectionStore(filepath.Join(old, "connections"))
	if err != nil {
		t.Fatal(err)
	}
	st := connectionState{
		PID: 4242, DeviceID: "dev-1", Instance: "1", Address: "127.0.0.1:40000",
		Server: testServer, StartedAt: time.Now().UTC(),
	}
	p := store.paths(st.DeviceID, st.Instance)
	lock, err := filelock.TryLock(p.lock)
	if err != nil {
		t.Skipf("no file locks here: %v", err)
	}
	t.Cleanup(func() { filelock.Release(lock, p.lock) })
	if err := store.write(st); err != nil {
		t.Fatal(err)
	}

	err = legacyConfigError(&config.LegacyError{Path: filepath.Join(old, "config.yaml"), Old: config.Config{Server: testServer}})
	msg := err.Error()
	for _, want := range []string{"pid 4242", "device dev-1", config.DirEnv + "=" + old + " cfctl device disconnect --all"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal lacks %q:\n%s", want, msg)
		}
	}
}
