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

// Package cli defines the command-line interface using Kong.
package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alecthomas/kong"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// NewParser builds the Kong parser with the production wiring. main and the
// test harness share it. file is the loaded config file, nil when there is
// none. AfterApply resolves the effective config from it once the flags are
// parsed, so --profile and --server can take part. The client provider
// receives the context the caller binds after parsing, which is the
// --timeout-bounded command context.
func NewParser(c *CLI, file *config.File, version string) (*kong.Kong, error) {
	c.configFile = file
	cfg := &config.Config{}
	parser, err := kong.New(c,
		kong.Name("cfctl"),
		kong.Description("Cuttlefish Control - CLI for Cloud Cuttlefish Orchestrator\n\n"+exitCodeHelp),
		// Full usage would print every command on a parse error and push the
		// typo suggestion out of a CI log.
		kong.ShortUsageOnError(),
		// Group titles and descriptions, keyed by the group: struct tags.
		kong.ExplicitGroups([]kong.Group{
			{
				Key: "placement-imperative", Title: "Imperative placement:",
				Description: "The device lands on the host you name; it must exist and have capacity.",
			},
			{
				Key: "placement-declarative", Title: "Declarative placement:",
				Description: "You state how the device will be used and the platform picks or provisions a host to fit. The default when --host is not given, with --interaction headless.",
			},
			{
				Key: "build-source", Title: "Build source:",
				Description: "Omit all of these to run the registered default build.",
			},
			{Key: "config-profiles", Title: "Profiles:"},
			{Key: "config-server", Title: "Server Configuration (selected profile):"},
			{Key: "config-credentials", Title: "Credentials (selected profile):"},
		}),
		kong.ConfigureHelp(kong.HelpOptions{
			Compact: true,
			// Root help lists only the top-level commands. The expanded
			// form runs to about 100 leaves.
			NoExpandSubcommands: true,
		}),
		kong.Vars{
			"version": version,
		},
		kong.Bind(cfg),

		// The resolved server URL is also the self-signed JWT audience. The
		// provider is a singleton because Kong calls a plain provider once per
		// recipient, which would mint a second client and token.
		kong.BindSingletonProvider(func(ctx context.Context, c *CLI) (*cco.Client, error) {
			return cco.NewClient(ctx, c.Server, cfg, c.Timeout)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("building parser: %w", err)
	}
	return parser, nil
}

// CLI represents the top-level command structure.
type CLI struct {
	// Global flags. Only these read the environment through Kong; the
	// per-field auth variables are read by config.Resolve.
	Server  string        `env:"CFCTL_SERVER" placeholder:"URL" help:"Orchestrator server URL. A server other than the profile's uses none of the profile's auth settings"`
	Profile string        `env:"CFCTL_PROFILE" placeholder:"NAME" help:"Config profile to use instead of the current one"`
	JSON    bool          `negatable:"" help:"Output as JSON (use --no-json to disable)"`
	Verbose bool          `short:"v" help:"Enable verbose output"`
	Timeout time.Duration `env:"CFCTL_TIMEOUT" help:"Maximum time for the command to complete, bounding every API request (0 = no overall limit; each API request then times out after 30s)" default:"0"`

	// Version is Kong's built-in version flag.
	Version kong.VersionFlag `name:"version" short:"V" help:"Show version information"`

	// hints collects next-step suggestions during Run for AfterRun to print.
	hints []string `kong:"-"`

	// ConfigErr is the config-file load error, set by main. AfterApply fails
	// config-dependent commands with it so "no server configured" does not
	// mask the real cause.
	ConfigErr error `kong:"-"`

	// configFile is the loaded config file, set by NewParser.
	configFile *config.File `kong:"-"`
	// resolved is the effective config, set by AfterApply.
	resolved *config.Resolved `kong:"-"`
	// profile is the selected profile name, empty when none is selected.
	profile string `kong:"-"`
	// serverFlag is --server as typed, set only when the flag was given.
	serverFlag    string `kong:"-"`
	serverFlagSet bool   `kong:"-"`

	// Commands, in help order.
	Setup  SetupCmd  `cmd:"" help:"Interactive setup wizard"`
	Auth   AuthCmd   `cmd:"" help:"Authentication commands"`
	Config ConfigCmd `cmd:"" help:"Manage CLI configuration"`
	Status StatusCmd `cmd:"" help:"Check server health"`
	Doctor DoctorCmd `cmd:"" help:"Diagnose connectivity, auth, quota and storage access"`

	Host        HostCmd        `cmd:"" help:"Manage host VMs"`
	Device      DeviceCmd      `cmd:"" help:"Manage Cuttlefish devices"`
	Build       BuildCmd       `cmd:"" help:"Manage the build registry (push local builds, list)"`
	Batch       BatchCmd       `cmd:"" help:"Manage batches of devices (CI/fleet)"`
	Operation   OperationCmd   `cmd:"" help:"Inspect long-running operations (async device verbs)"`
	MachineType MachineTypeCmd `cmd:"" name:"machine-type" help:"Browse the machine-type catalog"`
	Zone        ZoneCmd        `cmd:"" help:"Browse the zone catalog"`
	Quota       QuotaCmd       `cmd:"" help:"Host-capacity quota"`
	Storage     StorageCmd     `cmd:"" help:"Browse the artifact buckets (ls, rm)"`

	Admin      AdminCmd      `cmd:"" help:"Administrative operations"`
	Completion CompletionCmd `cmd:"" help:"Generate shell completion scripts"`
}

// AuthCmd contains authentication subcommands.
type AuthCmd struct {
	Login  AuthLoginCmd  `cmd:"" help:"Set up Application Default Credentials for SA impersonation"`
	Whoami AuthWhoamiCmd `cmd:"" help:"Show current user information"`
	Logout AuthLogoutCmd `cmd:"" help:"Delete cached tokens and revoke Application Default Credentials"`
	Status AuthStatusCmd `cmd:"" help:"Show authentication configuration and credential source"`
}

// ConfigCmd contains configuration management subcommands.
type ConfigCmd struct {
	Show ConfigShowCmd `cmd:"" help:"Show each effective setting and where it came from (flag, env or profile)"`

	Profiles ConfigProfilesCmd `cmd:"" aliases:"ls" group:"config-profiles" help:"List profiles, marking the current one"`
	Use      ConfigUseCmd      `cmd:"" group:"config-profiles" help:"Make a profile the current one"`
	Create   ConfigCreateCmd   `cmd:"" group:"config-profiles" help:"Create a profile without prompts (for CI): config create NAME --server URL [--impersonate SA | --credentials KEY | --auth none] [--audience ID]"`
	Delete   ConfigDeleteCmd   `cmd:"" aliases:"rm" group:"config-profiles" help:"Delete a profile"`
	Rename   ConfigRenameCmd   `cmd:"" group:"config-profiles" help:"Rename a profile"`

	// API backend commands.
	SetServer   ConfigSetServerCmd   `cmd:"" group:"config-server" help:"Set the orchestrator server URL"`
	SetAudience ConfigSetAudienceCmd `cmd:"" group:"config-server" help:"Set the ID token audience (a custom IAP OAuth client ID)"`
	SetAuth     ConfigSetAuthCmd     `cmd:"" group:"config-server" help:"Set the client auth mode: auto (credential ladder) or none (network-mode deployment)"`

	// Credentials.
	SetCredentials   ConfigSetCredentialsCmd   `cmd:"" group:"config-credentials" help:"Set path to Google Service Account key JSON"`
	ClearCredentials ConfigClearCredentialsCmd `cmd:"" group:"config-credentials" help:"Clear stored credentials path"`
	SetImpersonate   ConfigSetImpersonateCmd   `cmd:"" group:"config-credentials" help:"Set service account to impersonate"`
	ClearImpersonate ConfigClearImpersonateCmd `cmd:"" group:"config-credentials" help:"Clear service account impersonation"`
}

// HostCmd contains host management subcommands.
type HostCmd struct {
	List   HostListCmd   `cmd:"" aliases:"ls" help:"List all hosts"`
	Get    HostGetCmd    `cmd:"" aliases:"show,info" help:"Get host details"`
	Create HostCreateCmd `cmd:"" aliases:"new" help:"Create a new host VM"`
	Delete HostDeleteCmd `cmd:"" aliases:"rm,remove" help:"Delete a host VM"`
	Start  HostStartCmd  `cmd:"" help:"Start a host VM"`
	Stop   HostStopCmd   `cmd:"" help:"Stop a host VM"`
	Retry  HostRetryCmd  `cmd:"" help:"Re-provision a preempted or failed host in place"`
	Sync   HostSyncCmd   `cmd:"" help:"Reconcile a host against GCE now"`
	Update HostUpdateCmd `cmd:"" aliases:"set" help:"Change how many devices a host accepts"`
	Extend HostExtendCmd `cmd:"" help:"Extend a reserved host's lease"`
	SSH    HostSSHCmd    `cmd:"" aliases:"shell" help:"SSH into a host VM"`
	CVDs   HostCVDsCmd   `cmd:"" name:"cvds" help:"List every CVD on a host, including ones created by cvdr"`
}

// DeviceCmd contains device management subcommands.
type DeviceCmd struct {
	List        DeviceListCmd        `cmd:"" aliases:"ls" help:"List all devices"`
	Get         DeviceGetCmd         `cmd:"" aliases:"show,info" help:"Get device details"`
	Create      DeviceCreateCmd      `cmd:"" aliases:"new" help:"Create a new device"`
	Delete      DeviceDeleteCmd      `cmd:"" aliases:"rm,remove" help:"Delete a device"`
	Start       DeviceStartCmd       `cmd:"" help:"Start a device"`
	Stop        DeviceStopCmd        `cmd:"" help:"Stop a device"`
	Restart     DeviceRestartCmd     `cmd:"" help:"Restart a device's CVD in place"`
	Powerwash   DevicePowerwashCmd   `cmd:"" help:"Reset a device to first-boot state (wipes userdata)"`
	Powerbtn    DevicePowerbtnCmd    `cmd:"" help:"Press the device's power button"`
	Extend      DeviceExtendCmd      `cmd:"" help:"Extend a device's TTL before the reaper reclaims it"`
	Pin         DevicePinCmd         `cmd:"" help:"Fence a device from automatic reclamation"`
	Unpin       DeviceUnpinCmd       `cmd:"" help:"Return a pinned device to normal reclamation"`
	Lifecycle   DeviceLifecycleCmd   `cmd:"" help:"Show a device's TTL, expiry and pin state"`
	Screenshot  DeviceScreenshotCmd  `cmd:"" help:"Capture a display screenshot to a file"`
	Bugreport   DeviceBugreportCmd   `cmd:"" help:"Capture a bugreport to the artifact bucket (and download it)"`
	Record      DeviceRecordCmd      `cmd:"" help:"Manage device screen recordings"`
	Snapshot    DeviceSnapshotCmd    `cmd:"" help:"Manage device snapshots (host-pinned guest state captures)"`
	Display     DeviceDisplayCmd     `cmd:"" help:"Manage a device's displays"`
	Connect     DeviceConnectCmd     `cmd:"" aliases:"adb" help:"Connect to device via ADB"`
	Disconnect  DeviceDisconnectCmd  `cmd:"" help:"Stop background ADB connections"`
	Connections DeviceConnectionsCmd `cmd:"" help:"List background ADB connections"`
	WebRTC      DeviceWebRTCCmd      `cmd:"" name:"webrtc" aliases:"stream,view" help:"Open device WebRTC in browser"`

	// ADBAgent is the process 'connect --background' starts. It is hidden so
	// help and the completion scripts do not offer it.
	ADBAgent DeviceADBAgentCmd `cmd:"" name:"adb-agent" hidden:"" help:"Serve one background ADB tunnel (started by 'device connect --background')"`
}

// DeviceRecordCmd groups the screen-recording subcommands.
type DeviceRecordCmd struct {
	Start DeviceRecordStartCmd `cmd:"" help:"Start a screen recording"`
	Stop  DeviceRecordStopCmd  `cmd:"" help:"Stop a screen recording"`
	List  DeviceRecordListCmd  `cmd:"" aliases:"ls" help:"List host-local screen recordings"`
	Fetch DeviceRecordFetchCmd `cmd:"" help:"Archive a recording to storage and download it"`
}

// DeviceSnapshotCmd groups the snapshot subcommands.
type DeviceSnapshotCmd struct {
	Take    DeviceSnapshotTakeCmd    `cmd:"" aliases:"create" help:"Capture a running device's guest state onto its host"`
	List    DeviceSnapshotListCmd    `cmd:"" aliases:"ls" help:"List a device's snapshots"`
	Delete  DeviceSnapshotDeleteCmd  `cmd:"" aliases:"rm,remove" help:"Delete a snapshot"`
	Restore DeviceSnapshotRestoreCmd `cmd:"" help:"Start a stopped device from a snapshot"`
}

// AdminCmd contains admin subcommands.
type AdminCmd struct {
	Tasks        AdminTasksCmd        `cmd:"" help:"Task-queue operations (dead letters, depth)"`
	Config       AdminConfigCmd       `cmd:"" help:"Platform settings: get/set the system-config keys"`
	Stats        AdminStatsCmd        `cmd:"" help:"Platform statistics: host and device counts by status"`
	Provisioning AdminProvisioningCmd `cmd:"" help:"Recent provisioning outcome counts"`
	Reclaimable  AdminReclaimableCmd  `cmd:"" help:"Pinned, stopped and expiring resources an operator can reclaim"`
	Audit        AdminAuditCmd        `cmd:"" help:"Query the audit log, newest first"`
	ControlPlane AdminControlPlaneCmd `cmd:"" name:"control-plane" help:"One-call snapshot of the background machinery: queue, live work and leadership"`
	Capacity     AdminCapacityCmd     `cmd:"" help:"Where the fleet can grow: the zone board, its verdicts and the regional quota behind them"`

	Zone             AdminZoneCmd             `cmd:"" help:"Curate the placement zones: catalog enablement and capacity cooldowns"`
	MachineType      AdminMachineTypeCmd      `cmd:"" name:"machine-type" help:"Curate the machine-type catalog"`
	CatalogFreshness AdminCatalogFreshnessCmd `cmd:"" name:"catalog-freshness" help:"Catalog snapshot age and the scopes Compute Engine did not report"`
	HostImage        AdminHostImageCmd        `cmd:"" name:"host-image" help:"Curate which GCE image family each machine family boots"`
}

// StatusCmd checks server health.
type StatusCmd struct{}

// CompletionCmd generates shell completion scripts.
type CompletionCmd struct {
	Bash CompletionBashCmd `cmd:"" help:"Generate bash completion script"`
	Zsh  CompletionZshCmd  `cmd:"" help:"Generate zsh completion script"`
	Fish CompletionFishCmd `cmd:"" help:"Generate fish completion script"`

	// Values is the server-backed callback the completion scripts call. It is
	// hidden so the scripts do not offer it.
	Values CompletionValuesCmd `cmd:"" hidden:"" help:"Print completion values for a kind (used by the completion scripts)"`
}

// CompletionBashCmd generates bash completion.
type CompletionBashCmd struct{}

// CompletionZshCmd generates zsh completion.
type CompletionZshCmd struct{}

// CompletionFishCmd generates fish completion.
type CompletionFishCmd struct{}

// Command paths named in more than one of the command maps.
const (
	cmdConfigCreate      = "config create"
	cmdDeviceDisconnect  = "device disconnect"
	cmdDeviceConnections = "device connections"
)

// commandsNotRequiringServer lists commands that don't need a server configured.
var commandsNotRequiringServer = map[string]bool{
	"setup":                    true,
	"config":                   true,
	"config show":              true,
	"config profiles":          true,
	"config use":               true,
	cmdConfigCreate:            true,
	"config delete":            true,
	"config rename":            true,
	"config set-server":        true,
	"config set-audience":      true,
	"config set-auth":          true,
	"config set-credentials":   true,
	"config clear-credentials": true,
	"config set-impersonate":   true,
	"config clear-impersonate": true,
	"completion":               true,
	"completion bash":          true,
	"completion zsh":           true,
	"completion fish":          true,
	"completion values":        true,
	"auth":                     true,
	"auth login":               true,
	"auth logout":              true,
	"auth status":              true,
	cmdDeviceDisconnect:        true,
	cmdDeviceConnections:       true,
}

// CommandContext bounds ctx by the user's --timeout when one is set. main and
// the test harness both use it.
func (c *CLI) CommandContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.Timeout > 0 {
		return context.WithTimeout(ctx, c.Timeout)
	}
	return ctx, func() {}
}

// Hint queues a next-step suggestion for AfterRun.
func (c *CLI) Hint(format string, args ...any) {
	c.hints = append(c.hints, fmt.Sprintf(format, args...))
}

// AfterRun prints the queued next-step hints. It prints nothing under --json.
func (c *CLI) AfterRun() error {
	if c.JSON {
		return nil
	}
	for _, h := range c.hints {
		output.Info("%s", h)
	}
	return nil
}

// commandsWithoutProfile run while the selected profile does not exist: they
// create, list or remove profiles, or touch only local agent state.
var commandsWithoutProfile = map[string]bool{
	"setup":              true,
	"config profiles":    true,
	"config use":         true,
	cmdConfigCreate:      true,
	"config delete":      true,
	"config rename":      true,
	cmdDeviceDisconnect:  true,
	cmdDeviceConnections: true,
}

// agentCommands list and stop background agents. While an old-format file
// stands they run only when that file is in the config directory, as with
// CFCTL_CONFIG_DIR set to the old directory, because they read the config
// directory's connection state and would otherwise miss the old agents.
var agentCommands = map[string]bool{
	cmdDeviceDisconnect:  true,
	cmdDeviceConnections: true,
}

// flagGiven reports whether the named flag was typed on the command line, as
// opposed to filled from its environment variable.
func flagGiven(kctx *kong.Context, name string) bool {
	for _, p := range kctx.Path {
		if p.Flag != nil && !p.Resolved && p.Flag.Name == name {
			return true
		}
	}
	return false
}

// AfterApply is a Kong hook that runs after flag parsing. It resolves the
// effective config into cfg and validates the server configuration.
func (c *CLI) AfterApply(kctx *kong.Context, cfg *config.Config) error {
	output.SetVerbose(c.Verbose)
	cmd := stripArgPlaceholders(kctx.Command())

	// --version and --help exit inside Kong before this hook.
	if err := c.configFileError(cmd); err != nil {
		return err
	}
	res, err := c.resolve(kctx, cmd)
	if err != nil {
		return err
	}
	*cfg = res.Config
	// Commands that never use the server stay quiet; config show prints the
	// same fact in its own output.
	if res.DroppedProfileAuth && (!commandsNotRequiringServer[cmd] || cmd == "auth status") {
		output.Warn("%s is not profile %q's server, so none of the profile's auth settings are used", res.Server, res.Profile)
	}

	// A trailing slash would survive into URLs built by concatenation.
	c.Server = strings.TrimRight(res.Server, "/")
	cfg.Server = c.Server
	if c.Verbose {
		output.Debug("Verbose mode enabled")
		output.Debug("Profile: %s (%s)", cmp.Or(res.Profile, "none"), res.ProfileSource)
		output.Debug("Server: %s (%s)", c.Server, res.Sources.Server)
	}

	if commandsNotRequiringServer[cmd] {
		return nil
	}

	if c.Server == "" {
		return &ServerNotConfiguredError{}
	}

	// This URL carries the credential, so --server and CFCTL_SERVER get the
	// same validation as set-server.
	server, err := normalizeServerURL(c.Server, cfg.AuthNone)
	if err != nil {
		return err
	}
	c.Server, cfg.Server = server, server

	return nil
}

// configFileError fails cmd when the config file did not load. A corrupt file
// fails every command that could act on it with the parse error; an
// old-format file lets through only the commands that migrate away from it.
func (c *CLI) configFileError(cmd string) error {
	if c.ConfigErr == nil || strings.HasPrefix(cmd, "completion") {
		return nil
	}
	legacy, ok := errors.AsType[*config.LegacyError](c.ConfigErr)
	if !ok {
		path, _ := config.Path()
		return fmt.Errorf("%w\nFix %s by hand or delete it, then re-run 'cfctl setup'", c.ConfigErr, path)
	}
	if cmd == cmdConfigCreate {
		return nil
	}
	if dir, err := config.Dir(); err == nil && agentCommands[cmd] && filepath.Clean(dir) == legacy.Dir() {
		return nil
	}
	return legacyConfigError(legacy)
}

// resolve computes the effective config from the file, the flags as typed
// and the environment. An unknown profile fails only the commands that need
// one.
func (c *CLI) resolve(kctx *kong.Context, cmd string) (*config.Resolved, error) {
	in := config.Inputs{LookupEnv: os.LookupEnv}
	if flagGiven(kctx, "profile") {
		in.ProfileFlag = c.Profile
	}
	if flagGiven(kctx, "server") {
		c.serverFlag, c.serverFlagSet = c.Server, true
		in.ServerFlag, in.ServerFlagSet = c.Server, true
	}
	res, err := config.Resolve(c.configFile, in)
	c.resolved, c.profile = res, res.Profile
	if err == nil || strings.HasPrefix(cmd, "completion") {
		return res, nil
	}
	if _, unknown := errors.AsType[*config.UnknownProfileError](err); unknown && commandsWithoutProfile[cmd] {
		return res, nil
	}
	return nil, fmt.Errorf("resolving the configuration: %w", err)
}

// legacyConfigError adds to the refusal the agents the old binary started,
// since this binary looks for agents in the new config directory and would
// not list or stop them.
func legacyConfigError(legacy *config.LegacyError) error {
	agents := legacyAgents(legacy)
	if agents == "" {
		return legacy
	}
	return errors.New(legacy.Error() + "\n\n" + agents)
}

// legacyAgents describes the live agents in the old file's directory and the
// command that stops them, or returns "" when there are none.
func legacyAgents(legacy *config.LegacyError) string {
	connDir := filepath.Join(legacy.Dir(), "connections")
	if _, err := os.Stat(connDir); err != nil {
		return ""
	}
	live, err := connectionStore{dir: connDir}.list()
	if err != nil {
		output.Debug("listing agents in %s: %v", connDir, err)
	}
	if len(live) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Background ADB agents started from %s are still running:\n", legacy.Dir())
	for _, st := range live {
		fmt.Fprintf(&b, "  pid %d  device %s  instance %s  %s\n", st.PID, st.DeviceID, st.Instance, st.Address)
	}
	fmt.Fprintf(&b, "Stop them with:\n  %s=%s cfctl device disconnect --all", config.DirEnv, legacy.Dir())
	return b.String()
}

// ServerNotConfiguredError is returned when server is not configured.
type ServerNotConfiguredError struct{}

func (e *ServerNotConfiguredError) Error() string {
	return `no server configured

Set server with:
  cfctl config create NAME --server URL    (or 'cfctl setup')
  cfctl --profile NAME ...                 to use an existing profile
  --server URL or CFCTL_SERVER             for one command or shell`
}

// passthroughCommand returns a passthrough arg slice without the leading "--"
// separator, which Kong keeps for passthrough positionals.
func passthroughCommand(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}

// stripArgPlaceholders removes positional argument placeholders such as
// "<url>" from a Kong command path, leaving only the command names.
func stripArgPlaceholders(cmd string) string {
	parts := strings.Fields(cmd)
	filtered := parts[:0]
	for _, p := range parts {
		if !strings.HasPrefix(p, "<") {
			filtered = append(filtered, p)
		}
	}
	return strings.Join(filtered, " ")
}
