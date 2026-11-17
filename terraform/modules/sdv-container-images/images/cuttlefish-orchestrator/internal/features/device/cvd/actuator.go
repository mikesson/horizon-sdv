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

package cvd

import (
	"context"
	"log/slog"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/build"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// DeviceStore is the slice of the device repository the actuator drives: it
// reads rows, records status transitions, and commits CAS mutations. The
// feature's DeviceRepository satisfies it.
type DeviceStore interface {
	Get(ctx context.Context, id string) (*device.Device, error)
	UpdateStatus(ctx context.Context, id string, status device.Status, errorMsg string) error
	Mutate(ctx context.Context, id string, fn func(*device.Device) error) (*device.Device, error)
	// ListSnapshotsByDevice feeds cleanup's best-effort host-side snapshot
	// removal. Snapshot directories are host-global and live outside the CVD
	// group, so tearing down the group alone leaks them until the host dies.
	ListSnapshotsByDevice(ctx context.Context, deviceID string) ([]*device.Snapshot, error)
	// RecordBootObservation persists the boot-watch sample (observation and
	// CVD enum together, so one write stores one coherent sample).
	RecordBootObservation(ctx context.Context, hostID, deviceID, cvdStatus string, obs *device.BootObservation) error
	// ListByHost feeds the boot-loop attribution discriminator: distinct
	// builds looping on one host indict the host, one build does not.
	ListByHost(ctx context.Context, hostID string) ([]*device.Device, error)
}

// HostStore is the slice of the host repository the actuator consults: the
// device's host row, the poison-condition writers, and the ho_reachable
// latch the starting-host probe promotes through. The host feature's
// repository satisfies it.
type HostStore interface {
	Get(ctx context.Context, id string) (*host.Host, error)
	RecordCreateFailure(ctx context.Context, hostID, reason string) (newlyDegraded bool, err error)
	ClearCreateFailures(ctx context.Context, hostID string) error
	MarkHOReady(ctx context.Context, hostID string) (promoted bool, err error)
}

// ArtifactLedger records pinned fetch keys whose artifacts a host's download
// cache now holds. Placement orders hosts by that build-affinity signal. The
// platform artifact-cache repository satisfies it.
type ArtifactLedger interface {
	Record(ctx context.Context, hostID, fetchKey, buildID string, hostIncarnation int64) error
}

// Option configures an Actuator.
type Option func(*Actuator)

// WithArtifactLedger wires the build-affinity ledger. When unset, creates
// record no cache residency, which stays correct but loses warm placement.
func WithArtifactLedger(l ArtifactLedger) Option {
	return func(a *Actuator) { a.artifacts = l }
}

// WithOperationJournal wires the task-step journal: CVD creates record their
// HO operation name so a redelivery resumes the same operation instead of
// adopting the half-finished result by observation. When unset (or when a
// call carries no task ID), creates run unjournaled, which stays correct but
// loses the resume optimization.
func WithOperationJournal(j task.OperationJournal) Option {
	return func(a *Actuator) { a.journal = j }
}

// WithCreateCancelPoll enables cooperative cancellation of an in-flight CVD
// create: while waiting on the (multi-minute) Host Orchestrator operation, the
// actuator polls the device row at this interval and abandons the wait when
// the device enters teardown (deleting/deleted), freeing the worker instead of
// running the doomed create to completion. The device's cleanup task, enqueued
// atomically with the delete, then tears down whatever the launch left. A
// zero or negative interval (the default) disables the poll. The wait then
// runs to completion and the end-of-create re-read still tears down a device
// deleted underneath it, just less responsively.
func WithCreateCancelPoll(interval time.Duration) Option {
	return func(a *Actuator) { a.cancelPoll = interval }
}

// Actuator drives a device's CVD on its host: the queued create with its
// journal-resume and adopt ladder, the probe-first cleanup, and the two-step
// zombie recovery. It owns no admission or placement. The device service
// admits and places; this type converges what was admitted.
type Actuator struct {
	devices    DeviceStore
	hosts      HostStore
	builds     build.Reader
	ho         *hoclient.Factory
	journal    task.OperationJournal
	artifacts  ArtifactLedger
	cancelPoll time.Duration
	// loadSample paces the load-wait sampler (sampleCVDLoad); tests shrink it.
	loadSample time.Duration
	logger     *slog.Logger
}

// NewActuator creates the CVD actuator.
func NewActuator(devices DeviceStore, hosts HostStore, builds build.Reader, ho *hoclient.Factory, logger *slog.Logger, opts ...Option) *Actuator {
	if logger == nil {
		logger = slog.Default()
	}
	a := &Actuator{
		devices: devices, hosts: hosts, builds: builds, ho: ho, logger: logger,
		loadSample: defaultLoadSampleInterval,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// terminalWriteTimeout bounds a detached terminal-status write so it cannot
// hang the worker during shutdown.
const terminalWriteTimeout = 15 * time.Second

// servingHOClient resolves the Host Orchestrator client for a running host, or
// serving=false when the host is gone or not yet serving.
func (a *Actuator) servingHOClient(ctx context.Context, hostID string) (hoclient.Client, bool) {
	h, err := a.hosts.Get(ctx, hostID)
	if err != nil || h.Status != host.StatusRunning || h.HostOrchestratorURL() == "" {
		return nil, false
	}
	return a.ho.ClientForHost(h.HostOrchestratorURL()), true
}

// markCreateFailed records a terminal CVD-creation failure on the device. The
// worker calls it when the task is exhausted (dead-lettered).
func (a *Actuator) markCreateFailed(ctx context.Context, deviceID string, cause error) {
	// The attempt is exhausted, so this terminal write must land even when the
	// handler's context is already canceled or timed out. That cancellation is
	// often the failure cause itself (HandleTimeout) or a pool shutdown. Detach
	// from it, but keep a bound so it cannot hang.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminalWriteTimeout)
	defer cancel()
	if err := a.devices.UpdateStatus(ctx, deviceID, device.StatusError, cause.Error()); err != nil {
		a.logger.Error("failed to mark device create as failed",
			slog.String("device_id", deviceID), slog.Any("error", err))
	}
	// A create that exhausted on the poison signature is evidence the HOST is
	// bad. Attribute it so repeated failures degrade and remediate it.
	if isHostPoisonError(cause) {
		if d, gerr := a.devices.Get(ctx, deviceID); gerr == nil {
			a.recordHostPoison(ctx, d.HostID, cause.Error())
		}
	}
}
