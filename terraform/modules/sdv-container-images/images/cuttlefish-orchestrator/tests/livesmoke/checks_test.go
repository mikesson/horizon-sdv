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
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeZIP builds an archive with the given members and returns its path.
// Members are Stored, not Deflated, so a test can corrupt a known plaintext byte
// and still find it in the file.
func writeZIP(t *testing.T, members map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range members {
		f, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatalf("creating %s: %v", name, err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing archive: %v", err)
	}
	path := filepath.Join(t.TempDir(), "artifact.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("writing archive: %v", err)
	}
	return path
}

func TestReadZIPListsEveryMember(t *testing.T) {
	t.Parallel()
	path := writeZIP(t, map[string]string{
		"cvd-1/logs/kernel.log":   "kernel",
		"cvd-1/logs/launcher.log": "launcher",
		"fetch.log":               "fetch",
	})

	names, err := readZIP(path)
	if err != nil {
		t.Fatalf("readZIP: %v", err)
	}
	if len(names) != 3 {
		t.Fatalf("got %d members, want 3: %v", len(names), names)
	}
	// The arm matches members by suffix; prove that predicate against this
	// archive rather than trusting it holds.
	for _, want := range bugreportMembers {
		found := false
		for _, n := range names {
			if strings.HasSuffix(n, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no member ends with %q: %v", want, names)
		}
	}
}

// The failure that actually happened in production, reduced: a body that is a
// web page, saved with a .zip name.
func TestReadZIPRejectsAWebPage(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "artifact.zip")
	body := []byte(`<!doctype html><html lang="en-US"><title>Sign in - Google Accounts</title></html>`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	if _, err := readZIP(path); err == nil {
		t.Fatal("a sign-in page was accepted as a bugreport archive")
	}
}

func TestReadZIPRejectsAnEmptyFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "artifact.zip")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	if _, err := readZIP(path); err == nil {
		t.Fatal("an empty file was accepted as a bugreport archive")
	}
}

// This is the case that justifies decompressing every entry instead of just
// opening the archive. The central directory is untouched, so the file opens and
// lists its members perfectly; only pulling the bytes through exposes that one
// member's contents no longer match its checksum.
func TestReadZIPRejectsIntactDirectoryOverCorruptData(t *testing.T) {
	t.Parallel()
	const payload = "UNIQUE-PAYLOAD-BYTES"
	path := writeZIP(t, map[string]string{"cvd-1/logs/kernel.log": payload})

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	at := bytes.Index(raw, []byte(payload))
	if at < 0 {
		t.Fatal("stored payload not found; the fixture is not byte-addressable")
	}
	raw[at] ^= 0xFF
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("rewriting fixture: %v", err)
	}

	// Precondition: the archive must still open, or this proves nothing about
	// reading entries and would only repeat the previous test.
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("corrupting the payload also broke the directory: %v", err)
	}
	_ = r.Close()

	if _, err := readZIP(path); err == nil {
		t.Fatal("corrupt member data was accepted; opening the archive is not enough")
	}
}

// The classifier must sort a real header-only artifact into the reported
// bucket rather than the failed one, and must still fail a body that is not
// WebM at all. The bytes below are the leading elements of an artifact the
// live smoke actually archived.
func TestRecordingClassification(t *testing.T) {
	t.Parallel()
	headerOnly := []byte{0x1A, 0x45, 0xDF, 0xA3, 0x9F, 0x42, 0x86, 0x81, 0x01}
	withTrack := append(append([]byte{}, headerOnly...), 0x16, 0x54, 0xAE, 0x6B)
	withFrames := append(append([]byte{}, withTrack...), 0x1F, 0x43, 0xB6, 0x75)

	for _, tc := range []struct {
		name                 string
		body                 []byte
		webm, tracks, frames bool
	}{
		{"a sign-in page", []byte("<!doctype html>"), false, false, false},
		{"header only", headerOnly, true, false, false},
		{"track but no frames", withTrack, true, true, false},
		{"a usable recording", withFrames, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := bytes.HasPrefix(tc.body, webmMagic); got != tc.webm {
				t.Errorf("WebM-framed = %v, want %v", got, tc.webm)
			}
			if got := bytes.Contains(tc.body, webmTracks); got != tc.tracks {
				t.Errorf("has Tracks = %v, want %v", got, tc.tracks)
			}
			if got := bytes.Contains(tc.body, webmCluster); got != tc.frames {
				t.Errorf("has Cluster = %v, want %v", got, tc.frames)
			}
		})
	}
}

// Placement bin-packs, so a create can land on a host somebody else is already
// using. Teardown force-deletes the host, which cascades every device on it,
// so getting this predicate wrong destroys another operator's device. It did
// exactly that once.
func TestOwnsHostOnlyClaimsWhatTheRunProvisioned(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 8, 8, 18, 59, 0, 0, time.UTC)

	for _, tc := range []struct {
		name    string
		created time.Time
		want    bool
	}{
		{"provisioned by this run", started.Add(time.Minute), true},
		{"pre-dates the run", started.Add(-2 * time.Hour), false},
		{"created at the same instant", started, false},
		{"host detail unavailable", time.Time{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := &driver{started: started, host: host{CreatedAt: tc.created}}
			if got := d.ownsHost(); got != tc.want {
				t.Errorf("ownsHost() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The dlq check excuses the dead-lettered take behind a tolerated retry, and
// nothing else: not a second take, not another device's, not another verb.
func TestExcuseRetriedTakes(t *testing.T) {
	const dev = "d1"
	take := deadTask{ID: "t1", Type: "device.snapshot_operation", ResourceID: dev}
	tests := []struct {
		name        string
		tasks       []deadTask
		retried     int
		wantLeft    int
		wantExcused int
	}{
		{"no retry excuses nothing", []deadTask{take}, 0, 1, 0},
		{"one retry excuses its take", []deadTask{take}, 1, 0, 1},
		{"a second take is still a defect", []deadTask{take, {ID: "t2", Type: take.Type, ResourceID: dev}}, 1, 1, 1},
		{"another device's take is a defect", []deadTask{{ID: "t3", Type: take.Type, ResourceID: "d2"}}, 1, 1, 0},
		{"another verb is a defect", []deadTask{{ID: "t4", Type: "device.powerwash_operation", ResourceID: dev}}, 1, 1, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			left, excused := excuseRetriedTakes(tc.tasks, dev, tc.retried)
			if len(left) != tc.wantLeft || excused != tc.wantExcused {
				t.Errorf("excuseRetriedTakes = (%d left, %d excused), want (%d, %d)",
					len(left), excused, tc.wantLeft, tc.wantExcused)
			}
		})
	}
}
