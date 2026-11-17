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
	"errors"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"cloud-cuttlefish-orchestrator/internal/platform/relay/turnrs"
)

// hookService answers the relay's authentication hook.
type hookService struct {
	turnrs.UnimplementedTurnHooksServiceServer

	authority *authority
	logger    *slog.Logger
	maxCache  time.Duration
	now       func() time.Time

	mu     sync.RWMutex
	cached map[cacheKey]decision
}

// cacheKey is everything the derived key depends on. Realm and algorithm are
// part of the digest, so a change in either must miss.
//
// The fields are never read: they carry the key's identity through map
// equality, which is exactly what must not be simplified away. Dropping one
// would collapse distinct realms or algorithms onto a single cache entry and
// hand back a key derived for different inputs.
//
//nolint:unused // Written but never read; the fields are the map-key identity.
type cacheKey struct {
	username  string
	realm     string
	algorithm string
}

func newHookService(a *authority, maxCache time.Duration, logger *slog.Logger) *hookService {
	return &hookService{
		authority: a,
		logger:    logger,
		maxCache:  maxCache,
		now:       time.Now,
		cached:    make(map[cacheKey]decision),
	}
}

// GetPassword returns the long-term key for a credential, or refuses it.
//
// The relay maps any error to "no password" and answers the client 401, so
// every failure path here denies. That is the intended behavior: a hook that
// cannot reach the orchestrator must not fall back to admitting clients.
func (s *hookService) GetPassword(ctx context.Context, req *turnrs.GetTurnPasswordRequest) (*turnrs.GetTurnPasswordResponse, error) {
	algorithm, err := algorithmName(req.GetAlgorithm())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	key := cacheKey{username: req.GetUsername(), realm: req.GetRealm(), algorithm: algorithm}

	if cached, ok := s.lookup(key); ok {
		return &turnrs.GetTurnPasswordResponse{Password: cached.key}, nil
	}

	got, err := s.authority.authorize(ctx, authorizeRequest{
		Username:  req.GetUsername(),
		Realm:     req.GetRealm(),
		Algorithm: algorithm,
	})
	if err != nil {
		// A refusal is the orchestrator's verdict; anything else is this
		// process failing to obtain one. Both deny, and the distinction only
		// exists in the log.
		level, reason := slog.LevelWarn, "denied"
		if !errors.Is(err, errDenied) {
			level, reason = slog.LevelError, "unavailable"
		}
		s.logger.LogAttrs(ctx, level, "refusing credential",
			slog.String("outcome", reason),
			slog.String("username", req.GetUsername()),
			slog.Any("error", err))
		return nil, status.Error(codes.PermissionDenied, "credential refused")
	}

	s.store(key, got)
	s.logger.LogAttrs(ctx, slog.LevelInfo, "credential authorized",
		slog.String("username", req.GetUsername()),
		slog.Time("expires_at", got.expiresAt))
	return &turnrs.GetTurnPasswordResponse{Password: got.key}, nil
}

// lookup returns a cached decision that is still good.
func (s *hookService) lookup(key cacheKey) (decision, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	got, ok := s.cached[key]
	if !ok || !s.now().Before(got.expiresAt) {
		return decision{}, false
	}
	return got, true
}

// store caches a decision until the credential expires, or maxCache from now,
// whichever comes first. The ceiling bounds how long a revocation upstream can
// go unnoticed; the credential's own expiry is the hard limit.
func (s *hookService) store(key cacheKey, d decision) {
	if ceiling := s.now().Add(s.maxCache); d.expiresAt.After(ceiling) {
		d.expiresAt = ceiling
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictExpiredLocked()
	s.cached[key] = d
}

// evictExpiredLocked drops lapsed entries. Called on write, which is the only
// path that grows the map, so it cannot be outrun by reads.
func (s *hookService) evictExpiredLocked() {
	now := s.now()
	for k, v := range s.cached {
		if !now.Before(v.expiresAt) {
			delete(s.cached, k)
		}
	}
}

// algorithmName maps the relay's enum onto the orchestrator's wire values.
func algorithmName(a turnrs.PasswordAlgorithm) (string, error) {
	switch a {
	case turnrs.PasswordAlgorithm_PASSWORD_ALGORITHM_MD5, turnrs.PasswordAlgorithm_PASSWORD_ALGORITHM_UNSPECIFIED:
		return "md5", nil
	case turnrs.PasswordAlgorithm_PASSWORD_ALGORITHM_SHA256:
		return "sha256", nil
	default:
		return "", errors.New("unsupported password algorithm")
	}
}

// The event hooks are informational. They are answered immediately and never
// touch the network: they share the relay's one-second budget with
// GetPassword, and blocking here would stall authentication.

func (s *hookService) OnAllocatedEvent(ctx context.Context, e *turnrs.TurnAllocatedEvent) (*emptypb.Empty, error) {
	s.logger.LogAttrs(ctx, slog.LevelDebug, "allocation opened",
		slog.String("username", e.GetUsername()), slog.Int("port", int(e.GetPort())))
	return &emptypb.Empty{}, nil
}

func (s *hookService) OnChannelBindEvent(ctx context.Context, e *turnrs.TurnChannelBindEvent) (*emptypb.Empty, error) {
	s.logger.LogAttrs(ctx, slog.LevelDebug, "channel bound", slog.String("username", e.GetUsername()))
	return &emptypb.Empty{}, nil
}

func (s *hookService) OnCreatePermissionEvent(ctx context.Context, e *turnrs.TurnCreatePermissionEvent) (*emptypb.Empty, error) {
	s.logger.LogAttrs(ctx, slog.LevelDebug, "permission created", slog.String("username", e.GetUsername()))
	return &emptypb.Empty{}, nil
}

func (s *hookService) OnRefreshEvent(ctx context.Context, e *turnrs.TurnRefreshEvent) (*emptypb.Empty, error) {
	s.logger.LogAttrs(ctx, slog.LevelDebug, "allocation refreshed",
		slog.String("username", e.GetUsername()), slog.Int("lifetime", int(e.GetLifetime())))
	return &emptypb.Empty{}, nil
}

func (s *hookService) OnDestroyEvent(ctx context.Context, e *turnrs.TurnDestroyEvent) (*emptypb.Empty, error) {
	s.logger.LogAttrs(ctx, slog.LevelDebug, "session destroyed", slog.String("username", e.GetUsername()))
	return &emptypb.Empty{}, nil
}
