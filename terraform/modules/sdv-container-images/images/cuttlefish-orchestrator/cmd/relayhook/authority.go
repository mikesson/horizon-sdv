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
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// errDenied reports a credential the orchestrator refused. It is separate
// from a transport failure because only one of the two is the credential's
// fault, and only one of them is worth retrying.
var errDenied = errors.New("credential refused")

// authority is the orchestrator, as this process needs it.
type authority struct {
	client   *http.Client
	endpoint string
	timeout  time.Duration
}

type authorizeRequest struct {
	Username  string `json:"username"`
	Realm     string `json:"realm"`
	Algorithm string `json:"algorithm"`
}

type authorizeResponse struct {
	Key       string `json:"key"`
	ExpiresAt int64  `json:"expires_at"`
}

// decision is an accepted credential: the key to hand the relay, and the
// moment it stops being valid.
type decision struct {
	key       []byte
	expiresAt time.Time
}

// authorize asks the orchestrator to vouch for a credential.
//
// A refusal is reported as errDenied so the caller can tell it apart from a
// network failure; both deny the client, but only the first is conclusive.
func (a *authority) authorize(ctx context.Context, req authorizeRequest) (decision, error) {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	body, err := json.Marshal(req)
	if err != nil {
		return decision{}, fmt.Errorf("encoding request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(body))
	if err != nil {
		return decision{}, fmt.Errorf("building request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return decision{}, fmt.Errorf("calling orchestrator: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Cap the read: an error body is small, and an IAP redirect page is
		// not something to log in full.
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return decision{}, fmt.Errorf("%w: %s: %s", errDenied, resp.Status, bytes.TrimSpace(detail))
		}
		return decision{}, fmt.Errorf("orchestrator returned %s: %s", resp.Status, bytes.TrimSpace(detail))
	}

	var decoded authorizeResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return decision{}, fmt.Errorf("decoding response: %w", err)
	}
	key, err := base64.StdEncoding.DecodeString(decoded.Key)
	if err != nil {
		return decision{}, fmt.Errorf("decoding key: %w", err)
	}
	if len(key) == 0 {
		return decision{}, errors.New("orchestrator returned an empty key")
	}
	return decision{key: key, expiresAt: time.Unix(decoded.ExpiresAt, 0)}, nil
}
