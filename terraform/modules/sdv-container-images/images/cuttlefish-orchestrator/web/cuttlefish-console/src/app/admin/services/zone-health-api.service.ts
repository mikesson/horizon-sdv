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

/**
 * The placement plane's reactive memory. Cells are read through the
 * control-plane snapshot; this service only clears them.
 */
@Service()
export class ZoneHealthApiService {
  private readonly http = inject(HttpClient);
  private readonly handleError = createApiErrorHandler('Cooldown', 'ZoneHealthApiService');

  /**
   * Drops a (zone, machine family) cell: the cooldown ends and the
   * consecutive-failure count that decays the zone's ranking score goes with
   * it. Idempotent, so a cell a concurrent provision already cleared still
   * succeeds.
   */
  clearCooldown(zone: string, machineFamily: string): Observable<void> {
    const path = resolveBasePath(
      `/v2/admin/zone-health/${encodeURIComponent(zone)}/${encodeURIComponent(machineFamily)}`
    );
    return this.http.delete<unknown>(path).pipe(
      map(() => undefined),
      catchError(this.handleError)
    );
  }
}
