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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/auth/authtest"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
)

// testProfile is the profile name testFile stores.
const testProfile = "test"

// testFile returns a config file whose one profile, current, is p.
func testFile(p config.Config) *config.File {
	return &config.File{CurrentProfile: testProfile, Profiles: map[string]config.Config{testProfile: p}}
}

// parseCLI creates a Kong parser for the real CLI struct and parses args with
// profile as the current profile. AfterApply runs during parsing.
func parseCLI(t *testing.T, args []string, profile *config.Config) (*CLI, *kong.Context, error) {
	t.Helper()

	var c CLI
	c.configFile = testFile(*profile)
	parser, err := kong.New(&c,
		kong.Name("cfctl"),
		kong.Exit(func(int) {}), // Prevent os.Exit on --help/--version
		kong.Bind(&config.Config{}),
		kong.Vars{"version": "test"},
	)
	if err != nil {
		t.Fatalf("failed to create kong parser: %v", err)
	}

	kctx, err := parser.Parse(args)
	return &c, kctx, err
}

func TestAfterApply_ServerValidation(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		cfgServer   string // server set in config
		wantErr     bool
		errContains string
	}{
		// Commands that do NOT require a server.
		{
			name: "config show",
			args: []string{"config", "show"},
		},
		{
			name: "config set-server",
			args: []string{"config", "set-server", "https://example.com"},
		},
		{
			name: "config set-audience",
			args: []string{"config", "set-audience", "my-audience"},
		},
		{
			name: "config set-credentials",
			args: []string{"config", "set-credentials", "/tmp/creds.json"},
		},
		{
			name: "config clear-credentials",
			args: []string{"config", "clear-credentials"},
		},
		{
			name: "config set-impersonate",
			args: []string{"config", "set-impersonate", "sa@proj.iam.gserviceaccount.com"},
		},
		{
			name: "config clear-impersonate",
			args: []string{"config", "clear-impersonate"},
		},
		{
			name: "auth login",
			args: []string{"auth", "login"},
		},
		{
			name: "auth logout",
			args: []string{"auth", "logout"},
		},
		{
			name: "auth status",
			args: []string{"auth", "status"},
		},
		{
			name: "completion bash",
			args: []string{"completion", "bash"},
		},
		{
			name: "completion zsh",
			args: []string{"completion", "zsh"},
		},
		{
			name: "completion fish",
			args: []string{"completion", "fish"},
		},

		// Commands that DO require a server.
		{
			name:        "host list requires server",
			args:        []string{"host", "list"},
			wantErr:     true,
			errContains: "no server configured",
		},
		{
			name:        "host get requires server",
			args:        []string{"host", "get", "h1"},
			wantErr:     true,
			errContains: "no server configured",
		},
		{
			name:        "device list requires server",
			args:        []string{"device", "list"},
			wantErr:     true,
			errContains: "no server configured",
		},
		{
			name:        "status requires server",
			args:        []string{"status"},
			wantErr:     true,
			errContains: "no server configured",
		},
		{
			name:        "admin tasks list requires server",
			args:        []string{"admin", "tasks", "list"},
			wantErr:     true,
			errContains: "no server configured",
		},
		{
			name:        "auth whoami requires server",
			args:        []string{"auth", "whoami"},
			wantErr:     true,
			errContains: "no server configured",
		},

		// Server provided via --server flag.
		{
			name: "host list with --server flag passes validation",
			args: []string{"--server", "https://example.com", "host", "list"},
		},

		// The --server value carries the bearer credential, so it gets the
		// same validation as set-server.
		{
			name:        "bare host without scheme is rejected",
			args:        []string{"--server", "cuttlefish.example.com", "host", "list"},
			wantErr:     true,
			errContains: "invalid server URL",
		},
		{
			name:        "non-http scheme is rejected",
			args:        []string{"--server", "ftp://example.com", "host", "list"},
			wantErr:     true,
			errContains: "invalid server URL",
		},
		{
			name:        "plain http to a remote host is rejected",
			args:        []string{"--server", "http://cco.example.com", "host", "list"},
			wantErr:     true,
			errContains: "uses http",
		},
		{
			name: "plain http to loopback is allowed",
			args: []string{"--server", "http://127.0.0.1:8080", "host", "list"},
		},
		{
			name: "plain http to localhost is allowed",
			args: []string{"--server", "http://localhost:8080", "host", "list"},
		},
		{
			name: "plain http to IPv6 loopback is allowed",
			args: []string{"--server", "http://[::1]:8080", "host", "list"},
		},
		{
			name: "completion ignores a broken server value",
			args: []string{"--server", "cuttlefish.example.com", "completion", "bash"},
		},

		// Server provided via config.
		{
			name:      "host list with config server passes validation",
			args:      []string{"host", "list"},
			cfgServer: "https://from-config.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Server: tt.cfgServer}

			_, _, err := parseCLI(t, tt.args, cfg)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errContains)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestAfterApply_ServerFromConfig(t *testing.T) {
	cfg := &config.Config{Server: "https://from-config.example.com"}

	cli, _, err := parseCLI(t, []string{"host", "list"}, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cli.Server != "https://from-config.example.com" {
		t.Errorf("expected server from config, got %q", cli.Server)
	}
}

func TestAfterApply_NormalizesTrailingSlash(t *testing.T) {
	// A trailing slash would break URLs built by concatenation, such as the
	// device player link.
	cfg := &config.Config{Server: "https://example.com//"}

	cli, _, err := parseCLI(t, []string{"host", "list"}, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cli.Server != "https://example.com" {
		t.Errorf("Server = %q, want trailing slashes stripped", cli.Server)
	}
}

func TestAfterApply_CLIFlagOverridesConfig(t *testing.T) {
	cfg := &config.Config{Server: "https://from-config.example.com"}

	cli, _, err := parseCLI(t, []string{"--server", "https://from-flag.example.com", "host", "list"}, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cli.Server != "https://from-flag.example.com" {
		t.Errorf("expected server from CLI flag, got %q", cli.Server)
	}
}

func TestAfterApply_EnvVarServer(t *testing.T) {
	t.Setenv("CFCTL_SERVER", "https://from-env.example.com")

	cli, _, err := parseCLI(t, []string{"host", "list"}, &config.Config{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cli.Server != "https://from-env.example.com" {
		t.Errorf("expected server from env var, got %q", cli.Server)
	}
}

func TestEnumValidation(t *testing.T) {
	cfg := &config.Config{Server: "https://test.example.com"}

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name: "valid host status running",
			args: []string{"host", "list", "--status", "running"},
		},
		{
			name: "valid host status stopped",
			args: []string{"host", "list", "--status", "stopped"},
		},
		{
			name: "valid host status pending",
			args: []string{"host", "list", "--status", "pending"},
		},
		{
			name: "valid host status provisioning",
			args: []string{"host", "list", "--status", "provisioning"},
		},
		{
			name: "valid host status error",
			args: []string{"host", "list", "--status", "error"},
		},
		{
			name: "valid host status preempted",
			args: []string{"host", "list", "--status", "preempted"},
		},
		{
			name: "empty host status is valid (default)",
			args: []string{"host", "list", "--status", ""},
		},
		{
			name:    "invalid host status",
			args:    []string{"host", "list", "--status", "invalid"},
			wantErr: true,
		},
		{
			name: "valid device status running",
			args: []string{"device", "list", "--status", "running"},
		},
		{
			name: "valid device status stopped",
			args: []string{"device", "list", "--status", "stopped"},
		},
		{
			name: "valid device status starting",
			args: []string{"device", "list", "--status", "starting"},
		},
		{
			name: "valid device status stopping",
			args: []string{"device", "list", "--status", "stopping"},
		},
		{
			name:    "invalid device status",
			args:    []string{"device", "list", "--status", "unknown"},
			wantErr: true,
		},
		{
			name: "valid GPU mode gfxstream",
			args: []string{"device", "create", "--host", "h1", "--name", "d1", "--gpu-mode", "gfxstream"},
		},
		{
			name: "valid GPU mode drm_virgl",
			args: []string{"device", "create", "--host", "h1", "--name", "d1", "--gpu-mode", "drm_virgl"},
		},
		{
			name: "valid GPU mode swiftshader",
			args: []string{"device", "create", "--host", "h1", "--name", "d1", "--gpu-mode", "swiftshader"},
		},
		{
			name: "valid GPU mode auto",
			args: []string{"device", "create", "--host", "h1", "--name", "d1", "--gpu-mode", "auto"},
		},
		{
			name:    "invalid GPU mode",
			args:    []string{"device", "create", "--host", "h1", "--name", "d1", "--gpu-mode", "invalid"},
			wantErr: true,
		},
		{
			name: "device create needs no flags",
			args: []string{"device", "create"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parseCLI(t, tt.args, cfg)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestGlobalFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		get  func(cli *CLI) any
		want any
	}{
		{"default JSON is false", nil, func(cli *CLI) any { return cli.JSON }, false},
		{"--json enables JSON output", []string{"--json"}, func(cli *CLI) any { return cli.JSON }, true},
		{"--no-json disables JSON output", []string{"--no-json"}, func(cli *CLI) any { return cli.JSON }, false},
		{"default verbose is false", nil, func(cli *CLI) any { return cli.Verbose }, false},
		{"-v enables verbose", []string{"-v"}, func(cli *CLI) any { return cli.Verbose }, true},
		{"--verbose enables verbose", []string{"--verbose"}, func(cli *CLI) any { return cli.Verbose }, true},
		{"default timeout is zero", nil, func(cli *CLI) any { return cli.Timeout }, time.Duration(0)},
		{"--timeout parses duration", []string{"--timeout", "30s"}, func(cli *CLI) any { return cli.Timeout }, 30 * time.Second},
		{"--timeout with minutes", []string{"--timeout", "5m"}, func(cli *CLI) any { return cli.Timeout }, 5 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli, _, err := parseCLI(t, append(tt.args, "config", "show"), &config.Config{})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := tt.get(cli); got != tt.want {
				t.Errorf("args %v: got %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestCommandAliases(t *testing.T) {
	cfg := &config.Config{Server: "https://test.example.com"}

	tests := []struct {
		name        string
		args        []string
		wantCommand string
	}{
		// Host aliases.
		{"host ls -> host list", []string{"host", "ls"}, "host list"},
		{"host show -> host get", []string{"host", "show", "h1"}, "host get <id>"},
		{"host info -> host get", []string{"host", "info", "h1"}, "host get <id>"},
		{"host new -> host create", []string{"host", "new", "--name", "h1", "--zone", "us-c1-a"}, "host create"},
		{"host rm -> host delete", []string{"host", "rm", "h1"}, "host delete <id>"},
		{"host remove -> host delete", []string{"host", "remove", "h1"}, "host delete <id>"},
		{"host shell -> host ssh", []string{"host", "shell", "h1"}, "host ssh <id>"},

		// Device aliases.
		{"device ls -> device list", []string{"device", "ls"}, "device list"},
		{"device show -> device get", []string{"device", "show", "d1"}, "device get <id>"},
		{"device info -> device get", []string{"device", "info", "d1"}, "device get <id>"},
		{"device new -> device create", []string{"device", "new", "--host", "h1", "--name", "d1"}, "device create"},
		{"device rm -> device delete", []string{"device", "rm", "d1"}, "device delete <id>"},
		{"device remove -> device delete", []string{"device", "remove", "d1"}, "device delete <id>"},
		{"device adb -> device connect", []string{"device", "adb", "d1"}, "device connect <id>"},
		{"device stream -> device webrtc", []string{"device", "stream", "d1"}, "device webrtc <id>"},
		{"device view -> device webrtc", []string{"device", "view", "d1"}, "device webrtc <id>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, kctx, err := parseCLI(t, tt.args, cfg)
			if err != nil {
				t.Fatalf("alias should parse: %v", err)
			}
			if got := kctx.Command(); got != tt.wantCommand {
				t.Errorf("expected command %q, got %q", tt.wantCommand, got)
			}
		})
	}
}

func TestCommandTree(t *testing.T) {
	// Every listed command must parse through the real CLI struct.
	cfg := &config.Config{Server: "https://test.example.com"}

	commands := []struct {
		name string
		args []string
	}{
		// Top-level.
		{"status", []string{"status"}},

		// Auth.
		{"auth login", []string{"auth", "login"}},
		{"auth whoami", []string{"auth", "whoami"}},
		{"auth logout", []string{"auth", "logout"}},
		{"auth status", []string{"auth", "status"}},

		// Config.
		{"config show", []string{"config", "show"}},
		{"config set-server", []string{"config", "set-server", "https://example.com"}},
		{"config set-audience", []string{"config", "set-audience", "my-aud"}},
		{"config set-credentials", []string{"config", "set-credentials", "/tmp/c.json"}},
		{"config clear-credentials", []string{"config", "clear-credentials"}},
		{"config set-impersonate", []string{"config", "set-impersonate", "sa@p.iam.gserviceaccount.com"}},
		{"config clear-impersonate", []string{"config", "clear-impersonate"}},
		{"config profiles", []string{"config", "profiles"}},
		{"config use", []string{"config", "use", "prod"}},
		{"config create", []string{"config", "create", "prod", "--server", "https://p.example.com", "--impersonate", "sa@p"}},
		{"config delete", []string{"config", "delete", "prod"}},
		{"config rename", []string{"config", "rename", "prod", "live"}},

		// Host.
		{"host list", []string{"host", "list"}},
		{"host get", []string{"host", "get", "h1"}},
		{"host create", []string{"host", "create", "--name", "h1", "--zone", "us-central1-a"}},
		{"host delete", []string{"host", "delete", "h1"}},
		{"host start", []string{"host", "start", "h1"}},
		{"host stop", []string{"host", "stop", "h1"}},
		{"host ssh", []string{"host", "ssh", "h1"}},
		{"host extend", []string{"host", "extend", "h1", "--hours", "24"}},

		// Device.
		{"device list", []string{"device", "list"}},
		{"device get", []string{"device", "get", "d1"}},
		{"device create", []string{"device", "create", "--host", "h1", "--name", "d1"}},
		{"device delete", []string{"device", "delete", "d1"}},
		{"device start", []string{"device", "start", "d1"}},
		{"device stop", []string{"device", "stop", "d1"}},
		{"device connect", []string{"device", "connect", "d1"}},
		{"device webrtc", []string{"device", "webrtc", "d1"}},

		// Admin.
		{"admin tasks list", []string{"admin", "tasks", "list"}},

		// Completion.
		{"completion bash", []string{"completion", "bash"}},
		{"completion zsh", []string{"completion", "zsh"}},
		{"completion fish", []string{"completion", "fish"}},
	}

	for _, tt := range commands {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parseCLI(t, tt.args, cfg)
			if err != nil {
				t.Fatalf("failed to parse %v: %v", tt.args, err)
			}
		})
	}
}

func TestCommandsNotRequiringServer_Complete(t *testing.T) {
	// The map must include every local-only command.
	localCommands := []string{
		"setup",
		"config", "config show",
		"config set-server", "config set-audience",
		"config set-credentials", "config clear-credentials",
		"config set-impersonate", "config clear-impersonate",
		"config profiles", "config use", "config create", "config delete", "config rename",
		"completion", "completion bash", "completion zsh", "completion fish",
		"auth", "auth login", "auth logout", "auth status",
		"device disconnect", "device connections",
	}
	for _, cmd := range localCommands {
		if !commandsNotRequiringServer[cmd] {
			t.Errorf("command %q should be in commandsNotRequiringServer", cmd)
		}
	}

	// Commands that require a server must not be in the map.
	serverCommands := []string{
		"host list", "host get", "host create", "host delete",
		"host start", "host stop", "host ssh",
		"device list", "device get", "device create", "device delete",
		"device start", "device stop", "device connect", "device webrtc",
		"admin tasks list", "status", "auth whoami",
	}
	for _, cmd := range serverCommands {
		if commandsNotRequiringServer[cmd] {
			t.Errorf("command %q should NOT be in commandsNotRequiringServer", cmd)
		}
	}
}

func TestServerNotConfiguredError(t *testing.T) {
	err := &ServerNotConfiguredError{}
	msg := err.Error()

	if !strings.Contains(msg, "no server configured") {
		t.Errorf("error should contain 'no server configured', got: %s", msg)
	}
	for _, want := range []string{"cfctl config create NAME --server URL", "--profile NAME", "CFCTL_SERVER"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should mention %q, got: %s", want, msg)
		}
	}
}

// TestTypoKeepsSuggestionVisible checks that a mistyped command prints the
// "did you mean" suggestion without the full command dump.
func TestTypoKeepsSuggestionVisible(t *testing.T) {
	var c CLI
	parser, err := NewParser(&c, nil, "test")
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	var stdout, stderr strings.Builder
	parser.Stdout = &stdout
	parser.Stderr = &stderr
	parser.Exit = func(int) {}

	_, parseErr := parser.Parse([]string{"hots", "list"})
	if parseErr == nil {
		t.Fatal("expected a parse error for unknown command 'hots'")
	}
	parser.FatalIfErrorf(parseErr)

	if !strings.Contains(stderr.String(), `did you mean "host"?`) {
		t.Errorf("typo suggestion missing from stderr:\n%s", stderr.String())
	}
	if out := stdout.String(); strings.Contains(out, "completion") {
		t.Errorf("full command dump printed on error:\n%s", out)
	}
}

// TestClientProviderIsSingleton checks that one parse resolves *cco.Client to
// the same instance every time.
func TestClientProviderIsSingleton(t *testing.T) {
	keyPath, _ := authtest.WriteKeyFile(t)
	cfg := testFile(config.Config{Server: "https://cco.example.com", CredentialsPath: keyPath})

	var c CLI
	parser, err := NewParser(&c, cfg, "test")
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	kctx, err := parser.Parse([]string{"host", "list"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	kctx.BindTo(t.Context(), (*context.Context)(nil))

	get := func() *cco.Client {
		t.Helper()
		// Outside Run, the *CLI binding must be passed explicitly.
		out, err := kctx.Call(func(cl *cco.Client) *cco.Client { return cl }, &c)
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		return out[0].(*cco.Client)
	}
	first, second := get(), get()
	if first != second {
		t.Error("provider built a new client per recipient; want one client per invocation")
	}
}

// Kong renders a Help() method only when the type implements HelpProvider.
var (
	_ kong.HelpProvider = (*DeviceCreateCmd)(nil)
	_ kong.HelpProvider = (*HostCreateCmd)(nil)
	_ kong.HelpProvider = (*BatchCreateCmd)(nil)
	_ kong.HelpProvider = (*SetupCmd)(nil)
)

func TestDetailedHelpRenders(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"device", "create", "--help"}, "Placement is either imperative"},
		{[]string{"host", "create", "--help"}, "cfctl machine-type list"},
		{[]string{"batch", "create", "--help"}, "resumes the same batch"},
		{[]string{"setup", "--help"}, "whoami round-trip"},
	}
	for _, tc := range cases {
		var c CLI
		parser, err := NewParser(&c, nil, "test")
		if err != nil {
			t.Fatalf("NewParser: %v", err)
		}
		var stdout strings.Builder
		parser.Stdout = &stdout
		// A no-op Exit lets Kong continue into required-flag validation after
		// printing help, so the parse error is ignored.
		parser.Exit = func(int) {}
		_, _ = parser.Parse(tc.args)
		if !strings.Contains(stdout.String(), tc.want) {
			t.Errorf("%v: help does not contain %q:\n%s", tc.args, tc.want, stdout.String())
		}
	}
}

// TestEnvarPolicy checks that only the global --server, --timeout and
// --profile read the environment through Kong. A variable on any subcommand
// flag (CFCTL_HOST, CFCTL_PORT, CFCTL_FORCE) would change commands from the
// ambient shell.
func TestEnvarPolicy(t *testing.T) {
	var c CLI
	parser, err := NewParser(&c, nil, "test")
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	want := map[string]string{"server": "CFCTL_SERVER", "timeout": "CFCTL_TIMEOUT", "profile": "CFCTL_PROFILE"}
	for _, f := range parser.Model.Flags {
		if env, ok := want[f.Name]; ok {
			if !slices.Equal(f.Tag.Envs, []string{env}) {
				t.Errorf("--%s envs = %v, want [%s]", f.Name, f.Tag.Envs, env)
			}
			delete(want, f.Name)
		} else if len(f.Tag.Envs) > 0 {
			t.Errorf("global --%s reads %v; only --server, --timeout and --profile may", f.Name, f.Tag.Envs)
		}
	}
	if len(want) > 0 {
		t.Errorf("global flags missing: %v", want)
	}
	walked := 0
	var walk func(path string, node *kong.Node)
	walk = func(path string, node *kong.Node) {
		for _, child := range node.Children {
			if child.Type != kong.CommandNode {
				continue
			}
			for _, f := range child.Flags {
				walked++
				if len(f.Tag.Envs) > 0 || len(f.Envs) > 0 {
					t.Errorf("%s %s --%s reads %v from the environment", path, child.Name, f.Name, f.Tag.Envs)
				}
			}
			walk(path+" "+child.Name, child)
		}
	}
	walk("cfctl", parser.Model.Node)
	if walked < 100 {
		t.Fatalf("walk saw %d subcommand flags. The walk is broken", walked)
	}
}

// With the environment cut down to the globals, an ambient CFCTL_HOST no
// longer fills --host. Restoring kong.DefaultEnvars turns this red.
func TestAmbientSubcommandVariableIsIgnored(t *testing.T) {
	t.Setenv("CFCTL_HOST", "ambient-host")
	var c CLI
	parser, err := NewParser(&c, testFile(config.Config{Server: "https://cco.example.com"}), "test")
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	if _, err := parser.Parse([]string{"device", "list"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.Device.List.Host != "" {
		t.Errorf("device list --host = %q, want the ambient CFCTL_HOST ignored", c.Device.List.Host)
	}
}

func TestNormalizeServerURLHTTPPolicy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw      string
		authNone bool
		ok       bool
	}{
		{"https://cco.example.com/", false, true},
		{"http://cco.example.com", false, false},
		{"http://cco.example.com", true, true}, // no credential is sent
		{"http://127.0.0.2:9000", false, true},
		{"http://LOCALHOST:8080", false, true},
		{"http://[::1]:8080", false, true},
		{"http://10.0.0.1", false, false},
	}
	for _, tc := range cases {
		_, err := normalizeServerURL(tc.raw, tc.authNone)
		if (err == nil) != tc.ok {
			t.Errorf("normalizeServerURL(%q, authNone=%v) err = %v, want ok=%v", tc.raw, tc.authNone, err, tc.ok)
		}
	}
}
