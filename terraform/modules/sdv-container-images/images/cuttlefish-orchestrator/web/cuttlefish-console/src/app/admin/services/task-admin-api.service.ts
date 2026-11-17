/**
 * Copyright 2026 Google LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     https://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import {HttpClient, HttpParams, httpResource} from '@angular/common/http';
import {computed, inject, Service} from '@angular/core';
import type {Observable} from 'rxjs';
import {catchError, map} from 'rxjs/operators';
import {createApiErrorHandler, resolveBasePath} from '../../shared/utils';
import type {
  ControlPlaneSnapshot,
  SweepPolicy,
  SweepPolicyPatch,
} from '../models/control-plane.model';
import {ControlPlaneSnapshotSchema, SweepPolicySchema} from '../models/control-plane.schema';
import type {FailedTask, QueueStats} from '../models/task-admin.model';
import {FailedTaskListSchema, QueueStatsSchema} from '../models/task-admin.schema';

/** Dead-letter queue administration (list, retry, delete) and queue depth. */
@Service()
export class TaskAdminApiService {
  private readonly http = inject(HttpClient);
  private readonly baseUrl = resolveBasePath('/v2/admin/tasks');
  private readonly handleError = createApiErrorHandler('Task', 'TaskAdminApiService');

  controlPlane(): Observable<ControlPlaneSnapshot> {
    return this.http.get<unknown>(resolveBasePath('/v2/admin/control-plane')).pipe(
      map(data => ControlPlaneSnapshotSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /**
   * Moves one or more autonomy switches. The body carries only what changed,
   * so a flip lands one audited config change rather than re-asserting three
   * values the operator never touched.
   */
  updateSweeps(patch: SweepPolicyPatch): Observable<SweepPolicy> {
    const body: Record<string, boolean> = {};
    if (patch.deleteOrphanVms !== undefined) {
      body['delete_orphan_vms'] = patch.deleteOrphanVms;
    }
    if (patch.orphanCvds !== undefined) {
      body['orphan_cvds'] = patch.orphanCvds;
    }
    if (patch.zombieRecreate !== undefined) {
      body['zombie_recreate'] = patch.zombieRecreate;
    }
    if (patch.reaperDryRun !== undefined) {
      body['reaper_dry_run'] = patch.reaperDryRun;
    }
    return this.http.put<unknown>(resolveBasePath('/v2/admin/system-config/sweeps'), body).pipe(
      map(data => SweepPolicySchema.parse(data)),
      catchError(this.handleError)
    );
  }

  private readonly statsResource = httpResource<QueueStats>(() => `${this.baseUrl}/stats`, {
    parse: raw => QueueStatsSchema.parse(raw),
  });

  /** Queue-depth snapshot, shared by the dashboard and the Operations page. */
  readonly queueStats = computed(() =>
    this.statsResource.hasValue() ? this.statsResource.value() : null
  );

  readonly statsLoading = this.statsResource.isLoading;

  refreshStats(): void {
    this.statsResource.reload();
  }

  listFailed(limit = 100, resourceId?: string): Observable<FailedTask[]> {
    let params = new HttpParams().set('limit', limit.toString());
    if (resourceId) {
      params = params.set('resource_id', resourceId);
    }
    return this.http.get<unknown>(`${this.baseUrl}/failed`, {params}).pipe(
      map(data => FailedTaskListSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  retry(taskId: string): Observable<void> {
    return this.http.post<unknown>(`${this.baseUrl}/${taskId}/retry`, {}).pipe(
      map(() => undefined),
      catchError(this.handleError)
    );
  }

  delete(taskId: string): Observable<void> {
    return this.http.delete<unknown>(`${this.baseUrl}/${taskId}`).pipe(
      map(() => undefined),
      catchError(this.handleError)
    );
  }
}
