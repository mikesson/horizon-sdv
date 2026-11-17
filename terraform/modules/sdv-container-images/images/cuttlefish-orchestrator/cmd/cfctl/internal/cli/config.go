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
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// normalizeServerURL validates a server URL and strips trailing slashes, which
// would otherwise end up inside built URLs such as the device player link.
// Plain http would expose the credential, so it is allowed only for a loopback
// host or when authNone means no credential is sent.
func normalizeServerURL(raw string, authNone bool) (string, error) {
	s := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("invalid server URL %q (expected https://host)", raw)
	}
	if u.Scheme == "http" && !authNone && !isLoopbackHost(u.Hostname()) {
		return "", fmt.Errorf("server URL %q uses http, which would send the credential in clear text: use https, or 'cfctl config set-auth none' for a network-mode deployment", raw)
	}
	return s, nil
}

// isLoopbackHost reports whether host is localhost or a loopback IP address.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// resolveKeyFile converts a service-account key path to an absolute path and
// verifies the file exists.
func resolveKeyFile(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve path: %w", err)
	}
	if _, err := os.Stat(absPath); err != nil {
		return "", fmt.Errorf("credentials file not found: %s", absPath)
	}
	return absPath, nil
}

// updateConfig applies fn to the config file and wraps the error.
func updateConfig(fn func(f *config.File) error) error {
	if err := config.Update(fn); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	return nil
}

// updateProfile applies fn to the selected profile and returns its name. With
// no profile selected it writes the default profile, creating it and making
// it current as setup does, so a first run can start with set-server.
func (c *CLI) updateProfile(fn func(p *config.Config) error) (string, error) {
	name, implicit := c.profile, c.profile == ""
	if implicit {
		name = defaultProfileName
	}
	err := updateConfig(func(f *config.File) error {
		p, ok := f.Profiles[name]
		if !ok && !implicit {
			return fmt.Errorf("profile %q does not exist", name)
		}
		if err := fn(&p); err != nil {
			return err
		}
		f.Set(name, p)
		if implicit && f.CurrentProfile == "" {
			f.CurrentProfile = name
		}
		return nil
	})
	return name, err
}

// ConfigShowCmd implements 'cfctl config show'.
type ConfigShowCmd struct{}

// configShowValue is one effective setting and the layer it came from.
type configShowValue struct {
	Value  any           `json:"value"`
	Source config.Source `json:"source"`
}

// configShowResult is the --json shape of 'cfctl config show'.
type configShowResult struct {
	Path                      string          `json:"path"`
	Profile                   configShowValue `json:"profile"`
	Server                    configShowValue `json:"server"`
	IAPAudience               configShowValue `json:"iap_audience"`
	AuthNone                  configShowValue `json:"auth_none"`
	ProxyEmail                configShowValue `json:"proxy_email"`
	CredentialsPath           configShowValue `json:"credentials_path"`
	ImpersonateServiceAccount configShowValue `json:"impersonate_service_account"`
	// DroppedProfileAuth reports that the server is not the profile's, so the
	// profile's auth settings were ignored.
	DroppedProfileAuth bool `json:"dropped_profile_auth,omitempty"`
}

// Run prints each effective value and its source.
func (c *ConfigShowCmd) Run(cli *CLI) error {
	path, _ := config.Path()
	r := cli.resolved
	res := configShowResult{
		Path:                      path,
		Profile:                   configShowValue{r.Profile, r.ProfileSource},
		Server:                    configShowValue{cli.Server, r.Sources.Server},
		IAPAudience:               configShowValue{r.IAPAudience, r.Sources.IAPAudience},
		AuthNone:                  configShowValue{r.AuthNone, r.Sources.AuthNone},
		ProxyEmail:                configShowValue{r.ProxyEmail, r.Sources.ProxyEmail},
		CredentialsPath:           configShowValue{r.CredentialsPath, r.Sources.CredentialsPath},
		ImpersonateServiceAccount: configShowValue{r.ImpersonateServiceAccount, r.Sources.ImpersonateServiceAccount},
		DroppedProfileAuth:        r.DroppedProfileAuth,
	}
	if cli.JSON {
		return printJSON(res)
	}

	fmt.Printf("Configuration file: %s\n\n", path)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SETTING\tVALUE\tSOURCE")
	for _, row := range []struct {
		name string
		v    configShowValue
	}{
		{"profile", res.Profile},
		{"server", res.Server},
		{"iap_audience", res.IAPAudience},
		{"auth_none", res.AuthNone},
		{"proxy_email", res.ProxyEmail},
		{"credentials_path", res.CredentialsPath},
		{"impersonate_service_account", res.ImpersonateServiceAccount},
	} {
		value := fmt.Sprint(row.v.Value)
		if value == "" {
			value = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", row.name, value, row.v.Source)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("writing output: %w", err)
	}
	if r.DroppedProfileAuth {
		fmt.Println()
		fmt.Printf("The server is not profile %q's, so none of its auth settings are used.\n", r.Profile)
	}
	return nil
}

// ConfigProfilesCmd implements 'cfctl config profiles'.
type ConfigProfilesCmd struct{}

// configProfileRow is one entry of 'cfctl config profiles --json'.
type configProfileRow struct {
	Name    string `json:"name"`
	Current bool   `json:"current"`
	Server  string `json:"server"`
	Auth    string `json:"auth"`
}

// Run lists the profiles. The current mark is the stored current_profile;
// --profile and CFCTL_PROFILE do not move it.
func (c *ConfigProfilesCmd) Run(cli *CLI) error {
	f := cli.configFile
	names := f.Names()
	rows := make([]configProfileRow, 0, len(names))
	for _, name := range names {
		p := f.Profiles[name]
		rows = append(rows, configProfileRow{
			Name: name, Current: name == f.CurrentProfile, Server: p.Server, Auth: profileAuthSummary(p),
		})
	}
	if cli.JSON {
		return printJSON(rows)
	}
	if len(rows) == 0 {
		output.Info("No profiles. Create one with 'cfctl config create NAME --server URL' or 'cfctl setup'.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CURRENT\tNAME\tSERVER\tAUTH")
	for _, r := range rows {
		mark := ""
		if r.Current {
			mark = "*"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", mark, r.Name, r.Server, r.Auth)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("writing output: %w", err)
	}
	return nil
}

// profileAuthSummary names a profile's auth settings in one short phrase.
func profileAuthSummary(p config.Config) string {
	var parts []string
	switch {
	case p.AuthNone:
		return valueNone
	case p.ImpersonateServiceAccount != "":
		parts = append(parts, "impersonate "+p.ImpersonateServiceAccount)
	case p.CredentialsPath != "":
		parts = append(parts, "key "+p.CredentialsPath)
	default:
		parts = append(parts, "adc")
	}
	if p.IAPAudience != "" {
		parts = append(parts, "audience "+p.IAPAudience)
	}
	return strings.Join(parts, ", ")
}

// ConfigUseCmd implements 'cfctl config use'.
type ConfigUseCmd struct {
	Name string `arg:"" placeholder:"NAME" help:"Profile to make current"`
}

// Run stores the profile as current_profile.
func (c *ConfigUseCmd) Run() error {
	err := updateConfig(func(f *config.File) error {
		if _, ok := f.Profiles[c.Name]; !ok {
			return &config.UnknownProfileError{Name: c.Name, Known: f.Names()}
		}
		f.CurrentProfile = c.Name
		return nil
	})
	if err != nil {
		return err
	}
	output.Success("Now using profile %q", c.Name)
	if v, ok := os.LookupEnv(config.EnvProfile); ok && v != "" && v != c.Name {
		output.Warn("%s=%s still selects %q in this shell", config.EnvProfile, v, v)
	}
	return nil
}

// ConfigCreateCmd implements 'cfctl config create'. The server comes from
// the global --server flag; CFCTL_SERVER is never stored.
type ConfigCreateCmd struct {
	Name        string `arg:"" placeholder:"NAME" help:"Profile name: letters, digits, '.', '_' and '-'"`
	Impersonate string `xor:"identity" placeholder:"SA_EMAIL" help:"Service account your own credentials impersonate"`
	Credentials string `xor:"identity" placeholder:"KEY_FILE" help:"Service-account key JSON file to sign with"`
	Audience    string `placeholder:"CLIENT_ID" help:"ID token audience, for a custom IAP OAuth client only"`
	Auth        string `enum:"auto,none" default:"auto" help:"auto = the credential ladder; none = send no credentials, for a network-mode deployment"`
}

// Run adds the profile. The first profile becomes the current one.
func (c *ConfigCreateCmd) Run(cli *CLI) error {
	if err := config.ValidateName(c.Name); err != nil {
		return fmt.Errorf("creating profile: %w", err)
	}
	if !cli.serverFlagSet {
		return fmt.Errorf("--server is required: cfctl config create %s --server URL", c.Name)
	}
	authNone := c.Auth == "none"
	if authNone && (c.Impersonate != "" || c.Credentials != "" || c.Audience != "") {
		return errors.New("--auth none sends no credentials; drop --impersonate, --credentials and --audience")
	}
	server, err := normalizeServerURL(cli.serverFlag, authNone)
	if err != nil {
		return err
	}
	p := config.Config{
		Server: server, AuthNone: authNone, IAPAudience: c.Audience, ImpersonateServiceAccount: c.Impersonate,
	}
	if c.Credentials != "" {
		if p.CredentialsPath, err = resolveKeyFile(c.Credentials); err != nil {
			return err
		}
	}

	current := false
	err = updateConfig(func(f *config.File) error {
		if _, ok := f.Profiles[c.Name]; ok {
			return fmt.Errorf("profile %q already exists; change it with 'cfctl --profile %s config set-...' or delete it first", c.Name, c.Name)
		}
		f.Set(c.Name, p)
		if f.CurrentProfile == "" {
			f.CurrentProfile, current = c.Name, true
		}
		return nil
	})
	if err != nil {
		return err
	}
	output.Success("Created profile %q for %s", c.Name, server)
	if current {
		output.Info("It is the current profile.")
	} else {
		cli.Hint("Select it with 'cfctl config use %s', or per command with --profile %s", c.Name, c.Name)
	}
	warnLegacyAgents(cli.ConfigErr)
	return nil
}

// warnLegacyAgents reports agents the old binary left running when the config
// failed to load as an old-format file. Once config create writes the new
// file the old one is never read again, so this is the last chance.
func warnLegacyAgents(configErr error) {
	if legacy, ok := errors.AsType[*config.LegacyError](configErr); ok {
		if agents := legacyAgents(legacy); agents != "" {
			output.Warn("%s", agents)
		}
	}
}

// ConfigDeleteCmd implements 'cfctl config delete'.
type ConfigDeleteCmd struct {
	Name string `arg:"" placeholder:"NAME" help:"Profile to delete"`
}

// Run removes the profile, and clears current_profile when it named it.
func (c *ConfigDeleteCmd) Run() error {
	err := updateConfig(func(f *config.File) error {
		if _, ok := f.Profiles[c.Name]; !ok {
			return &config.UnknownProfileError{Name: c.Name, Known: f.Names()}
		}
		delete(f.Profiles, c.Name)
		if f.CurrentProfile == c.Name {
			f.CurrentProfile = ""
		}
		return nil
	})
	if err != nil {
		return err
	}
	output.Success("Deleted profile %q", c.Name)
	return nil
}

// ConfigRenameCmd implements 'cfctl config rename'.
type ConfigRenameCmd struct {
	Old string `arg:"" placeholder:"OLD" help:"Current profile name"`
	New string `arg:"" placeholder:"NEW" help:"New profile name"`
}

// Run renames the profile, keeping it current if it was.
func (c *ConfigRenameCmd) Run() error {
	if err := config.ValidateName(c.New); err != nil {
		return fmt.Errorf("renaming profile: %w", err)
	}
	err := updateConfig(func(f *config.File) error {
		p, ok := f.Profiles[c.Old]
		if !ok {
			return &config.UnknownProfileError{Name: c.Old, Known: f.Names()}
		}
		if _, taken := f.Profiles[c.New]; taken {
			return fmt.Errorf("profile %q already exists", c.New)
		}
		delete(f.Profiles, c.Old)
		f.Set(c.New, p)
		if f.CurrentProfile == c.Old {
			f.CurrentProfile = c.New
		}
		return nil
	})
	if err != nil {
		return err
	}
	output.Success("Renamed profile %q to %q", c.Old, c.New)
	return nil
}

// ConfigSetServerCmd implements 'cfctl config set-server'.
type ConfigSetServerCmd struct {
	URL string `arg:"" help:"The orchestrator API server URL (e.g., https://api.cuttlefish.example.com)"`
}

// Run executes the config set-server command.
func (c *ConfigSetServerCmd) Run(cli *CLI) error {
	var server string
	name, err := cli.updateProfile(func(p *config.Config) error {
		var err error
		server, err = normalizeServerURL(c.URL, p.AuthNone)
		p.Server = server
		return err
	})
	if err != nil {
		return err
	}
	output.Success("Profile %q server: %s", name, server)
	return nil
}

// ConfigSetAuthCmd implements 'cfctl config set-auth'.
type ConfigSetAuthCmd struct {
	Mode string `arg:"" enum:"auto,none" help:"auto = the credential ladder (default); none = send no credentials, for a network-mode deployment"`
}

// Run executes the config set-auth command.
func (c *ConfigSetAuthCmd) Run(cli *CLI) error {
	authNone := c.Mode == "none"
	name, err := cli.updateProfile(func(p *config.Config) error {
		p.AuthNone = authNone
		return nil
	})
	if err != nil {
		return err
	}

	if authNone {
		output.Success("Profile %q auth mode: none (no credentials will be sent)", name)
	} else {
		output.Success("Profile %q auth mode: auto (credential ladder)", name)
	}
	return nil
}

// ConfigSetAudienceCmd implements 'cfctl config set-audience'.
type ConfigSetAudienceCmd struct {
	Audience string `arg:"" help:"ID token audience: a custom IAP OAuth client ID (unset = self-signed JWT for built-in IAP)"`
}

// Run executes the config set-audience command.
func (c *ConfigSetAudienceCmd) Run(cli *CLI) error {
	name, err := cli.updateProfile(func(p *config.Config) error {
		p.IAPAudience = c.Audience
		return nil
	})
	if err != nil {
		return err
	}

	output.Success("Profile %q IAP audience: %s", name, c.Audience)
	return nil
}

// ConfigSetCredentialsCmd implements 'cfctl config set-credentials'.
type ConfigSetCredentialsCmd struct {
	Path string `arg:"" help:"Path to Google Service Account key JSON file"`
}

// Run executes the config set-credentials command.
func (c *ConfigSetCredentialsCmd) Run(cli *CLI) error {
	absPath, err := resolveKeyFile(c.Path)
	if err != nil {
		return err
	}
	name, err := cli.updateProfile(func(p *config.Config) error {
		p.CredentialsPath = absPath
		return nil
	})
	if err != nil {
		return err
	}

	output.Success("Profile %q credentials file: %s", name, absPath)
	fmt.Println()
	output.Info("Run 'cfctl auth whoami' to verify authentication.")
	return nil
}

// ConfigClearCredentialsCmd implements 'cfctl config clear-credentials'.
type ConfigClearCredentialsCmd struct{}

// Run executes the config clear-credentials command.
func (c *ConfigClearCredentialsCmd) Run(cli *CLI) error {
	name, err := cli.updateProfile(func(p *config.Config) error {
		p.CredentialsPath = ""
		return nil
	})
	if err != nil {
		return err
	}

	output.Success("Profile %q credentials cleared", name)
	output.Info("The CLI will now use Application Default Credentials (e.g. Workload Identity Federation or gcloud auth).")
	return nil
}

// ConfigSetImpersonateCmd implements 'cfctl config set-impersonate'.
type ConfigSetImpersonateCmd struct {
	ServiceAccount string `arg:"" help:"Service account email to impersonate (e.g., cco-cli@project.iam.gserviceaccount.com)"`
}

// Run executes the config set-impersonate command.
func (c *ConfigSetImpersonateCmd) Run(cli *CLI) error {
	name, err := cli.updateProfile(func(p *config.Config) error {
		p.ImpersonateServiceAccount = c.ServiceAccount
		return nil
	})
	if err != nil {
		return err
	}

	output.Success("Profile %q impersonates: %s", name, c.ServiceAccount)
	fmt.Println()
	output.Info("Your credentials will be used to impersonate this service account.")
	output.Info("Ensure you have the 'roles/iam.serviceAccountTokenCreator' role on the target SA.")
	return nil
}

// ConfigClearImpersonateCmd implements 'cfctl config clear-impersonate'.
type ConfigClearImpersonateCmd struct{}

// Run executes the config clear-impersonate command.
func (c *ConfigClearImpersonateCmd) Run(cli *CLI) error {
	name, err := cli.updateProfile(func(p *config.Config) error {
		p.ImpersonateServiceAccount = ""
		return nil
	})
	if err != nil {
		return err
	}

	output.Success("Profile %q impersonation cleared", name)
	output.Info("The CLI will now authenticate directly with your credentials.")
	return nil
}
