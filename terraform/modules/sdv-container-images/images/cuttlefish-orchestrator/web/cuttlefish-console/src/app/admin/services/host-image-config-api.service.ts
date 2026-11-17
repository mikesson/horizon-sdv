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
import type {
  CreateHostImageConfigRequest,
  HostImageConfig,
  HostImageConfigListResponse,
  UpdateHostImageConfigRequest,
} from '../models/host-image-config.model';
import {
  HostImageConfigListResponseSchema,
  HostImageConfigSchema,
} from '../models/host-image-config.schema';

/**
 * Filter options for listing host image configs.
 */
export interface HostImageConfigFilter {
  enabledOnly?: boolean;
  limit?: number;
  offset?: number;
}

/**
 * API service for managing host image configurations.
 *
 * Provides CRUD operations for machine family to image family mappings.
 * All endpoints require platform administrator privileges.
 */
@Service()
export class HostImageConfigApiService {
  private readonly http = inject(HttpClient);
  private readonly baseUrl = resolveBasePath('/v2/admin/host-image-configs');
  private readonly handleError = createApiErrorHandler(
    'HostImageConfig',
    'HostImageConfigApiService'
  );

  /**
   * List all host image configurations.
   */
  list(filter?: HostImageConfigFilter): Observable<HostImageConfigListResponse> {
    let params = new HttpParams();

    if (filter) {
      if (filter.enabledOnly !== undefined) {
        params = params.set('enabled_only', filter.enabledOnly.toString());
      }
      if (filter.limit !== undefined) {
        params = params.set('limit', filter.limit.toString());
      }
      if (filter.offset !== undefined) {
        params = params.set('offset', filter.offset.toString());
      }
    }

    return this.http.get<unknown>(this.baseUrl, {params}).pipe(
      map(data => HostImageConfigListResponseSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /**
   * Create a new host image configuration.
   */
  create(request: CreateHostImageConfigRequest): Observable<HostImageConfig> {
    const body = {
      machine_family: request.machineFamily,
      image_family: request.imageFamily,
      source_project: request.sourceProject ?? null,
      description: request.description ?? null,
      enabled: request.enabled ?? true,
    };

    return this.http.post<unknown>(this.baseUrl, body).pipe(
      map(data => HostImageConfigSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /**
   * Update an existing host image configuration.
   */
  update(
    machineFamily: string,
    request: UpdateHostImageConfigRequest
  ): Observable<HostImageConfig> {
    const body = {
      image_family: request.imageFamily,
      source_project: request.sourceProject ?? null,
      description: request.description ?? null,
      enabled: request.enabled,
    };

    return this.http.put<unknown>(`${this.baseUrl}/${machineFamily}`, body).pipe(
      map(data => HostImageConfigSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /**
   * Delete a host image configuration.
   */
  delete(machineFamily: string): Observable<void> {
    return this.http
      .delete<void>(`${this.baseUrl}/${machineFamily}`)
      .pipe(catchError(this.handleError));
  }
}
