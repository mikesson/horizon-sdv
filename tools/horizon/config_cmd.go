// Copyright (c) 2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const configInitHelp = `# Horizon CLI defaults (optional). Precedence: CLI flags > environment > this file.
# domain: example.com
# api_base_url: https://example.com/horizon-api
# keycloak_base: https://example.com/auth
# keycloak_realm: horizon
# module: sample
# template: sample-smoke-test
`

func runConfig(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: %s config <init|get|set> ...", progName())
	}
	switch args[0] {
	case "init":
		return runConfigInit(args[1:])
	case "get":
		return runConfigGet(args[1:])
	case "set":
		return runConfigSet(args[1:])
	default:
		return fmt.Errorf("unknown config subcommand %q", args[0])
	}
}

func runConfigInit(args []string) error {
	fs := flag.NewFlagSet("config init", flag.ExitOnError)
	force := fs.Bool("force", false, "overwrite existing config.yaml")
	_ = fs.Parse(args)

	dir, err := horizonConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "config.yaml")
	if _, err := os.Stat(path); err == nil && !*force {
		fmt.Fprintf(os.Stderr, "%s exists (use --force to overwrite)\n", path)
		return nil
	}
	if err := os.WriteFile(path, []byte(configInitHelp), 0o644); err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

func runConfigGet(args []string) error {
	fs := flag.NewFlagSet("config get", flag.ExitOnError)
	merged := fs.Bool("merged", false, "print effective values (file+env), secrets redacted")
	_ = fs.Parse(args)
	key := ""
	if rem := fs.Args(); len(rem) > 0 {
		key = strings.TrimSpace(rem[0])
	}

	if *merged {
		cfg, err := LoadConfig()
		if err != nil {
			return err
		}
		m := map[string]any{
			"domain":              cfg.Domain,
			"api_base_url":        cfg.BaseURL,
			"keycloak_base":       cfg.KeycloakBase,
			"keycloak_realm":      cfg.KeycloakRealm,
			"module":              cfg.Module,
			"template":            cfg.Template,
			"running_poll_limit":  cfg.RunningPollLimit,
			"wait_terminal_secs":  cfg.WaitTerminalSecs,
			"log_stream_max_secs": cfg.LogStreamMaxSecs,
			"log_stream_format":   cfg.LogStreamFormat,
		}
		if key != "" {
			if v, ok := m[key]; ok {
				fmt.Println(v)
				return nil
			}
			return fmt.Errorf("unknown key %q", key)
		}
		b, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}

	path, err := HorizonConfigFilePath()
	if err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "no file at %s (run `%s config init`)\n", path, progName())
			return nil
		}
		return err
	}
	if key != "" {
		var root map[string]any
		if err := yaml.Unmarshal(b, &root); err != nil {
			return err
		}
		v, ok := root[key]
		if !ok {
			return fmt.Errorf("key %q not in %s", key, path)
		}
		fmt.Println(v)
		return nil
	}
	fmt.Print(string(b))
	return nil
}

func runConfigSet(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: %s config set <key> <value>", progName())
	}
	k, v := args[0], args[1]
	allowed := map[string]bool{
		"domain": true, "api_base_url": true, "keycloak_base": true, "keycloak_realm": true,
		"module": true, "template": true,
	}
	if !allowed[k] {
		return fmt.Errorf("unsupported key %q (allowed: %v)", k, keysOf(allowed))
	}

	dir, err := horizonConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "config.yaml")

	var root map[string]any
	if b, err := os.ReadFile(path); err == nil {
		_ = yaml.Unmarshal(b, &root)
	}
	if root == nil {
		root = map[string]any{}
	}
	root[k] = v
	out, err := yaml.Marshal(root)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

func keysOf(m map[string]bool) []string {
	s := make([]string, 0, len(m))
	for k := range m {
		s = append(s, k)
	}
	return s
}

// mergeLoginIntoConfigFile writes domain/keycloak fields into config.yaml (for auth login --write-config).
func mergeLoginIntoConfigFile(domain, apiBase, keycloakBase, realm string) error {
	dir, err := horizonConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "config.yaml")

	var fc fileConfig
	if b, err := os.ReadFile(path); err == nil {
		_ = yaml.Unmarshal(b, &fc)
	}
	if d := strings.TrimSpace(domain); d != "" {
		fc.Domain = d
	}
	if s := strings.TrimSpace(apiBase); s != "" {
		fc.APIBaseURL = strings.TrimSuffix(s, "/")
	}
	if s := strings.TrimSpace(keycloakBase); s != "" {
		fc.KeycloakBase = strings.TrimSuffix(s, "/")
	}
	if s := strings.TrimSpace(realm); s != "" {
		fc.KeycloakRealm = s
	}
	out, err := yaml.Marshal(&fc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}
