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
	"fmt"
	"slices"
	"strings"
	"time"
)

// MaxSchedules bounds the list an operator can store. Eight covers a distinct
// window for every day of the week with one spare; a list long enough to need
// paging is a forecast, and that is a different mechanism.
const MaxSchedules = 8

// A window's duration. The floor is a minute because anything shorter is not a
// window an operator can act on. The ceiling is a week, past which the window
// never closes and the pool's standby buffer says the same thing more plainly.
const (
	MinScheduleDurationSec = 60
	MaxScheduleDurationSec = 604800
)

// MaxScheduleNameLen bounds the label. Display and audit text only: nothing
// keys on it and duplicates are allowed, so the bound is about the settings row
// staying readable, not about identity.
const MaxScheduleNameLen = 64

// StartLayout is the wall-clock format a schedule's start is stored in. Shared
// by the write validation and the evaluation so a value that validates always
// evaluates: two parsers would eventually disagree about "8:00", and the half
// that disagreed at evaluation time would silently drop the window.
const StartLayout = "15:04"

// FleetSchedule raises the target across a recurring wall-clock window. It is
// the open-loop half of the signal set: an operator declaring demand they know
// is coming, where the demand signal can only report demand that already
// arrived.
//
// Wall clock in a named zone, not an absolute instant, because the demand it
// anticipates is human: a CI shift or a working day starts at 08:00 local
// whatever the offset is doing that month.
type FleetSchedule struct {
	Name        string  `json:"name" maxLength:"64" doc:"Label for this window; display and audit text only"`
	Hosts       int64   `json:"hosts" minimum:"1" maximum:"16" doc:"Standby hosts this window asks for while it is open"`
	Start       string  `json:"start" pattern:"^([01][0-9]|2[0-3]):[0-5][0-9]$" doc:"Wall-clock start in the window's own zone, as HH:MM"`
	DurationSec int64   `json:"duration_sec" minimum:"60" maximum:"604800" doc:"How long the window stays open, in seconds"`
	Days        []int64 `json:"days,omitempty" required:"false" doc:"Weekdays the window opens on, 0 = Sunday; empty means every day"`
	Location    string  `json:"location" doc:"IANA zone name the start is read in (e.g. Europe/Stockholm)"`
}

// ScheduleHosts is the schedule signal: the most hosts any window open at now
// asks for, and zero when none is. Overlapping windows compose by max, like
// every other pair of signals here. Two windows both asking for capacity are
// two answers to one question, not two demands to add up.
func ScheduleHosts(schedules []FleetSchedule, now time.Time) int64 {
	hosts := int64(0)
	for _, s := range schedules {
		if s.OpenAt(now) {
			hosts = max(hosts, s.Hosts)
		}
	}
	return hosts
}

// OpenAt reports whether the window is open at now.
//
// Anything unusable answers false rather than erroring, and that direction is
// chosen: the schedule signal can only ever raise the target, so a window
// that cannot be evaluated contributes nothing and the target falls back to
// whatever standing intent and observed demand say. The write path rejects all
// of these, and this check is what stands between money and a value that
// reached storage some other way.
func (s FleetSchedule) OpenAt(now time.Time) bool {
	if s.Hosts <= 0 || s.DurationSec < MinScheduleDurationSec || s.DurationSec > MaxScheduleDurationSec {
		return false
	}
	// The same check the write path makes, called rather than restated: a zone
	// rule the evaluator and the validator each carried their own copy of would
	// eventually let one accept what the other rejects, and the direction that
	// drifts silently is the evaluator spending on a window nobody could store.
	if err := validateLocation(s.Location, ""); err != nil {
		return false
	}
	loc, err := time.LoadLocation(s.Location)
	if err != nil {
		return false
	}
	clock, err := time.Parse(StartLayout, strings.TrimSpace(s.Start))
	if err != nil {
		return false
	}

	duration := time.Duration(s.DurationSec) * time.Second
	year, month, day := now.In(loc).Date()
	// A window can be a week wide, so one that started days ago may still be
	// open. Scan back far enough to cover the widest, plus a day for the window
	// whose start today is still ahead of now.
	for back := range int(duration/(24*time.Hour)) + 2 {
		start := startInstant(year, month, day-back, clock.Hour(), clock.Minute(), loc)
		// Days match the START instant's weekday, which is what makes a window
		// crossing midnight belong to the day it opened on rather than
		// half-belonging to two.
		if len(s.Days) > 0 && !slices.Contains(s.Days, int64(start.Weekday())) {
			continue
		}
		if !now.Before(start) && now.Before(start.Add(duration)) {
			return true
		}
	}
	return false
}

// FieldSchedules names the schedules list in a ValidationError. Per-entry
// failures extend it with the index and the field, so a 400 says which of eight
// windows is wrong rather than that one of them is.
const FieldSchedules = "schedules"

// ValidateSchedules rejects a list that cannot be evaluated as written.
//
// The bounds here are the write-path copy of what the Huma tags enforce at the
// transport and what OpenAt re-checks before spending anything. Three copies is
// the house rule for a bound, and the reason is that only one of them (the tag)
// is what the API actually rejects on, while only one of them (OpenAt) is
// what stands between storage and money.
//
// The zone is the one rule the tags cannot express at all: whether a name loads
// is a property of the tzdata the binary carries, not of the string.
func ValidateSchedules(schedules []FleetSchedule) error {
	if len(schedules) > MaxSchedules {
		return &ValidationError{
			Field:   FieldSchedules,
			Message: fmt.Sprintf("must not exceed %d schedules", MaxSchedules),
		}
	}
	for i, s := range schedules {
		if err := s.validate(i); err != nil {
			return err
		}
	}
	return nil
}

// validate checks one window, naming its index so the message locates it.
func (s FleetSchedule) validate(index int) error {
	field := func(name string) string {
		return fmt.Sprintf("%s[%d].%s", FieldSchedules, index, name)
	}
	switch {
	case strings.TrimSpace(s.Name) == "":
		return &ValidationError{Field: field("name"), Message: "name is required"}
	case len(s.Name) > MaxScheduleNameLen:
		return &ValidationError{
			Field:   field("name"),
			Message: fmt.Sprintf("must not exceed %d characters", MaxScheduleNameLen),
		}
	// One host, not zero. A window asking for nothing never opens, so storing
	// it leaves a row that reads on the card exactly like a working window and
	// holds no capacity. Removing the window is how a window is switched off.
	//
	// Bounded by MaxStandbyHosts, which is why the two limits are separate. A
	// stored value is bounded by what an operator may declare, and the runtime
	// ceiling (StandbyCap) is at least that, so a window that validates can
	// never be clipped by the cap it is evaluated against. Reading the fleet here
	// would also put a repository behind a write validation that has none.
	case s.Hosts < 1 || s.Hosts > MaxStandbyHosts:
		return &ValidationError{
			Field:   field("hosts"),
			Message: fmt.Sprintf("must be between 1 and %d", MaxStandbyHosts),
		}
	case s.DurationSec < MinScheduleDurationSec || s.DurationSec > MaxScheduleDurationSec:
		return &ValidationError{
			Field:   field("duration_sec"),
			Message: fmt.Sprintf("must be between %d and %d seconds", MinScheduleDurationSec, MaxScheduleDurationSec),
		}
	}
	if _, err := time.Parse(StartLayout, strings.TrimSpace(s.Start)); err != nil {
		return &ValidationError{Field: field("start"), Message: "must be a wall-clock time as HH:MM"}
	}
	seen := make(map[int64]bool, len(s.Days))
	for _, d := range s.Days {
		if d < int64(time.Sunday) || d > int64(time.Saturday) {
			return &ValidationError{Field: field("days"), Message: "each day must be between 0 (Sunday) and 6 (Saturday)"}
		}
		if seen[d] {
			return &ValidationError{Field: field("days"), Message: "must not repeat a day"}
		}
		seen[d] = true
	}
	return validateLocation(s.Location, field("location"))
}

// validateLocation rejects a zone the evaluator could not read the start in.
//
// "Local" is refused by name even though it loads. It resolves to whatever the
// process's TZ says, which is a property of the machine rather than a
// declaration: the same stored schedule would open at different instants on a
// workstation and on Cloud Run, and nothing about the value would say so. An
// empty name is refused for the same reason: LoadLocation reads it as UTC, so
// it would silently become a zone the operator never chose.
func validateLocation(name, field string) error {
	if strings.TrimSpace(name) == "" {
		return &ValidationError{Field: field, Message: "an IANA zone name is required (e.g. Europe/Stockholm or UTC)"}
	}
	if name == "Local" {
		return &ValidationError{Field: field, Message: `"Local" depends on the server's clock; name the zone explicitly`}
	}
	if _, err := time.LoadLocation(name); err != nil {
		return &ValidationError{Field: field, Message: "unknown IANA zone name"}
	}
	return nil
}

// startInstant is the instant a window beginning at the given wall clock
// actually starts, expressed in loc.
//
// Both daylight-saving cases are decided here rather than inherited, because
// time.Date decides neither: its own documentation says that for a wall clock
// that does not exist or occurs twice, "the choice of time zone, and therefore
// the time, is not guaranteed". Building a recurring window on an explicitly
// unguaranteed answer is how a schedule opens an hour off twice a year.
//
// It guesses the offset by reading the wall clock as if it were UTC, and both
// of its answers are wrong for this:
//
//   - On the fall-back repeat it lands on the earlier occurrence in a zone
//     behind UTC and the LATER one in a zone ahead of it. Asking for both
//     candidate offsets and keeping the earlier one actually in force makes the
//     rule the same on either side of the meridian.
//   - On the spring-forward gap it lands BEFORE the gap: 02:30 in a zone that
//     jumps 02:00 to 03:00 comes back as 01:30. A window opening earlier than
//     its stated time holds capacity nobody asked for yet, so the rule here is
//     forward: the instant 02:30 would have been on the pre-jump offset, which
//     reads as 03:30 once the clock has moved.
func startInstant(year int, month time.Month, day, hour, minute int, loc *time.Location) time.Time {
	wall := time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
	// Any transition on this date is bracketed by the offsets a day either
	// side; no zone transitions twice that closely.
	_, before := wall.AddDate(0, 0, -1).In(loc).Zone()
	_, after := wall.AddDate(0, 0, 1).In(loc).Zone()

	first := time.Time{}
	for _, offset := range []int{before, after} {
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		if _, inForce := candidate.In(loc).Zone(); inForce != offset {
			continue
		}
		if first.IsZero() || candidate.Before(first) {
			first = candidate
		}
	}
	if first.IsZero() {
		// Neither offset holds, so no instant reads as this wall clock at all:
		// the gap. Take the pre-jump offset, which puts the window one gap-width
		// later on the clock rather than a whole gap early.
		return wall.Add(-time.Duration(before) * time.Second).In(loc)
	}
	return first.In(loc)
}
