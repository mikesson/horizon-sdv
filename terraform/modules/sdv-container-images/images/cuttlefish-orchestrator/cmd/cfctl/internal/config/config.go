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

// Package config manages the cfctl configuration file: named profiles, each
// one deployment and the way cfctl authenticates to it.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/filelock"
)

// DirEnv moves every cfctl store: the config file, the token and completion
// caches and the background connection state.
const DirEnv = "CFCTL_CONFIG_DIR"

// Config is one target deployment and how cfctl authenticates to it. A
// profile stores one, and Resolve produces the effective one for a command.
type Config struct {
	// Server is the orchestrator URL, for example
	// https://cuttlefish-orchestrator-xxxx.a.run.app.
	Server string `yaml:"server,omitempty"`

	// CredentialsPath is the path to a Google service account key JSON file.
	// CI systems should prefer Workload Identity Federation.
	CredentialsPath string `yaml:"credentials_path,omitempty"`

	// IAPAudience is the ID token audience, a custom IAP OAuth client ID.
	// Empty selects the self-signed-JWT path for built-in Cloud Run IAP.
	IAPAudience string `yaml:"iap_audience,omitempty"`

	// AuthNone sends no credentials. It is for network-mode deployments.
	AuthNone bool `yaml:"auth_none,omitempty"`
	// ProxyEmail sets X-Auth-Request-Email when AuthNone is true against proxy-mode deployments.
	ProxyEmail string `yaml:"proxy_email,omitempty"`

	// ImpersonateServiceAccount is the service account email the user's
	// credentials impersonate. It must be on the deployment's IAP access list.
	ImpersonateServiceAccount string `yaml:"impersonate_service_account,omitempty"`
}

// File is the on-disk config: named profiles and the stored default.
type File struct {
	CurrentProfile string            `yaml:"current_profile,omitempty"`
	Profiles       map[string]Config `yaml:"profiles,omitempty"`
}

// Names returns the profile names in sorted order.
func (f *File) Names() []string {
	if f == nil {
		return nil
	}
	names := make([]string, 0, len(f.Profiles))
	for name := range f.Profiles {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Set stores p as the profile name.
func (f *File) Set(name string, p Config) {
	if f.Profiles == nil {
		f.Profiles = map[string]Config{}
	}
	f.Profiles[name] = p
}

// profileNamePattern keeps names usable as a shell word and a YAML key.
var profileNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateName rejects a profile name that is empty or has characters other
// than letters, digits, '.', '_' and '-'.
func ValidateName(name string) error {
	if !profileNamePattern.MatchString(name) {
		return fmt.Errorf("invalid profile name %q: use letters, digits, '.', '_' and '-', starting with a letter or digit", name)
	}
	return nil
}

// Dir returns the cfctl config directory: $CFCTL_CONFIG_DIR when set and
// non-empty, else cfctl under the user config directory (~/.config on Linux,
// ~/Library/Application Support on macOS, %AppData% on Windows).
func Dir() (string, error) {
	if dir := os.Getenv(DirEnv); dir != "" {
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating the user config directory (set %s to choose one): %w", DirEnv, err)
	}
	return filepath.Join(base, "cfctl"), nil
}

// Path returns the cfctl config file path.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// Load reads the config file. A missing file yields an empty File and a
// corrupt one is an error. A file in the old single-target format, or, when
// the config file is missing and CFCTL_CONFIG_DIR is unset, an old file left
// in ~/.cfctl, is a *LegacyError.
func Load() (*File, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	f, err := readFile(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return f, err
	}
	if legacy := legacyHomeFile(); legacy != nil {
		return nil, legacy
	}
	return &File{}, nil
}

// legacyHomeFile returns the *LegacyError for an old-format ~/.cfctl file, or
// nil when CFCTL_CONFIG_DIR is set or no such file exists.
func legacyHomeFile() *LegacyError {
	if os.Getenv(DirEnv) != "" {
		return nil
	}
	// No home directory means no ~/.cfctl to migrate from.
	home, _ := os.UserHomeDir()
	if home == "" {
		return nil
	}
	_, err := readFile(filepath.Join(home, ".cfctl", "config.yaml"))
	legacy, _ := errors.AsType[*LegacyError](err)
	return legacy
}

// legacyKeys are the top-level target fields of the old format.
var legacyKeys = []string{"server", "credentials_path", "iap_audience", "auth_none", "impersonate_service_account"}

// readFile reads and parses one config file. A missing file is an error
// matching fs.ErrNotExist.
func readFile(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var top map[string]any
	if err := yaml.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("failed to parse config %s: %w", path, err)
	}
	for _, key := range legacyKeys {
		if _, ok := top[key]; ok {
			var old Config
			if err := yaml.Unmarshal(data, &old); err != nil {
				return nil, fmt.Errorf("failed to parse config %s: %w", path, err)
			}
			return nil, &LegacyError{Path: path, Old: old}
		}
	}

	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("failed to parse config %s: %w", path, err)
	}
	for name := range f.Profiles {
		if err := ValidateName(name); err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
	}
	if f.CurrentProfile != "" {
		if _, ok := f.Profiles[f.CurrentProfile]; !ok {
			return nil, fmt.Errorf("config %s: current_profile %q names no profile", path, f.CurrentProfile)
		}
	}
	return &f, nil
}

// LegacyError reports a config file in the single-target format cfctl used
// before profiles. It is refused, and the message gives the command that
// recreates its values as a profile.
type LegacyError struct {
	Path string
	Old  Config
}

// Dir is the directory that holds the old file, and with it any connection
// state that agents started by the old binary still use.
func (e *LegacyError) Dir() string {
	return filepath.Dir(e.Path)
}

func (e *LegacyError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s is in the old single-target format, which this cfctl no longer reads.\n", e.Path)
	b.WriteString("Recreate it as a named profile:\n  ")
	b.WriteString(CreateCommand("default", e.Old))
	current, err := Path()
	if err == nil && current == e.Path {
		fmt.Fprintf(&b, "\nMove %s aside first, since the new file goes in the same place.", e.Path)
	}
	return b.String()
}

// CreateCommand returns the 'cfctl config create' line that stores p as the
// profile name. config create takes one identity, so a config holding both
// keeps impersonation, the one the auth ladder uses.
func CreateCommand(name string, p Config) string {
	parts := []string{"cfctl", "config", "create", shellQuote(name)}
	if p.Server != "" {
		parts = append(parts, "--server", shellQuote(p.Server))
	}
	if p.AuthNone {
		return strings.Join(append(parts, "--auth", "none"), " ")
	}
	switch {
	case p.ImpersonateServiceAccount != "":
		parts = append(parts, "--impersonate", shellQuote(p.ImpersonateServiceAccount))
	case p.CredentialsPath != "":
		parts = append(parts, "--credentials", shellQuote(p.CredentialsPath))
	}
	if p.IAPAudience != "" {
		parts = append(parts, "--audience", shellQuote(p.IAPAudience))
	}
	return strings.Join(parts, " ")
}

// shellQuote single-quotes s unless it is made only of characters a POSIX
// shell passes through unchanged.
func shellQuote(s string) string {
	safe := s != "" && strings.IndexFunc(s, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return false
		}
		return !strings.ContainsRune("@%+=:,./_-", r)
	}) < 0
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// lockTimeout bounds how long Update waits for concurrent serialized config
// read-modify-write + fsync operations before returning ErrHeld.
const lockTimeout = 5 * time.Second

// Update applies fn to the config file under the config lock and saves the
// result atomically. It reads the file only, so a CFCTL_* value is never
// persisted. A load error, an old-format file or an fn error aborts without
// writing, so a file the user may want to fix is never overwritten.
func Update(fn func(f *File) error) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	// Where the platform has no file lock, the write goes ahead unlocked.
	lockPath := filepath.Join(dir, "config.lock")
	lock, err := filelock.AcquireWithin(lockPath, lockTimeout)
	switch {
	case errors.Is(err, errors.ErrUnsupported):
	case err != nil:
		return fmt.Errorf("locking %s: %w", lockPath, err)
	default:
		defer filelock.Release(lock, lockPath)
	}

	path := filepath.Join(dir, "config.yaml")
	f, err := readFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		f = &File{}
	case err != nil:
		return err
	}
	if err := fn(f); err != nil {
		return err
	}
	return save(dir, path, f)
}

// save writes f to path atomically.
func save(dir, path string, f *File) error {
	data, err := yaml.Marshal(f)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write a temp file, fsync it, then rename it into place.
	tmp, err := os.CreateTemp(dir, "config-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to write config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to sync config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return fmt.Errorf("failed to set permissions on temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	success = true
	return nil
}
