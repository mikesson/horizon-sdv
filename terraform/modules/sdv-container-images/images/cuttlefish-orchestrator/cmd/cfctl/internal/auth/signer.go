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

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// Signer is the principal whose credentials sign or mint cfctl's token. With
// impersonation that is the Application Default Credentials identity, which
// needs roles/iam.serviceAccountTokenCreator on the target service account.
type Signer struct {
	// Email is the principal's email, empty when it could not be read.
	Email string `json:"email,omitempty"`
	// Member is Email as an IAM member, such as serviceAccount:EMAIL.
	Member string `json:"member,omitempty"`
	// Description names the signer for a person.
	Description string `json:"description"`
	// VM reports a Compute Engine VM's attached service account.
	VM bool `json:"vm,omitempty"`
}

// SignerDetector reads the signing principal of the default credentials.
type SignerDetector func(ctx context.Context) Signer

const (
	cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"
	// signerDetectTimeout bounds the metadata and tokeninfo reads. They run
	// only on an error path or for a status display.
	signerDetectTimeout = 5 * time.Second
)

// DetectSigner reads the principal behind the Application Default
// Credentials: the VM's account from the metadata server on Compute Engine,
// client_email for a key file, the impersonated account for an
// external_account or impersonated_service_account file, and the gcloud user
// from one tokeninfo call for an authorized_user file.
func DetectSigner(ctx context.Context) Signer {
	ctx, cancel := context.WithTimeout(ctx, signerDetectTimeout)
	defer cancel()
	creds, err := google.FindDefaultCredentials(ctx, cloudPlatformScope)
	if err != nil {
		output.Debug("auth: detecting the signer: %v", err)
		return Signer{Description: "your Application Default Credentials (none were found)"}
	}
	if len(creds.JSON) == 0 {
		email, err := metadataServiceAccountEmail(ctx)
		if err != nil {
			output.Debug("auth: reading the VM's service account: %v", err)
			return Signer{Description: "this VM's attached service account", VM: true}
		}
		return Signer{Email: email, Member: "serviceAccount:" + email, Description: "this VM's service account " + email, VM: true}
	}
	return signerFromJSON(ctx, creds.JSON, creds.TokenSource)
}

// signerFromJSON reads the signer from a credentials file. ts is used only
// for an authorized_user file.
func signerFromJSON(ctx context.Context, data []byte, ts oauth2.TokenSource) Signer {
	var f struct {
		Type                           string `json:"type"`
		ClientEmail                    string `json:"client_email"`
		ServiceAccountImpersonationURL string `json:"service_account_impersonation_url"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return Signer{Description: "your Application Default Credentials"}
	}
	switch f.Type {
	case "service_account":
		if f.ClientEmail != "" {
			return Signer{Email: f.ClientEmail, Member: "serviceAccount:" + f.ClientEmail, Description: "the key file's service account " + f.ClientEmail}
		}
	case "external_account", "impersonated_service_account":
		if sa := impersonationTarget(f.ServiceAccountImpersonationURL); sa != "" {
			return Signer{Email: sa, Member: "serviceAccount:" + sa, Description: "the impersonated service account " + sa}
		}
		return Signer{Description: "the federated identity"}
	case "authorized_user":
		if email := tokenInfoEmail(ctx, ts); email != "" {
			return Signer{Email: email, Member: "user:" + email, Description: "your gcloud ADC user " + email}
		}
		return Signer{Description: "your gcloud ADC user"}
	}
	return Signer{Description: "your Application Default Credentials"}
}

// impersonationTarget extracts SA from
// .../serviceAccounts/SA:generateAccessToken, or returns "".
func impersonationTarget(raw string) string {
	_, rest, ok := strings.Cut(raw, "/serviceAccounts/")
	if !ok {
		return ""
	}
	sa, _, _ := strings.Cut(rest, ":")
	sa, err := url.PathUnescape(sa)
	if err != nil || !strings.Contains(sa, "@") {
		return ""
	}
	return sa
}

// metadataEmailURL is the Compute Engine metadata entry for the VM's default
// service account.
const metadataEmailURL = "http://169.254.169.254/computeMetadata/v1/instance/service-accounts/default/email"

// metadataServiceAccountEmail reads the default service account of the VM
// cfctl runs on.
func metadataServiceAccountEmail(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataEmailURL, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("building the metadata request: %w", err)
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("querying the metadata server: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", fmt.Errorf("reading the metadata response: %w", err)
	}
	email := strings.TrimSpace(string(body))
	if resp.StatusCode != http.StatusOK || !strings.Contains(email, "@") {
		return "", fmt.Errorf("metadata server answered %d", resp.StatusCode)
	}
	return email, nil
}

// tokenInfoEmail asks Google's tokeninfo endpoint whose access token ts
// mints, or returns "" when it cannot tell.
func tokenInfoEmail(ctx context.Context, ts oauth2.TokenSource) string {
	if ts == nil {
		return ""
	}
	tok, err := ts.Token()
	if err != nil {
		output.Debug("auth: minting a token for tokeninfo: %v", err)
		return ""
	}
	form := url.Values{"access_token": {tok.AccessToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/tokeninfo",
		strings.NewReader(form.Encode()))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		output.Debug("auth: tokeninfo: %v", err)
		return ""
	}
	defer resp.Body.Close()
	var info struct {
		Email string `json:"email"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&info) != nil {
		return ""
	}
	return info.Email
}

// SignerFor returns the principal cfctl signs as under cfg, and false when
// no credential is sent.
func SignerFor(ctx context.Context, cfg *config.Config, detect SignerDetector) (Signer, bool) {
	switch mode := Resolve(cfg); mode.Method {
	case MethodNone, MethodUnconfigured:
		return Signer{}, false
	case MethodSelfSignedKeyFile, MethodIDTokenKeyFile:
		data, err := os.ReadFile(cfg.CredentialsPath)
		if err != nil {
			return Signer{Description: "the key file " + cfg.CredentialsPath + " (unreadable)"}, true
		}
		return signerFromJSON(ctx, data, nil), true
	default:
		return detect(ctx), true
	}
}

// tokenCreatorHelp is the remediation for a signer refused on targetSA. It
// prints the exact grant, and for a VM signer the way to sign as yourself.
func tokenCreatorHelp(signer Signer, targetSA string) string {
	member := signer.Member
	if member == "" {
		member = "PRINCIPAL"
		if strings.HasPrefix(signer.Description, "your gcloud ADC user") {
			member = "user:YOUR_EMAIL"
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, `The signing principal, %s, needs
roles/iam.serviceAccountTokenCreator on %s (granted on the
service account itself, not the project). The deployment's operator grants it
with:

  gcloud iam service-accounts add-iam-policy-binding %s --member=%s --role=roles/iam.serviceAccountTokenCreator
`, signer.Description, targetSA, targetSA, member)
	if signer.VM {
		b.WriteString("\nOr run 'cfctl auth login' to sign with your own account.")
	}
	b.WriteString("\nOr switch to a key file: cfctl config set-credentials <key.json>.")
	return b.String()
}
