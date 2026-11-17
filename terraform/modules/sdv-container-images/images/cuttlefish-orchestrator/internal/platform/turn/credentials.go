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

// Package turn mints and verifies the ephemeral credentials WebRTC clients use
// to relay media through the deployment's TURN servers.
//
// Credentials are stateless: the expiry, the caller and the resource whose
// media is carried all live in the username, and the password is that username
// signed with the shared secret. Nothing is stored, which is also why a minted
// credential cannot be revoked before it expires.
package turn

import (
	"crypto/hmac"
	"crypto/md5"  //nolint:gosec // Required by the STUN long-term credential mechanism (RFC 5389)
	"crypto/sha1" //nolint:gosec // Required by TURN REST API spec (RFC 5766)
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// PasswordAlgorithm selects the digest a TURN server uses to derive the
// long-term key from the password.
type PasswordAlgorithm string

// The digests a TURN server may ask for. MD5 is the RFC 5389 default;
// SHA256 is the RFC 8489 successor.
const (
	AlgorithmMD5    PasswordAlgorithm = "md5"
	AlgorithmSHA256 PasswordAlgorithm = "sha256"
)

var (
	// ErrMalformedUsername reports a username that is not the REST API triple
	// this package mints.
	ErrMalformedUsername = errors.New("malformed TURN username")

	// ErrUnknownAlgorithm reports a key derivation this package cannot perform.
	ErrUnknownAlgorithm = errors.New("unknown TURN password algorithm")
)

// SubjectKind names the kind of resource a credential carries media for.
//
// It is part of the username because the two surfaces scope differently: the
// native API mints per device, while the legacy infra_config endpoint is per
// host, whose path carries no device. Without the kind the authority cannot
// know which repository to resolve the identifier against.
type SubjectKind string

// The resources a credential may be scoped to.
const (
	SubjectDevice SubjectKind = "device"
	SubjectHost   SubjectKind = "host"
)

// IsValid reports whether the kind is one this package mints.
func (k SubjectKind) IsValid() bool {
	switch k {
	case SubjectDevice, SubjectHost:
		return true
	default:
		return false
	}
}

// Subject is the resource a credential authorizes media for.
type Subject struct {
	Kind SubjectKind
	ID   string
}

// DeviceSubject scopes a credential to one device.
func DeviceSubject(id string) Subject { return Subject{Kind: SubjectDevice, ID: id} }

// HostSubject scopes a credential to a host, and so to every device on it.
func HostSubject(id string) Subject { return Subject{Kind: SubjectHost, ID: id} }

// String renders the subject as it appears inside a username.
func (s Subject) String() string { return string(s.Kind) + ":" + s.ID }

// Username is the parsed form of a credential username minted by Generate.
type Username struct {
	ExpiresAt time.Time
	Email     string
	Subject   Subject
}

// ParseUsername splits the "<expiry>:<email>:<kind>:<id>" quadruple.
//
// None of an RFC 5322 address, a subject kind, or a resource UUID may contain
// a colon, so the four fields are unambiguous.
func ParseUsername(username string) (Username, error) {
	parts := strings.Split(username, ":")
	if len(parts) != 4 {
		return Username{}, fmt.Errorf("%w: want <expiry>:<email>:<kind>:<id>", ErrMalformedUsername)
	}
	expiry, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return Username{}, fmt.Errorf("%w: unparseable expiry: %w", ErrMalformedUsername, err)
	}
	if parts[1] == "" || parts[3] == "" {
		return Username{}, fmt.Errorf("%w: empty email or subject ID", ErrMalformedUsername)
	}
	kind := SubjectKind(parts[2])
	if !kind.IsValid() {
		return Username{}, fmt.Errorf("%w: unknown subject kind %q", ErrMalformedUsername, parts[2])
	}
	return Username{
		ExpiresAt: time.Unix(expiry, 0),
		Email:     parts[1],
		Subject:   Subject{Kind: kind, ID: parts[3]},
	}, nil
}

// CredentialService generates ephemeral credentials for TURN servers.
//
// It implements the standard TURN REST API authentication mechanism (HMAC-SHA1),
// allowing clients to relay WebRTC media through the deployment's TURN servers.
//
// Lifetime is the only policy an instance carries, so a deployment needs one.
// No client can renew a credential mid-session. An expiry is baked into the
// username, so a new one means a new allocation, and neither the browser nor
// the legacy client has a way to re-gather without rebuilding the connection.
// The TTL is therefore a session ceiling on every surface rather than a
// per-surface trade-off.
type CredentialService struct {
	secret string
	ttl    time.Duration
}

// Credential is a minted username and its password.
//
// It names no servers. One credential authenticates on every relay the
// deployment runs, because verification depends on the shared secret alone.
// Which URIs it is advertised with belongs to whoever composes the answer,
// and the two legs of an asymmetric configuration can share a single mint.
type Credential struct {
	Username string
	Password string
}

// NewCredentialService creates a minting and verification authority over one
// shared secret.
func NewCredentialService(secret string, ttl time.Duration) *CredentialService {
	return &CredentialService{secret: secret, ttl: ttl}
}

// Sign returns the password for a username: the base64 HMAC-SHA1 of the
// username under the shared secret, per the TURN REST API.
//
// Minting and verification both go through here so the two can never drift.
func (s *CredentialService) Sign(username string) string {
	h := hmac.New(sha1.New, []byte(s.secret))
	h.Write([]byte(username))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// DeriveKey returns the long-term key a TURN server checks MESSAGE-INTEGRITY
// against: the digest of "username:realm:password".
//
// A server that delegates authentication asks for this rather than the
// password itself, so the shared secret never has to leave the orchestrator.
func (s *CredentialService) DeriveKey(username, realm string, algorithm PasswordAlgorithm) ([]byte, error) {
	material := strings.Join([]string{username, realm, s.Sign(username)}, ":")
	switch algorithm {
	case AlgorithmMD5:
		sum := md5.Sum([]byte(material)) //nolint:gosec // Fixed by RFC 5389.
		return sum[:], nil
	case AlgorithmSHA256:
		sum := sha256.Sum256([]byte(material))
		return sum[:], nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownAlgorithm, algorithm)
	}
}

// Mint creates a TURN credential for a caller and the resource whose media it
// will carry.
func (s *CredentialService) Mint(userEmail string, subject Subject) Credential {
	expiry := time.Now().Add(s.ttl).Unix()
	username := fmt.Sprintf("%d:%s:%s", expiry, userEmail, subject)
	return Credential{Username: username, Password: s.Sign(username)}
}
