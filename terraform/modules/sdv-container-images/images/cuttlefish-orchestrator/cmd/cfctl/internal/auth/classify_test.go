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
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/api/googleapi"
)

// A missing local ADC must point to 'cfctl auth login', which the user runs
// themselves, and never to the tokenCreator grant.
func TestClassifyCredentialError(t *testing.T) {
	t.Parallel()
	const sa = "cco-cli@p.iam.gserviceaccount.com"
	tests := []struct {
		name     string
		err      error
		wantHelp string // Substring of the classified Help; empty means unclassified.
	}{
		{
			name:     "missing ADC -> auth login, not tokenCreator",
			err:      fmt.Errorf("creating service: %w", errors.New("google: could not find default credentials. See https://cloud.google.com/docs/authentication")),
			wantHelp: "cfctl auth login",
		},
		{
			name:     "scope-insufficient ADC -> re-login hint",
			err:      fmt.Errorf("wrapped: %w", &googleapi.Error{Code: http.StatusForbidden, Message: "Request had insufficient authentication scopes.", Body: `{"reason":"ACCESS_TOKEN_SCOPE_INSUFFICIENT"}`}),
			wantHelp: "cloud-platform",
		},
		{
			name:     "plain 403 -> tokenCreator grant",
			err:      fmt.Errorf("wrapped: %w", &googleapi.Error{Code: http.StatusForbidden, Message: "Permission 'iam.serviceAccounts.signJwt' denied."}),
			wantHelp: "serviceAccountTokenCreator",
		},
		{
			name:     "404 -> wrong SA email",
			err:      &googleapi.Error{Code: http.StatusNotFound, Message: "Not found"},
			wantHelp: "set-impersonate",
		},
		{
			name: "anything else stays unclassified",
			err:  errors.New("dial tcp: connection refused"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := classifyCredentialError(t.Context(), tt.err, sa, noSigner)
			if tt.wantHelp == "" {
				if got != nil {
					t.Fatalf("classified %v as %v, want unclassified (nil)", tt.err, got)
				}
				return
			}
			var authErr *Error
			if !errors.As(got, &authErr) {
				t.Fatalf("classify(%v) = %v, want *auth.Error", tt.err, got)
			}
			if !strings.Contains(got.Error(), tt.wantHelp) {
				t.Errorf("classified error = %q, want it to mention %q", got, tt.wantHelp)
			}
			if !strings.Contains(got.Error(), sa) {
				t.Errorf("classified error does not name the target SA:\n%s", got)
			}
		})
	}
}

func TestClassifyCredentialErrorNil(t *testing.T) {
	t.Parallel()
	if got := classifyCredentialError(t.Context(), nil, "sa@x", noSigner); got != nil {
		t.Errorf("classify(nil) = %v, want nil", got)
	}
}

// noSigner is a detector that learns nothing about the signer.
func noSigner(context.Context) Signer {
	return Signer{Description: "your Application Default Credentials"}
}

// On a VM without user ADC the VM's account signs. The 403 hint must name
// that account and print the grant for it, where it used to say "Your
// account" and send the operator to grant the role to the wrong principal.
func TestClassifyForbiddenNamesTheMetadataSigner(t *testing.T) {
	t.Parallel()
	const (
		target = "cco-cli@p.iam.gserviceaccount.com"
		vmSA   = "1234-compute@developer.gserviceaccount.com"
	)
	detected := false
	detect := func(context.Context) Signer {
		detected = true
		return Signer{Email: vmSA, Member: "serviceAccount:" + vmSA, Description: "this VM's service account " + vmSA, VM: true}
	}
	err := classifyCredentialError(t.Context(),
		&googleapi.Error{Code: http.StatusForbidden, Message: "Permission 'iam.serviceAccounts.signJwt' denied."},
		target, detect)
	if !detected {
		t.Fatal("the 403 path did not ask the detector for the signer")
	}
	if err == nil {
		t.Fatal("a 403 must classify")
	}
	msg := err.Error()
	for _, want := range []string{
		vmSA,
		"gcloud iam service-accounts add-iam-policy-binding " + target +
			" --member=serviceAccount:" + vmSA + " --role=roles/iam.serviceAccountTokenCreator",
		"cfctl auth login",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("hint lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "Your account") {
		t.Errorf("hint still blames the user's own account:\n%s", msg)
	}
}

// The detector runs only on the 403 path, since it may call the metadata
// server or tokeninfo.
func TestClassifyDetectsTheSignerOnlyForForbidden(t *testing.T) {
	t.Parallel()
	detect := func(context.Context) Signer {
		t.Error("detector called outside the 403 path")
		return Signer{}
	}
	for _, err := range []error{
		errors.New("google: could not find default credentials"),
		&googleapi.Error{Code: http.StatusNotFound, Message: "Not found"},
		errors.New("dial tcp: connection refused"),
	} {
		_ = classifyCredentialError(t.Context(), err, "sa@x", detect)
	}
}

func TestSignerFromJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, json, wantMember, wantDesc string
	}{
		{
			name:       "key file",
			json:       `{"type":"service_account","client_email":"k@p.iam.gserviceaccount.com"}`,
			wantMember: "serviceAccount:k@p.iam.gserviceaccount.com",
			wantDesc:   "the key file's service account k@p.iam.gserviceaccount.com",
		},
		{
			name: "federated with impersonation",
			json: `{"type":"external_account","service_account_impersonation_url":` +
				`"https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/ci@p.iam.gserviceaccount.com:generateAccessToken"}`,
			wantMember: "serviceAccount:ci@p.iam.gserviceaccount.com",
			wantDesc:   "the impersonated service account ci@p.iam.gserviceaccount.com",
		},
		{name: "federated without impersonation", json: `{"type":"external_account"}`, wantDesc: "the federated identity"},
		{name: "gcloud user, no token source", json: `{"type":"authorized_user"}`, wantDesc: "your gcloud ADC user"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := signerFromJSON(t.Context(), []byte(tt.json), nil)
			if got.Member != tt.wantMember || got.Description != tt.wantDesc {
				t.Errorf("signer = %+v, want member %q, description %q", got, tt.wantMember, tt.wantDesc)
			}
		})
	}
}
