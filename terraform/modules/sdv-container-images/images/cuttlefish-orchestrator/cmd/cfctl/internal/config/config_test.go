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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// isolate points every store at a fresh directory and HOME at another, so
// neither the developer's config nor a legacy ~/.cfctl leaks in.
func isolate(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	t.Setenv(DirEnv, dir)
	return dir
}

func addProfile(name string, p Config) func(*File) error {
	return func(f *File) error {
		f.Set(name, p)
		if f.CurrentProfile == "" {
			f.CurrentProfile = name
		}
		return nil
	}
}

func TestDirHonoursConfigDirAndTreatsEmptyAsUnset(t *testing.T) {
	t.Setenv(DirEnv, "/tmp/cfctl-elsewhere")
	if got, _ := Dir(); got != "/tmp/cfctl-elsewhere" {
		t.Errorf("Dir() = %q, want the %s value", got, DirEnv)
	}
	t.Setenv(DirEnv, "")
	base, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("no user config dir here: %v", err)
	}
	if got, _ := Dir(); got != filepath.Join(base, "cfctl") {
		t.Errorf("Dir() with empty %s = %q, want %q", DirEnv, got, filepath.Join(base, "cfctl"))
	}
}

func TestLoad_NonExistentYieldsEmptyFile(t *testing.T) {
	isolate(t)
	f, err := Load()
	if err != nil {
		t.Fatalf("Load should not return error for missing config: %v", err)
	}
	if f.CurrentProfile != "" || len(f.Profiles) != 0 {
		t.Errorf("expected an empty file, got %+v", f)
	}
}

func TestUpdateAndLoadRoundTrip(t *testing.T) {
	dir := isolate(t)
	prod := Config{
		Server:                    "https://prod.example.com",
		IAPAudience:               "123.apps.googleusercontent.com",
		ImpersonateServiceAccount: "sa@project.iam.gserviceaccount.com",
	}
	if err := Update(addProfile("prod", prod)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// A second update must keep what it does not touch.
	if err := Update(addProfile("staging", Config{Server: "https://staging.example.com", AuthNone: true})); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// The config can hold credential paths; it must not be world-readable.
	info, err := os.Stat(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("config file not created: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("permissions = %o, want 0600", info.Mode().Perm())
	}

	f, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f.CurrentProfile != "prod" {
		t.Errorf("current_profile = %q, want the first profile", f.CurrentProfile)
	}
	if f.Profiles["prod"] != prod || !f.Profiles["staging"].AuthNone {
		t.Errorf("round-trip mismatch: %+v", f.Profiles)
	}
}

// Every write takes the config lock, so concurrent updates never lose one.
// Without the lock the read-modify-write cycles interleave and profiles go
// missing.
func TestUpdateConcurrentWritesAllLand(t *testing.T) {
	isolate(t)
	const writers = 16
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Go(func() {
			name := fmt.Sprintf("p%02d", i)
			errs <- Update(addProfile(name, Config{Server: "https://" + name + ".example.com"}))
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	f, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Profiles) != writers {
		t.Errorf("%d profiles survived %d concurrent writes: %v", len(f.Profiles), writers, f.Names())
	}
}

func TestUpdate_CorruptConfigIsNotOverwritten(t *testing.T) {
	dir := isolate(t)
	configPath := filepath.Join(dir, "config.yaml")
	const badYAML = "bad yaml:\n  content:\n    - ["
	if err := os.WriteFile(configPath, []byte(badYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(); err == nil {
		t.Fatal("expected an error loading a corrupt config")
	}
	if err := Update(addProfile("x", Config{Server: "https://new.example.com"})); err == nil {
		t.Fatal("expected an error updating a corrupt config")
	}
	content, _ := os.ReadFile(configPath)
	if string(content) != badYAML {
		t.Errorf("config was overwritten despite the load error: %s", content)
	}
}

func TestUpdate_FnErrorWritesNothing(t *testing.T) {
	dir := isolate(t)
	if err := Update(func(*File) error { return errors.New("refused") }); err == nil {
		t.Fatal("Update must return fn's error")
	}
	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused update wrote the file: %v", err)
	}
}

// An old single-target file is refused with the exact create line for its
// values, and Update will not overwrite it.
func TestLoad_OldFormatIsRefusedWithTheCreateLine(t *testing.T) {
	dir := isolate(t)
	old := "server: https://cco.example.com\nimpersonate_service_account: cli@p.iam.gserviceaccount.com\niap_audience: 12 34\noutput: table\n"
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load()
	legacy, ok := errors.AsType[*LegacyError](err)
	if !ok {
		t.Fatalf("Load() err = %v, want *LegacyError", err)
	}
	const want = "cfctl config create default --server https://cco.example.com --impersonate cli@p.iam.gserviceaccount.com --audience '12 34'"
	if !strings.Contains(legacy.Error(), want) {
		t.Errorf("refusal lacks %q:\n%s", want, legacy.Error())
	}
	if err := Update(addProfile("default", Config{Server: "https://x.example.com"})); err == nil {
		t.Error("Update overwrote an old-format file")
	}
	if data, _ := os.ReadFile(path); string(data) != old {
		t.Errorf("old-format file changed: %s", data)
	}
}

// With no new file and CFCTL_CONFIG_DIR unset, the old ~/.cfctl file is still
// found and refused, so an upgrade never silently starts from nothing.
func TestLoad_OldHomeDirFileIsRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv(DirEnv, "")
	if err := os.MkdirAll(filepath.Join(home, ".cfctl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".cfctl", "config.yaml"), []byte("server: https://s.example.com\nauth_none: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load()
	legacy, ok := errors.AsType[*LegacyError](err)
	if !ok {
		t.Fatalf("Load() err = %v, want *LegacyError for ~/.cfctl/config.yaml", err)
	}
	if legacy.Dir() != filepath.Join(home, ".cfctl") {
		t.Errorf("Dir() = %q, want ~/.cfctl", legacy.Dir())
	}
	if !strings.Contains(err.Error(), "cfctl config create default --server https://s.example.com --auth none") {
		t.Errorf("refusal lacks the create line:\n%s", err)
	}
	// The new location is empty, so creating the profile there is allowed.
	if err := Update(addProfile("default", Config{Server: "https://s.example.com", AuthNone: true})); err != nil {
		t.Fatalf("Update into the new directory: %v", err)
	}
	if _, err := Load(); err != nil {
		t.Errorf("Load after migrating: %v", err)
	}
}

func TestLoad_CurrentProfileMustExist(t *testing.T) {
	dir := isolate(t)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("current_profile: gone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), `"gone"`) {
		t.Errorf("Load() err = %v, want a dangling current_profile named", err)
	}
}
