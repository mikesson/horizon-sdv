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

package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/api/idtoken"

	"cloud-cuttlefish-orchestrator/internal/domain/auth"
)

// fakeValidator returns a TokenValidator that accepts exactly one token and
// audience, yielding the given payload.
func fakeValidator(wantToken, wantAudience string, payload *idtoken.Payload) TokenValidator {
	return func(_ context.Context, token, audience string) (*idtoken.Payload, error) {
		if token != wantToken || audience != wantAudience {
			return nil, errors.New("token validation failed")
		}
		return payload, nil
	}
}

// servePrincipal runs the Auth middleware over a request and returns the
// response and the Principal seen by the next handler (nil if not reached).
func servePrincipal(t *testing.T, cfg AuthConfig, r *http.Request) (*httptest.ResponseRecorder, *auth.Principal) {
	t.Helper()
	var got *auth.Principal
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	w := httptest.NewRecorder()
	Auth(cfg)(next).ServeHTTP(w, r)
	return w, got
}

func TestAuthIAPMode(t *testing.T) {
	t.Parallel()
	const audience = "/projects/123/locations/europe-west4/services/cuttlefish-orchestrator"

	tests := []struct {
		name       string
		header     string
		payload    *idtoken.Payload
		wantStatus int
		wantEmail  string
	}{
		{
			name:   "valid assertion",
			header: "valid-jwt",
			payload: &idtoken.Payload{
				Issuer: "https://cloud.google.com/iap",
				Claims: map[string]any{"email": "dev@example.com"},
			},
			wantStatus: http.StatusOK,
			wantEmail:  "dev@example.com",
		},
		{
			name:       "missing assertion header",
			header:     "",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "invalid signature",
			header:     "forged-jwt",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:   "wrong issuer",
			header: "valid-jwt",
			payload: &idtoken.Payload{
				Issuer: "https://evil.example.com",
				Claims: map[string]any{"email": "dev@example.com"},
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:   "missing email claim",
			header: "valid-jwt",
			payload: &idtoken.Payload{
				Issuer: "https://cloud.google.com/iap",
				Claims: map[string]any{},
			},
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := AuthConfig{
				Mode:          AccessModeIAP,
				IAPAudience:   audience,
				ValidateToken: fakeValidator("valid-jwt", audience, tt.payload),
			}
			r := httptest.NewRequest(http.MethodGet, "/v2/devices", nil)
			if tt.header != "" {
				r.Header.Set("X-Goog-IAP-JWT-Assertion", tt.header)
			}

			w, principal := servePrincipal(t, cfg, r)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if tt.wantEmail != "" {
				if principal == nil {
					t.Fatal("principal not attached to context")
				}
				if principal.Email != tt.wantEmail {
					t.Errorf("principal.Email = %q, want %q", principal.Email, tt.wantEmail)
				}
			}
		})
	}
}

func TestAuthNetworkMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		headers map[string]string
	}{
		{name: "no credentials"},
		{
			// OmniLab ATS hardcodes the username-only Basic header; the mode
			// must ignore it rather than echo or reject it.
			name:    "legacy username-only Basic header",
			headers: map[string]string{"Authorization": "Basic dXNlcjo="},
		},
		{
			// No opportunistic validation: a real credential yields the SAME
			// synthetic principal as none at all.
			name:    "valid IAP assertion still yields the synthetic principal",
			headers: map[string]string{"X-Goog-IAP-JWT-Assertion": "valid-token"},
		},
		{
			name:    "identity header is not echoed",
			headers: map[string]string{"X-Goog-Authenticated-User-Email": "accounts.google.com:spoofed@example.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := AuthConfig{
				Mode: AccessModeNetwork,
				// The mode must never validate anything: reaching the
				// validator is itself the failure.
				ValidateToken: func(context.Context, string, string) (*idtoken.Payload, error) {
					t.Error("network mode must not validate tokens")
					return nil, errors.New("unreachable")
				},
			}
			r := httptest.NewRequest(http.MethodGet, "/v1/zones", nil)
			for k, v := range tt.headers {
				r.Header.Set(k, v)
			}

			w, principal := servePrincipal(t, cfg, r)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			if principal == nil {
				t.Fatal("principal not attached to context")
			}
			if *principal != NetworkPrincipal {
				t.Errorf("principal = %+v, want the synthetic %+v", *principal, NetworkPrincipal)
			}
		})
	}
}

func TestAuthDevMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		header     string
		fallback   string
		wantStatus int
		wantEmail  string
	}{
		{
			name:       "dev header",
			header:     "accounts.google.com:dev@example.com",
			wantStatus: http.StatusOK,
			wantEmail:  "dev@example.com",
		},
		{
			name:       "fallback email when no header",
			fallback:   "local@example.com",
			wantStatus: http.StatusOK,
			wantEmail:  "local@example.com",
		},
		{
			name:       "no header and no fallback",
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := AuthConfig{
				Mode:             AccessModeIAP,
				DevMode:          true,
				DevFallbackEmail: tt.fallback,
			}
			r := httptest.NewRequest(http.MethodGet, "/v2/devices", nil)
			if tt.header != "" {
				r.Header.Set("X-Goog-Authenticated-User-Email", tt.header)
			}

			w, principal := servePrincipal(t, cfg, r)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if tt.wantEmail != "" && (principal == nil || principal.Email != tt.wantEmail) {
				t.Errorf("principal = %+v, want email %q", principal, tt.wantEmail)
			}
		})
	}
}
