// Copyright (c) 2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// makeJWT builds an unsigned, display-only JWT string (header.payload.signature)
// whose payload encodes the given claims as unpadded base64url, exactly like a
// real Keycloak access token.
func makeJWT(claims map[string]any) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	pb, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(pb)
	sig := base64.RawURLEncoding.EncodeToString([]byte("unverified-signature"))
	return hdr + "." + payload + "." + sig
}

// jwtWithPayloadMod returns a JWT whose base64url payload length ≡ want (mod 4).
// Valid base64url lengths are only ever 0, 2, or 3 mod 4; adding one filler byte
// cycles the residue, so a match is always found within a few iterations.
func jwtWithPayloadMod(t *testing.T, want int) string {
	t.Helper()
	claims := map[string]any{
		"sub":                "4437810e-0c1f-466c-aefd-22256423a31e",
		"preferred_username": "weronika.pluta@accenture.com",
		"exp":                1749999999,
	}
	for i := 0; i < 8; i++ {
		claims["_pad"] = strings.Repeat("x", i)
		pb, _ := json.Marshal(claims)
		if len(base64.RawURLEncoding.EncodeToString(pb))%4 == want {
			return makeJWT(claims)
		}
	}
	t.Fatalf("could not construct payload with len%%4==%d", want)
	return ""
}

// TestDecodeJWTPayload_AllPaddingShapes is the TAA-1799 regression: every valid
// payload length shape (0, 2, 3 mod 4) must decode. Before the fix, shapes 2 and
// 3 failed with "illegal base64 data at input byte N" because the code appended
// '=' padding and then decoded with RawURLEncoding (which rejects padding).
func TestDecodeJWTPayload_AllPaddingShapes(t *testing.T) {
	for _, want := range []int{0, 2, 3} {
		tok := jwtWithPayloadMod(t, want)
		got, err := decodeJWTPayload(tok)
		if err != nil {
			t.Fatalf("payload len%%4==%d: unexpected error: %v", want, err)
		}
		if got["preferred_username"] != "weronika.pluta@accenture.com" {
			t.Fatalf("payload len%%4==%d: wrong claim: %v", want, got["preferred_username"])
		}
	}
}

func TestDecodeJWTPayload_NotAJWT(t *testing.T) {
	if _, err := decodeJWTPayload("only.two-parts"); err == nil {
		t.Fatal("expected error for a token without exactly 3 segments")
	}
}
