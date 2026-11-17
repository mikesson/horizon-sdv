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
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	devicedomain "cloud-cuttlefish-orchestrator/internal/domain/device"
	hostdomain "cloud-cuttlefish-orchestrator/internal/domain/host"
)

// transitionClosure returns every status a ValidTransitions table mentions,
// as keys or targets, sorted. This is the server's full status vocabulary.
func transitionClosure[S ~string](transitions map[S][]S) []string {
	set := map[string]bool{}
	for from, targets := range transitions {
		set[string(from)] = true
		for _, to := range targets {
			set[string(to)] = true
		}
	}
	return slices.Sorted(maps.Keys(set))
}

// enumTagValues returns a flag's enum tag as a sorted slice, dropping the
// empty value that only marks the filter as optional.
func enumTagValues(t *testing.T, cmd any, field string) []string {
	t.Helper()
	f, ok := reflect.TypeOf(cmd).FieldByName(field)
	if !ok {
		t.Fatalf("%T has no %s field", cmd, field)
	}
	var values []string
	for v := range strings.SplitSeq(f.Tag.Get("enum"), ",") {
		if v != "" {
			values = append(values, v)
		}
	}
	slices.Sort(values)
	return values
}

// TestStatusFilterEnumsMatchDomainStateMachines pins the --status enum tags
// to the statuses in the domain transition tables.
func TestStatusFilterEnumsMatchDomainStateMachines(t *testing.T) {
	deviceWant := transitionClosure(devicedomain.ValidTransitions)
	if got := enumTagValues(t, DeviceListCmd{}, "Status"); !slices.Equal(got, deviceWant) {
		t.Errorf("device list --status enum = %v, domain vocabulary = %v", got, deviceWant)
	}

	hostWant := transitionClosure(hostdomain.ValidTransitions)
	if got := enumTagValues(t, HostListCmd{}, "Status"); !slices.Equal(got, hostWant) {
		t.Errorf("host list --status enum = %v, domain vocabulary = %v", got, hostWant)
	}
}
