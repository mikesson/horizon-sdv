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
	"cmp"
	"context"
	"fmt"
	"os"
	"os/exec"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/auth"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// AuthLoginCmd implements 'cfctl auth login'.
type AuthLoginCmd struct{}

// Run executes the login command.
func (c *AuthLoginCmd) Run(ctx context.Context, cfg *config.Config) error {
	output.Info("Setting up Application Default Credentials via gcloud...")

	if err := runGcloudADCLogin(ctx); err != nil {
		return err
	}

	output.Success("Application Default Credentials configured!")
	fmt.Println()

	if cfg.ImpersonateServiceAccount == "" {
		output.Info("To complete setup, configure SA impersonation:")
		fmt.Println("  cfctl config set-impersonate <sa-email>")
		fmt.Println()
		output.Info("The service account must:")
		fmt.Println("  - Be on the IAP access list (iap_members / roles/iap.httpsResourceAccessor)")
		fmt.Println("  - Grant you roles/iam.serviceAccountTokenCreator")
	} else {
		output.Info("SA impersonation is configured: %s", cfg.ImpersonateServiceAccount)
		output.Info("Run 'cfctl auth whoami' to verify authentication.")
	}

	return nil
}

// runGcloudADCLogin runs the interactive gcloud ADC login on the caller's
// terminal. Shared by 'auth login' and the setup wizard.
func runGcloudADCLogin(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "gcloud", "auth", "application-default", "login")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run gcloud login: %w", err)
	}
	return nil
}

// AuthWhoamiCmd implements 'cfctl auth whoami'.
type AuthWhoamiCmd struct{}

// Run executes the whoami command.
func (c *AuthWhoamiCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	user, err := client.Me(ctx)
	if err != nil {
		return fmt.Errorf("failed to get user info: %w", err)
	}

	if cli.JSON {
		return printJSON(user)
	}
	fmt.Printf("Email: %s\n", user.Email)
	if user.DisplayName != "" {
		fmt.Printf("Name:  %s\n", user.DisplayName)
	}
	return nil
}

// AuthLogoutCmd implements 'cfctl auth logout'.
type AuthLogoutCmd struct {
	All bool `help:"Revoke all Application Default Credentials"`
}

// Run executes the logout command. The cached signed tokens go first, so
// they are gone even when the gcloud revoke fails or is declined.
func (c *AuthLogoutCmd) Run(ctx context.Context) error {
	if err := auth.ClearTokenCache(); err != nil {
		return fmt.Errorf("clearing cached tokens: %w", err)
	}
	output.Info("Revoking Application Default Credentials...")

	// gcloud prompts for confirmation before revoking.
	cmd := exec.CommandContext(ctx, "gcloud", "auth", "application-default", "revoke")
	if c.All {
		cmd.Args = append(cmd.Args, "--all")
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run gcloud logout: %w", err)
	}

	output.Success("Application Default Credentials revoked")
	return nil
}

// AuthStatusCmd implements 'cfctl auth status'.
type AuthStatusCmd struct{}

// authStatusResult is the --json shape of 'cfctl auth status'. An
// unconfigured server or audience is an empty string.
type authStatusResult struct {
	Profile    string       `json:"profile,omitempty"`
	Server     string       `json:"server,omitempty"`
	Audience   string       `json:"audience,omitempty"`
	Method     string       `json:"method"`
	Credential string       `json:"credential,omitempty"`
	Signer     *auth.Signer `json:"signer,omitempty"`
}

// Run prints the resolved authentication mode. It calls the same auth.Resolve
// as the API client, so the two always agree.
func (c *AuthStatusCmd) Run(ctx context.Context, cli *CLI, cfg *config.Config) error {
	mode := auth.Resolve(cfg)
	var signer *auth.Signer
	if s, ok := auth.SignerFor(ctx, cfg, auth.DetectSigner); ok {
		signer = &s
	}

	if cli.JSON {
		return printJSON(authStatusResult{
			Profile:    cli.profile,
			Server:     cli.Server,
			Audience:   cfg.IAPAudience,
			Method:     string(mode.Method),
			Credential: mode.Detail,
			Signer:     signer,
		})
	}

	// cli.Server respects --server and CFCTL_SERVER as well as the config file.
	server := cli.Server
	if server == "" {
		server = "(not configured)"
	}
	audience := cfg.IAPAudience
	if audience == "" {
		audience = "(none: built-in Cloud Run IAP, self-signed SA JWT)"
	}

	fmt.Println("Authentication Status:")
	fmt.Println()
	fmt.Printf("  Profile:    %s\n", cmp.Or(cli.profile, "(none)"))
	fmt.Printf("  API Server: %s\n", server)
	fmt.Printf("  Audience:   %s\n", audience)
	fmt.Println()
	fmt.Printf("  Method:     %s\n", mode.Method)
	if mode.Detail != "" {
		fmt.Printf("  Credential: %s\n", mode.Detail)
	}
	if signer != nil {
		fmt.Printf("  Signer:     %s\n", signer.Description)
	}

	if mode.Method == auth.MethodUnconfigured {
		fmt.Println()
		fmt.Println("  No service account is configured, so requests will fail. Run")
		fmt.Println("  'cfctl config set-impersonate <sa-email>' or 'cfctl config set-credentials <key.json>'.")
	}

	return nil
}
