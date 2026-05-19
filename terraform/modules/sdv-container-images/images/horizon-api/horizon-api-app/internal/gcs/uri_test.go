// Copyright (c) 2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
package gcs

import (
	"fmt"
	"testing"
)

func TestObjectBaseName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want string
	}{
		{"gs://my-bucket/workflows/abc/nodes/node-0/smoke-result.tgz", "smoke-result.tgz"},
		{"gs://b/single", "single"},
		{"gs://b/prefix/main-logs.gz", "main-logs.gz"},
		{"not gs://", ""},
		{"", ""},
		{"gs://onlybucket", ""},
	}
	for i, tc := range tests {
		tc := tc
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			t.Parallel()
			got := ObjectBaseName(tc.in)
			if got != tc.want {
				t.Fatalf("ObjectBaseName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
