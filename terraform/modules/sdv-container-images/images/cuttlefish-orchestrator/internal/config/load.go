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
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultConfig returns a configuration with sensible defaults
func DefaultConfig() *Config {
	return &Config{
		// DeviceIngress is NOT defaulted: the topology must be set
		// explicitly (fail-fast beats a silent default).
		Auth: AuthConfig{Mode: AuthModeIAP},
		Server: ServerConfig{
			Address:            ":8080",
			ReadHeaderTimeout:  Duration(10 * time.Second),
			ReadTimeout:        Duration(30 * time.Second),
			WriteTimeout:       Duration(30 * time.Second),
			SlowRequestTimeout: Duration(290 * time.Second),
			IdleTimeout:        Duration(120 * time.Second),
		},
		Database: DatabaseConfig{
			Type: DatabaseTypeSpanner,
		},
		GCP: GCPConfig{
			// Plain-HTTP port of the nginx front for the Host Orchestrator
			// on host VMs (cuttlefish-orchestration package).
			HostOrchestratorPort: 2080,
		},
		TURN: TURNConfig{
			TTL: Duration(12 * time.Hour),
			// A default keeps single-server setups working out of the box; a
			// relay configured with a different realm fails loudly rather than
			// silently, since every authorization reports the mismatch.
			Realm: "cco",
		},
		ICE: ICEConfig{
			// Google's public STUN servers, which is what every deployment has
			// used since before the setting existed. A code default rather than
			// a Terraform-supplied one: these are what a public-ingress
			// deployment's device leg depends on for its server-reflexive
			// candidate, so an apply that forgot to set them would take media
			// down.
			StunServers: []string{
				"stun:stun.l.google.com:19302",
				"stun:stun1.l.google.com:19302",
			},
		},
		Reaper: ReaperConfig{
			Enabled:   true,
			Interval:  Duration(1 * time.Minute),
			BatchSize: 50,
			DryRun:    false,
		},
		// Numeric task-queue defaults live in jobs/taskworker and
		// jobs/taskreclaimer; only Enabled needs a non-zero default here.
		TaskWorker:       TaskWorkerConfig{Enabled: true},
		TaskReclaimer:    TaskReclaimerConfig{Enabled: true},
		HostReconciler:   HostReconcilerConfig{Interval: Duration(1 * time.Minute)},
		DeviceController: DeviceControllerConfig{Interval: Duration(1 * time.Minute)},
		CatalogSync: CatalogSyncConfig{
			SpotAdvice:             false,
			SpotAdviceSyncInterval: Duration(15 * time.Minute),
		},
	}
}

// Load builds the configuration from CCO_* environment variables layered over
// the code defaults, with secrets read from their mounted volumes, then
// validates. There is no config file: deployment values come from CCO_* env
// (Terraform sets them in production; a dotenv supplies them for local dev, see
// .env.example), and operational tuning stays in the code defaults.
func Load() (*Config, error) {
	cfg := DefaultConfig()
	if err := cfg.applyEnv(); err != nil {
		return nil, err
	}
	if err := cfg.loadSecrets(); err != nil {
		return nil, err
	}
	cfg.derive()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

// envBinding binds one CCO_* environment variable to a config field. apply
// receives the variable's value and only runs when it is non-empty, so unset
// keys keep the code default.
type envBinding struct {
	key   string
	apply func(v string) error
}

// applyEnv overlays CCO_* environment variables onto the code defaults,
// walking the envBindings table. Only deployment-determined values are
// env-backed. Operational tuning (timeouts, job intervals, pool sizes) stays
// in the code defaults. Secrets are handled by loadSecrets.
func (c *Config) applyEnv() error {
	for _, b := range c.envBindings() {
		v := os.Getenv(b.key)
		if v == "" {
			continue
		}
		if err := b.apply(v); err != nil {
			return fmt.Errorf("config env %s: %w", b.key, err)
		}
	}
	return nil
}

// envBindings is the single declaration of every deployment env variable: adding
// one is a field plus one row here.
func (c *Config) envBindings() []envBinding {
	return []envBinding{
		{"CCO_DEVICE_INGRESS", setString((*string)(&c.DeviceIngress))},
		{"CCO_PROBE_PORT", setInt(&c.Server.ProbePort)},
		{"CCO_DEPLOYMENT", setString(&c.Deployment)},
		{"CCO_AUTH_MODE", setString((*string)(&c.Auth.Mode))},
		{"CCO_IAP_AUDIENCE", setString(&c.Auth.IAPAudience)},
		{"CCO_TRUSTED_PROXIES", setList(&c.Auth.TrustedProxies)},
		{"CCO_DEFAULT_USER", setString(&c.Auth.ProxyDefaultUser)},
		{"CCO_DEV_FALLBACK_EMAIL", setString(&c.Auth.DevFallbackEmail)},
		{"CCO_DEV_MODE", setBool(&c.Auth.DevMode)},
		{"CCO_SPANNER_PROJECT", setString(&c.Database.Spanner.Project)},
		{"CCO_SPANNER_INSTANCE", setString(&c.Database.Spanner.Instance)},
		{"CCO_SPANNER_DATABASE", setString(&c.Database.Spanner.Database)},
		{"CCO_PROJECT_ID", setString(&c.GCP.ProjectID)},
		{"CCO_NETWORK", setString(&c.GCP.Network)},
		{"CCO_SUBNETWORK", setString(&c.GCP.Subnetwork)},
		{"CCO_HOST_SERVICE_ACCOUNT", setString(&c.GCP.HostServiceAccount)},
		{"CCO_HOST_IMAGE_PROJECT", setString(&c.GCP.HostImageProject)},
		{"CCO_ARTIFACTS_BUCKET", setString(&c.GCP.ArtifactsBucket)},
		{"CCO_HOST_ORCHESTRATOR_PORT", setInt(&c.GCP.HostOrchestratorPort)},
		{"CCO_ALLOW_SELF_SIGNED_HOST_SSL_CERT", setBool(&c.GCP.AllowSelfSignedHostSSLCert)},
		{"CCO_SPOT_ADVICE", setBool(&c.CatalogSync.SpotAdvice)},
		{"CCO_TURN_SERVERS", setList(&c.TURN.Servers)},
		{"CCO_DEVICE_TURN_SERVERS", setList(&c.TURN.DeviceServers)},
		{"CCO_TURN_REALM", setString(&c.TURN.Realm)},
		{"CCO_STUN_SERVERS", setList(&c.ICE.StunServers)},
	}
}

// setString overwrites *dst with the env value.
func setString(dst *string) func(string) error {
	return func(v string) error {
		*dst = v
		return nil
	}
}

// setBool parses the env value as a boolean into *dst.
func setBool(dst *bool) func(string) error {
	return func(v string) error {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("invalid boolean %q: %w", v, err)
		}
		*dst = b
		return nil
	}
}

// setInt parses the env value as an integer into *dst.
func setInt(dst *int) func(string) error {
	return func(v string) error {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid integer %q: %w", v, err)
		}
		*dst = n
		return nil
	}
}

// setList splits the comma-separated env value into *dst (trimmed, non-empty).
func setList(dst *[]string) func(string) error {
	return func(v string) error {
		*dst = splitList(v)
		return nil
	}
}

// loadSecrets populates secret fields from their mounted Secret Manager volumes
// (CCO_*_FILE), applied before Validate (relay mode requires the TURN secret).
func (c *Config) loadSecrets() error {
	secret, err := SecretFromFileOrEnv("CCO_TURN_SHARED_SECRET_FILE", "CCO_TURN_SHARED_SECRET")
	if err != nil {
		return fmt.Errorf("loading TURN shared secret: %w", err)
	}
	if secret != "" {
		c.TURN.SharedSecret = secret
	}
	return nil
}

// derive computes fields not read directly from config.
func (c *Config) derive() {
	if c.Database.Type == DatabaseTypeSpanner {
		c.Database.Spanner.FullDatabasePath = fmt.Sprintf(
			"projects/%s/instances/%s/databases/%s",
			c.Database.Spanner.Project,
			c.Database.Spanner.Instance,
			c.Database.Spanner.Database,
		)
	}
}

// SecretFromFileOrEnv resolves a secret from a mounted file whose path is named
// by fileEnv, a Secret Manager volume mount. That is the preferred source
// because a volume read fetches the latest version. It falls back to
// the value of valueEnv, and returns "" when neither is set.
func SecretFromFileOrEnv(fileEnv, valueEnv string) (string, error) {
	if path := os.Getenv(fileEnv); path != "" {
		data, err := os.ReadFile(path) //nolint:gosec // The path is the operator's own CCO_*_FILE env (a mounted secret volume), not request input.
		if err != nil {
			return "", fmt.Errorf("reading secret file from %s=%q: %w", fileEnv, path, err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	return os.Getenv(valueEnv), nil
}

// splitList parses a comma-separated env value into a trimmed, non-empty slice.
func splitList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
