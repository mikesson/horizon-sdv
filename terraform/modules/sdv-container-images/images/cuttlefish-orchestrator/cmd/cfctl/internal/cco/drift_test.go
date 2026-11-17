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
	"reflect"
	"strings"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	hostconfigfeature "cloud-cuttlefish-orchestrator/internal/features/hostconfig"
)

// deliberateDrops lists server response fields cfctl does not mirror, keyed by
// pair name. Add an entry only with a reason, because json.Unmarshal discards
// unknown fields without an error.
var deliberateDrops = map[string]map[string]bool{}

// TestResponseTypesMirrorServer checks that each hand-written response type
// has every JSON field the server sends and no field the server never sends.
// The scripts/ci.sh gen job covers the generated aliases.
func TestResponseTypesMirrorServer(t *testing.T) {
	t.Parallel()
	pairs := []struct {
		name           string
		server, client any
	}{
		// System-config bodies, one pair per distinct shape. The GET body
		// stands for the PUT echo, which has the same shape.
		{"ZonePinValue", hostconfigfeature.GetZonePinOutput{}.Body, StringValue{}},
		{"QuotaMarginValue", hostconfigfeature.GetQuotaMarginOutput{}.Body, Int64Value{}},
		{"V1ZoneHintValue", hostconfigfeature.GetV1ZoneHintOutput{}.Body, BoolValue{}},
		{"SpotAutoDefaultValue", hostconfigfeature.GetSpotAutoDefaultOutput{}.Body, BoolValue{}},
		{"GPUMachineTypeValue", hostconfigfeature.GetDefaultGPUMachineTypeOutput{}.Body, StringValue{}},
		// The operation handle and its two nested shapes.
		{"Operation", operation.Operation{}, Operation{}},
		{"OperationMetadata", operation.Metadata{}, OperationMetadata{}},
		{"OperationProblem", operation.Problem{}, OperationProblem{}},
	}
	for _, p := range pairs {
		serverFields := jsonFieldNames(t, reflect.TypeOf(p.server))
		clientFields := jsonFieldNames(t, reflect.TypeOf(p.client))
		for f := range serverFields {
			if !clientFields[f] && !deliberateDrops[p.name][f] {
				t.Errorf("cfctl %s drops server field %q: json.Unmarshal discards it silently. Mirror it in types.go or record it in deliberateDrops with a reason", p.name, f)
			}
		}
		for f := range clientFields {
			if !serverFields[f] {
				t.Errorf("cfctl %s carries phantom field %q the server never sends", p.name, f)
			}
		}
	}
}

// jsonFieldNames returns the type's JSON-visible field names, flattening
// embedded structs the way encoding/json does.
func jsonFieldNames(t *testing.T, typ reflect.Type) map[string]bool {
	t.Helper()
	fields := map[string]bool{}
	for f := range typ.Fields() {
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		if f.Anonymous && tag == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				for name := range jsonFieldNames(t, ft) {
					fields[name] = true
				}
				continue
			}
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			name = f.Name
		}
		fields[name] = true
	}
	return fields
}
