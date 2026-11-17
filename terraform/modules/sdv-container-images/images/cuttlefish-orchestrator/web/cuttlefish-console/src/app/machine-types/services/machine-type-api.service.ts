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

import {HttpClient, HttpErrorResponse, HttpParams} from '@angular/common/http';
import {inject, Service} from '@angular/core';
import type {Observable} from 'rxjs';
import {of} from 'rxjs';
import {catchError, map} from 'rxjs/operators';
import {
  createApiErrorHandler,
  extractErrorMessage,
  getErrorMessage,
  resolveBasePath,
} from '../../shared/utils';
import type {MachineType, MachineTypeFilter} from '../models';
import {MachineTypeListResponseSchema, MachineTypeSchema} from '../models';

/**
 * What one machine-type name resolved to. Exactly one side is populated: a
 * shape Compute Engine will build, or the sentence in which it refused.
 */
export interface ShapeResolution {
  shape: MachineType | null;
  refusal: string;
}

/**
 * API service for fetching machine types from the backend.
 * Machine types are stored in the Spanner database and served via /v2/machine-types.
 */
@Service()
export class MachineTypeApiService {
  private readonly http = inject(HttpClient);
  private readonly baseUrl = resolveBasePath('/v2/machine-types');
  private readonly handleError = createApiErrorHandler('MachineType', 'MachineTypeApiService');

  /**
   * List all machine types with optional filtering.
   */
  list(filter?: MachineTypeFilter): Observable<MachineType[]> {
    let params = new HttpParams();

    if (filter) {
      if (filter.family) {
        params = params.set('family', filter.family);
      }
      if (filter.category) {
        params = params.set('category', filter.category);
      }
      if (filter.architecture) {
        params = params.set('architecture', filter.architecture);
      }
      if (filter.gpuSupport !== undefined) {
        params = params.set('gpu_support', filter.gpuSupport.toString());
      }
      if (filter.enabled !== undefined) {
        params = params.set('enabled', filter.enabled.toString());
      }
    }

    return this.http.get<unknown>(this.baseUrl, {params}).pipe(
      map(data => MachineTypeListResponseSchema.parse(data).items),
      catchError(this.handleError)
    );
  }

  /**
   * Resolves one machine type by name, which is how a custom shape gets its
   * size. Compute Engine synthesizes the resource for a legal name and refuses
   * an illegal one with the rule it broke.
   *
   * The refusal is an answer here rather than a failure, so it comes back as
   * data. It also bypasses the shared error handler, which flattens every 404
   * to "MachineType not found" and would replace the one sentence the operator
   * needs, Compute Engine's own, with a sentence about nothing.
   */
  resolve(id: string): Observable<ShapeResolution> {
    return this.http.get<unknown>(`${this.baseUrl}/${encodeURIComponent(id)}`).pipe(
      map((data): ShapeResolution => ({shape: MachineTypeSchema.parse(data), refusal: ''})),
      catchError((error: unknown) =>
        of({
          shape: null,
          refusal:
            error instanceof HttpErrorResponse
              ? extractErrorMessage(error)
              : getErrorMessage(error, `${id} could not be resolved`),
        })
      )
    );
  }
}
