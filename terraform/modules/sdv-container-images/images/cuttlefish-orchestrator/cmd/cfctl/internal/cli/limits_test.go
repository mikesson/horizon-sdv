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

package cli

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	adminfeature "cloud-cuttlefish-orchestrator/internal/features/admin"
	operationfeature "cloud-cuttlefish-orchestrator/internal/features/operation"
)

// TestListLimitsMatchServerCaps pins the CLI's --limit bounds and the range
// in their help text to the server's Huma maximum tags.
func TestListLimitsMatchServerCaps(t *testing.T) {
	cases := []struct {
		name        string
		serverInput any
		clientMax   int
		flags       any
	}{
		{"operation list", operationfeature.ListOperationsInput{}, maxOperationsLimit, OperationListCmd{}},
		{"admin tasks list", adminfeature.ListFailedTasksInput{}, maxFailedTasksLimit, AdminTasksListCmd{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			serverField, ok := reflect.TypeOf(tc.serverInput).FieldByName("Limit")
			if !ok {
				t.Fatalf("server input %T has no Limit field", tc.serverInput)
			}
			if got := serverField.Tag.Get("maximum"); got != strconv.Itoa(tc.clientMax) {
				t.Errorf("server maximum = %s, client cap = %d. Update the CLI constant and help text", got, tc.clientMax)
			}

			clientField, ok := reflect.TypeOf(tc.flags).FieldByName("Limit")
			if !ok {
				t.Fatalf("command %T has no Limit flag", tc.flags)
			}
			wantRange := fmt.Sprintf("(1-%d)", tc.clientMax)
			if help := clientField.Tag.Get("help"); !strings.Contains(help, wantRange) {
				t.Errorf("--limit help %q does not state the range %s", help, wantRange)
			}
		})
	}
}
