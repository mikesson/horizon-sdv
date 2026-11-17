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

package hostconfig

import (
	"errors"
	"testing"
	"time"
	// The zone database, so these tests answer to the IANA rules rather than to
	// whether the machine running them happens to carry /usr/share/zoneinfo. A
	// DST assertion that silently degrades to "zone not found, window closed"
	// passes for the wrong reason.
	_ "time/tzdata"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// at parses a UTC instant, because every assertion below is about which side of
// a transition an absolute moment falls on.
func at(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("bad test instant %q: %v", value, err)
	}
	return parsed
}

// window is a valid schedule with the fields a row cares about overridden.
func window(start string, durationSec int64, location string, days ...int64) FleetSchedule {
	return FleetSchedule{
		Name:        "test",
		Hosts:       2,
		Start:       start,
		DurationSec: durationSec,
		Days:        days,
		Location:    location,
	}
}

// The window's shape: half-open, midnight-crossing, weekday-filtered. Each row
// names what it kills.
func TestScheduleOpenAt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		s    FleetSchedule
		now  string
		want bool
	}{
		{"before the window", window("08:00", 3600, "UTC"), "2026-08-10T07:59:59Z", false},
		{"at the opening instant", window("08:00", 3600, "UTC"), "2026-08-10T08:00:00Z", true},
		{"inside", window("08:00", 3600, "UTC"), "2026-08-10T08:30:00Z", true},
		// Half-open, like the demand window: the closing instant is the first
		// moment the window is shut. A closed interval keeps a host a second
		// past every window every day.
		{"at the closing instant", window("08:00", 3600, "UTC"), "2026-08-10T09:00:00Z", false},

		// A window crossing midnight belongs to the day it OPENED on, which is
		// what makes the weekday filter mean what an operator wrote.
		{
			"a midnight-crossing window is still open after midnight",
			window("23:00", 4*3600, "UTC"), "2026-08-11T01:00:00Z", true,
		},
		// 2026-08-10 is a Monday, so the Monday-only window that opened at 23:00
		// is open at 01:00 on Tuesday. A filter reading the CURRENT weekday
		// instead of the start's closes it at midnight and goes red here.
		{
			"the weekday filter follows the start, not the moment",
			window("23:00", 4*3600, "UTC", 1), "2026-08-11T01:00:00Z", true,
		},
		{
			"a window whose start day is excluded stays shut",
			window("23:00", 4*3600, "UTC", 2), "2026-08-11T01:00:00Z", false,
		},
		{"an empty day set is every day", window("08:00", 3600, "UTC"), "2026-08-15T08:30:00Z", true},

		// Unusable windows contribute nothing rather than erroring: the schedule
		// signal can only raise the target, so refusing to evaluate is the
		// spend-nothing direction.
		{"an unknown zone is not open", window("08:00", 3600, "Mars/Olympus"), "2026-08-10T08:30:00Z", false},
		{"an empty zone is not open", window("08:00", 3600, ""), "2026-08-10T08:30:00Z", false},
		// "Local" loads, and would silently mean the server's clock.
		{"Local is not a zone", window("08:00", 3600, "Local"), "2026-08-10T08:30:00Z", false},
		{"an unparseable start is not open", window("8am", 3600, "UTC"), "2026-08-10T08:30:00Z", false},
		{"a sub-minimum duration is not open", window("08:00", 30, "UTC"), "2026-08-10T08:00:30Z", false},
		{"an over-maximum duration is not open", window("08:00", 604801, "UTC"), "2026-08-10T08:30:00Z", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.s.OpenAt(at(t, tc.now)); got != tc.want {
				t.Errorf("OpenAt(%s) = %v, want %v", tc.now, got, tc.want)
			}
		})
	}
}

// A multi-day window is still open days after it opened, and shuts on time.
// Finding it means scanning back through candidate start days: an evaluator
// that only ever considers today's and yesterday's start compiles, passes every
// row above, and closes every long window at the first midnight it crosses.
//
// Monday-only and six days wide, so there is a real shut day to assert. A
// daily long window is open continuously and could not tell the two apart.
func TestScheduleSpansSeveralDays(t *testing.T) {
	t.Parallel()
	// 2026-08-10 is a Monday: the window runs to 08:00 on Sunday the 16th.
	sixDays := window("08:00", 6*24*3600, "UTC", 1)

	if !sixDays.OpenAt(at(t, "2026-08-15T12:00:00Z")) {
		t.Error("Saturday is five days into a window that opened on Monday; it must be open")
	}
	if sixDays.OpenAt(at(t, "2026-08-16T12:00:00Z")) {
		t.Error("Sunday afternoon is past the window's close; it must be shut")
	}
	if sixDays.OpenAt(at(t, "2026-08-09T12:00:00Z")) {
		t.Error("the Sunday before is past the PREVIOUS window's close; it must be shut")
	}
}

// Daylight saving is why a schedule stores a zone and an offset would not do.
// Both rows pin behavior this package decided, because time.Date documents
// its answer for these wall clocks as not guaranteed.
func TestScheduleDaylightSaving(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		s    FleetSchedule
		now  string
		want bool
	}{
		// 2026-03-08, America/New_York: 02:00 EST jumps to 03:00 EDT, so 02:30
		// never happens. The window opens where the gap pushed it (03:30 EDT,
		// which is 07:30Z) rather than an hour EARLY at 01:30 EST (06:30Z),
		// which is where time.Date's normalization actually lands.
		{
			"a start inside the spring-forward gap opens after the jump",
			window("02:30", 3600, "America/New_York"), "2026-03-08T07:45:00Z", true,
		},
		{
			"and not before it",
			window("02:30", 3600, "America/New_York"), "2026-03-08T06:45:00Z", false,
		},

		// 2026-11-01, America/New_York: 02:00 EDT falls back to 01:00 EST, so
		// 01:30 happens twice, at 05:30Z and 06:30Z. The FIRST one wins.
		{
			"a fall-back repeat opens on the first occurrence",
			window("01:30", 1800, "America/New_York"), "2026-11-01T05:45:00Z", true,
		},
		{
			"and is shut by the second",
			window("01:30", 1800, "America/New_York"), "2026-11-01T06:45:00Z", false,
		},

		// The same rule east of the meridian, and this is the row that kills the
		// obvious implementation: time.Date picks the EARLIER occurrence in a
		// zone behind UTC and the LATER one in a zone ahead of it, so a New York
		// row alone would pass while Stockholm and Berlin opened an hour late.
		// 2026-10-25, Europe/Berlin: 03:00 CEST falls back to 02:00 CET, so
		// 02:30 happens at 00:30Z and again at 01:30Z.
		{
			"a fall-back repeat east of UTC also opens on the first occurrence",
			window("02:30", 1800, "Europe/Berlin"), "2026-10-25T00:45:00Z", true,
		},
		{
			"and is shut by its second occurrence too",
			window("02:30", 1800, "Europe/Berlin"), "2026-10-25T01:45:00Z", false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.s.OpenAt(at(t, tc.now)); got != tc.want {
				t.Errorf("OpenAt(%s) = %v, want %v", tc.now, got, tc.want)
			}
		})
	}
}

// Overlapping windows compose by max, like every other pair of signals here. A
// sum would hold five hosts where the operator asked for three and two.
func TestScheduleHostsComposesByMax(t *testing.T) {
	t.Parallel()
	small := window("08:00", 4*3600, "UTC")
	small.Hosts = 3
	big := window("09:00", 3600, "UTC")
	big.Hosts = 5
	shut := window("22:00", 3600, "UTC")
	shut.Hosts = 9
	schedules := []FleetSchedule{small, big, shut}

	if got := ScheduleHosts(schedules, at(t, "2026-08-10T09:30:00Z")); got != 5 {
		t.Errorf("both open = %d, want 5 (the max, not the sum)", got)
	}
	if got := ScheduleHosts(schedules, at(t, "2026-08-10T08:30:00Z")); got != 3 {
		t.Errorf("one open = %d, want 3", got)
	}
	if got := ScheduleHosts(schedules, at(t, "2026-08-10T12:00:00Z")); got != 0 {
		t.Errorf("none open = %d, want 0", got)
	}
	if got := ScheduleHosts(nil, at(t, "2026-08-10T09:30:00Z")); got != 0 {
		t.Errorf("no schedules = %d, want 0", got)
	}
}

// The schedule signal routes THROUGH the clamp. A window asking for more than
// the fleet can ever hold reports the bound, which leaves the keeper no
// deficit to chase forever.
func TestScheduleTargetIsClamped(t *testing.T) {
	t.Parallel()
	huge := window("08:00", 3600, "UTC")
	huge.Hosts = MaxStandbyHosts + 24

	got := Target(FleetTier{}, TargetSignals{Schedule: ScheduleHosts([]FleetSchedule{huge}, at(t, "2026-08-10T08:30:00Z"))}, 0)
	if got.Target != MaxStandbyHosts {
		t.Errorf("target = %d, want %d", got.Target, MaxStandbyHosts)
	}
	if got.LimitedBy != LimitStandbyCap {
		t.Errorf("limited_by = %q, want %q", got.LimitedBy, LimitStandbyCap)
	}
	if got.Signal != SignalSchedule {
		t.Errorf("signal = %q, want %q", got.Signal, SignalSchedule)
	}
}

// The composition the card and the keeper share has to actually read the
// windows. Leaving TargetSignals.Schedule at zero compiles, publishes a target
// that ignores every schedule, and passes every other test in this package.
func TestComposeStatusReadsTheSchedule(t *testing.T) {
	t.Parallel()
	p := DefaultFleetPolicy()
	open := window("08:00", 3600, "UTC")
	open.Hosts = 4
	p.Tiers = map[string]FleetTier{DefaultTier: {Standby: 1, Schedules: []FleetSchedule{open}}}

	inside := ComposeStatus(p, DefaultTier, at(t, "2026-08-10T08:30:00Z"), host.StandbyInventory{}, DemandWindow{})
	if inside.Target != 4 || inside.Signal != SignalSchedule {
		t.Errorf("inside the window: target/signal = %d/%q, want 4/schedule", inside.Target, inside.Signal)
	}

	outside := ComposeStatus(p, DefaultTier, at(t, "2026-08-10T12:00:00Z"), host.StandbyInventory{}, DemandWindow{})
	if outside.Target != 1 || outside.Signal != SignalStandby {
		t.Errorf("outside the window: target/signal = %d/%q, want 1/standby", outside.Target, outside.Signal)
	}
}

// A pool's windows are its own, and a policy with no pools at all (which a
// malformed read produces) must answer nil rather than panic.
func TestSchedulesAreReadPerPool(t *testing.T) {
	t.Parallel()
	p := DefaultFleetPolicy()
	p.Tiers = map[string]FleetTier{DefaultTier: {Schedules: []FleetSchedule{window("08:00", 3600, "UTC")}}}
	if got := len(p.Pool(DefaultTier).Schedules); got != 1 {
		t.Errorf("the default pool holds %d windows, want 1", got)
	}
	if got := p.Pool("gpu").Schedules; got != nil {
		t.Errorf("an unconfigured pool holds %v, want no windows", got)
	}
	if got := (FleetPolicy{}).Pool(DefaultTier).Schedules; got != nil {
		t.Errorf("a policy with no pools holds %v, want no windows", got)
	}
}

// The write path refuses what the evaluator would silently skip. Anything
// storable-but-unevaluable is a window an operator believes is holding capacity
// and that never opens, which nothing else in the system reports.
func TestValidateSchedules(t *testing.T) {
	t.Parallel()
	valid := window("08:00", 3600, "Europe/Stockholm", 1, 2, 3, 4, 5)
	for _, tc := range []struct {
		name    string
		s       FleetSchedule
		wantErr bool
	}{
		{"a valid window", valid, false},
		{"no days is every day", window("08:00", 3600, "UTC"), false},

		{"an unnamed window", FleetSchedule{
			Hosts: 1, Start: "08:00", DurationSec: 3600, Location: "UTC",
		}, true},
		{"hosts past the cap", FleetSchedule{
			Name: "x", Hosts: MaxStandbyHosts + 1, Start: "08:00", DurationSec: 3600, Location: "UTC",
		}, true},
		{"a duration under the floor", window("08:00", MinScheduleDurationSec-1, "UTC"), true},
		{"a duration over the ceiling", window("08:00", MaxScheduleDurationSec+1, "UTC"), true},
		{"a start that is not a wall clock", window("half eight", 3600, "UTC"), true},
		{"an out-of-range weekday", window("08:00", 3600, "UTC", 7), true},
		{"a repeated weekday", window("08:00", 3600, "UTC", 1, 1), true},
		{"an unknown zone", window("08:00", 3600, "Mars/Olympus"), true},
		{"an empty zone", window("08:00", 3600, ""), true},
		{"the server's own clock", window("08:00", 3600, "Local"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateSchedules([]FleetSchedule{tc.s})
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateSchedules = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// Zero hosts is refused rather than treated as a pause switch: it stores a
// window that can never raise the target, which reads on the card exactly like
// one that is working. Deleting the window is the way to stop it.
func TestValidateSchedulesRefusesAZeroHostWindow(t *testing.T) {
	t.Parallel()
	paused := window("08:00", 3600, "UTC")
	paused.Hosts = 0
	if err := ValidateSchedules([]FleetSchedule{paused}); err == nil {
		t.Error("a window asking for no hosts must be refused, not stored as a no-op")
	}
}

// The list bound, which the Huma tag also carries. A list past it is refused
// whole rather than truncated: truncation stores a policy nobody wrote.
func TestValidateSchedulesBoundsTheList(t *testing.T) {
	t.Parallel()
	overfull := make([]FleetSchedule, 0, MaxSchedules+1)
	for range MaxSchedules + 1 {
		overfull = append(overfull, window("08:00", 3600, "UTC"))
	}
	if err := ValidateSchedules(overfull[:MaxSchedules]); err != nil {
		t.Errorf("exactly MaxSchedules must be allowed: %v", err)
	}
	if err := ValidateSchedules(overfull); err == nil {
		t.Errorf("more than %d windows must be refused", MaxSchedules)
	}
	if err := ValidateSchedules(nil); err != nil {
		t.Errorf("an empty list is a valid policy: %v", err)
	}
}

// A per-entry failure has to say WHICH entry: an operator with eight windows
// and a message naming none of them is being told to check all eight.
func TestValidateSchedulesNamesTheFailingEntry(t *testing.T) {
	t.Parallel()
	good := window("08:00", 3600, "UTC")
	err := ValidateSchedules([]FleetSchedule{good, good, window("08:00", 3600, "Mars/Olympus")})
	if err == nil {
		t.Fatal("a bad third window must be refused")
	}
	// The typed error is what web.MapError turns into a 400 rather than a 500;
	// a plain fmt.Errorf here would surface as an internal error.
	verr, ok := errors.AsType[*ValidationError](err)
	if !ok {
		t.Fatalf("error is %T, want *ValidationError so the handler maps it", err)
	}
	if verr.Field != "schedules[2].location" {
		t.Errorf("field = %q, want schedules[2].location", verr.Field)
	}
}
