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

package hostconfigfeature

import (
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
)

// The fleet PUTs' bound tags are second copies of the compiled bounds. The
// tag is what the API enforces, the constant is what the keeper and the
// settings read enforce, and nothing but this test links them.
//
// Every bound the autoscaler has, not only the standby count: this test
// replaced one that read the deleted pool input's Floor tag, and replacing it
// with a same-shaped single check would have left three of the four new bounds
// unlinked while looking like the same coverage.
func TestFleetBoundTagsMatchDomain(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		input reflect.Type
		field string
		tag   string
		want  int64
	}{
		{reflect.TypeFor[UpdateFleetAutoscalerInput](), "Standby", "maximum", hostconfig.MaxStandbyHosts},
		{reflect.TypeFor[UpdateFleetAutoscalerInput](), "Floor", "minimum", 0},
		{reflect.TypeFor[UpdateFleetAutoscalerInput](), "Floor", "maximum", hostconfig.MaxFleetFloor},
		{reflect.TypeFor[UpdateFleetTuningInput](), "DemandHorizonSec", "minimum", hostconfig.MinDemandHorizonSec},
		{reflect.TypeFor[UpdateFleetTuningInput](), "DemandHorizonSec", "maximum", hostconfig.MaxDemandHorizonSec},
		{reflect.TypeFor[UpdateFleetTuningInput](), "DrainGraceSec", "minimum", hostconfig.MinDrainGraceSec},
		{reflect.TypeFor[UpdateFleetTuningInput](), "DrainGraceSec", "maximum", hostconfig.MaxDrainGraceSec},
		{reflect.TypeFor[UpdateFleetSchedulesInput](), "Schedules", "maxItems", hostconfig.MaxSchedules},
		{reflect.TypeFor[UpdateFleetLadderInput](), "ShapeLadder", "maxItems", hostconfig.MaxLadderRungs},
	} {
		body, ok := tc.input.FieldByName("Body")
		if !ok {
			t.Fatalf("%s has no Body field", tc.input.Name())
		}
		f, ok := body.Type.FieldByName(tc.field)
		if !ok {
			t.Fatalf("%s.Body has no %s field", tc.input.Name(), tc.field)
		}
		if tag := f.Tag.Get(tc.tag); tag != strconv.FormatInt(tc.want, 10) {
			t.Errorf("%s.%s %s tag = %q, domain bound = %d",
				tc.input.Name(), tc.field, tc.tag, tag, tc.want)
		}
	}
}

// A schedule's own bounds are tags on the domain type, because the type is the
// wire shape, so they are two copies of the same constants the write
// validation and OpenAt read, one layer further out than the fleet PUT's.
func TestScheduleBoundTagsMatchDomain(t *testing.T) {
	t.Parallel()
	schedule := reflect.TypeFor[hostconfig.FleetSchedule]()
	for _, tc := range []struct {
		field string
		tag   string
		want  int64
	}{
		{"Name", "maxLength", hostconfig.MaxScheduleNameLen},
		{"Hosts", "maximum", hostconfig.MaxStandbyHosts},
		{"DurationSec", "minimum", hostconfig.MinScheduleDurationSec},
		{"DurationSec", "maximum", hostconfig.MaxScheduleDurationSec},
	} {
		f, ok := schedule.FieldByName(tc.field)
		if !ok {
			t.Fatalf("hostconfig.FleetSchedule has no %s field", tc.field)
		}
		if tag := f.Tag.Get(tc.tag); tag != strconv.FormatInt(tc.want, 10) {
			t.Errorf("Schedule.%s %s tag = %q, domain bound = %d", tc.field, tc.tag, tag, tc.want)
		}
	}
}

// The start's pattern tag and hostconfig.StartLayout are two descriptions of
// one format, and only one of them rejects a request. What must hold is the
// direction that costs something: every value the API accepts must be one the
// evaluator can parse, or a stored window silently never opens.
//
// The converse does not hold, since the layout also parses "8:00" which the
// pattern refuses. That asymmetry is safe, because the stricter copy is the
// one at the door.
func TestScheduleStartPatternIsParseableByTheLayout(t *testing.T) {
	t.Parallel()
	f, ok := reflect.TypeFor[hostconfig.FleetSchedule]().FieldByName("Start")
	if !ok {
		t.Fatal("hostconfig.FleetSchedule has no Start field")
	}
	pattern, err := regexp.Compile(f.Tag.Get("pattern"))
	if err != nil {
		t.Fatalf("the start pattern tag does not compile: %v", err)
	}
	for hour := range 24 {
		for _, minute := range []int{0, 7, 30, 59} {
			value := fmt.Sprintf("%02d:%02d", hour, minute)
			if !pattern.MatchString(value) {
				t.Errorf("the pattern rejects %q, which is a legal wall clock", value)
			}
			if _, perr := time.Parse(hostconfig.StartLayout, value); perr != nil {
				t.Errorf("the layout cannot parse %q, which the pattern accepts", value)
			}
		}
	}
	for _, bad := range []string{"24:00", "23:60", "8:00", "0800", "", "aa:bb"} {
		if pattern.MatchString(bad) {
			t.Errorf("the pattern accepts %q", bad)
		}
	}
}

// The drain grace's ceiling is not a taste: at or above the provisioning lead,
// a shortened host's remaining lease still clears the standby cutoff, so it
// keeps counting as inventory, the surplus never falls, and the loop shortens
// the whole fleet one cap at a time. The bound is stated as a literal in
// hostconfig because domain packages do not import each other for constants,
// which is exactly why it needs pinning here.
func TestDrainGraceCeilingSitsBelowTheProvisioningLead(t *testing.T) {
	t.Parallel()
	lead := int64(host.ProvisionLead.Seconds())
	if hostconfig.MaxDrainGraceSec >= lead {
		t.Errorf("MaxDrainGraceSec = %d, must be below host.ProvisionLead (%d s)",
			hostconfig.MaxDrainGraceSec, lead)
	}
	if hostconfig.MaxDrainGraceSec != lead-1 {
		t.Errorf("MaxDrainGraceSec = %d, want %d (one second under the lead)",
			hostconfig.MaxDrainGraceSec, lead-1)
	}
	if hostconfig.DefaultDrainGraceSec < hostconfig.MinDrainGraceSec ||
		hostconfig.DefaultDrainGraceSec > hostconfig.MaxDrainGraceSec {
		t.Errorf("DefaultDrainGraceSec = %d, outside [%d, %d]",
			hostconfig.DefaultDrainGraceSec, hostconfig.MinDrainGraceSec, hostconfig.MaxDrainGraceSec)
	}
}

// The mode enum tag is a second copy of hostconfig.AutoscaleModes, and it is
// the copy the API enforces: a mode the tag admits but the domain does not
// would be stored and then read back as off, silently.
func TestAutoscaleModeEnumTagMatchesDomain(t *testing.T) {
	t.Parallel()
	body, ok := reflect.TypeFor[UpdateFleetAutoscalerInput]().FieldByName("Body")
	if !ok {
		t.Fatal("UpdateFleetAutoscalerInput has no Body field")
	}
	f, ok := body.Type.FieldByName("Mode")
	if !ok {
		t.Fatal("UpdateFleetAutoscalerInput.Body has no Mode field")
	}
	want := make([]string, 0, len(hostconfig.AutoscaleModes))
	for _, m := range hostconfig.AutoscaleModes {
		want = append(want, string(m))
	}
	if tag := f.Tag.Get("enum"); tag != strings.Join(want, ",") {
		t.Errorf("enum tag = %q, want %q", tag, strings.Join(want, ","))
	}
}

// The lifetimes PUT's maximum tags are second copies of the compiled caps.
// The read side clamps against the constants, the write side against the
// tags, and only this test keeps the two refusals identical. The density
// bound is the same story against host.MaxDevicesPerHost.
func TestLifetimeMaximumTagsMatchDomain(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		field string
		want  int64
	}{
		{"DeviceTTLHours", device.MaxTTLHours},
		{"ExtendHours", device.MaxExtendHours},
		{"ExtendCount", int64(device.MaxExtendCount)},
		{"HostTTLHours", host.MaxHostTTLHours},
	} {
		field, ok := reflect.TypeFor[UpdateLifetimesInput]().FieldByName("Body")
		if !ok {
			t.Fatal("UpdateLifetimesInput has no Body field")
		}
		f, ok := field.Type.FieldByName(tc.field)
		if !ok {
			t.Fatalf("UpdateLifetimesInput.Body has no %s field", tc.field)
		}
		if tag := f.Tag.Get("maximum"); tag != strconv.FormatInt(tc.want, 10) {
			t.Errorf("%s maximum tag = %q, domain cap = %d", tc.field, tag, tc.want)
		}
	}

	body, ok := reflect.TypeFor[UpdateDockerHostInput]().FieldByName("Body")
	if !ok {
		t.Fatal("UpdateDockerHostInput has no Body field")
	}
	f, ok := body.Type.FieldByName("TTLHours")
	if !ok {
		t.Fatal("UpdateDockerHostInput.Body has no TTLHours field")
	}
	if tag := f.Tag.Get("maximum"); tag != strconv.FormatInt(host.MaxHostTTLHours, 10) {
		t.Errorf("docker-host TTL maximum tag = %q, host.MaxHostTTLHours = %d", tag, host.MaxHostTTLHours)
	}
}
