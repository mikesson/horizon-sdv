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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/api/iamcredentials/v1"
	"google.golang.org/api/option"
)

// TestSignedJWTTokenSource checks the impersonation mint against a fake IAM
// Credentials endpoint. It checks the SA resource name, the IAP claims and
// the 50-minute expiry.
func TestSignedJWTTokenSource(t *testing.T) {
	const (
		targetSA  = "cfctl-test@example.iam.gserviceaccount.com"
		audience  = "https://example.com/*"
		signedJWT = "header.payload.signature"
	)

	var gotPath string
	var gotPayload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body struct {
			Payload string `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding signJwt request: %v", err)
		}
		if err := json.Unmarshal([]byte(body.Payload), &gotPayload); err != nil {
			t.Errorf("decoding claim set: %v", err)
		}
		fmt.Fprintf(w, `{"signedJwt":%q}`, signedJWT)
	}))
	t.Cleanup(srv.Close)

	svc, err := iamcredentials.NewService(t.Context(),
		option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("iamcredentials.NewService: %v", err)
	}

	ts := &signedJWTTokenSource{ctx: t.Context(), svc: svc, targetSA: targetSA, audience: audience}
	before := time.Now()
	tok, err := ts.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}

	if want := "/v1/projects/-/serviceAccounts/" + targetSA + ":signJwt"; gotPath != want {
		t.Errorf("signJwt path = %q, want %q", gotPath, want)
	}
	for claim, want := range map[string]string{"iss": targetSA, "sub": targetSA, "aud": audience} {
		if gotPayload[claim] != want {
			t.Errorf("claim %s = %v, want %q", claim, gotPayload[claim], want)
		}
	}
	iat, _ := gotPayload["iat"].(float64)
	exp, _ := gotPayload["exp"].(float64)
	if exp-iat != (50 * time.Minute).Seconds() {
		t.Errorf("exp-iat = %vs, want 3000s (50m, under IAP's 1h cap)", exp-iat)
	}

	if tok.AccessToken != signedJWT {
		t.Errorf("AccessToken = %q, want the signed JWT", tok.AccessToken)
	}
	if tok.TokenType != "Bearer" {
		t.Errorf("TokenType = %q, want Bearer", tok.TokenType)
	}
	if tok.Expiry.Before(before.Add(49*time.Minute)) || tok.Expiry.After(before.Add(51*time.Minute)) {
		t.Errorf("Expiry = %v, want ~50m from now", tok.Expiry)
	}
}
