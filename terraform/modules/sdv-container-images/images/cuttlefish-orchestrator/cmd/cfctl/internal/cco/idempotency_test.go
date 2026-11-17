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

package cco

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// okJSON answers every attempt with an empty JSON object, which decodes into
// any of the create outcomes.
func okJSON(w http.ResponseWriter, _ int) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{}`))
}

// createCase is one create endpoint under the shared Idempotency-Key contract.
type createCase struct {
	name string
	// create issues a create with the caller's key, or "" to mint one.
	create func(ctx context.Context, c *Client, key string) error
	// plan issues the endpoint's validate_only form. It is nil when the
	// endpoint has no plan mode.
	plan func(ctx context.Context, c *Client) error
}

func createCases() []createCase {
	return []createCase{
		{
			name: "device",
			create: func(ctx context.Context, c *Client, key string) error {
				_, err := c.CreateDevice(ctx, CreateDeviceRequest{Name: "d1"}, key)
				return err
			},
			plan: func(ctx context.Context, c *Client) error {
				_, err := c.CreateDevice(ctx, CreateDeviceRequest{Name: "d1", ValidateOnly: true}, "")
				return err
			},
		},
		{
			name: "host",
			create: func(ctx context.Context, c *Client, key string) error {
				_, err := c.CreateHost(ctx, CreateHostRequest{Name: "h1"}, key)
				return err
			},
		},
		{
			name: "batch",
			create: func(ctx context.Context, c *Client, key string) error {
				_, err := c.CreateBatch(ctx, CreateBatchRequest{NamePrefix: "ci", Count: 2}, key)
				return err
			},
			plan: func(ctx context.Context, c *Client) error {
				_, err := c.CreateBatch(ctx, CreateBatchRequest{NamePrefix: "ci", Count: 2, ValidateOnly: true}, "")
				return err
			},
		},
	}
}

// TestCreatesCarryAnIdempotencyKey checks the shared create contract. A
// caller's key is sent as given, a missing key is minted per call, and a plan
// sends none.
func TestCreatesCarryAnIdempotencyKey(t *testing.T) {
	t.Parallel()

	for _, tc := range createCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := newRecordingServer(t, okJSON)
			c := newTestClient(srv, time.Millisecond)

			if err := tc.create(t.Context(), c, "run-99"); err != nil {
				t.Fatalf("create with a caller key: %v", err)
			}
			if got := srv.key(1); got != "run-99" {
				t.Errorf("Idempotency-Key = %q, want the caller's run-99", got)
			}

			if err := tc.create(t.Context(), c, ""); err != nil {
				t.Fatalf("create without a caller key: %v", err)
			}
			minted := srv.key(2)
			if minted == "" {
				t.Error("Idempotency-Key is empty: a create without a caller key must mint one")
			}

			// A second keyless create must mint a different key. A constant key
			// would make the server merge two creates into one resource.
			if err := tc.create(t.Context(), c, ""); err != nil {
				t.Fatalf("second create without a caller key: %v", err)
			}
			if again := srv.key(3); again == minted {
				t.Errorf("both keyless creates minted %q: the key must be per-call", again)
			}

			if tc.plan == nil {
				return
			}
			if err := tc.plan(t.Context(), c); err != nil {
				t.Fatalf("plan: %v", err)
			}
			if got := srv.key(4); got != "" {
				t.Errorf("plan sent Idempotency-Key %q, want none: a plan commits nothing", got)
			}
		})
	}
}

// TestCreateReplaysWithTheSameKey checks that a create retried after a 503
// reuses the same key, so the server returns the original resource. A key
// minted per attempt would provision a second resource on retry.
func TestCreateReplaysWithTheSameKey(t *testing.T) {
	t.Parallel()

	// Each case returns the ID the client decoded, so the test checks what the
	// caller receives.
	tests := []struct {
		name string
		call func(ctx context.Context, c *Client) (string, error)
	}{
		{
			name: "host",
			call: func(ctx context.Context, c *Client) (string, error) {
				h, err := c.CreateHost(ctx, CreateHostRequest{Name: "h1"}, "")
				if err != nil {
					return "", err
				}
				return h.ID, nil
			},
		},
		{
			name: "device",
			call: func(ctx context.Context, c *Client) (string, error) {
				d, err := c.CreateDevice(ctx, CreateDeviceRequest{Name: "d1"}, "")
				if err != nil {
					return "", err
				}
				return d.ID, nil
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := newRecordingServer(t, func(w http.ResponseWriter, attempt int) {
				if attempt == 1 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"settled-1"}`))
			})
			c := newTestClient(srv, time.Millisecond)

			id, err := tc.call(t.Context(), c)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if id != "settled-1" {
				t.Errorf("id = %q, want settled-1: the caller must get the replay's success", id)
			}

			if got := srv.attempts(); got != 2 {
				t.Fatalf("attempts = %d, want 2: a keyed POST must be replayed after a 503", got)
			}
			first, second := srv.key(1), srv.key(2)
			if first == "" {
				t.Fatal("the first attempt carried no Idempotency-Key")
			}
			if first != second {
				t.Errorf("keys = %q then %q, want the replay to reuse the first key", first, second)
			}
		})
	}
}
