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

// Package auth authenticates cfctl requests.
//
// Resolve maps a config to an authentication Mode. With no audience
// configured (built-in Cloud Run IAP), cfctl sends a service-account
// self-signed JWT whose audience is the service URL. With an audience
// configured, it sends a Google OIDC ID token. Either is minted via SA
// impersonation or an SA key file; the ID token can also come from
// Application Default Credentials. With auth set to none (a network-mode
// deployment), it sends no credentials.
//
// CI systems should prefer Workload Identity Federation to SA keys.
package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iamcredentials/v1"
	"google.golang.org/api/idtoken"
	"google.golang.org/api/impersonate"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// Method identifies how cfctl authenticates requests.
type Method string

const (
	// MethodSelfSignedImpersonate signs the IAP JWT via the IAM Credentials
	// API, with no local key material.
	MethodSelfSignedImpersonate Method = "self-signed JWT via SA impersonation"
	// MethodSelfSignedKeyFile signs the IAP JWT locally with an SA key file.
	MethodSelfSignedKeyFile Method = "self-signed JWT via SA key file"
	// MethodIDTokenImpersonate mints an OIDC ID token via SA impersonation.
	MethodIDTokenImpersonate Method = "ID token via SA impersonation"
	// MethodIDTokenKeyFile mints an OIDC ID token from an SA key file.
	MethodIDTokenKeyFile Method = "ID token via SA key file"
	// MethodIDTokenADC mints an OIDC ID token from Application Default
	// Credentials (WIF, attached SA, or GOOGLE_APPLICATION_CREDENTIALS).
	MethodIDTokenADC Method = "ID token via Application Default Credentials"
	// MethodUnconfigured means no audience is set and no service account is
	// configured to sign the self-signed JWT.
	MethodUnconfigured Method = "unconfigured"
	// MethodNone sends no credentials. It is for network-mode deployments.
	MethodNone Method = "no credentials (network-mode deployment)"
)

// Mode is the resolved authentication method and the credential it uses
// (SA email, key path, or an ADC hint).
type Mode struct {
	Method Method
	Detail string
}

// Resolve maps a config to its authentication Mode. NewHTTPClient and
// `auth status` both use it, so they always agree.
func Resolve(cfg *config.Config) Mode {
	if cfg.AuthNone {
		return Mode{Method: MethodNone, Detail: strings.TrimSpace(cfg.ProxyEmail)}
	}
	if cfg.IAPAudience == "" {
		switch {
		case cfg.ImpersonateServiceAccount != "":
			return Mode{Method: MethodSelfSignedImpersonate, Detail: cfg.ImpersonateServiceAccount}
		case cfg.CredentialsPath != "":
			return Mode{Method: MethodSelfSignedKeyFile, Detail: cfg.CredentialsPath}
		default:
			return Mode{Method: MethodUnconfigured}
		}
	}

	switch {
	case cfg.ImpersonateServiceAccount != "":
		return Mode{Method: MethodIDTokenImpersonate, Detail: cfg.ImpersonateServiceAccount}
	case cfg.CredentialsPath != "":
		return Mode{Method: MethodIDTokenKeyFile, Detail: cfg.CredentialsPath}
	default:
		detail := "(auto-detected)"
		if gac := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); gac != "" {
			detail = gac
		}
		return Mode{Method: MethodIDTokenADC, Detail: detail}
	}
}

const (
	// tokenRequestTimeout bounds each self-signed-JWT mint. The library ID-token
	// sources have no per-call deadline and rely on --timeout instead.
	tokenRequestTimeout = 30 * time.Second
	// tokenEarlyExpiry refreshes cached tokens this long before they expire.
	tokenEarlyExpiry = time.Minute

	bearerTokenType = "Bearer"
)

// NewHTTPClient returns an HTTP client that authenticates per Resolve(cfg).
// On the self-signed-JWT path serverURL is also the JWT audience.
func NewHTTPClient(ctx context.Context, cfg *config.Config, serverURL string) (*http.Client, error) {
	mode := Resolve(cfg)
	output.Debug("auth: %s (%s)", mode.Method, mode.Detail)

	if mode.Method == MethodNone {
		if strings.IndexFunc(cfg.ProxyEmail, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			return nil, &Error{Message: "CFCTL_PROXY_EMAIL contains invalid control characters"}
		}
		if email := strings.TrimSpace(cfg.ProxyEmail); email != "" {
			return &http.Client{
				Transport: &proxyEmailTransport{
					base:  http.DefaultTransport,
					email: email,
				},
			}, nil
		}
		return &http.Client{Transport: http.DefaultTransport}, nil
	}

	if cfg.IAPAudience == "" {
		return newSelfSignedJWTClient(ctx, cfg, mode, serverURL)
	}

	switch mode.Method {
	case MethodIDTokenImpersonate:
		ts, err := impersonate.IDTokenSource(ctx, impersonate.IDTokenConfig{
			Audience:        cfg.IAPAudience,
			TargetPrincipal: cfg.ImpersonateServiceAccount,
			IncludeEmail:    true,
		})
		if err != nil {
			if cls := classifyCredentialError(ctx, err, cfg.ImpersonateServiceAccount, DetectSigner); cls != nil {
				return nil, cls
			}
			return nil, fmt.Errorf("configuring SA impersonation: %w", err)
		}
		// Mint a token now so a missing tokenCreator role fails here with
		// guidance.
		if _, err := ts.Token(); err != nil {
			if cls := classifyCredentialError(ctx, err, cfg.ImpersonateServiceAccount, DetectSigner); cls != nil {
				return nil, cls
			}
			return nil, fmt.Errorf("impersonating %s: %w", cfg.ImpersonateServiceAccount, err)
		}
		return oauth2.NewClient(ctx, ts), nil

	case MethodIDTokenKeyFile:
		// The path and JSON credential options are deprecated. Declaring the
		// key type makes the library reject anything except an SA key.
		key, err := os.ReadFile(cfg.CredentialsPath)
		if err != nil {
			return nil, fmt.Errorf("reading credentials file %s: %w", cfg.CredentialsPath, err)
		}
		client, err := idtoken.NewClient(ctx, cfg.IAPAudience, idtoken.WithAuthCredentialsJSON(idtoken.ServiceAccount, key))
		if err != nil {
			return nil, fmt.Errorf("authentication failed (%s): %w", cfg.CredentialsPath, err)
		}
		return client, nil

	default: // MethodIDTokenADC
		client, err := idtoken.NewClient(ctx, cfg.IAPAudience)
		if err == nil {
			return client, nil
		}
		output.Debug("auth: ADC failed: %v", err)
		return nil, &Error{
			Message: "authentication required",
			Help: `No valid credentials found. Configure one of:

For CI/CD (Workload Identity Federation):
  Configure WIF in your CI provider - credentials are detected automatically via ADC

For a service-account key file:
  cfctl config set-credentials <key.json>

For developers (SA impersonation):
  gcloud auth application-default login
  cfctl config set-impersonate <sa-email>     # Requires roles/iam.serviceAccountTokenCreator

Environment variables:
  GOOGLE_APPLICATION_CREDENTIALS=<path>       # Path to SA key JSON`,
		}
	}
}

// newSelfSignedJWTClient authenticates to built-in Cloud Run IAP with a
// service-account self-signed JWT. The service account must be on the IAP
// access list (iap_members).
func newSelfSignedJWTClient(ctx context.Context, cfg *config.Config, mode Mode, serverURL string) (*http.Client, error) {
	if serverURL == "" {
		return nil, &Error{
			Message: "server URL not configured",
			Help:    "cfctl config set-server <service-url>",
		}
	}

	// Without the path wildcard the token covers only "/".
	audience := strings.TrimRight(serverURL, "/") + "/*"

	var ts oauth2.TokenSource
	switch mode.Method {
	case MethodSelfSignedImpersonate:
		svc, err := iamcredentials.NewService(ctx)
		if err != nil {
			if cls := classifyCredentialError(ctx, err, cfg.ImpersonateServiceAccount, DetectSigner); cls != nil {
				return nil, cls
			}
			return nil, fmt.Errorf("creating IAM credentials service: %w", err)
		}
		ts = newDiskCachedTokenSource(&signedJWTTokenSource{
			ctx:      ctx,
			svc:      svc,
			targetSA: cfg.ImpersonateServiceAccount,
			audience: audience,
		}, cfg.ImpersonateServiceAccount, audience)
	case MethodSelfSignedKeyFile:
		src, err := newLocalKeyJWTTokenSource(cfg.CredentialsPath, audience)
		if err != nil {
			return nil, err
		}
		ts = src
	default: // MethodUnconfigured
		return nil, &Error{
			Message: "no service account configured for self-signed JWT authentication",
			Help: `Built-in Cloud Run IAP (the Google-managed OAuth client) requires a
service-account self-signed JWT for programmatic access. Configure a
service account that is on the deployment's IAP access list (iap_members):

For developers (impersonation):
  cfctl config set-impersonate <sa-email>     # needs roles/iam.serviceAccountTokenCreator on the SA

For CI (service account key):
  cfctl config set-credentials <key.json>

If your deployment uses a custom IAP OAuth client instead, set its client ID
as the audience: cfctl config set-audience <client-id>

For a network-mode deployment, which takes no credentials:
  cfctl config set-auth none`,
		}
	}

	// Validate by signing an initial token; reuse it until expiry.
	tok, err := ts.Token()
	if err != nil {
		return nil, fmt.Errorf("signing initial token: %w", err)
	}
	return oauth2.NewClient(ctx, oauth2.ReuseTokenSourceWithExpiry(tok, ts, tokenEarlyExpiry)), nil
}

// jwtPayload builds the IAP service-account JWT claim set.
func jwtPayload(saEmail, audience string, iat, exp time.Time) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"iss": saEmail,
		"sub": saEmail,
		"aud": audience,
		"iat": iat.Unix(),
		"exp": exp.Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("marshaling JWT payload: %w", err)
	}
	return string(payload), nil
}

// signedJWTTokenSource signs the IAP JWT through the IAM Credentials API. It
// mints a new token on every call; callers add caching.
type signedJWTTokenSource struct {
	// ctx bounds refresh calls, since oauth2.TokenSource.Token takes no context.
	ctx      context.Context
	svc      *iamcredentials.Service
	targetSA string
	audience string
}

// Token mints a self-signed JWT for the target service account.
func (ts *signedJWTTokenSource) Token() (*oauth2.Token, error) {
	ctx, cancel := context.WithTimeout(ts.ctx, tokenRequestTimeout)
	defer cancel()

	now := time.Now()
	// IAP caps the lifetime at one hour; stay under it to allow for clock skew.
	exp := now.Add(50 * time.Minute)
	payload, err := jwtPayload(ts.targetSA, ts.audience, now, exp)
	if err != nil {
		return nil, err
	}

	name := "projects/-/serviceAccounts/" + ts.targetSA
	resp, err := ts.svc.Projects.ServiceAccounts.SignJwt(name, &iamcredentials.SignJwtRequest{
		Payload: payload,
	}).Context(ctx).Do()
	if err != nil {
		if cls := classifyCredentialError(ts.ctx, err, ts.targetSA, DetectSigner); cls != nil {
			return nil, cls
		}
		return nil, fmt.Errorf("signing JWT as %s: %w", ts.targetSA, err)
	}

	return &oauth2.Token{AccessToken: resp.SignedJwt, TokenType: bearerTokenType, Expiry: exp}, nil
}

// localKeyJWTTokenSource signs the IAP JWT locally with a service-account key.
// It mints a new token on every call; callers add caching.
type localKeyJWTTokenSource struct {
	saEmail  string
	keyID    string
	key      *rsa.PrivateKey
	audience string
}

func newLocalKeyJWTTokenSource(path, audience string) (*localKeyJWTTokenSource, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading credentials file %s: %w", path, err)
	}
	var f struct {
		ClientEmail  string `json:"client_email"`
		PrivateKey   string `json:"private_key"`
		PrivateKeyID string `json:"private_key_id"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing credentials file %s: %w", path, err)
	}
	if f.ClientEmail == "" || f.PrivateKey == "" {
		return nil, fmt.Errorf("credentials file %s is missing client_email or private_key", path)
	}
	block, _ := pem.Decode([]byte(f.PrivateKey))
	if block == nil {
		return nil, fmt.Errorf("credentials file %s has no PEM private key", path)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing private key from %s: %w", path, err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key in %s is not RSA", path)
	}
	return &localKeyJWTTokenSource{saEmail: f.ClientEmail, keyID: f.PrivateKeyID, key: key, audience: audience}, nil
}

// Token returns a locally signed JWT for the key file's service account.
func (ts *localKeyJWTTokenSource) Token() (*oauth2.Token, error) {
	now := time.Now()
	exp := now.Add(50 * time.Minute)

	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": ts.keyID})
	if err != nil {
		return nil, fmt.Errorf("marshaling JWT header: %w", err)
	}
	payload, err := jwtPayload(ts.saEmail, ts.audience, now, exp)
	if err != nil {
		return nil, err
	}

	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString([]byte(payload))
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, ts.key, crypto.SHA256, digest[:])
	if err != nil {
		return nil, fmt.Errorf("signing JWT: %w", err)
	}
	jwt := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)

	return &oauth2.Token{AccessToken: jwt, TokenType: bearerTokenType, Expiry: exp}, nil
}

// Error is an authentication error with a remediation message.
type Error struct {
	Message string
	Help    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s\n\n%s", e.Message, e.Help)
}

// classifyCredentialError maps common failures of minting a token as
// targetSA to actionable errors, or returns nil. Missing local ADC is checked
// first because the user fixes it with `cfctl auth login`, without a grant.
// detect runs only for a 403, to name the principal that needs the grant.
func classifyCredentialError(ctx context.Context, err error, targetSA string, detect SignerDetector) error {
	if err == nil {
		return nil
	}
	output.Debug("auth: raw credential error: %v", err)

	msg := err.Error()
	switch {
	case strings.Contains(msg, "could not find default credentials"):
		return &Error{
			Message: "no Application Default Credentials",
			Help: fmt.Sprintf(`cfctl needs your own Google credentials to sign as %s.
Run 'cfctl auth login' (gcloud auth application-default login), then retry.`, targetSA),
		}
	case strings.Contains(msg, "ACCESS_TOKEN_SCOPE_INSUFFICIENT"),
		strings.Contains(msg, "insufficient authentication scopes"):
		return &Error{
			Message: "the active Google credentials lack the cloud-platform scope",
			Help: fmt.Sprintf(`The current Application Default Credentials cannot call the IAM
Credentials API to sign as %s. That is common on a VM whose attached
credentials carry limited scopes. Run 'cfctl auth login' to use your own
account instead.`, targetSA),
		}
	}

	if apiErr, ok := errors.AsType[*googleapi.Error](err); ok {
		switch apiErr.Code {
		case http.StatusForbidden:
			return &Error{
				Message: "not allowed to sign as " + targetSA,
				Help:    tokenCreatorHelp(detect(ctx), targetSA),
			}
		case http.StatusNotFound:
			return &Error{
				Message: fmt.Sprintf("service account %s not found", targetSA),
				Help:    "Check the email with the deployment's operator, then re-run 'cfctl config set-impersonate <sa-email>'.",
			}
		}
	}
	return nil
}

type proxyEmailTransport struct {
	base  http.RoundTripper
	email string
}

func (t *proxyEmailTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("X-Auth-Request-Email", t.email)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(clone)
	if err != nil {
		return nil, fmt.Errorf("sending proxy-authenticated request: %w", err)
	}
	return resp, nil
}
