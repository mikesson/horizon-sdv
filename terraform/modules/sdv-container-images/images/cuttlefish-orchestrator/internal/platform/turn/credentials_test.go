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

package turn

import (
	"testing"
	"time"
)

// Minting and verification are the two halves of one contract, and they live
// in different packages. A username must survive the trip.
func TestMintedUsernameParsesBack(t *testing.T) {
	svc := NewCredentialService("secret", 12*time.Hour)

	for _, subject := range []Subject{DeviceSubject("dev-1"), HostSubject("host-1")} {
		t.Run(string(subject.Kind), func(t *testing.T) {
			cred := svc.Mint("user@example.com", subject)
			if cred.Password == "" {
				t.Error("minted credential carries no password")
			}

			parsed, err := ParseUsername(cred.Username)
			if err != nil {
				t.Fatalf("ParseUsername(%q) error = %v", cred.Username, err)
			}
			if parsed.Subject != subject {
				t.Errorf("subject = %+v, want %+v", parsed.Subject, subject)
			}
			if parsed.Email != "user@example.com" {
				t.Errorf("email = %q, want user@example.com", parsed.Email)
			}
			// The TTL is what the relay's authority enforces, so it has to
			// round-trip as the expiry rather than as a duration.
			if got := time.Until(parsed.ExpiresAt); got < 11*time.Hour || got > 12*time.Hour {
				t.Errorf("expiry in %v, want ~12h", got)
			}
		})
	}
}

// The password is the HMAC of the username, so two mints for the same subject
// carry different secrets, which is what makes an expiry unforgeable rather
// than merely declared.
func TestMintSignsTheUsernameItIssues(t *testing.T) {
	svc := NewCredentialService("secret", time.Hour)

	cred := svc.Mint("user@example.com", DeviceSubject("dev-1"))
	if got := svc.Sign(cred.Username); got != cred.Password {
		t.Errorf("password = %q, want the signature of its own username %q", cred.Password, got)
	}
	if same := svc.Sign(cred.Username + "x"); same == cred.Password {
		t.Error("a different username signed to the same password")
	}
}

func TestParseUsernameRejectsMalformed(t *testing.T) {
	for name, username := range map[string]string{
		"empty":            "",
		"no fields":        "not-a-username",
		"three fields":     "1769000000:user@example.com:dev-1",
		"five fields":      "1769000000:user@example.com:device:dev-1:extra",
		"unparseable time": "notanumber:user@example.com:device:dev-1",
		"empty email":      "1769000000::device:dev-1",
		"empty subject":    "1769000000:user@example.com:device:",
		"unknown kind":     "1769000000:user@example.com:batch:b-1",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseUsername(username); err == nil {
				t.Errorf("ParseUsername(%q) succeeded, want ErrMalformedUsername", username)
			}
		})
	}
}

// The subject kind is what tells the authority which repository to resolve
// against, so an unrecognized one must never reach it as a valid subject.
func TestSubjectKindValidity(t *testing.T) {
	for kind, want := range map[SubjectKind]bool{
		SubjectDevice: true,
		SubjectHost:   true,
		"":            false,
		"batch":       false,
		"Device":      false,
	} {
		if got := kind.IsValid(); got != want {
			t.Errorf("SubjectKind(%q).IsValid() = %v, want %v", kind, got, want)
		}
	}
}
