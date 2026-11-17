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

package host

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

const testHostID = "9f8e7d6c-1111-2222-3333-444455556666"

func TestInstanceName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		userInput   string
		incarnation int64
		want        string
	}{
		{"label and id", "myhost", 0, "cf-myhost-9f8e7d6c"},
		{"no label", "", 0, "cf-9f8e7d6c"},
		{"incarnation suffixed", "myhost", 2, "cf-myhost-9f8e7d6c-i2"},
		{"no label, incarnation", "", 1, "cf-9f8e7d6c-i1"},
		{"uppercase and spaces folded", "My Host", 0, "cf-my-host-9f8e7d6c"},
		{"underscores folded", "my_host", 0, "cf-my-host-9f8e7d6c"},
		{"hyphen runs collapsed", "my---host", 0, "cf-my-host-9f8e7d6c"},
		{"outside-alphabet dropped", "my.host!", 0, "cf-myhost-9f8e7d6c"},
		{"all-invalid degrades to id only", "!!!", 0, "cf-9f8e7d6c"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := InstanceName(tt.userInput, testHostID, tt.incarnation); got != tt.want {
				t.Errorf("InstanceName(%q, _, %d) = %q, want %q",
					tt.userInput, tt.incarnation, got, tt.want)
			}
		})
	}
}

// TestInstanceNameBudgetsTheLabel pins where truncation lands. GCE caps
// instance names at 63, and the identity segment is what must survive: cutting
// from the end would eat the uuid and narrow the uniqueness margin.
func TestInstanceNameBudgetsTheLabel(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 80)
	for _, inc := range []int64{0, 2, 11} {
		got := InstanceName(long, testHostID, inc)
		if len(got) > 63 {
			t.Errorf("incarnation %d: len = %d, want <= 63 (%q)", inc, len(got), got)
		}
		if !strings.HasPrefix(got, InstancePrefix) {
			t.Errorf("incarnation %d: %q lost its prefix", inc, got)
		}
		if !strings.Contains(got, "9f8e7d6c") {
			t.Errorf("incarnation %d: %q truncated into the identity segment", inc, got)
		}
	}
}

func TestValidateDisplayName(t *testing.T) {
	t.Parallel()

	valid := []string{"myhost", "host-x7k2q", "a", "batch", "host-001", ""}
	for _, name := range valid {
		if err := ValidateDisplayName(name); err != nil {
			t.Errorf("ValidateDisplayName(%q) = %v, want nil", name, err)
		}
	}

	// Every one of these was produced by a real caller: the console composed
	// the first, /v1's generateHostName the second.
	reserved := []string{"cf-myhost-a1b2c3d4", "cf-3a2b1c9d", "cf-", "cf-x"}
	for _, name := range reserved {
		if err := ValidateDisplayName(name); err == nil {
			t.Errorf("ValidateDisplayName(%q) = nil, want a ReservedNameError", name)
		}
	}

	// Each matches DisplayNamePattern, so only this check keeps it out.
	pattern := regexp.MustCompile(DisplayNamePattern)
	idShaped := []string{"abcdef01-2345-6789-abcd-ef0123456789", "abcdef0123456789abcdef0123456789"}
	for _, name := range idShaped {
		if !pattern.MatchString(name) {
			t.Fatalf("%q does not match DisplayNamePattern; this case proves nothing", name)
		}
		err := ValidateDisplayName(name)
		if _, ok := errors.AsType[*IDShapedNameError](err); !ok {
			t.Errorf("ValidateDisplayName(%q) = %v, want an IDShapedNameError", name, err)
		}
	}

	// One character off the UUID shape is an ordinary name.
	for _, name := range []string{"abcdef01-2345-6789-abcd-ef012345678", "abcdef0123456789abcdef012345678g", "build-abcdef01"} {
		if err := ValidateDisplayName(name); err != nil {
			t.Errorf("ValidateDisplayName(%q) = %v, want nil", name, err)
		}
	}
}

// TestReservedPrefixIsWhatBlocksDoubling is the assertion the guard rests on:
// it shows the guard is not decorative by composing the exact names it rejects
// and confirming they would have produced "cf-cf-…".
func TestReservedPrefixIsWhatBlocksDoubling(t *testing.T) {
	t.Parallel()

	pattern := regexp.MustCompile(DisplayNamePattern)

	for _, name := range []string{"cf-myhost-a1b2c3d4", "cf-3a2b1c9d"} {
		// The pattern alone lets these through, which is why the prefix
		// reservation cannot be expressed as a `pattern` tag.
		if !pattern.MatchString(name) {
			t.Errorf("%q does not match DisplayNamePattern; this test no longer proves anything", name)
		}
		if got := InstanceName(name, testHostID, 0); !strings.HasPrefix(got, "cf-cf-") {
			t.Errorf("InstanceName(%q) = %q, want the doubled form this guard exists to prevent", name, got)
		}
		if err := ValidateDisplayName(name); err == nil {
			t.Errorf("ValidateDisplayName(%q) = nil: the doubled name above would ship", name)
		}
	}

	// And a name the guard admits never doubles.
	for _, name := range []string{"myhost", "host-x7k2q", "batch", ""} {
		if got := InstanceName(name, testHostID, 0); strings.HasPrefix(got, "cf-cf-") {
			t.Errorf("InstanceName(%q) = %q, doubled despite passing validation", name, got)
		}
	}
}
