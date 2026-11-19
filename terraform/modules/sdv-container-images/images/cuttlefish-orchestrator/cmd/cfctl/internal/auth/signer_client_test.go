// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/auth/authtest"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
)

type errTokenSource struct{}

func (errTokenSource) Token() (*oauth2.Token, error) {
	return nil, errors.New("token source failed")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestSignerFromJSONAndTokenCreatorHelp(t *testing.T) {
	ctx := t.Context()

	t.Run("service_account with and without client_email", func(t *testing.T) {
		got := signerFromJSON(ctx, []byte(`{"type":"service_account","client_email":"sa@proj.iam.gserviceaccount.com"}`), nil)
		if got.Email != "sa@proj.iam.gserviceaccount.com" || got.Member != "serviceAccount:sa@proj.iam.gserviceaccount.com" {
			t.Errorf("unexpected signer: %+v", got)
		}

		fallback := signerFromJSON(ctx, []byte(`{"type":"service_account"}`), nil)
		if fallback.Description != "your Application Default Credentials" {
			t.Errorf("unexpected fallback signer: %+v", fallback)
		}
	})

	t.Run("impersonated and external account", func(t *testing.T) {
		got := signerFromJSON(ctx, []byte(`{
			"type":"impersonated_service_account",
			"service_account_impersonation_url":"https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/imp%40proj.iam.gserviceaccount.com:generateAccessToken"
		}`), nil)
		if got.Email != "imp@proj.iam.gserviceaccount.com" || got.Member != "serviceAccount:imp@proj.iam.gserviceaccount.com" {
			t.Errorf("unexpected impersonated signer: %+v", got)
		}

		noAt := signerFromJSON(ctx, []byte(`{
			"type":"external_account",
			"service_account_impersonation_url":"https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/notanemail:generateAccessToken"
		}`), nil)
		if noAt.Description != "the federated identity" || noAt.Email != "" {
			t.Errorf("expected federated fallback, got %+v", noAt)
		}
	})

	t.Run("authorized_user without tokeninfo lookup succeeds gracefully", func(t *testing.T) {
		nilTS := signerFromJSON(ctx, []byte(`{"type":"authorized_user"}`), nil)
		if nilTS.Description != "your gcloud ADC user" {
			t.Errorf("got %+v", nilTS)
		}
		errTS := signerFromJSON(ctx, []byte(`{"type":"authorized_user"}`), errTokenSource{})
		if errTS.Description != "your gcloud ADC user" {
			t.Errorf("got %+v", errTS)
		}
	})

	t.Run("invalid JSON falls back cleanly", func(t *testing.T) {
		got := signerFromJSON(ctx, []byte(`{bad json`), nil)
		if got.Description != "your Application Default Credentials" {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("tokenCreatorHelp formats VM and ADC guidance", func(t *testing.T) {
		vmHelp := tokenCreatorHelp(Signer{Description: "vm", VM: true}, "target@p.iam.gserviceaccount.com")
		if !strings.Contains(vmHelp, "member=PRINCIPAL") || !strings.Contains(vmHelp, "cfctl auth login") {
			t.Errorf("unexpected vm help:\n%s", vmHelp)
		}

		adcHelp := tokenCreatorHelp(Signer{Description: "your gcloud ADC user"}, "target@p.iam.gserviceaccount.com")
		if !strings.Contains(adcHelp, "member=user:YOUR_EMAIL") {
			t.Errorf("unexpected adc help:\n%s", adcHelp)
		}
	})
}

func TestSignerFor(t *testing.T) {
	keyPath, _ := authtest.WriteKeyFile(t)
	detect := func(context.Context) Signer {
		return Signer{Email: "detected@example.com", Description: "detected"}
	}

	if _, ok := SignerFor(t.Context(), &config.Config{AuthNone: true}, detect); ok {
		t.Fatal("MethodNone should return ok=false")
	}
	if _, ok := SignerFor(t.Context(), &config.Config{}, detect); ok {
		t.Fatal("MethodUnconfigured should return ok=false")
	}

	s, ok := SignerFor(t.Context(), &config.Config{CredentialsPath: keyPath}, detect)
	if !ok || s.Email != authtest.SAEmail {
		t.Errorf("key file signer = (%+v, %v), want %s", s, ok, authtest.SAEmail)
	}

	missingPath := filepath.Join(t.TempDir(), "missing.json")
	unreadable, ok := SignerFor(t.Context(), &config.Config{IAPAudience: "aud", CredentialsPath: missingPath}, detect)
	if !ok || !strings.Contains(unreadable.Description, "(unreadable)") {
		t.Errorf("unreadable key signer = (%+v, %v)", unreadable, ok)
	}

	imp, ok := SignerFor(t.Context(), &config.Config{ImpersonateServiceAccount: "sa@example.com"}, detect)
	if !ok || imp.Email != "detected@example.com" {
		t.Errorf("impersonate signer = (%+v, %v)", imp, ok)
	}
}

func TestNewHTTPClient_ModesAndErrors(t *testing.T) {
	ctx := t.Context()

	t.Run("MethodNone rejects control chars and injects trimmed ProxyEmail", func(t *testing.T) {
		_, err := NewHTTPClient(ctx, &config.Config{AuthNone: true, ProxyEmail: "bad\n@example.com"}, "https://svc")
		var authErr *Error
		if !errors.As(err, &authErr) {
			t.Fatalf("expected *auth.Error for newline in ProxyEmail, got %v", err)
		}

		cNoEmail, err := NewHTTPClient(ctx, &config.Config{AuthNone: true}, "https://svc")
		if err != nil || cNoEmail == nil {
			t.Fatalf("MethodNone empty email failed: %v", err)
		}

		cWithEmail, err := NewHTTPClient(ctx, &config.Config{AuthNone: true, ProxyEmail: "  dev@example.com  "}, "https://svc")
		if err != nil {
			t.Fatalf("MethodNone proxy email failed: %v", err)
		}
		pt, ok := cWithEmail.Transport.(*proxyEmailTransport)
		if !ok {
			t.Fatalf("expected *proxyEmailTransport, got %T", cWithEmail.Transport)
		}

		var gotHeader string
		pt.base = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotHeader = r.Header.Get("X-Auth-Request-Email")
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		})
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://svc/v2/me", http.NoBody)
		if _, err := pt.RoundTrip(req); err != nil {
			t.Fatalf("RoundTrip failed: %v", err)
		}
		if gotHeader != "dev@example.com" {
			t.Errorf("X-Auth-Request-Email = %q, want dev@example.com", gotHeader)
		}

		pt.base = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed")
		})
		if _, err := pt.RoundTrip(req); err == nil || !strings.Contains(err.Error(), "sending proxy-authenticated request") {
			t.Errorf("expected wrapped RoundTrip error, got %v", err)
		}
	})

	t.Run("SelfSigned empty serverURL returns Error", func(t *testing.T) {
		keyPath, _ := authtest.WriteKeyFile(t)
		_, err := NewHTTPClient(ctx, &config.Config{CredentialsPath: keyPath}, "")
		var authErr *Error
		if !errors.As(err, &authErr) || authErr.Message != "server URL not configured" {
			t.Fatalf("expected server URL not configured *auth.Error, got %v", err)
		}
	})

	t.Run("IDTokenKeyFile errors on missing or invalid key", func(t *testing.T) {
		_, err := NewHTTPClient(ctx, &config.Config{
			IAPAudience:     "aud",
			CredentialsPath: filepath.Join(t.TempDir(), "nope.json"),
		}, "https://svc")
		if err == nil || !strings.Contains(err.Error(), "reading credentials file") {
			t.Fatalf("expected file read error, got %v", err)
		}

		badKey := filepath.Join(t.TempDir(), "bad.json")
		if err := os.WriteFile(badKey, []byte(`{"not":"valid"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = NewHTTPClient(ctx, &config.Config{
			IAPAudience:     "aud",
			CredentialsPath: badKey,
		}, "https://svc")
		if err == nil || !strings.Contains(err.Error(), "authentication failed") {
			t.Fatalf("expected authentication failed error, got %v", err)
		}
	})
}

func TestNewLocalKeyJWTTokenSource_ValidationErrors(t *testing.T) {
	dir := t.TempDir()

	if _, err := newLocalKeyJWTTokenSource(filepath.Join(dir, "missing.json"), "aud"); err == nil {
		t.Error("expected missing file error")
	}

	writeTemp := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	if _, err := newLocalKeyJWTTokenSource(writeTemp("badjson.json", "{"), "aud"); err == nil {
		t.Error("expected JSON parse error")
	}
	if _, err := newLocalKeyJWTTokenSource(writeTemp("nofields.json", `{}`), "aud"); err == nil {
		t.Error("expected missing client_email/private_key error")
	}
	if _, err := newLocalKeyJWTTokenSource(writeTemp("nopem.json", `{"client_email":"sa@p","private_key":"not-pem"}`), "aud"); err == nil {
		t.Error("expected missing PEM error")
	}
	badPKCS8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("corrupt")})
	if _, err := newLocalKeyJWTTokenSource(writeTemp("badpkcs8.json", `{"client_email":"sa@p","private_key":`+strconvQuote(string(badPKCS8))+`}`), "aud"); err == nil {
		t.Error("expected PKCS8 parse error")
	}

	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	ecPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER})
	if _, err := newLocalKeyJWTTokenSource(writeTemp("ec.json", `{"client_email":"sa@p","private_key":`+strconvQuote(string(ecPEM))+`}`), "aud"); err == nil || !strings.Contains(err.Error(), "not RSA") {
		t.Errorf("expected not RSA error, got %v", err)
	}
}

func strconvQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return `"` + s + `"`
}

func TestNewDiskCachedTokenSource_PrunesExpired(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	src := newDiskCachedTokenSource(&signedJWTTokenSource{}, "sa@example.com", "https://svc/*")
	if src.path == "" || src.key != "sa@example.com https://svc/*" {
		t.Fatalf("unexpected cache source: %+v", src)
	}

	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	src.now = func() time.Time { return now }
	if err := writeFileAtomic(src.path, []byte(`{"entries":{"stale":{"token":"old","expiry":"2026-05-01T11:00:00Z"}}}`)); err != nil {
		t.Fatal(err)
	}

	if err := src.store(&oauth2.Token{AccessToken: "fresh", Expiry: now.Add(30 * time.Minute)}, now); err != nil {
		t.Fatalf("store failed: %v", err)
	}
	loaded := readTokenCache(src.path)
	if _, hasStale := loaded.Entries["stale"]; hasStale {
		t.Error("expected stale expired entry to be pruned")
	}
	if loaded.Entries[src.key].Token != "fresh" {
		t.Errorf("expected fresh entry stored, got %+v", loaded.Entries)
	}
}
