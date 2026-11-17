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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/oauth2"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// tokenCacheMinValidity is how much validity a cached token must have left to
// be reused, so a long command does not start on a token about to expire.
const tokenCacheMinValidity = 5 * time.Minute

// tokenCacheFile holds impersonation-signed JWTs across invocations, so a
// command does not pay an IAM round trip before its real request.
type tokenCacheFile struct {
	Entries map[string]tokenCacheEntry `json:"entries"`
}

type tokenCacheEntry struct {
	Token  string    `json:"token"`
	Expiry time.Time `json:"expiry"`
}

// TokenCachePath returns token-cache.json in the config directory.
func TokenCachePath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", fmt.Errorf("resolving config dir: %w", err)
	}
	return filepath.Join(dir, "token-cache.json"), nil
}

// ClearTokenCache deletes the token cache. A missing cache is not an error.
func ClearTokenCache() error {
	path, err := TokenCachePath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing token cache: %w", err)
	}
	return nil
}

// diskCachedTokenSource serves a token from the on-disk cache while it has
// tokenCacheMinValidity left, and otherwise mints one with src and stores it.
// Cache failures are logged at debug level and never fail the command.
type diskCachedTokenSource struct {
	mint func() (*oauth2.Token, error)
	path string // empty disables the cache
	key  string
	now  func() time.Time
}

// newDiskCachedTokenSource caches src's tokens under the target service
// account and audience.
func newDiskCachedTokenSource(src *signedJWTTokenSource, targetSA, audience string) *diskCachedTokenSource {
	path, err := TokenCachePath()
	if err != nil {
		output.Debug("auth: token cache disabled: %v", err)
		path = ""
	}
	return &diskCachedTokenSource{mint: src.Token, path: path, key: targetSA + " " + audience, now: time.Now}
}

// Token returns a cached token with enough validity left, or a fresh one.
func (s *diskCachedTokenSource) Token() (*oauth2.Token, error) {
	now := s.now()
	if s.path != "" {
		if e, ok := readTokenCache(s.path).Entries[s.key]; ok && e.Token != "" && e.Expiry.Sub(now) >= tokenCacheMinValidity {
			output.Debug("auth: reusing cached token (expires %s)", e.Expiry.Format(time.RFC3339))
			return &oauth2.Token{AccessToken: e.Token, TokenType: bearerTokenType, Expiry: e.Expiry}, nil
		}
	}
	tok, err := s.mint()
	if err != nil {
		return nil, err
	}
	if s.path != "" {
		if err := s.store(tok, now); err != nil {
			output.Debug("auth: token cache not written: %v", err)
		}
	}
	return tok, nil
}

// store adds tok to the cache file and drops expired entries.
func (s *diskCachedTokenSource) store(tok *oauth2.Token, now time.Time) error {
	cache := readTokenCache(s.path)
	for k, e := range cache.Entries {
		if !e.Expiry.After(now) {
			delete(cache.Entries, k)
		}
	}
	cache.Entries[s.key] = tokenCacheEntry{Token: tok.AccessToken, Expiry: tok.Expiry}
	data, err := json.Marshal(cache)
	if err != nil {
		return fmt.Errorf("encoding token cache: %w", err)
	}
	return writeFileAtomic(s.path, data)
}

// readTokenCache reads the cache file. A missing or corrupt file yields an
// empty cache.
func readTokenCache(path string) *tokenCacheFile {
	cache := &tokenCacheFile{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, cache); err != nil {
			output.Debug("auth: ignoring corrupt token cache: %v", err)
			cache = &tokenCacheFile{}
		}
	}
	if cache.Entries == nil {
		cache.Entries = map[string]tokenCacheEntry{}
	}
	return cache
}

// writeFileAtomic writes data to path with mode 0600 through a rename, so a
// concurrent reader never sees a partial file.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".token-cache-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("renaming token cache: %w", err)
	}
	return nil
}
