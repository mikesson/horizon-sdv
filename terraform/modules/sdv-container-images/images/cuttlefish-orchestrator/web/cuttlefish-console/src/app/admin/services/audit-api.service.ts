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
import type {AuditFilter, AuditPage} from '../models/audit.model';
import {AuditPageSchema} from '../models/audit.schema';

/** Read side of the audit log (GET /v2/admin/audit), newest first. */
@Service()
export class AuditApiService {
  private readonly http = inject(HttpClient);
  private readonly handleError = createApiErrorHandler('Audit', 'AuditApiService');

  list(filter: AuditFilter, limit: number, offset: number): Observable<AuditPage> {
    let params = new HttpParams().set('limit', limit.toString()).set('offset', offset.toString());
    if (filter.actorEmail) {
      params = params.set('actor_email', filter.actorEmail);
    }
    if (filter.action) {
      params = params.set('action', filter.action);
    }
    if (filter.resourceType) {
      params = params.set('resource_type', filter.resourceType);
    }
    if (filter.resourceId) {
      params = params.set('resource_id', filter.resourceId);
    }
    if (filter.kind) {
      params = params.set('kind', filter.kind);
    }
    if (filter.startTime) {
      params = params.set('start_time', filter.startTime.toISOString());
    }
    if (filter.endTime) {
      params = params.set('end_time', filter.endTime.toISOString());
    }
    return this.http.get<unknown>(resolveBasePath('/v2/admin/audit'), {params}).pipe(
      map(data => AuditPageSchema.parse(data)),
      catchError(this.handleError)
    );
  }
}
