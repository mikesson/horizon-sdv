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
	"bytes"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func castagnoli(b []byte) uint32 {
	return crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli))
}

// flakySession persists part of the body and then fails once, like a GCS
// session that lost its connection. The client must resume from the persisted
// range that the next probe reports.
type flakySession struct {
	mu        sync.Mutex
	failAfter int // bytes to accept before the injected failure; 0 = healthy
	persisted []byte
	total     int64
	putRanges []string // Content-Range of each data PUT, for assertions
	hashes    []string // X-Goog-Hash of each data PUT
}

func (s *flakySession) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cr := r.Header.Get("Content-Range")

	// Probe: empty body, "bytes */N".
	if r.ContentLength == 0 {
		if int64(len(s.persisted)) == s.total {
			w.WriteHeader(http.StatusOK)
			return
		}
		if len(s.persisted) > 0 {
			w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(s.persisted)-1))
		}
		w.WriteHeader(statusResumeIncomplete)
		return
	}

	s.putRanges = append(s.putRanges, cr)
	s.hashes = append(s.hashes, r.Header.Get("X-Goog-Hash"))
	if s.failAfter > 0 {
		s.persisted = append(s.persisted, mustRead(r.Body, s.failAfter)...)
		s.failAfter = 0
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	rest, _ := io.ReadAll(r.Body)
	s.persisted = append(s.persisted, rest...)
	w.WriteHeader(http.StatusOK)
}

func mustRead(r io.Reader, n int) []byte {
	buf := make([]byte, n)
	_, _ = io.ReadFull(r, buf)
	return buf
}

func TestUploadFileToSessionResumesFromPersistedOffset(t *testing.T) {
	t.Parallel()
	content := bytes.Repeat([]byte("0123456789"), 100) // 1000 bytes
	path := filepath.Join(t.TempDir(), "artifact.zip")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	session := &flakySession{failAfter: 400, total: int64(len(content))}
	srv := httptest.NewServer(session)
	t.Cleanup(srv.Close)

	var lastProgress int64
	err := UploadFileToSession(t.Context(), srv.URL, UploadFile{Path: path, Size: int64(len(content)), CRC32C: castagnoli(content)},
		func(sent int64) { lastProgress = sent })
	if err != nil {
		t.Fatalf("UploadFileToSession: %v", err)
	}

	if !bytes.Equal(session.persisted, content) {
		t.Errorf("assembled %d bytes, want the original %d", len(session.persisted), len(content))
	}
	if len(session.putRanges) != 2 {
		t.Fatalf("putRanges = %v, want an initial PUT and one resume", session.putRanges)
	}
	if want := fmt.Sprintf("bytes 400-999/%d", len(content)); session.putRanges[1] != want {
		t.Errorf("resume Content-Range = %q, want %q (resume from the persisted offset)", session.putRanges[1], want)
	}
	if lastProgress != int64(len(content)) {
		t.Errorf("final progress = %d, want %d", lastProgress, len(content))
	}
	// Both PUTs run to the end of the object, so both carry the whole-object
	// checksum.
	want := crc32cHeader(castagnoli(content))
	for i, h := range session.hashes {
		if h != want {
			t.Errorf("PUT %d X-Goog-Hash = %q, want %q", i, h, want)
		}
	}
}

func TestCRC32CHeaderIsBigEndianBase64(t *testing.T) {
	t.Parallel()
	// RFC 3720 test vector: CRC32C of 32 zero bytes is 0x8a9136aa.
	if got, want := crc32cHeader(castagnoli(make([]byte, 32))), "crc32c=ipE2qg=="; got != want {
		t.Errorf("crc32cHeader = %q, want %q", got, want)
	}
}

func TestUploadFileToSessionTerminalRefusal(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "artifact.zip")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	err := UploadFileToSession(t.Context(), srv.URL, UploadFile{Path: path, Size: 4}, nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("err = %v, want an immediate 403 refusal (no retries)", err)
	}
}

func TestUploadFileToSessionChecksumRejectionIsTyped(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "artifact.zip")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength == 0 {
			w.WriteHeader(statusResumeIncomplete)
			return
		}
		http.Error(w, "Provided CRC32C \"AAAAAA==\" doesn't match calculated CRC32C", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	err := UploadFileToSession(t.Context(), srv.URL, UploadFile{Path: path, Size: 4, CRC32C: castagnoli([]byte("other"))}, nil)
	if !errors.Is(err, ErrUploadChecksumMismatch) {
		t.Fatalf("err = %v, want ErrUploadChecksumMismatch", err)
	}
}

func TestIsChecksumRejection(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		body string
		want bool
	}{
		{"Provided CRC32C \"AAAAAA==\" doesn't match calculated CRC32C", true},
		{"checksum mismatch", true},
		{"Invalid X-Goog-Hash header value", false},
		{"The crc32c hash is not valid base64", false},
		{"Invalid request", false},
	} {
		if got := isChecksumRejection(tc.body); got != tc.want {
			t.Errorf("isChecksumRejection(%q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}

// TestUploadFileToSessionRetries408And429 checks that GCS's retry codes do not
// end the upload, and that a 429's Retry-After is honored.
func TestUploadFileToSessionRetries408And429(t *testing.T) {
	shrinkUploadBackoff(t)
	content := []byte("payload")
	path := filepath.Join(t.TempDir(), "artifact.zip")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	puts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.ContentLength == 0 {
			w.WriteHeader(statusResumeIncomplete)
			return
		}
		_, _ = io.ReadAll(r.Body)
		puts++
		switch puts {
		case 1:
			w.WriteHeader(http.StatusRequestTimeout)
		case 2:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)

	if err := UploadFileToSession(t.Context(), srv.URL, UploadFile{Path: path, Size: int64(len(content)), CRC32C: castagnoli(content)}, nil); err != nil {
		t.Fatalf("UploadFileToSession: %v", err)
	}
	if puts != 3 {
		t.Errorf("puts = %d, want 3 (408 and 429 retried)", puts)
	}
}

func TestParseRetryAfterIsCappedForUploads(t *testing.T) {
	t.Parallel()
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"86400"}},
		Body:       http.NoBody,
	}
	r, ok := errors.AsType[*retryableUploadError](classifySessionResponse(resp, "upload"))
	if !ok {
		t.Fatal("429 must be retryable")
	}
	if r.retryAfter != maxRetryAfter {
		t.Errorf("retryAfter = %v, want the %v cap", r.retryAfter, maxRetryAfter)
	}
	resp = &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: http.NoBody}
	if _, ok := errors.AsType[*terminalUploadError](classifySessionResponse(resp, "upload")); !ok {
		t.Error("404 must stay terminal")
	}
}

// TestUploadFileToSessionResetsBudgetOnProgress fails every PUT after
// persisting a few bytes. Each failure advances the confirmed offset, so the
// upload outlives uploadAttempts failures in total.
func TestUploadFileToSessionResetsBudgetOnProgress(t *testing.T) {
	shrinkUploadBackoff(t)
	content := bytes.Repeat([]byte("x"), 60)
	path := filepath.Join(t.TempDir(), "artifact.zip")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	const chunk = 10 // six failing PUTs of 10 bytes, more than uploadAttempts
	var mu sync.Mutex
	var persisted []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.ContentLength == 0 {
			if len(persisted) == len(content) {
				w.WriteHeader(http.StatusOK)
				return
			}
			if len(persisted) > 0 {
				w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(persisted)-1))
			}
			w.WriteHeader(statusResumeIncomplete)
			return
		}
		persisted = append(persisted, mustRead(r.Body, chunk)...)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	if err := UploadFileToSession(t.Context(), srv.URL, UploadFile{Path: path, Size: int64(len(content)), CRC32C: castagnoli(content)}, nil); err != nil {
		t.Fatalf("UploadFileToSession: %v (budget must reset on progress)", err)
	}
	if !bytes.Equal(persisted, content) {
		t.Errorf("persisted %d bytes, want %d", len(persisted), len(content))
	}
}

// shrinkUploadBackoff makes retry waits instant for one test. Callers must not
// be parallel, since the unit is package state.
func shrinkUploadBackoff(t *testing.T) {
	t.Helper()
	old := uploadRetryUnit
	uploadRetryUnit = time.Millisecond
	t.Cleanup(func() { uploadRetryUnit = old })
}
