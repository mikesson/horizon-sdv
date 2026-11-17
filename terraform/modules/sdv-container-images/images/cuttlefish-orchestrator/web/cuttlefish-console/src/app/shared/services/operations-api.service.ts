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
import type {Operation} from '../../devices/models/operation.model';
import {OperationListSchema} from '../../devices/models/operation.schema';
import {createApiErrorHandler, resolveBasePath} from '../utils';

/**
 * The fleet-wide view of `/v2/operations`. A device's own history stays on
 * DeviceApiService. That listing is scoped, paged by the detail page, and
 * polled with the device. This one is the Control Plane's cross-resource
 * read and has nothing to do with a single device.
 */
@Service()
export class OperationsApiService {
  private readonly http = inject(HttpClient);
  private readonly handleError = createApiErrorHandler('Operation', 'OperationsApiService');

  /**
   * In-flight operations across every resource, newest first.
   *
   * `done=false` is served off the null-filtered open index, so the cost is
   * the size of the live set rather than the table's 90-day retention.
   */
  listOpen(limit = 25): Observable<Operation[]> {
    const params = new HttpParams().set('done', 'false').set('limit', limit);
    return this.http.get<unknown>(resolveBasePath('/v2/operations'), {params}).pipe(
      map(data => OperationListSchema.parse(data)),
      catchError(this.handleError)
    );
  }
}
