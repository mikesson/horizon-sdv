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

import {HttpClient, HttpParams} from '@angular/common/http';
import {inject, Service} from '@angular/core';
import type {Observable} from 'rxjs';
import {catchError, map} from 'rxjs/operators';
import {createApiErrorHandler, resolveBasePath} from '../../shared/utils';
import type {Collection} from '../../shared/utils/paged-collection';
import type {
  Batch,
  BatchAdmission,
  BatchListFilter,
  BatchListResponse,
  BatchMember,
  CreateBatchRequest,
} from '../models/batch.model';
import {
  BatchListResponseSchema,
  BatchMembersResponseSchema,
  BatchPlanSchema,
  BatchSchema,
} from '../models/batch.schema';

/** HTTP transport for the Batch vertical slice. */
@Service()
export class BatchApiService {
  private readonly http = inject(HttpClient);
  private readonly baseUrl = resolveBasePath('/v2/batches');
  private readonly handleError = createApiErrorHandler('Batch', 'BatchApiService');

  list(filter?: BatchListFilter): Observable<BatchListResponse> {
    let params = new HttpParams();
    if (filter?.mine) {
      params = params.set('mine', 'true');
    }
    if (filter?.createdBy) {
      params = params.set('created_by', filter.createdBy);
    }
    return this.http.get<unknown>(this.baseUrl, {params}).pipe(
      map(data => BatchListResponseSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /**
   * One request. The batch list publishes no limit/offset, because batches
   * are a derived view over a small set, so its single response is the whole
   * collection.
   */
  listAll(filter?: BatchListFilter): Observable<Collection<Batch>> {
    return this.list(filter).pipe(map(page => ({...page, complete: true})));
  }

  get(id: string): Observable<Batch> {
    return this.http.get<unknown>(`${this.baseUrl}/${id}`).pipe(
      map(data => BatchSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  members(id: string): Observable<BatchMember[]> {
    return this.http.get<unknown>(`${this.baseUrl}/${id}/devices`).pipe(
      map(data => BatchMembersResponseSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /**
   * Dry-runs an admission (AIP-163 validate_only). The backend resolves the
   * build, ranks a zone, gates on host quota and computes the placement, then
   * commits none of it. A plan that cannot admit min_ready devices is an
   * error rather than a reduced plan.
   */
  planCreate(request: CreateBatchRequest): Observable<BatchAdmission> {
    const body = {...this.toCreateBody(request), validate_only: true};
    return this.http.post<unknown>(this.baseUrl, body).pipe(
      map(data => BatchPlanSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /**
   * Creates the batch. The key makes the call retry-safe, so a repeat resumes
   * the same fleet instead of provisioning a second one. The backend fingerprints
   * the spec against the key, so a key may not be reused for an edited request.
   */
  create(request: CreateBatchRequest, idempotencyKey: string): Observable<Batch> {
    return this.http
      .post<unknown>(this.baseUrl, this.toCreateBody(request), {
        headers: {'Idempotency-Key': idempotencyKey},
      })
      .pipe(
        map(data => BatchSchema.parse(data)),
        catchError(this.handleError)
      );
  }

  delete(id: string): Observable<void> {
    return this.http.delete<void>(`${this.baseUrl}/${id}`);
  }

  /** Serializes a create request to the wire shape (camelCase → snake_case). */
  private toCreateBody(request: CreateBatchRequest): Record<string, unknown> {
    const body: Record<string, unknown> = {
      name_prefix: request.namePrefix,
      count: request.count,
      min_ready: request.minReady,
      fill_policy: request.fillPolicy,
      placement: request.placement,
      requirements: this.toRequirementsBody(request.requirements),
    };
    // The backend rejects a ready timeout outside the atomic policy, so an
    // unset one must be absent rather than 0.
    if (request.readyTimeoutSeconds) {
      body['ready_timeout_seconds'] = request.readyTimeoutSeconds;
    }
    if (request.ttlHours) {
      body['ttl_hours'] = request.ttlHours;
    }
    return body;
  }

  private toRequirementsBody(
    requirements: CreateBatchRequest['requirements']
  ): Record<string, unknown> {
    const wire: Record<string, unknown> = {interaction: requirements.interaction};
    if (requirements.gpuRequired) {
      wire['gpu_required'] = true;
    }
    if (requirements.preferredRegion) {
      wire['location'] = {preferred_region: requirements.preferredRegion};
    }
    if (requirements.capacity && requirements.capacity !== 'auto') {
      wire['capacity'] = requirements.capacity;
    }
    // x86 and an unpinned pool are the backend's own defaults, so neither is
    // sent. The field absent and the field at its default mean the same thing.
    if (requirements.architecture === 'arm') {
      wire['architecture'] = 'arm';
    }
    if (requirements.pool) {
      wire['pool'] = requirements.pool;
    }
    const build = requirements.build;
    if (build?.kind === 'registered') {
      wire['build'] = {registered: build.id};
    } else if (build?.kind === 'track') {
      wire['build'] = {track: {branch: build.branch, target: build.target}};
    }
    return wire;
  }
}
