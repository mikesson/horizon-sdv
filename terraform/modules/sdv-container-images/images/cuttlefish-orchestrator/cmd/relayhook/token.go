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

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/iamcredentials/v1"
)

// tokenLifetime stays under the hour IAP allows, leaving room for clock skew.
const tokenLifetime = 50 * time.Minute

// mintTimeout bounds one signing call. It is generous relative to the hook's
// own budget because a mint happens once per token, not once per request.
const mintTimeout = 10 * time.Second

// selfSignedJWT authenticates to IAP by signing a JWT as the VM's own service
// account, through the IAM Credentials API so no key is held on disk.
//
// The obvious alternative does not work. An identity token from the metadata
// server is Google-signed, and built-in Cloud Run IAP rejects it whatever
// audience it carries. This was verified against the live deployment, which
// answers "Invalid JWT audience" for the service URL, the URL with a path
// wildcard, and the direct Cloud Run audience alike. IAP wants a token the
// service account signed for itself, which is also how the CLI reaches this
// deployment.
type selfSignedJWT struct {
	// ctx bounds refresh calls; oauth2.TokenSource.Token takes no context.
	ctx      context.Context
	svc      *iamcredentials.Service
	account  string
	audience string
}

// Token mints a fresh JWT. Callers wrap this in a caching source, so it is not
// called per request.
func (s *selfSignedJWT) Token() (*oauth2.Token, error) {
	ctx, cancel := context.WithTimeout(s.ctx, mintTimeout)
	defer cancel()

	now := time.Now()
	expiry := now.Add(tokenLifetime)
	payload, err := json.Marshal(map[string]any{
		"iss": s.account,
		"sub": s.account,
		"aud": s.audience,
		"iat": now.Unix(),
		"exp": expiry.Unix(),
	})
	if err != nil {
		return nil, fmt.Errorf("building JWT payload: %w", err)
	}

	resp, err := s.svc.Projects.ServiceAccounts.
		SignJwt("projects/-/serviceAccounts/"+s.account, &iamcredentials.SignJwtRequest{
			Payload: string(payload),
		}).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf(
			"signing JWT as %s (the account needs roles/iam.serviceAccountTokenCreator on itself): %w",
			s.account, err)
	}

	return &oauth2.Token{AccessToken: resp.SignedJwt, TokenType: "Bearer", Expiry: expiry}, nil
}
