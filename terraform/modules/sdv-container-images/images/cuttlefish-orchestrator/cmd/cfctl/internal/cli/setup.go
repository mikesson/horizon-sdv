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
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/oauth2/google"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// defaultProfileName is the profile setup writes when none is selected.
const defaultProfileName = "default"

// SetupCmd implements 'cfctl setup', the interactive onboarding wizard. It asks
// for the server, the audience and the identity, and writes them to the
// selected profile (--profile), creating it if needed. A key-file argument
// skips the identity question and configures key-file signing.
type SetupCmd struct {
	KeyFile string `arg:"" optional:"" help:"Path to a Google Service Account key JSON file (skips the identity question)"`

	// in overrides os.Stdin so tests can drive the prompts.
	in io.Reader
}

func (c *SetupCmd) stdin() io.Reader {
	if c.in != nil {
		return c.in
	}
	return os.Stdin
}

// Run executes the setup wizard.
func (c *SetupCmd) Run(ctx context.Context, cli *CLI) error {
	output.Info("Cloud Cuttlefish Orchestrator CLI Setup")
	fmt.Println()

	r := bufio.NewReader(c.stdin())

	// cli.Server holds the existing config or --server value, offered as the
	// default so a re-run keeps it with a bare Enter.
	serverURL, err := promptServerURL(r, cli.Server)
	if err != nil {
		return err
	}

	// An empty audience selects the self-signed JWT used by built-in Cloud Run
	// IAP.
	audience, err := promptLine(r,
		"Token audience (Enter for built-in Cloud Run IAP; set only for a custom IAP OAuth client): ")
	if err != nil {
		return err
	}

	keyPath, saEmail, err := c.collectIdentity(ctx, r)
	if err != nil {
		return err
	}

	// The identity not chosen is cleared so it cannot outrank the chosen one in
	// the auth ladder. AuthNone would suppress the chosen identity entirely.
	name := cmp.Or(cli.profile, defaultProfileName)
	output.Info("Saving profile %q...", name)
	if err := updateConfig(func(f *config.File) error {
		f.Set(name, config.Config{
			Server:                    serverURL,
			IAPAudience:               audience,
			CredentialsPath:           keyPath,
			ImpersonateServiceAccount: saEmail,
		})
		if f.CurrentProfile == "" {
			f.CurrentProfile = name
		}
		return nil
	}); err != nil {
		return err
	}
	output.Success("Configuration saved!")
	fmt.Println()

	// Test the saved profile with the same resolution and client
	// construction every command uses.
	output.Info("Testing connection...")
	file, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to reload config: %w", err)
	}
	res, err := config.Resolve(file, config.Inputs{
		ProfileFlag: name, ServerFlag: serverURL, ServerFlagSet: true, LookupEnv: os.LookupEnv,
	})
	if err != nil {
		return fmt.Errorf("failed to resolve profile %q: %w", name, err)
	}
	client, err := cco.NewClient(ctx, serverURL, &res.Config, 0)
	if err != nil {
		output.Warn("Could not create API client: %v", err)
		output.Info("Setup complete, but authentication is not working yet (see above).")
		return nil
	}
	user, err := client.Me(ctx)
	if err != nil {
		output.Warn("Authentication check failed: %v", err)
		output.Info("Setup complete, but the credentials could not be verified.")
		if saEmail != "" {
			output.Info("Common causes: %s is not on the deployment's access list, or you lack roles/iam.serviceAccountTokenCreator on it.", saEmail)
		}
		return nil
	}

	output.Success("Authentication successful!")
	output.Info("Logged in as: %s", user.Email)
	fmt.Println()
	output.Success("Setup complete! You can now use cfctl.")
	return nil
}

// collectIdentity resolves the signing identity from the key-file argument, or
// asks with impersonation as the default. It returns a key path or a service
// account email, never both.
func (c *SetupCmd) collectIdentity(ctx context.Context, r *bufio.Reader) (string, string, error) {
	if c.KeyFile != "" {
		keyPath, err := resolveKeyFile(c.KeyFile)
		if err != nil {
			return "", "", err
		}
		output.Success("Found key file: %s", keyPath)
		return keyPath, "", nil
	}

	method, err := promptChoice(r, `How should cfctl authenticate?
  1) Impersonate a service account with your Google account (recommended)
  2) Sign locally with a service-account key file (CI / automation)
Choice [1]: `, 2, 1)
	if err != nil {
		return "", "", err
	}
	if method == 1 {
		saEmail, err := c.setupImpersonation(ctx, r)
		return "", saEmail, err
	}

	path, err := promptLine(r, "Path to the service-account key file: ")
	if err != nil {
		return "", "", err
	}
	keyPath, err := resolveKeyFile(path)
	if err != nil {
		return "", "", err
	}
	output.Success("Found key file: %s", keyPath)
	return keyPath, "", nil
}

// setupImpersonation collects the impersonation identity. It checks for
// usable Application Default Credentials, offering to run the gcloud login
// inline when they are missing, then asks which service account to sign as.
func (c *SetupCmd) setupImpersonation(ctx context.Context, r *bufio.Reader) (string, error) {
	if _, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform"); err != nil {
		if err := offerADCLogin(ctx, r); err != nil {
			return "", err
		}
	}

	for {
		sa, err := promptLine(r, "Service account to impersonate (ask your operator; e.g. cco-cli@PROJECT.iam.gserviceaccount.com): ")
		if err != nil {
			return "", err
		}
		if strings.Contains(sa, "@") {
			return sa, nil
		}
		output.Warn("%q does not look like a service-account email.", sa)
	}
}

// offerADCLogin tells the user their ADC is missing and offers to run the
// gcloud login right here. Declining or a failed gcloud never aborts the
// wizard. The identity still saves, and 'cfctl auth login' fixes it later.
func offerADCLogin(ctx context.Context, r *bufio.Reader) error {
	output.Info("No Application Default Credentials found. Impersonation signs with your own Google account.")
	yes, err := promptYesNo(r, "Run 'gcloud auth application-default login' now? [Y/n]: ", true)
	if err != nil {
		return err
	}
	if !yes {
		output.Info("Continuing without ADC; run 'cfctl auth login' before using cfctl.")
		return nil
	}
	if err := runGcloudADCLogin(ctx); err != nil {
		output.Warn("gcloud login failed: %v", err)
		output.Info("Continuing; run 'cfctl auth login' once gcloud is available.")
	}
	return nil
}

// promptServerURL asks for the orchestrator URL, offering def (existing
// config or --server) as the Enter default, and normalizes the answer.
func promptServerURL(r *bufio.Reader, def string) (string, error) {
	prompt := "Orchestrator URL (https://SERVICE.run.app): "
	if def != "" {
		prompt = fmt.Sprintf("Orchestrator URL [%s]: ", def)
	}
	raw, err := promptLine(r, prompt)
	if err != nil {
		return "", err
	}
	if raw == "" {
		if def == "" {
			return "", errors.New("a server URL is required")
		}
		raw = def
	}
	// Setup always configures a credential, so plain http is loopback-only.
	return normalizeServerURL(raw, false)
}

// promptLine prints prompt and returns one trimmed input line.
func promptLine(r *bufio.Reader, prompt string) (string, error) {
	fmt.Print(prompt)
	input, err := r.ReadString('\n')
	if err != nil && input == "" {
		return "", fmt.Errorf("failed to read input: %w", err)
	}
	return strings.TrimSpace(input), nil
}

// promptChoice returns a 1-based menu selection, def on a bare Enter.
func promptChoice(r *bufio.Reader, prompt string, options, def int) (int, error) {
	for {
		raw, err := promptLine(r, prompt)
		if err != nil {
			return 0, err
		}
		if raw == "" {
			return def, nil
		}
		if len(raw) == 1 && raw[0] >= '1' && int(raw[0]-'0') <= options {
			return int(raw[0] - '0'), nil
		}
		output.Warn("Enter a number between 1 and %d.", options)
	}
}

// promptYesNo returns the user's yes/no answer, def on a bare Enter.
func promptYesNo(r *bufio.Reader, prompt string, def bool) (bool, error) {
	raw, err := promptLine(r, prompt)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(raw) {
	case "":
		return def, nil
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// Help augments setup's help with what the wizard decides.
func (c *SetupCmd) Help() string {
	return `Interactively writes one profile of the config file with the server URL and
how to authenticate: service-account impersonation via your own Google login
(the default; offers to run 'gcloud auth application-default login' inline)
or a service-account key file passed as the argument, which skips the method
question. Ends with a whoami round-trip so a broken setup fails here, not
on your first real command.

The profile is the one --profile names, else the current one, else
"default". A new profile becomes current only when none is current yet. For
CI, 'cfctl config create' writes a profile without prompts.

Diagnose an existing setup with 'cfctl doctor'.`
}
