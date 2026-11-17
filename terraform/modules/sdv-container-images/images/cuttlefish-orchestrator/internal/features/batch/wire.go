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

package batchfeature

import (
	"log/slog"

	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

// Wire creates the batch feature: repository, service, handler. The batch
// drives the device and host engines through the consumer-side interfaces
// declared in this package; the repository is returned for the settlement job
// (atomic-fill enforcement, batch-row GC, drained-host reclamation).
func Wire(client *spannerplatform.Client, devices DeviceOrchestrator, hosts HostProvisioner, quota QuotaReader, logger *slog.Logger) (*Handler, *Repository) {
	repo := NewRepository(client)
	return NewHandler(NewService(devices, hosts, quota, repo, logger)), repo
}
