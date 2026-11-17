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

package devicefeature

import (
	"log/slog"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/build"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/features/device/cvd"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/internal/platform/ice"
)

// Deps carries the device slice's injected collaborators beyond the
// repositories and clients; nil fields disable the corresponding capability
// exactly as the underlying service options do.
type Deps struct {
	AuditLogger  audit.Sink
	TaskEnqueuer TaskEnqueuer
	// OperationJournal lets queued CVD creates record their HO operation so a
	// redelivery resumes it (the task queue serves both queue ports).
	OperationJournal task.OperationJournal
	IdempotencyStore IdempotencyStore

	// ArtifactLedger records pinned fetch keys a completed create left in the
	// host's download cache, the build-affinity signal placement orders by.
	ArtifactLedger cvd.ArtifactLedger

	// Operations admits the async in-place verbs (restart, powerwash) and is the
	// store their handlers settle. Nil disables those verbs rather than falling
	// back to a synchronous call whose outcome nothing records.
	Operations VerbOperationStore

	// Artifacts, ArtifactPolicy, and ArtifactsBucket enable artifact capture.
	// They are the GCS uploader, the per-type bucket policy, and the
	// deployment-default bucket.
	Artifacts       ArtifactUploader
	ArtifactPolicy  PolicyReader
	ArtifactsBucket string

	// LifetimePolicy resolves the operator lifetime defaults for create and
	// extend; nil reads as the compiled defaults.
	LifetimePolicy PolicyReader

	// Declarative enables requirements-based creation (placement and
	// auto-provisioning).
	Declarative DeclarativeDeps

	// ICE composes the per-leg server lists and transport policy served by the
	// ICE endpoint, minting a credential where the deployment relays media.
	ICE *ice.Composer

	// AllowInsecureHostTLS skips certificate verification when the WebSocket
	// proxy dials the Host Orchestrator (self-signed HO certs).
	AllowInsecureHostTLS bool
	// DevOrigin additionally accepts the local Angular dev server as a
	// WebSocket origin; production keeps the strict same-host check.
	DevOrigin bool
}

// Slice is the device feature's wired product set: the HTTP handler (which
// carries the long-poll signaling handler and the WebSocket proxy) plus the
// two cross-feature products, the service (batch admission, the
// reaper/settlement jobs, capture tasks) and the CVD actuator (the queue
// worker handlers, which stay app-wired).
type Slice struct {
	Handler  *DeviceHandler
	Service  *DeviceService
	Actuator *cvd.Actuator

	// VerbHandlers are the queue handlers for the async in-place verbs, empty
	// when Deps.Operations is nil. They ship as products rather than being
	// registered here because the composition root owns handler registration.
	VerbHandlers []task.Handler
}

// Wire creates the device feature: service, CVD actuator, both signaling
// handlers, and the HTTP handler. repo is constructed by the composition root
// because the host slice consumes its live-device counter before this slice
// can be built (host remediation gates on it while device creation drives the
// host service).
func Wire(
	repo *DeviceRepository,
	hosts host.Repository,
	builds build.Reader,
	ho *hoclient.Factory,
	logger *slog.Logger,
	deps Deps,
) (Slice, error) {
	opts := []DeviceServiceOption{
		WithDeviceAuditLogger(deps.AuditLogger),
		WithTaskEnqueuer(deps.TaskEnqueuer),
		WithIdempotencyStore(deps.IdempotencyStore),
		WithArtifacts(deps.Artifacts, deps.ArtifactPolicy, deps.ArtifactsBucket),
		WithLifetimePolicy(deps.LifetimePolicy),
		WithDeclarativeCreate(deps.Declarative),
	}
	if deps.Operations != nil {
		opts = append(opts, WithOperations(deps.Operations))
	}
	svc, err := NewDeviceService(repo, hosts, builds, ho, logger, opts...)
	if err != nil {
		return Slice{}, err
	}

	verbHandlers, err := wireVerbHandlers(svc, deps.Operations, logger)
	if err != nil {
		return Slice{}, err
	}
	snapshotHandlers, err := wireSnapshotVerbHandlers(svc, deps.Operations, logger)
	if err != nil {
		return Slice{}, err
	}
	verbHandlers = append(verbHandlers, snapshotHandlers...)

	// The CVD actuator converges what the service admitted: the queued create
	// (journal-resumed), the probe-first cleanup, and zombie recovery.
	actuator := cvd.NewActuator(repo, hosts, builds, ho, logger,
		cvd.WithOperationJournal(deps.OperationJournal),
		cvd.WithArtifactLedger(deps.ArtifactLedger),
		// Abandon a doomed in-flight CVD create ~5s after the device is
		// deleted, freeing the worker instead of waiting out the full create.
		cvd.WithCreateCancelPoll(5*time.Second))

	signaling := NewSignalingHandler(repo, hosts, ho, logger)
	var wsOpts []WSOption
	if deps.DevOrigin {
		wsOpts = append(wsOpts, WithDevOrigin())
	}
	ws := NewWebSocketHandler(repo, hosts, ho, logger, deps.AllowInsecureHostTLS, wsOpts...)

	return Slice{
		Handler:      NewDeviceHandler(svc, deps.ICE, signaling, ws),
		Service:      svc,
		Actuator:     actuator,
		VerbHandlers: verbHandlers,
	}, nil
}

// wireVerbHandlers builds one queue handler per async in-place verb.
//
// The verb table is here rather than in the composition root because the mapping
// from a verb to its client method is this slice's knowledge; the registry in the
// domain owns everything else about each verb (exclusivity, retry budget, response
// shape), so adding one means adding a registry entry and a row here.
func wireVerbHandlers(svc *DeviceService, ops VerbOperationStore, logger *slog.Logger) ([]task.Handler, error) {
	if ops == nil {
		return nil, nil
	}
	verbs := []VerbDefinition{
		{operation.VerbDeviceRestart, "restart", audit.ActionDeviceRestart, hoclient.Client.RestartCVD},
		{operation.VerbDevicePowerwash, "powerwash", audit.ActionDevicePowerwash, hoclient.Client.PowerwashCVD},
	}
	handlers := make([]task.Handler, 0, len(verbs))
	for _, def := range verbs {
		h, err := NewVerbOperationHandler(def, svc, ops, logger)
		if err != nil {
			return nil, err
		}
		handlers = append(handlers, h)
	}
	return handlers, nil
}
