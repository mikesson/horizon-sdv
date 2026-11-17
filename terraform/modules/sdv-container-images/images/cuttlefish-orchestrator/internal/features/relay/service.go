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

package relayfeature

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	devicedomain "cloud-cuttlefish-orchestrator/internal/domain/device"
	hostdomain "cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/platform/turn"
)

var (
	// ErrCredentialExpired reports a credential presented past its expiry.
	ErrCredentialExpired = errors.New("TURN credential expired")

	// ErrRealmMismatch reports a relay authenticating under a realm this
	// deployment does not serve.
	ErrRealmMismatch = errors.New("TURN realm mismatch")

	// ErrSubjectGone reports a credential whose subject no longer exists in a
	// state that can carry media.
	ErrSubjectGone = errors.New("credential subject is no longer streamable")
)

// DeviceLookup resolves the device a credential names.
//
// Declared here rather than imported: a feature slice may not depend on
// another feature slice, so the composition root supplies the device service.
type DeviceLookup interface {
	Get(ctx context.Context, id string) (*devicedomain.Device, error)
}

// HostLookup resolves the host a credential names. The legacy surface mints
// per host, because its infra_config path names no device.
type HostLookup interface {
	Get(ctx context.Context, id string) (*hostdomain.Host, error)
}

// Service decides whether a TURN credential is still good and, when it is,
// derives the key the relay needs.
type Service struct {
	creds   *turn.CredentialService
	devices DeviceLookup
	hosts   HostLookup
	realm   string
	logger  *slog.Logger

	// now is injectable so expiry can be tested without a clock.
	now func() time.Time
}

// NewService creates the relay credential authority for a realm.
func NewService(creds *turn.CredentialService, devices DeviceLookup, hosts HostLookup, realm string, logger *slog.Logger) *Service {
	return &Service{
		creds:   creds,
		devices: devices,
		hosts:   hosts,
		realm:   realm,
		logger:  logger,
		now:     time.Now,
	}
}

// AuthorizeRequest is a relay asking whether it may admit a client.
type AuthorizeRequest struct {
	Username  string
	Realm     string
	Algorithm turn.PasswordAlgorithm
}

// Authorization is the decision, when it is favorable.
type Authorization struct {
	Key       []byte
	ExpiresAt time.Time
}

// Authorize validates a credential and returns its long-term key.
//
// Checks run cheapest-first so a stale credential costs no database read.
func (s *Service) Authorize(ctx context.Context, req AuthorizeRequest) (*Authorization, error) {
	parsed, err := turn.ParseUsername(req.Username)
	if err != nil {
		return nil, fmt.Errorf("parsing presented username: %w", err)
	}

	if req.Realm != s.realm {
		s.deny(ctx, parsed, "realm mismatch", slog.String("presented_realm", req.Realm))
		return nil, fmt.Errorf("%w: relay presented %q, deployment serves %q", ErrRealmMismatch, req.Realm, s.realm)
	}

	if !s.now().Before(parsed.ExpiresAt) {
		s.deny(ctx, parsed, "credential expired", slog.Time("expired_at", parsed.ExpiresAt))
		return nil, fmt.Errorf("%w at %s", ErrCredentialExpired, parsed.ExpiresAt.UTC().Format(time.RFC3339))
	}

	if err := s.checkSubject(ctx, parsed); err != nil {
		return nil, err
	}

	key, err := s.creds.DeriveKey(req.Username, req.Realm, req.Algorithm)
	if err != nil {
		return nil, fmt.Errorf("deriving long-term key: %w", err)
	}

	s.logger.Debug("relay credential authorized",
		slog.String("subject_kind", string(parsed.Subject.Kind)),
		slog.String("subject_id", parsed.Subject.ID),
		slog.String("email", parsed.Email),
		slog.Time("expires_at", parsed.ExpiresAt))

	return &Authorization{Key: key, ExpiresAt: parsed.ExpiresAt}, nil
}

// checkSubject reports whether the resource a credential names may still carry
// media. A terminal one is refused: the credential outlives the thing it was
// minted for, and relay bandwidth is not free.
//
// The switch needs no default arm, because ParseUsername rejects a kind this
// package does not mint, so a new kind must appear here to compile.
func (s *Service) checkSubject(ctx context.Context, u turn.Username) error {
	switch u.Subject.Kind {
	case turn.SubjectDevice:
		dev, err := s.devices.Get(ctx, u.Subject.ID)
		if err != nil {
			s.deny(ctx, u, "device lookup failed", slog.Any("error", err))
			return fmt.Errorf("resolving device %s: %w", u.Subject.ID, err)
		}
		if dev.Status.IsTerminal() {
			s.deny(ctx, u, "device is terminal", slog.String("status", string(dev.Status)))
			return fmt.Errorf("%w: device %s is %s", ErrSubjectGone, u.Subject.ID, dev.Status)
		}
	case turn.SubjectHost:
		hostVM, err := s.hosts.Get(ctx, u.Subject.ID)
		if err != nil {
			s.deny(ctx, u, "host lookup failed", slog.Any("error", err))
			return fmt.Errorf("resolving host %s: %w", u.Subject.ID, err)
		}
		if hostVM.Status.IsTerminal() {
			s.deny(ctx, u, "host is terminal", slog.String("status", string(hostVM.Status)))
			return fmt.Errorf("%w: host %s is %s", ErrSubjectGone, u.Subject.ID, hostVM.Status)
		}
	}
	return nil
}

// deny records a refusal. Every denial is logged: a relay answers a bare 401
// and cannot explain itself, so this is the only place the reason exists.
func (s *Service) deny(ctx context.Context, u turn.Username, reason string, extra ...slog.Attr) {
	attrs := append([]slog.Attr{
		slog.String("reason", reason),
		slog.String("subject_kind", string(u.Subject.Kind)),
		slog.String("subject_id", u.Subject.ID),
		slog.String("email", u.Email),
	}, extra...)
	s.logger.LogAttrs(ctx, slog.LevelWarn, "relay credential denied", attrs...)
}
