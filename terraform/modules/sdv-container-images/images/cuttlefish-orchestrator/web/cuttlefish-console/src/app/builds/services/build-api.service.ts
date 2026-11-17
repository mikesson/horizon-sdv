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
import {collectPages, MAX_PAGE_SIZE} from '../../shared/utils/paged-collection';
import type {
  Build,
  BuildListFilter,
  BuildListResponse,
  CreateBuildRequest,
} from '../models/build.model';
import {BuildListResponseSchema, BuildSchema} from '../models/build.schema';

/**
 * Manages the HTTP transport layer for the Build vertical slice.
 *
 * This service interacts with the orchestrator to manage Android Build artifacts,
 * whether they originate from official CI or private GCS buckets.
 */
@Service()
export class BuildApiService {
  private readonly http = inject(HttpClient);
  private readonly baseUrl = resolveBasePath('/v2/builds');
  private readonly handleError = createApiErrorHandler('Build', 'BuildApiService');

  /**
   * Retrieves one window of builds matching the filter.
   *
   * VALIDATION: Uses BuildListResponseSchema to validate the response.
   *
   * @param filter - Optional filter criteria (source).
   */
  list(filter?: BuildListFilter): Observable<BuildListResponse> {
    // Always request the backend's maximum page size: its default (20)
    // silently truncates fleet lists. An explicit filter.limit still wins.
    let params = new HttpParams().set('limit', String(filter?.limit ?? MAX_PAGE_SIZE));

    if (filter) {
      if (filter.source) {
        params = params.set('source', filter.source);
      }
      if (filter.pageToken) {
        params = params.set('page_token', filter.pageToken);
      }
    }

    return this.http.get<unknown>(this.baseUrl, {params}).pipe(
      map(data => BuildListResponseSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /**
   * Retrieves every build matching the filter, following the server's pages.
   *
   * This is what the store loads. Every build picker in the console (the
   * device interview, the advanced editor, the batch wizard) offers what is
   * in the store, so a catalog past one page would hide launchable builds.
   */
  listAll(filter?: BuildListFilter): Observable<Collection<Build>> {
    return collectPages(window => this.list({...filter, ...window}));
  }

  /**
   * Registers a new build artifact in the system.
   */
  create(request: CreateBuildRequest): Observable<Build> {
    const body: Record<string, unknown> = {
      name: request.name,
      source: request.source,
      build_id: request.buildId,
      target: request.target,
      branch: request.branch,
      description: request.description,
      is_default: request.isDefault,
    };

    // For 'url' source, send artifact_specs array (backend expects artifact_specs, not artifact_sources)
    if (request.artifactSources && request.artifactSources.length > 0) {
      body['artifact_specs'] = request.artifactSources.map(src => ({
        type: src.type,
        url: src.url,
        role: src.role,
      }));
    }

    return this.http.post<unknown>(this.baseUrl, body).pipe(
      map(data => BuildSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /**
   * Updates metadata for an existing build.
   *
   * TRANSFORMATION: Explicitly maps camelCase fields to snake_case for the PATCH body.
   */
  update(id: string, request: Partial<CreateBuildRequest>): Observable<Build> {
    const body: Record<string, unknown> = {};
    if (request.name !== undefined) {
      body['name'] = request.name;
    }
    if (request.description !== undefined) {
      body['description'] = request.description;
    }
    if (request.isDefault !== undefined) {
      body['is_default'] = request.isDefault;
    }
    if (request.artifactSources !== undefined) {
      body['artifact_specs'] = request.artifactSources.map(src => ({
        type: src.type,
        url: src.url,
        role: src.role,
      }));
    }

    return this.http.patch<unknown>(`${this.baseUrl}/${id}`, body).pipe(
      map(data => BuildSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /**
   * Permanently deletes a build record.
   */
  delete(id: string): Observable<void> {
    return this.http.delete<void>(`${this.baseUrl}/${id}`);
  }
}
