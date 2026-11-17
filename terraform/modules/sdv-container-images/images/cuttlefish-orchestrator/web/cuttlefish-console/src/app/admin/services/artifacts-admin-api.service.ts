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

import {HttpClient} from '@angular/common/http';
import {inject, Service} from '@angular/core';
import type {Observable} from 'rxjs';
import {catchError, map} from 'rxjs/operators';
import {createApiErrorHandler, resolveBasePath} from '../../shared/utils';
import type {ArtifactBuckets} from '../models/artifact-buckets.model';
import {ArtifactBucketsSchema} from '../models/artifact-buckets.model';

/**
 * Read side of the artifact bucket settings, for the pages that need to know
 * where a capture will land. Writing them belongs to the Settings page, which
 * has the baseline needed to send only what changed.
 */
@Service()
export class ArtifactsAdminApiService {
  private readonly http = inject(HttpClient);
  private readonly baseUrl = resolveBasePath('/v2/admin/system-config/artifact-buckets');
  private readonly handleError = createApiErrorHandler(
    'ArtifactBuckets',
    'ArtifactsAdminApiService'
  );

  get(): Observable<ArtifactBuckets> {
    return this.http.get<unknown>(this.baseUrl).pipe(
      map(data => ArtifactBucketsSchema.parse(data)),
      catchError(this.handleError)
    );
  }
}
