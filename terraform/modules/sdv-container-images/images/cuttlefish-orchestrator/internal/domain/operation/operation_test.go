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

package operation

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

// declaredVerbs is every verb constant this package exports. Written out rather
// than derived so the check below can compare it against the registry: a verb
// added without a policy fails here, which is what forces the decision to be
// made rather than defaulted.
var declaredVerbs = []Verb{
	VerbDeviceRestart,
	VerbDevicePowerwash,
	VerbDeviceSnapshot,
	VerbDeviceSnapshotDelete,
	VerbDeviceSnapshotRestore,
}

// Four separate decisions read this registry (admission exclusivity, the
// reconciling signal, retry budget, response shape), so a forgotten row is a verb that is silently non-exclusive, non-reconciling,
// retried ten times and returning null. Every one of those defaults is wrong,
// and none of them would fail anywhere else.
func TestRegistryCoversEveryDeclaredVerb(t *testing.T) {
	t.Parallel()

	for _, v := range declaredVerbs {
		if _, err := PolicyFor(v); err != nil {
			t.Errorf("verb %q is declared but has no registry entry: %v", v, err)
		}
	}
	for _, v := range Verbs() {
		if !slices.Contains(declaredVerbs, v) {
			t.Errorf("registry entry %q is not a declared verb constant", v)
		}
	}
}

// A policy whose fields were left at their zero values is indistinguishable from
// a considered one at the call site, so the invariants are asserted here.
func TestEveryPolicyIsFullySpecified(t *testing.T) {
	t.Parallel()

	seenTaskTypes := map[string]Verb{}
	for _, v := range Verbs() {
		p, err := PolicyFor(v)
		if err != nil {
			t.Fatalf("PolicyFor(%q): %v", v, err)
		}

		if p.TaskType == "" {
			t.Errorf("%q: no task type, so nothing can dispatch it", v)
		}
		if p.TaskType == string(v) {
			t.Errorf("%q: task type equals the verb; they are kept distinct "+
				"because the verb is the response discriminator", v)
		}
		if prev, dup := seenTaskTypes[p.TaskType]; dup {
			t.Errorf("%q and %q share task type %q; the handler lookup would be ambiguous",
				prev, v, p.TaskType)
		}
		seenTaskTypes[p.TaskType] = v

		if p.MaxAttempts < 1 {
			t.Errorf("%q: MaxAttempts %d would dead-letter before the first attempt",
				v, p.MaxAttempts)
		}
		// AIP-151 makes a response type unwidenable once shipped, so an empty
		// one is the single choice that cannot be corrected later.
		if len(p.ResponseFields) == 0 {
			t.Errorf("%q: no response fields. A response shape cannot be widened "+
				"after release, so shipping an empty one is unrecoverable", v)
		}
	}
}

// Powerwash's attempt budget guards the DEVICE, not the record: the sweep
// dead-letters instead of redelivering once attempts are exhausted, so 1 is what
// prevents a second wipe after a worker dies mid-actuation. The terminal-write
// CAS does not do this; it only prevents a wrong terminal record. Asserted
// because "make retries work" is the obvious-looking change that breaks it.
func TestPowerwashNeverAutoRetries(t *testing.T) {
	t.Parallel()

	p, err := PolicyFor(VerbDevicePowerwash)
	if err != nil {
		t.Fatalf("PolicyFor: %v", err)
	}
	if p.MaxAttempts != 1 {
		t.Errorf("powerwash MaxAttempts = %d, want 1: a retry re-wipes a device "+
			"whose first attempt may already have succeeded", p.MaxAttempts)
	}
}

// Snapshot take's failure path can leave the device suspended (the host has no
// resume-only endpoint) plus a half-written directory under the snapshot id,
// and restore is a cvd start whose repeat against an already-started device
// records a false failure. Neither actuation is safe to redeliver blind, so
// both budgets guard the DEVICE the way powerwash's does.
func TestSnapshotTakeAndRestoreNeverAutoRetry(t *testing.T) {
	t.Parallel()

	for _, v := range []Verb{VerbDeviceSnapshot, VerbDeviceSnapshotRestore} {
		p, err := PolicyFor(v)
		if err != nil {
			t.Fatalf("PolicyFor(%q): %v", v, err)
		}
		if p.MaxAttempts != 1 {
			t.Errorf("%q MaxAttempts = %d, want 1: redelivery repeats an actuation "+
				"that is not safe to repeat", v, p.MaxAttempts)
		}
	}
}

// The take verb must ride the mutating index: the device controller's
// live-operation guard reads it, and the suspend window during a take is
// exactly the frozen state that guard exists to keep health from settling as
// a failure.
func TestSnapshotTakeIsGuardedByTheHealthPlane(t *testing.T) {
	t.Parallel()

	p, err := PolicyFor(VerbDeviceSnapshot)
	if err != nil {
		t.Fatalf("PolicyFor: %v", err)
	}
	if !p.Mutating {
		t.Error("snapshot take is not Mutating: the device controller's live-operation " +
			"guard reads the mutating index, so a take would be unguarded mid-suspend")
	}
}

func TestPolicyForUnknownVerbIsAnError(t *testing.T) {
	t.Parallel()

	_, err := PolicyFor(Verb("device.nonexistent"))
	if !errors.Is(err, ErrUnknownVerb) {
		t.Errorf("PolicyFor(unknown) = %v, want ErrUnknownVerb", err)
	}
}

func TestResourceNameIsRelative(t *testing.T) {
	t.Parallel()

	if got, want := ResourceName("abc"), "operations/abc"; got != want {
		t.Errorf("ResourceName = %q, want %q", got, want)
	}
}

// A handle that does not say which instance it acted on cannot be matched back
// to anything on a device with more than one, which is what makes the recorded
// instance worth serving rather than only storing. Asserting on the SERIALIZED
// form: a Go field that never reaches the JSON is the failure being guarded.
func TestOperationCarriesTheInstanceItActedOn(t *testing.T) {
	t.Parallel()

	body, err := json.Marshal(FromRecord(&Record{ID: "op-1", InstanceName: "watch"}, Progress{}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"instance_name":"watch"`) {
		t.Errorf("operation JSON does not name the instance: %s", body)
	}

	body, err = json.Marshal(FromRecord(&Record{ID: "op-2"}, Progress{}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), "instance_name") {
		t.Errorf("device-wide verb invented an instance: %s", body)
	}
}

// The orphan message is what a user reads after an instance died mid-verb, so it
// must admit the ambiguity rather than assert a failure that may not have
// happened.
func TestOrphanedProblemAdmitsTheAmbiguity(t *testing.T) {
	t.Parallel()

	p := OrphanedProblem()
	if p.Status != 500 {
		t.Errorf("status = %d, want 500", p.Status)
	}
	if p.Title == "" {
		t.Error("no title")
	}
	if !strings.Contains(p.Detail, "may or may not") {
		t.Errorf("detail does not admit that the action may have taken effect: %q", p.Detail)
	}
}

// TaskTypes feeds the requeue guard, which must cover every operation-backed
// type. Deriving it from the registry is what keeps the two from drifting.
func TestTaskTypesMatchesTheRegistry(t *testing.T) {
	t.Parallel()

	types := TaskTypes()
	if len(types) != len(Verbs()) {
		t.Fatalf("TaskTypes returned %d entries for %d verbs", len(types), len(Verbs()))
	}
	for _, v := range Verbs() {
		p, err := PolicyFor(v)
		if err != nil {
			t.Fatalf("PolicyFor(%q): %v", v, err)
		}
		if !slices.Contains(types, p.TaskType) {
			t.Errorf("%q's task type %q is missing from TaskTypes", v, p.TaskType)
		}
	}
}
