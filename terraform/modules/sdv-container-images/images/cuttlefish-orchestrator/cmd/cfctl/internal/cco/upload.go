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
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// uploadAttempts bounds consecutive failed probe-and-resume cycles against a
// session. The count resets whenever the confirmed offset advances.
const uploadAttempts = 4

// uploadRetryUnit scales the linear backoff between failed attempts. Tests
// shrink it.
var uploadRetryUnit = 2 * time.Second

// maxRetryAfter caps how long a 429 Retry-After can park the upload.
const maxRetryAfter = 5 * time.Minute

// statusResumeIncomplete is GCS's "Resume Incomplete", the 308 code with
// resumable-upload semantics instead of a permanent redirect.
const statusResumeIncomplete = 308

// ErrUploadChecksumMismatch reports that GCS rejected the bytes because their
// CRC32C differs from the one computed when the file was hashed.
var ErrUploadChecksumMismatch = errors.New("the uploaded bytes do not match the checksum computed when the file was hashed")

// sessionClient sends GCS resumable-session requests without credentials.
// GCS rejects the API credential on a session URL as a bad OAuth token. It has
// no timeout because the command context bounds a multi-gigabyte transfer.
var sessionClient = &http.Client{}

// UploadFile is a local file to upload, with the size and CRC32C (Castagnoli)
// computed when it was hashed. GCS verifies the checksum before it finalizes
// the object.
type UploadFile struct {
	Path   string
	Size   int64
	CRC32C uint32
}

// UploadFileToSession PUTs a file to a GCS resumable-upload session and
// resumes from the confirmed offset after an interruption. The optional
// progress callback observes the running byte count.
func UploadFileToSession(ctx context.Context, sessionURL string, file UploadFile, progress func(sent int64)) error {
	var lastErr error
	failures := 0
	var confirmed int64
	for {
		if failures > 0 {
			if failures >= uploadAttempts {
				return fmt.Errorf("upload did not complete after %d attempts without progress: %w", failures, lastErr)
			}
			delay := time.Duration(failures) * uploadRetryUnit
			if r, ok := errors.AsType[*retryableUploadError](lastErr); ok && r.retryAfter > delay {
				delay = r.retryAfter
			}
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return fmt.Errorf("upload canceled: %w", context.Cause(ctx))
			}
		}

		offset, done, err := probeSessionOffset(ctx, sessionURL, file.Size)
		if err != nil {
			if _, ok := errors.AsType[*terminalUploadError](err); ok {
				return err
			}
			lastErr = err
			failures++
			continue
		}
		if done {
			return nil
		}
		if offset > confirmed {
			confirmed = offset
			failures = 0
		}

		if err := putFromOffset(ctx, sessionURL, file, offset, progress); err != nil {
			if _, ok := errors.AsType[*terminalUploadError](err); ok {
				return err
			}
			lastErr = err
			failures++
			continue
		}
		return nil
	}
}

// terminalUploadError marks a server verdict retrying cannot change.
type terminalUploadError struct {
	msg string
	err error
}

func (e *terminalUploadError) Error() string { return e.msg }

func (e *terminalUploadError) Unwrap() error { return e.err }

// retryableUploadError is a refusal worth retrying (408, 429, 5xx).
// retryAfter is the server's requested delay, zero when it named none.
type retryableUploadError struct {
	msg        string
	retryAfter time.Duration
}

func (e *retryableUploadError) Error() string { return e.msg }

// classifySessionResponse turns a failed session response into a terminal or
// retryable error. GCS asks for a retry with 408 and 429; other 4xx codes are
// final.
func classifySessionResponse(resp *http.Response, what string) error {
	switch {
	case resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests:
		e := &retryableUploadError{msg: fmt.Sprintf("%s: HTTP %d", what, resp.StatusCode)}
		if resp.StatusCode == http.StatusTooManyRequests {
			if d, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
				e.retryAfter = min(d, maxRetryAfter)
			}
		}
		return e
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &terminalUploadError{msg: fmt.Sprintf("%s refused (HTTP %d): %s", what, resp.StatusCode, string(body))}
	default:
		return &retryableUploadError{msg: fmt.Sprintf("%s: HTTP %d", what, resp.StatusCode)}
	}
}

// probeSessionOffset asks the session how many bytes it has persisted.
// A fresh session reports offset 0; a finished one reports done.
func probeSessionOffset(ctx context.Context, sessionURL string, size int64) (int64, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, sessionURL, http.NoBody)
	if err != nil {
		return 0, false, fmt.Errorf("building session probe: %w", err)
	}
	req.Header.Set("Content-Range", fmt.Sprintf("bytes */%d", size))
	resp, err := sessionClient.Do(req)
	if err != nil {
		return 0, false, fmt.Errorf("probing session: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return size, true, nil
	case statusResumeIncomplete:
		r := resp.Header.Get("Range") // "bytes=0-<last persisted>"
		if r == "" {
			return 0, false, nil
		}
		last, err := strconv.ParseInt(r[strings.LastIndex(r, "-")+1:], 10, 64)
		if err != nil {
			return 0, false, fmt.Errorf("parsing session range %q: %w", r, err)
		}
		return last + 1, false, nil
	default:
		return 0, false, classifySessionResponse(resp, "session probe")
	}
}

// crc32cHeader is the X-Goog-Hash value for a CRC32C: the big-endian checksum,
// base64-encoded.
func crc32cHeader(sum uint32) string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], sum)
	return "crc32c=" + base64.StdEncoding.EncodeToString(b[:])
}

// putFromOffset streams the file's remainder into the session. The PUT always
// runs to the end of the object, so it carries the whole-object CRC32C.
func putFromOffset(ctx context.Context, sessionURL string, file UploadFile, offset int64, progress func(sent int64)) error {
	f, err := os.Open(file.Path)
	if err != nil {
		return fmt.Errorf("opening %s: %w", file.Path, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return fmt.Errorf("seeking %s: %w", file.Path, err)
	}

	var body io.Reader = f
	if progress != nil {
		body = &countingReader{r: f, base: offset, observe: progress}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, sessionURL, body)
	if err != nil {
		return fmt.Errorf("building upload request: %w", err)
	}
	req.ContentLength = file.Size - offset
	req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, file.Size-1, file.Size))
	req.Header.Set("X-Goog-Hash", crc32cHeader(file.CRC32C))

	resp, err := sessionClient.Do(req)
	if err != nil {
		return fmt.Errorf("uploading: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return nil
	case http.StatusBadRequest:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		msg := fmt.Sprintf("upload refused (HTTP %d): %s", resp.StatusCode, string(body))
		if isChecksumRejection(string(body)) {
			return &terminalUploadError{msg: msg, err: ErrUploadChecksumMismatch}
		}
		return &terminalUploadError{msg: msg}
	default:
		return classifySessionResponse(resp, "upload")
	}
}

// isChecksumRejection reports whether a GCS 400 body names a checksum
// mismatch. A 400 about a malformed X-Goog-Hash header does not qualify.
func isChecksumRejection(body string) bool {
	lower := strings.ToLower(body)
	names := strings.Contains(lower, "crc32c") || strings.Contains(lower, "checksum")
	return names && strings.Contains(lower, "match")
}

// countingReader reports cumulative bytes read to observe.
type countingReader struct {
	r       io.Reader
	base    int64
	sent    int64
	observe func(int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.sent += int64(n)
	c.observe(c.base + c.sent)
	return n, err
}
