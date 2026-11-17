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
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/oauth2"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/auth/authtest"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
)

func TestResolve(t *testing.T) {
	// `auth status` and NewHTTPClient both use Resolve, so this table is the
	// documented behavior.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")

	tests := []struct {
		name       string
		cfg        config.Config
		wantMethod Method
		wantDetail string
	}{
		{
			name:       "no audience, impersonation -> self-signed JWT via impersonation",
			cfg:        config.Config{ImpersonateServiceAccount: "sa@p.iam.gserviceaccount.com"},
			wantMethod: MethodSelfSignedImpersonate,
			wantDetail: "sa@p.iam.gserviceaccount.com",
		},
		{
			name:       "no audience, key file -> self-signed JWT via key file",
			cfg:        config.Config{CredentialsPath: "/keys/sa.json"},
			wantMethod: MethodSelfSignedKeyFile,
			wantDetail: "/keys/sa.json",
		},
		{
			name: "no audience, nothing configured -> unconfigured (NOT the old ADC lie)",
			// The self-signed-JWT path never uses ADC.
			cfg:        config.Config{},
			wantMethod: MethodUnconfigured,
		},
		{
			name: "audience + impersonation -> ID token via impersonation",
			cfg: config.Config{
				IAPAudience:               "aud",
				ImpersonateServiceAccount: "sa@p.iam.gserviceaccount.com",
			},
			wantMethod: MethodIDTokenImpersonate,
			wantDetail: "sa@p.iam.gserviceaccount.com",
		},
		{
			name: "audience + impersonation beats key file",
			cfg: config.Config{
				IAPAudience:               "aud",
				CredentialsPath:           "/keys/sa.json",
				ImpersonateServiceAccount: "sa@p.iam.gserviceaccount.com",
			},
			wantMethod: MethodIDTokenImpersonate,
			wantDetail: "sa@p.iam.gserviceaccount.com",
		},
		{
			name:       "audience + key file -> ID token via key file",
			cfg:        config.Config{IAPAudience: "aud", CredentialsPath: "/keys/sa.json"},
			wantMethod: MethodIDTokenKeyFile,
			wantDetail: "/keys/sa.json",
		},
		{
			name:       "audience only -> ADC",
			cfg:        config.Config{IAPAudience: "aud"},
			wantMethod: MethodIDTokenADC,
			wantDetail: "(auto-detected)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode := Resolve(&tt.cfg)
			if mode.Method != tt.wantMethod {
				t.Errorf("Method = %q, want %q", mode.Method, tt.wantMethod)
			}
			if mode.Detail != tt.wantDetail {
				t.Errorf("Detail = %q, want %q", mode.Detail, tt.wantDetail)
			}
		})
	}
}

func TestResolve_ADCDetailFromEnv(t *testing.T) {
	credsPath := filepath.Join(t.TempDir(), "adc-creds.json")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credsPath)

	mode := Resolve(&config.Config{IAPAudience: "aud"})
	if mode.Method != MethodIDTokenADC {
		t.Fatalf("Method = %q, want %q", mode.Method, MethodIDTokenADC)
	}
	if mode.Detail != credsPath {
		t.Errorf("Detail = %q, want %q (GOOGLE_APPLICATION_CREDENTIALS)", mode.Detail, credsPath)
	}
}

func TestNewHTTPClient_EmptyAudienceRequiresServiceAccount(t *testing.T) {
	// An empty audience selects the self-signed JWT path, which needs an SA to
	// sign as.
	_, err := NewHTTPClient(t.Context(), &config.Config{}, "https://example.com")
	if err == nil {
		t.Fatal("Expected error when no service account is configured for self-signed JWT")
	}

	authErr, ok := errors.AsType[*Error](err)
	if !ok {
		t.Fatalf("Expected *auth.Error, got %T: %v", err, err)
	}
	if authErr.Message != "no service account configured for self-signed JWT authentication" {
		t.Errorf("Unexpected message: %q", authErr.Message)
	}
}

func TestNewHTTPClient_JWTAudienceFromResolvedServerURL(t *testing.T) {
	// The JWT aud must come from the resolved server URL, which can differ
	// from the config file's server. A mismatch is an IAP 401.
	keyPath, _ := authtest.WriteKeyFile(t)
	cfg := &config.Config{
		Server:          "https://stale-config.example.com",
		CredentialsPath: keyPath,
	}

	client, err := NewHTTPClient(t.Context(), cfg, "https://resolved.example.com/")
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}

	transport, ok := client.Transport.(*oauth2.Transport)
	if !ok {
		t.Fatalf("expected *oauth2.Transport, got %T", client.Transport)
	}
	tok, err := transport.Source.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}

	parts := strings.Split(tok.AccessToken, ".")
	if len(parts) != 3 {
		t.Fatalf("expected a 3-part JWT, got %d parts", len(parts))
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decoding payload: %v", err)
	}
	var claims struct {
		Aud string `json:"aud"`
	}
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		t.Fatalf("parsing claims: %v", err)
	}
	if want := "https://resolved.example.com/*"; claims.Aud != want {
		t.Errorf("aud = %q, want %q (resolved server URL, not config server)", claims.Aud, want)
	}
}

func TestSelfSignedJWT_LocalKeyFile(t *testing.T) {
	keyPath, key := authtest.WriteKeyFile(t)
	const saEmail = authtest.SAEmail

	const audience = "https://example.com/*"
	ts, err := newLocalKeyJWTTokenSource(keyPath, audience)
	if err != nil {
		t.Fatalf("newLocalKeyJWTTokenSource: %v", err)
	}
	tok, err := ts.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}

	parts := strings.Split(tok.AccessToken, ".")
	if len(parts) != 3 {
		t.Fatalf("expected a 3-part JWT, got %d parts", len(parts))
	}

	// Header carries the key ID so IAP can locate the SA public key.
	headerJSON, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var header map[string]string
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		t.Fatalf("decoding header: %v", err)
	}
	if header["alg"] != "RS256" || header["kid"] != "kid-123" {
		t.Errorf("unexpected header: %v", header)
	}

	payloadJSON, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Iss, Sub, Aud string
		Iat, Exp      int64
	}
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		t.Fatalf("decoding payload: %v", err)
	}
	if claims.Iss != saEmail || claims.Sub != saEmail {
		t.Errorf("iss/sub = %q/%q, want %q", claims.Iss, claims.Sub, saEmail)
	}
	if claims.Aud != audience {
		t.Errorf("aud = %q, want %q", claims.Aud, audience)
	}
	if claims.Exp <= claims.Iat {
		t.Errorf("exp (%d) must be after iat (%d)", claims.Exp, claims.Iat)
	}

	// Signature must verify against the SA public key.
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decoding signature: %v", err)
	}
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
		t.Errorf("signature verification failed: %v", err)
	}
}

func TestError_Format(t *testing.T) {
	err := &Error{
		Message: "test message",
		Help:    "test help",
	}

	expected := "test message\n\ntest help"
	if err.Error() != expected {
		t.Errorf("Expected error string %q, got %q", expected, err.Error())
	}
}
