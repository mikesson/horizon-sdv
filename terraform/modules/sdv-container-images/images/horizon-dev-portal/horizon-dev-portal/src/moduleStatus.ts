// Copyright (c) 2024-2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
import type { StatusResponse } from './types';

export type DeploymentStatus =
  | 'NOT INSTALLED'
  | 'UNINSTALL IN PROGRESS'
  | 'INSTALLATION IN PROGRESS'
  | 'UPDATE IN PROGRESS'
  | 'READY';

function argoAppStatusPresent(st?: StatusResponse): boolean {
  if (!st) {
    return false;
  }
  return !!(
    (st.syncStatus ?? '').trim() ||
    (st.healthStatus ?? '').trim() ||
    (st.operationPhase ?? '').trim() ||
    (st.desiredRevision ?? '').trim() ||
    (st.syncRevision ?? '').trim() ||
    (st.applicationDeletionTimestamp ?? '').trim()
  );
}

/**
 * Maps Argo CD Application status to a coarse portal label.
 * UPDATE IN PROGRESS: drift on a previously healthy deploy (OutOfSync+Healthy), or an
 * operation in flight while the app was already synced/healthy (typical in-place update).
 * First-time install usually has health Progressing or sync Unknown, so it stays INSTALLATION.
 */
export function deploymentStatus(
  enabled: boolean,
  st?: StatusResponse
): DeploymentStatus {
  if (!enabled) {
    const rem = st?.remainingManagedApplications;
    if (rem != null && rem > 0) {
      return 'UNINSTALL IN PROGRESS';
    }
    if (rem === 0 && !argoAppStatusPresent(st)) {
      return 'NOT INSTALLED';
    }
    return argoAppStatusPresent(st) ? 'UNINSTALL IN PROGRESS' : 'NOT INSTALLED';
  }
  const sync = (st?.syncStatus ?? '').trim();
  const health = (st?.healthStatus ?? '').trim();
  const op = (st?.operationPhase ?? '').trim();
  const opBusy = op === 'Running' || op === 'Pending';

  if (sync === 'Synced' && health === 'Healthy' && !opBusy) {
    return 'READY';
  }
  if (sync === 'OutOfSync' && health === 'Healthy') {
    return 'UPDATE IN PROGRESS';
  }
  if (opBusy && (health === 'Healthy' || sync === 'Synced')) {
    return 'UPDATE IN PROGRESS';
  }
  return 'INSTALLATION IN PROGRESS';
}

export function isReady(
  enabled: boolean,
  st?: StatusResponse
): boolean {
  return deploymentStatus(enabled, st) === 'READY';
}
