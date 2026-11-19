// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package relayfeature

import (
	"bytes"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	devicedomain "cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/platform/turn"
)

func mountRelayRouter(svc *Service) chi.Router {
	r := chi.NewRouter()
	api := humachi.New(r, huma.DefaultConfig("Test Relay API", "1.0.0"))
	NewHandler(svc).Register(api)
	return r
}

func postRelayAuthorize(t *testing.T, router chi.Router, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v2/relay/authorize", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestHandlerAuthorize_AlgorithmsAndDefault(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_769_000_000, 0)
	expiry := now.Add(30 * time.Minute)
	username := usernameAt(expiry)
	svc := newTestService(t, stubDevices{status: devicedomain.StatusRunning}, now)
	router := mountRelayRouter(svc)

	cases := []struct {
		name      string
		algField  string
		wantAlg   turn.PasswordAlgorithm
	}{
		{name: "default_md5_when_omitted", algField: "", wantAlg: turn.AlgorithmMD5},
		{name: "explicit_md5", algField: "md5", wantAlg: turn.AlgorithmMD5},
		{name: "explicit_sha256", algField: "sha256", wantAlg: turn.AlgorithmSHA256},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{
				"username": username,
				"realm":    testRealm,
			}
			if tc.algField != "" {
				body["algorithm"] = tc.algField
			}
			rec := postRelayAuthorize(t, router, body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			var got AuthorizedKey
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("json.Unmarshal: %v", err)
			}
			decodedKey, err := base64.StdEncoding.DecodeString(got.Key)
			if err != nil {
				t.Fatalf("base64.DecodeString(%q): %v", got.Key, err)
			}
			want := wantKey(t, username, testRealm, tc.wantAlg)
			if !hmac.Equal(decodedKey, want) {
				t.Errorf("decoded key = %x, want %x", decodedKey, want)
			}
			if got.ExpiresAt != expiry.Unix() {
				t.Errorf("ExpiresAt = %d, want %d", got.ExpiresAt, expiry.Unix())
			}
		})
	}
}

func TestHandlerAuthorize_ErrorMappings(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_769_000_000, 0)
	liveUser := usernameAt(now.Add(30 * time.Minute))

	t.Run("malformed_username_is_400", func(t *testing.T) {
		router := mountRelayRouter(newTestService(t, stubDevices{status: devicedomain.StatusRunning}, now))
		rec := postRelayAuthorize(t, router, map[string]any{
			"username": "not-a-valid-credential",
			"realm":    testRealm,
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("expired_credential_is_403", func(t *testing.T) {
		router := mountRelayRouter(newTestService(t, stubDevices{status: devicedomain.StatusRunning}, now))
		rec := postRelayAuthorize(t, router, map[string]any{
			"username": usernameAt(now.Add(-time.Second)),
			"realm":    testRealm,
		})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("realm_mismatch_is_403", func(t *testing.T) {
		router := mountRelayRouter(newTestService(t, stubDevices{status: devicedomain.StatusRunning}, now))
		rec := postRelayAuthorize(t, router, map[string]any{
			"username": liveUser,
			"realm":    "other.example",
		})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("deleted_subject_is_403", func(t *testing.T) {
		router := mountRelayRouter(newTestService(t, stubDevices{status: devicedomain.StatusDeleted}, now))
		rec := postRelayAuthorize(t, router, map[string]any{
			"username": liveUser,
			"realm":    testRealm,
		})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("missing_device_is_404", func(t *testing.T) {
		router := mountRelayRouter(newTestService(t, stubDevices{err: devicedomain.ErrNotFound}, now))
		rec := postRelayAuthorize(t, router, map[string]any{
			"username": liveUser,
			"realm":    testRealm,
		})
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("repository_failure_is_500", func(t *testing.T) {
		router := mountRelayRouter(newTestService(t, stubDevices{err: errors.New("spanner down")}, now))
		rec := postRelayAuthorize(t, router, map[string]any{
			"username": liveUser,
			"realm":    testRealm,
		})
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown_algorithm_error_maps_to_400", func(t *testing.T) {
		mapped := mapAuthorizeError(turn.ErrUnknownAlgorithm)
		var statusErr huma.StatusError
		if !errors.As(mapped, &statusErr) || statusErr.GetStatus() != http.StatusBadRequest {
			t.Fatalf("mapAuthorizeError(ErrUnknownAlgorithm) = %v, want 400 StatusError", mapped)
		}
	})
}
