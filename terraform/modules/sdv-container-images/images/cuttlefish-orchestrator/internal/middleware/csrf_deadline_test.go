// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCSRFProtection(t *testing.T) {
	t.Parallel()

	if _, err := CSRFProtection([]string{"not a valid origin"}); err == nil {
		t.Fatal("expected invalid trusted origin error")
	}

	mw, err := CSRFProtection([]string{"http://localhost:4200"})
	if err != nil {
		t.Fatalf("CSRFProtection failed: %v", err)
	}
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Safe GET cross-site request is allowed.
	req := httptest.NewRequest(http.MethodGet, "/v2/devices", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("safe GET status = %d, want 200", rec.Code)
	}

	// Non-browser API client POST (no Origin, no Sec-Fetch-Site) is allowed.
	req = httptest.NewRequest(http.MethodPost, "/v2/devices", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("CLI POST status = %d, want 200", rec.Code)
	}

	// Cross-site POST is blocked with 403.
	req = httptest.NewRequest(http.MethodPost, "/v2/devices", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site POST status = %d, want 403", rec.Code)
	}

	// Cross-origin POST from explicitly trusted origin passes.
	req = httptest.NewRequest(http.MethodPost, "/v2/devices", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Origin", "http://localhost:4200")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("trusted-origin POST status = %d, want 200", rec.Code)
	}
}

func TestRequestDeadlines(t *testing.T) {
	t.Parallel()

	// Verify graceful behavior with ResponseRecorder (which has no SetWriteDeadline).
	mw := RequestDeadlines(func(_ *http.Request) Deadlines {
		return Deadlines{Read: 50 * time.Millisecond, Write: 50 * time.Millisecond}
	})
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fast", nil))
	if rec.Code != http.StatusNoContent {
		t.Errorf("recorder status = %d, want 204", rec.Code)
	}

	// Verify against a real TCP http.Server that short Write deadline aborts a stalled response.
	mwServer := RequestDeadlines(func(r *http.Request) Deadlines {
		if r.URL.Path == "/slow" {
			return Deadlines{Read: time.Second, Write: 40 * time.Millisecond}
		}
		return Deadlines{}
	})
	srv := httptest.NewServer(mwServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(150 * time.Millisecond)
			_, _ = w.Write([]byte("late"))
			return
		}
		_, _ = w.Write([]byte("ok"))
	})))
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(srv.URL + "/ok")
	if err != nil {
		t.Fatalf("GET /ok failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "ok" {
		t.Errorf("/ok body = %q, want ok", string(body))
	}

	slowResp, slowErr := srv.Client().Get(srv.URL + "/slow")
	if slowErr == nil {
		defer func() { _ = slowResp.Body.Close() }()
		slowBody, readErr := io.ReadAll(slowResp.Body)
		if readErr == nil && string(slowBody) == "late" {
			t.Fatal("expected /slow write deadline to interrupt response")
		}
	}
}
