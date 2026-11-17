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

package task

import (
	"errors"
	"slices"

	"cloud-cuttlefish-orchestrator/internal/domain/operation"
)

// Well-known task types. Defined here rather than in the owning features
// because cross-feature machinery (admin requeue, cross-loop waits) must
// name them without importing feature packages (features cannot import
// features). The features alias these under their local names.
const (
	TypeHostProvision    = "host.provision"
	TypeHostDeprovision  = "host.deprovision"
	TypeHostRemediate    = "host.remediate"
	TypeDeviceCreateCVD  = "device.create_cvd"
	TypeDeviceCleanupCVD = "device.cleanup_cvd"

	// TypeHostStopSettle waits on a user host stop's GCE operation and settles
	// the host and its devices to stopped once the VM is observed parked, or
	// returns the host's desired state to running when the stop fails.
	// Enqueued by host stop in the commit that marks the host stopping.
	TypeHostStopSettle = "host.stop_settle"

	// TypeDeviceCaptureBugreport captures a bugreport for a device that reached
	// the error state (policy-gated capture-on-error). Filed by the health
	// monitor's failed-capture detector, deduped while pending via HasActive.
	TypeDeviceCaptureBugreport = "device.capture_bugreport"

	// TypeCatalogReprobeSpotAdvice re-probes one Spot capacity-advice cell that
	// a stockout has just proved empty, so ranking repels on a fresh score
	// within seconds of a capacity storm instead of within a sync interval. Its
	// resource id is the cell: "<zone>/<machine type>".
	TypeCatalogReprobeSpotAdvice = "catalog.reprobe_spot_advice"

	// Keyed reconcile types: one logical convergence loop per
	// resource, deduped while pending and serialized per key while leased.
	// The schema's generated reconcile_key column names these literally, so
	// adding a keyed type means updating both.
	TypeHostReconcile   = "host.reconcile"
	TypeDeviceReconcile = "device.reconcile"
)

// IsKeyedReconcile reports whether a task type gets the keyed-queue
// treatment: pending dedup with coalesce-merge, and per-key claim
// serialization (no two workers reconcile one resource concurrently).
func IsKeyedReconcile(taskType string) bool {
	return taskType == TypeHostReconcile || taskType == TypeDeviceReconcile
}

// ErrRequeueLeadsNowhere rejects a DLQ requeue that would silently no-op;
// see RequeueLeadsNowhere.
var ErrRequeueLeadsNowhere = errors.New(
	"requeue would no-op: exhaustion already moved the resource to a terminal status; use the resource's retry API instead")

// RequeueLeadsNowhere reports whether requeueing a dead-lettered task of this
// type is a silent no-op: exhaustion already moved the task's resource out of
// the status its handler gates on (host/device error; for a host stop settle,
// stopping back to running), so a requeued task claims, no-ops, and completes,
// which looks retried while doing nothing. Recovery for these goes through the
// resource's own APIs (retry, recreate, a fresh host stop), which reset the
// status AND enqueue fresh. Deprovision and cleanup requeues do work (no
// status gate; probe-first).
func RequeueLeadsNowhere(taskType string) bool {
	return taskType == TypeHostProvision || taskType == TypeDeviceCreateCVD || taskType == TypeHostStopSettle
}

// ErrRequeueUnsafe rejects a DLQ requeue that would actuate outside the
// operation model; see RequeueUnsafe.
var ErrRequeueUnsafe = errors.New(
	"requeue would re-actuate a verb outside the operation model, untracked by the handle that recorded it; issue a new request for the verb instead")

// RequeueUnsafe reports whether requeueing a dead-lettered task of this type
// would reach around an operation handle. It covers EVERY operation-backed type,
// including the ones that are not destructive, and the reason is the model
// rather than the blast radius: RequeueFailed resets attempts to zero and drops the step
// journal, while the operation row is already done and its admission key already
// NULL, so the requeued work runs with its retry budget restored, with no live
// operation tracking it, and with a concurrent user request admissible alongside.
//
// For a destructive verb that means a second wipe with the MaxAttempts budget
// that prevented one reset. For a harmless one it means the operation stops
// describing what happened: the fresh attempt's terminal write is refused by the
// CAS, so an operator presses a button that reports success and changes nothing
// anyone can see. Neither is a retry.
//
// It is NOT folded into RequeueLeadsNowhere: that predicate promises the
// requeue would silently no-op, and its message says so. A requeued powerwash
// would not no-op, it would wipe a device, so reusing it puts a false statement
// in front of an operator.
//
// The list comes from the operation registry so it cannot drift from the verbs
// that actually produce handles.
func RequeueUnsafe(taskType string) bool {
	return slices.Contains(operation.TaskTypes(), taskType)
}
