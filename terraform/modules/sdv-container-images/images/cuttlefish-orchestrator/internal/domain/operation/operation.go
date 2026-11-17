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

// Package operation defines the durable handle a long-running device verb
// returns: the Operation entity, the per-verb registry that decides how each
// verb is admitted and retried, and the problem document a failed verb records.
//
// It is domain-homed because three packages consume it across import firewalls.
// features/device writes terminal state from its task handlers,
// features/operation serves the reads, and jobs/taskreclaimer sweeps orphans,
// and a feature-owned type could serve none of them. Standard library only:
// nothing here may reach for the HTTP framework or a cloud SDK.
package operation

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Sentinel errors. Callers match these rather than inspecting messages.
var (
	// ErrNotFound is returned for an unknown operation id.
	ErrNotFound = errors.New("operation not found")

	// ErrInFlight rejects a verb whose resource already has a live exclusive
	// operation. It carries the operation being waited on so the client can
	// poll it instead of guessing.
	ErrInFlight = errors.New("an exclusive operation is already running on this resource")

	// ErrUnknownVerb means a verb reached admission without a registry entry.
	// Loud on purpose: the alternative is a verb silently admitted as
	// non-exclusive, non-reconciling and retried ten times.
	ErrUnknownVerb = errors.New("no registry entry for operation verb")
)

// Verb names one long-running device action. It is the response
// DISCRIMINATOR, since a client selects the shape of Response by reading it, so
// it is API surface rather than description, and it is NOT the task type.
type Verb string

const (
	// VerbDeviceRestart relaunches a running device's CVD in place.
	VerbDeviceRestart Verb = "device.restart"
	// VerbDevicePowerwash resets a running device to first-boot state.
	VerbDevicePowerwash Verb = "device.powerwash"
	// VerbDeviceSnapshot captures a running device's guest state onto its
	// host's disk (suspend → snapshot → resume).
	VerbDeviceSnapshot Verb = "device.snapshot"
	// VerbDeviceSnapshotDelete removes a stored snapshot from the device's host.
	VerbDeviceSnapshotDelete Verb = "device.snapshot_delete"
	// VerbDeviceSnapshotRestore starts a stopped device from a stored snapshot.
	VerbDeviceSnapshotRestore Verb = "device.snapshot_restore"
)

// ResourceTypeDevice is the only resource kind with operations today. Stored
// alongside the id so the admission key cannot collide across kinds if hosts
// ever grow verbs of their own.
const ResourceTypeDevice = "device"

// LiveResource names what one live mutating operation is passing through: the
// resource, and the instance of it the verb actuates. An empty InstanceName is
// the stored NULL, a verb addressed at the whole resource rather than at one
// member of it.
type LiveResource struct {
	ResourceID   string
	InstanceName string
}

// LiveSet is which resources have a live mutating operation, and on which
// instance of each.
type LiveSet map[LiveResource]bool

// Covers reports whether a verb is passing this instance of this resource
// through right now.
//
// A verb with no instance name covers EVERY instance, which is what start,
// stop, delete and the group verbs are each really doing. A per-instance
// verb covers only the instance it names: restarting instance 2 must leave a
// dead instance 5 fully visible to whatever is watching the resource, and
// answering by resource alone is exactly the answer that hides it.
func (s LiveSet) Covers(resourceID, instanceName string) bool {
	return s[LiveResource{ResourceID: resourceID}] ||
		s[LiveResource{ResourceID: resourceID, InstanceName: instanceName}]
}

// Operation is a durable handle for one long-running verb.
//
// It serializes directly to the wire (there is no DTO layer) so the JSON tags
// here are the API contract. Metadata is derived at read time from the live task
// row rather than stored: while an operation is live its task exists by
// definition, and that row already carries attempts, last error and next retry.
type Operation struct {
	// Name is the RELATIVE RESOURCE NAME, "operations/{uuid}", not the bare id.
	// A generated client fetches operation.name mechanically; a bare uuid makes
	// every client learn to prepend the collection.
	Name string `json:"name"`

	// Done reports whether the operation has settled. Exactly one of Response
	// or Error is present once it is true.
	Done bool `json:"done"`

	// Metadata carries progress. Never stored; see Metadata.
	Metadata Metadata `json:"metadata"`

	// Response is the verb's result, present only on success and only for verbs
	// that produce one. Typed per verb by CONVENTION: one Operation type means
	// this is one field, so the OpenAPI schema cannot enforce the shape and a
	// per-verb test must.
	Response json.RawMessage `json:"response,omitempty"`

	// Error is the RFC 9457 problem document, present only on failure. It plays
	// the role AIP-151 gives google.rpc.Status; CCO has one error vocabulary
	// already and does not want a second.
	Error *Problem `json:"error,omitempty"`
}

// Metadata is an operation's progress. Derived on every read from the operation
// row's static half and the live task row's counters, so a retry or a postpone
// costs no write.
type Metadata struct {
	Verb         Verb      `json:"verb"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	CreateTime   time.Time `json:"create_time"`

	// InstanceName is the instance the verb actuated, absent for a
	// resource-wide one. Without it a handle cannot be matched back to what it
	// acted on: a device is a group of Android VMs, and "restart" against a
	// three-instance device names nothing at all.
	InstanceName string `json:"instance_name,omitempty"`

	// Attempt is the delivery count so far, absent once the task row is gone.
	Attempt int `json:"attempt,omitempty"`

	// LastError is the most recent TRANSIENT failure, or a dead-lettered task's
	// final cause. Free-form operator prose rather than a structured status: it
	// is a log line's worth of detail about a state that is usually gone by the
	// next poll. The terminal Error above is the one a client acts on.
	LastError string `json:"last_error,omitempty"`

	// NextAttemptAt is when a postponed or backing-off task becomes claimable.
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`
}

// Record is the stored half of an operation: what the row holds, before
// metadata is derived and the wire shape is composed.
type Record struct {
	ID           string
	Verb         Verb
	ResourceType string
	ResourceID   string
	// InstanceName is the instance of the resource this verb actuates, or ""
	// for a resource-wide verb. It is not part of the admission
	// key: one in-flight exclusive operation per resource stays the rule, so
	// restarting one instance while powerwashing another is refused.
	InstanceName string
	TaskID       string
	Done         bool
	Request      json.RawMessage
	Response     json.RawMessage
	Error        *Problem
	Exclusive    bool
	Mutating     bool
	CreatedBy    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ResourceName returns the relative resource name for an operation id.
func ResourceName(id string) string { return "operations/" + id }

// Progress is the live task state an operation's metadata is derived from. The
// zero value is correct for an operation that has just been admitted, and for one
// whose task is gone.
type Progress struct {
	Attempt       int
	LastError     string
	NextAttemptAt *time.Time
}

// FromRecord composes the wire shape from a stored record and the live task's
// progress.
//
// Metadata is assembled here rather than stored: while an operation is live its
// task row exists by definition and already carries attempts, last error and next
// retry, so storing progress would mean a worker write per retry and postpone for
// data that is stale by the next poll.
//
// Homed in the domain because both the verb slice (which returns a handle on
// admission) and the operations slice (which serves reads) must produce byte-identical
// shapes, and features may not import each other.
func FromRecord(rec *Record, p Progress) Operation {
	op := Operation{
		Name: ResourceName(rec.ID),
		Done: rec.Done,
		Metadata: Metadata{
			Verb:          rec.Verb,
			ResourceType:  rec.ResourceType,
			ResourceID:    rec.ResourceID,
			CreateTime:    rec.CreatedAt,
			InstanceName:  rec.InstanceName,
			Attempt:       p.Attempt,
			LastError:     p.LastError,
			NextAttemptAt: p.NextAttemptAt,
		},
		Response: rec.Response,
		Error:    rec.Error,
	}
	// Progress describes work still to come; once settled the outcome is the
	// answer and a stale retry hint would only mislead.
	if rec.Done {
		op.Metadata.NextAttemptAt = nil
	}
	return op
}

// Problem is an RFC 9457 problem document, the shape CCO already returns
// synchronously, stored so an async failure reads identically.
//
// It lives in the domain because all three terminal writers must produce the
// same shape and one of them is the orphan sweep, which runs in the jobs
// packages and may not reach into the transport layer. Stdlib only: the domain may not
// import the HTTP framework, so this cannot be Huma's model.
type Problem struct {
	Type   string `json:"type,omitempty"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// OrphanedProblem is the terminal error for an operation whose task vanished
// without writing an outcome, such as an instance killed mid-actuation or a
// force-dead-lettered delivery. It is a fixed value because the two writers that
// use it (admission's heal and the orphan sweep) have no error to map; only the
// handler has one.
//
// This is the message a user sees after an instance dies mid-verb, so it says
// what is known and what to do rather than naming an internal mechanism.
func OrphanedProblem() *Problem {
	return &Problem{
		Title:  "Operation abandoned",
		Status: 500,
		Detail: "the worker executing this operation stopped without recording an outcome; " +
			"the action may or may not have taken effect on the device; check the device state and retry if needed",
	}
}

// Policy is one verb's entry in the registry: everything admission, retry and
// the reconciling signal need to know about it, declared once.
type Policy struct {
	// TaskType is the queue dispatch key. Distinct from the verb: the type is
	// device.restart_operation while the verb is device.restart.
	TaskType string

	// Exclusive refuses a second exclusive operation on the same resource while
	// this one is live.
	Exclusive bool

	// Mutating means the verb converges the resource toward its desired state,
	// which is what the API's reconciling field reports. Separate from
	// Exclusive on purpose; see the schema comment on the generated columns.
	Mutating bool

	// MaxAttempts is the verb's retry budget, set on the task at enqueue.
	MaxAttempts int

	// ResponseFields are the keys the verb's Response must carry. The schema
	// cannot enforce this (one Operation type makes Response untyped in the
	// spec), so a per-verb test asserts the marshaled response against it.
	// Non-empty from a verb's first release: a response type cannot be widened
	// later without breaking every client, so `null` is the one choice that is
	// unrecoverable.
	ResponseFields []string
}

// registry is the single declaration four decisions read: admission
// exclusivity, the reconciling signal, retry budget, and response shape.
//
// A missing entry is refused loudly at admission (ErrUnknownVerb) rather than
// defaulted, because every default here is wrong: non-exclusive, non-reconciling,
// ten retries and a null response, none of them chosen.
var registry = map[Verb]Policy{
	VerbDeviceRestart: {
		TaskType:  "device.restart_operation",
		Exclusive: true,
		Mutating:  true,
		// Rebooting twice is harmless.
		MaxAttempts:    3,
		ResponseFields: []string{"restarted_at"},
	},
	VerbDevicePowerwash: {
		TaskType:  "device.powerwash_operation",
		Exclusive: true,
		Mutating:  true,
		// Destructive AND unobservable: the row is running before and after, so
		// nothing can tell a handler whether a failed call actuated. Never
		// auto-retry actuation whose outcome is unknown. This number is
		// what makes the sweep dead-letter instead of redelivering, so it
		// guards the DEVICE as well as the record.
		MaxAttempts:    1,
		ResponseFields: []string{"powerwashed_at"},
	},
	VerbDeviceSnapshot: {
		TaskType:  "device.snapshot_operation",
		Exclusive: true,
		// Mutating here is what routes the verb into the device controller's
		// live-operation guard: the take suspends the guest, and without the
		// guard that frozen window is exactly what health would settle as a
		// failure.
		Mutating: true,
		// The host's take sequence is suspend → snapshot → resume with no
		// resume-only endpoint, so a failed attempt can leave the device
		// suspended and a half-written directory under the snapshot id. A
		// blind redelivery collides with both; the handler compensates
		// instead and the user re-issues.
		MaxAttempts:    1,
		ResponseFields: []string{"snapshot_id", "created_at"},
	},
	VerbDeviceSnapshotDelete: {
		TaskType: "device.snapshot_delete_operation",
		// Exclusive so a delete can never race a restore reading the same
		// snapshot directory: the host-side delete is an unguarded
		// recursive remove.
		Exclusive: true,
		// Removing an artifact neither converges the device nor disturbs
		// it, so the device controller has nothing to guard.
		Mutating: false,
		// The host-side remove is idempotent (removing an absent directory
		// succeeds), so redelivery is safe.
		MaxAttempts:    3,
		ResponseFields: []string{"deleted_at"},
	},
	VerbDeviceSnapshotRestore: {
		TaskType:  "device.snapshot_restore_operation",
		Exclusive: true,
		Mutating:  true,
		// Restore is a cvd start: repeating it against a device the first
		// attempt already started fails as "already running", which would
		// record a false failure over a successful restore.
		MaxAttempts:    1,
		ResponseFields: []string{"snapshot_id", "restored_at"},
	},
}

// PolicyFor returns a verb's registry entry.
func PolicyFor(v Verb) (Policy, error) {
	p, ok := registry[v]
	if !ok {
		return Policy{}, fmt.Errorf("%w: %q", ErrUnknownVerb, v)
	}
	return p, nil
}

// Verbs returns every registered verb, so the agreement tests that must cover
// each one derive their cases from the registry instead of a hand-kept list
// that silently stops covering a verb added later. Only tests call it (the
// requeue predicate reads TaskTypes below) which is why the whole-program
// dead-code pass reports it: its roots are the shipped binaries.
func Verbs() []Verb {
	out := make([]Verb, 0, len(registry))
	for v := range registry {
		out = append(out, v)
	}
	return out
}

// TaskTypes returns every operation-backed task type. The requeue guard reads
// this rather than a hand-maintained list that could drift from the registry.
func TaskTypes() []string {
	out := make([]string, 0, len(registry))
	for _, p := range registry {
		out = append(out, p.TaskType)
	}
	return out
}
