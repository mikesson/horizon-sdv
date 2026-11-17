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

// Fuzzing for the name boundary: InstanceName is where a string a user typed
// becomes a Google Compute Engine resource identifier. GCE rejects a name that
// breaks its grammar, so a sanitizer that lets one character through fails VM
// creation for that host and nothing earlier catches it. The request is valid
// CCO input right up to the API call.

package host

import (
	"regexp"
	"strings"
	"testing"
)

// gceInstanceName is GCE's documented grammar for instance names: 1-63
// characters, lowercase letter first, then letters, digits or hyphens, and no
// trailing hyphen.
var gceInstanceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,61}[a-z0-9]$|^[a-z]$`)

// isUUIDLike reports whether s could be a UUID: hex digits in either case,
// hyphens anywhere, and at least the eight hex digits InstanceName consumes.
func isUUIDLike(s string) bool {
	hex := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
			hex++
		case r == '-':
		default:
			return false
		}
	}
	return hex >= 8
}

func FuzzInstanceName(f *testing.F) {
	// Seeds cover the shapes the sanitizer has to reduce: spaces and
	// underscores, uppercase, hyphen runs, leading and trailing punctuation,
	// non-ASCII, the reserved prefix, and a name long enough to be truncated.
	seeds := []struct {
		userInput   string
		hostID      string
		incarnation int64
	}{
		{"", "3f2b1a0c-1111-2222-3333-444455556666", 0},
		{"my host", "3f2b1a0c-1111-2222-3333-444455556666", 0},
		{"My_Host--Name", "3f2b1a0c-1111-2222-3333-444455556666", 1},
		{"---", "3f2b1a0c-1111-2222-3333-444455556666", 0},
		{"héllo wörld", "3f2b1a0c-1111-2222-3333-444455556666", 2},
		{"cf-already-prefixed", "3f2b1a0c-1111-2222-3333-444455556666", 0},
		{strings.Repeat("a", 200), "3f2b1a0c-1111-2222-3333-444455556666", 9},
		{"...", "3f2b1a0c-1111-2222-3333-444455556666", 1 << 62},
		{"ok", "00000000-0000-0000-0000-000000000000", 0},
	}
	for _, s := range seeds {
		f.Add(s.userInput, s.hostID, s.incarnation)
	}

	f.Fuzz(func(t *testing.T, userInput, hostID string, incarnation int64) {
		// The identity segment is the caller's contract: a host ID is a UUID.
		// Anything else is out of scope: fuzzing shorter ones would only
		// rediscover that slicing a short string panics, which is true of
		// every such function and says nothing about this one. Case is NOT
		// excluded: RFC 4122 requires readers to accept uppercase, so an
		// uppercase UUID is a UUID, and this is where the fuzzer earns its
		// keep.
		if !isUUIDLike(hostID) {
			t.Skip("host IDs are UUIDs; anything else is outside the contract")
		}

		name := InstanceName(userInput, hostID, incarnation)

		if len(name) > 63 {
			t.Fatalf("name is %d characters, GCE allows 63: %q", len(name), name)
		}
		if !gceInstanceName.MatchString(name) {
			t.Fatalf("name is not a legal GCE instance name: %q", name)
		}
		if !strings.HasPrefix(name, InstancePrefix) {
			t.Fatalf("name %q lost the %q namespace prefix", name, InstancePrefix)
		}
		if strings.Contains(name, "--") {
			t.Fatalf("name %q contains a collapsed hyphen run", name)
		}

		// Purity: the orphan GC recomputes the names of a live host's stale
		// incarnations rather than parsing them back, so the same inputs must
		// always give the same name.
		if again := InstanceName(userInput, hostID, incarnation); again != name {
			t.Fatalf("not deterministic: %q then %q", name, again)
		}

		// Incarnations must not collide, or the GC would delete the VM it is
		// standing on. Incarnation 0 is unsuffixed by design, so compare
		// against a different one rather than assuming a suffix is present.
		other := incarnation + 1
		if other > 0 {
			if o := InstanceName(userInput, hostID, other); o == name {
				t.Fatalf("incarnations %d and %d share the name %q", incarnation, other, o)
			}
		}
	})
}

func FuzzSanitizeForGCE(f *testing.F) {
	for _, s := range []string{"", "a", "A B", "__--__", "ünïcödé", "9leading-digit", "-x-", strings.Repeat("-a", 100)} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		got := sanitizeForGCE(input)
		if got == "" {
			return // Empty is a legal outcome; InstanceName handles it.
		}
		for _, r := range got {
			lower := r >= 'a' && r <= 'z'
			digit := r >= '0' && r <= '9'
			if !lower && !digit && r != '-' {
				t.Fatalf("sanitized %q kept %q, outside GCE's alphabet", got, r)
			}
		}
		if strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
			t.Fatalf("sanitized %q has a boundary hyphen", got)
		}
		if strings.Contains(got, "--") {
			t.Fatalf("sanitized %q has an uncollapsed hyphen run", got)
		}
		// Idempotent: sanitizing an already-sanitized name must not change it,
		// or a name would drift each time it passed through.
		if again := sanitizeForGCE(got); again != got {
			t.Fatalf("not idempotent: %q sanitized again is %q", got, again)
		}
	})
}
