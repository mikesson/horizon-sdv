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
	"reflect"
	"strconv"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
)

// bodyFieldTag resolves a tag on a field of an input's Body struct.
func bodyFieldTag(t *testing.T, input reflect.Type, field, tag string) string {
	t.Helper()
	body, ok := input.FieldByName("Body")
	if !ok {
		t.Fatalf("%s has no Body field", input.Name())
	}
	f, ok := body.Type.FieldByName(field)
	if !ok {
		t.Fatalf("%s.Body has no %s field", input.Name(), field)
	}
	value, ok := f.Tag.Lookup(tag)
	if !ok {
		t.Fatalf("%s.Body.%s has no `%s` tag: the bound is unenforced", input.Name(), field, tag)
	}
	return value
}

// The ttl_hours maximum tag is a second copy of device.MaxTTLHours. The tag
// is what the API enforces, before any handler runs, and nothing but this
// test links the two.
func TestTTLHoursMaximumTagMatchesDomain(t *testing.T) {
	t.Parallel()
	tag := bodyFieldTag(t, reflect.TypeFor[CreateDeviceInput](), "TTLHours", "maximum")
	if tag != strconv.FormatInt(device.MaxTTLHours, 10) {
		t.Errorf("ttl_hours maximum tag = %q, device.MaxTTLHours = %d", tag, device.MaxTTLHours)
	}
}

// The extend seconds maximum tag mirrors device.MaxExtendHours in seconds.
func TestExtendSecondsMaximumTagMatchesDomain(t *testing.T) {
	t.Parallel()
	tag := bodyFieldTag(t, reflect.TypeFor[ExtendDeviceInput](), "Seconds", "maximum")
	if tag != strconv.FormatInt(device.MaxExtendHours*3600, 10) {
		t.Errorf("extend seconds maximum tag = %q, device.MaxExtendHours = %dh", tag, device.MaxExtendHours)
	}
}
