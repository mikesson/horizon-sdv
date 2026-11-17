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

package models

import (
	"testing"

	"cloud.google.com/go/spanner"
)

func TestBuildRowToDomain_NonObjectMetadata(t *testing.T) {
	t.Parallel()
	r := &BuildRow{
		BuildID:  "b1",
		Name:     "build",
		Target:   "aosp_cf_x86_64_phone-userdebug",
		Metadata: spanner.NullJSON{Value: "just a string", Valid: true},
	}
	b := r.ToDomain()
	if len(b.Metadata) != 0 {
		t.Errorf("ToDomain().Metadata = %#v, want empty map", b.Metadata)
	}
}
