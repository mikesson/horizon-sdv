// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/iamcredentials/v1"
	"google.golang.org/api/option"
)

func newTestIAMService(t *testing.T, handler http.HandlerFunc) *iamcredentials.Service {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
	})
	svc, err := iamcredentials.NewService(
		t.Context(),
		option.WithEndpoint(srv.URL),
		option.WithHTTPClient(srv.Client()),
	)
	if err != nil {
		t.Fatalf("iamcredentials.NewService failed: %v", err)
	}
	return svc
}

func TestSelfSignedJWT_TokenClaims(t *testing.T) {
	t.Parallel()

	const (
		account  = "relay@proj.iam.gserviceaccount.com"
		audience = "https://cco.example.com/*"
		signed   = "header.payload.signature"
	)

	var gotPath string
	var gotClaims map[string]any

	svc := newTestIAMService(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body iamcredentials.SignJwtRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := json.Unmarshal([]byte(body.Payload), &gotClaims); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(iamcredentials.SignJwtResponse{SignedJwt: signed})
	})

	before := time.Now()
	src := &selfSignedJWT{
		ctx:      t.Context(),
		svc:      svc,
		account:  account,
		audience: audience,
	}
	tok, err := src.Token()
	after := time.Now()
	if err != nil {
		t.Fatalf("Token() failed: %v", err)
	}

	wantPath := "/v1/projects/-/serviceAccounts/" + account + ":signJwt"
	if gotPath != wantPath {
		t.Errorf("SignJwt path = %q, want %q", gotPath, wantPath)
	}
	if tok.AccessToken != signed {
		t.Errorf("AccessToken = %q, want %q", tok.AccessToken, signed)
	}
	if tok.TokenType != "Bearer" {
		t.Errorf("TokenType = %q, want Bearer", tok.TokenType)
	}
	if tok.Expiry.Before(before.Add(tokenLifetime)) || tok.Expiry.After(after.Add(tokenLifetime).Add(2*time.Second)) {
		t.Errorf("Expiry = %v, expected around %v", tok.Expiry, before.Add(tokenLifetime))
	}

	if gotClaims["iss"] != account || gotClaims["sub"] != account {
		t.Errorf("iss/sub claims = %v / %v, want %q", gotClaims["iss"], gotClaims["sub"], account)
	}
	if gotClaims["aud"] != audience {
		t.Errorf("aud claim = %v, want %q", gotClaims["aud"], audience)
	}
	iatFloat, ok1 := gotClaims["iat"].(float64)
	expFloat, ok2 := gotClaims["exp"].(float64)
	if !ok1 || !ok2 {
		t.Fatalf("missing numeric iat/exp in claims: %v", gotClaims)
	}
	if gotLife := int64(expFloat - iatFloat); gotLife != int64(tokenLifetime.Seconds()) {
		t.Errorf("exp - iat = %d, want %d", gotLife, int64(tokenLifetime.Seconds()))
	}
}

func TestSelfSignedJWT_SignJwtError(t *testing.T) {
	t.Parallel()

	const account = "relay@proj.iam.gserviceaccount.com"
	svc := newTestIAMService(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"code":403,"message":"permission denied"}}`, http.StatusForbidden)
	})

	src := &selfSignedJWT{
		ctx:      t.Context(),
		svc:      svc,
		account:  account,
		audience: "https://cco.example.com/*",
	}
	_, err := src.Token()
	if err == nil {
		t.Fatal("expected error from failing SignJwt call")
	}
	if !strings.Contains(err.Error(), account) {
		t.Errorf("error %q should mention service account %q", err.Error(), account)
	}
}

func TestSelfSignedJWT_Deadline(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	defer close(release)

	svc := newTestIAMService(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
		http.Error(w, "cancelled", http.StatusRequestTimeout)
	})

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	src := &selfSignedJWT{
		ctx:      ctx,
		svc:      svc,
		account:  "relay@proj.iam.gserviceaccount.com",
		audience: "https://cco.example.com/*",
	}
	_, err := src.Token()
	if err == nil {
		t.Fatal("expected deadline error from Token()")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("expected context deadline error, got: %v", err)
	}
}
