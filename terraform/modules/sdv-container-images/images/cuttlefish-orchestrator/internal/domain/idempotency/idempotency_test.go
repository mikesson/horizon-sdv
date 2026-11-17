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

package idempotency

import (
	"context"
	"errors"
	"testing"
)

// fakeStore is a single-record Store for exercising the resolve paths.
type fakeStore struct {
	rec *Record
	err error
}

func (f *fakeStore) Lookup(context.Context, string) (*Record, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	if f.rec == nil {
		return nil, false, nil
	}
	return f.rec, true, nil
}

func TestResolveRace(t *testing.T) {
	t.Parallel()
	winnerReq := map[string]string{"name": "a"}
	fp, err := Fingerprint(winnerReq)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	loadOK := func(_ context.Context, id string) (string, error) { return "resource-" + id, nil }

	t.Run("equivalent request gets the winner's resource", func(t *testing.T) {
		t.Parallel()
		store := &fakeStore{rec: &Record{ResourceID: "w1", Fingerprint: fp}}
		got, found, err := ResolveRace(t.Context(), store, "k1", fp, loadOK)
		if err != nil || !found || got != "resource-w1" {
			t.Errorf("got (%q, %v, %v), want the winner's resource", got, found, err)
		}
	})

	t.Run("different request reusing the key conflicts, never the wrong resource", func(t *testing.T) {
		t.Parallel()
		otherFP, err := Fingerprint(map[string]string{"name": "b"})
		if err != nil {
			t.Fatalf("Fingerprint: %v", err)
		}
		store := &fakeStore{rec: &Record{ResourceID: "w1", Fingerprint: fp}}
		got, found, err := ResolveRace(t.Context(), store, "k1", otherFP, loadOK)
		if !errors.Is(err, ErrConflict) {
			t.Errorf("err = %v, want ErrConflict", err)
		}
		if found || got != "" {
			t.Errorf("got (%q, %v), want no resource on conflict", got, found)
		}
	})

	t.Run("unused key falls through", func(t *testing.T) {
		t.Parallel()
		_, found, err := ResolveRace(t.Context(), &fakeStore{}, "k1", fp, loadOK)
		if found || err != nil {
			t.Errorf("(found, err) = (%v, %v), want a silent miss", found, err)
		}
	})

	t.Run("lookup failure falls through to the caller's commit error", func(t *testing.T) {
		t.Parallel()
		store := &fakeStore{err: errors.New("spanner unavailable")}
		_, found, err := ResolveRace(t.Context(), store, "k1", fp, loadOK)
		if found || err != nil {
			t.Errorf("(found, err) = (%v, %v), want a silent miss", found, err)
		}
	})

	t.Run("load failure falls through to the caller's commit error", func(t *testing.T) {
		t.Parallel()
		store := &fakeStore{rec: &Record{ResourceID: "w1", Fingerprint: fp}}
		loadErr := func(context.Context, string) (string, error) { return "", errors.New("not found") }
		_, found, err := ResolveRace(t.Context(), store, "k1", fp, loadErr)
		if found || err != nil {
			t.Errorf("(found, err) = (%v, %v), want a silent miss", found, err)
		}
	})
}
