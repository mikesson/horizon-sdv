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

package quotafeature

import (
	"log/slog"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

// Deps carries the quota slice's optional collaborators; nil fields disable
// the corresponding capability.
type Deps struct {
	AuditLogger audit.Sink
}

// Wire creates the quota feature: repository, service, handler. hosts
// supplies the transactional used_hosts recount ReconcileAll delegates to
// (the host repository satisfies it). The service is returned for its
// cross-slice consumers (declarative device planning, batch admission, admin
// stats, quota reconciliation). The startup EnsureDefaultLimit seed stays an
// explicit call in the composition root, because Wire does no I/O.
func Wire(client *spannerplatform.Client, hosts UsedHostsReconciler, logger *slog.Logger, deps Deps) (*QuotaHandler, *Service) {
	svc := NewService(NewRepository(client), hosts, logger)
	handler := NewQuotaHandler(svc)
	handler.SetAuditLogger(deps.AuditLogger)
	return handler, svc
}
