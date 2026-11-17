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
import {map} from 'rxjs/operators';
import {resolveBasePath} from '../../shared/utils';
import type {Bucket, ListObjectsResponse, VerifyAccessResponse} from '../models/storage.model';
import {
  ListBucketsResponseSchema,
  ListObjectsResponseSchema,
  VerifyAccessResponseSchema,
} from '../models/storage.schema';

/**
 * Service for interacting with Google Cloud Storage (GCS).
 *
 * This service lists buckets, browses objects, and detects valid Cuttlefish
 * build artifacts (images/packages) within GCS paths.
 *
 * ARCHITECTURE:
 * It bridges the gap between GCS's flat file structure and the hierarchical
 * artifact structure required by the Cuttlefish orchestrator.
 */
@Service()
export class StorageApiService {
  private readonly http = inject(HttpClient);

  private readonly baseUrl = resolveBasePath('/v2/storage');

  /**
   * Lists all GCS buckets accessible to the Orchestrator's service account.
   */
  listBuckets(): Observable<Bucket[]> {
    return this.http
      .get<unknown>(`${this.baseUrl}/buckets`)
      .pipe(map(data => ListBucketsResponseSchema.parse(data)));
  }

  /**
   * Lists objects (files/folders) within a specific bucket.
   *
   * Folder grouping is the server's: the GCS adapter delimits on "/" itself
   * and the API publishes no delimiter parameter to override it.
   *
   * @param prefix - Optional directory prefix to simulate folder navigation.
   */
  listObjects(bucket: string, prefix?: string): Observable<ListObjectsResponse> {
    let params = new HttpParams();
    if (prefix) {
      params = params.set('prefix', prefix);
    }

    return this.http
      .get<unknown>(`${this.baseUrl}/buckets/${bucket}/objects`, {params})
      .pipe(map(data => ListObjectsResponseSchema.parse(data)));
  }

  /**
   * Deletes one object. The name is a full object path and may contain
   * slashes, so it is encoded whole rather than per segment.
   */
  deleteObject(bucket: string, name: string): Observable<void> {
    const path = `${this.baseUrl}/buckets/${bucket}/objects/${encodeURIComponent(name)}`;
    return this.http.delete<unknown>(path).pipe(map(() => undefined));
  }

  /**
   * Verifies if the orchestrator (and optionally a specific host) has access to a GCS path.
   *
   * Use it to debug IAM permission issues. It checks:
   * 1. Orchestrator Service Account access (for metadata reading).
   * 2. Host Instance Service Account access (for artifact fetching during provisioning).
   *
   * TRANSFORMATION: Explicitly maps camelCase request fields to snake_case for the backend.
   */
  verifyAccess(
    bucket: string,
    objectPath?: string,
    hostId?: string,
    checkWrite?: boolean
  ): Observable<VerifyAccessResponse> {
    // Backend expects snake_case field names in JSON
    const request: Record<string, string | boolean | undefined> = {
      bucket,
      object_path: objectPath,
      host_id: hostId,
      check_write: checkWrite,
    };
    return this.http
      .post<unknown>(`${this.baseUrl}/verify-access`, request)
      .pipe(map(data => VerifyAccessResponseSchema.parse(data)));
  }
}
