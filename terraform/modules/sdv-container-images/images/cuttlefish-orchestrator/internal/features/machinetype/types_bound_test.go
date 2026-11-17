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

package machinetypefeature

import (
	"reflect"
	"strconv"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// TestMaxGPUsCeilingMatchesDomain binds the editable catalog ceiling to the
// constant host create is also bound to. Raise this tag alone and the catalog
// starts advertising GPUs that no create can request.
func TestMaxGPUsCeilingMatchesDomain(t *testing.T) {
	t.Parallel()

	body, ok := reflect.TypeFor[UpdateMachineFamilyAdminInput]().FieldByName("Body")
	if !ok {
		t.Fatal("UpdateMachineFamilyAdminInput has no Body field")
	}
	f, ok := body.Type.FieldByName("MaxGPUs")
	if !ok {
		t.Fatal("UpdateMachineFamilyAdminInput.Body has no MaxGPUs field")
	}
	tag, ok := f.Tag.Lookup("maximum")
	if !ok {
		t.Fatal("UpdateMachineFamilyAdminInput.Body.MaxGPUs has no `maximum` tag: the ceiling is unenforced")
	}

	got, err := strconv.ParseInt(tag, 10, 64)
	if err != nil {
		t.Fatalf("maximum tag %q is not an integer: %v", tag, err)
	}
	if got != machinetype.MaxAttachableGPUs {
		t.Errorf("max_gpus maximum tag = %d, machinetype.MaxAttachableGPUs = %d\n"+
			"update the tag in types.go and the constant in domain/machinetype together", got, machinetype.MaxAttachableGPUs)
	}
}
