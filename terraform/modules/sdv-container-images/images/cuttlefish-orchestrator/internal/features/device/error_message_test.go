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

package devicefeature

import (
	"strings"
	"testing"
)

func TestTruncateErrorMessage(t *testing.T) {
	if got := truncateErrorMessage("boom"); got != "boom" {
		t.Errorf("short message changed: %q", got)
	}

	long := strings.Repeat("x", maxErrorMessageLen+100)
	got := truncateErrorMessage(long)
	if n := len([]rune(got)); n != maxErrorMessageLen {
		t.Errorf("truncated length = %d, want %d", n, maxErrorMessageLen)
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("expected an ellipsis suffix on a truncated message")
	}

	// Length is counted in Unicode code points (Spanner STRING length), not bytes.
	multibyte := strings.Repeat("界", maxErrorMessageLen+50)
	if n := len([]rune(truncateErrorMessage(multibyte))); n != maxErrorMessageLen {
		t.Errorf("multibyte truncated rune length = %d, want %d", n, maxErrorMessageLen)
	}
}
