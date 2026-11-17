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

import {HttpClient, httpResource} from '@angular/common/http';
import {computed, effect, inject, Service} from '@angular/core';
import type {Observable} from 'rxjs';
import {catchError, map} from 'rxjs/operators';
import {LoggingService} from '../../shared/services';
import {resolveBasePath} from '../../shared/utils';
import type {Quota, UpdateQuotaRequest} from '../models/quota.model';
import {QuotaResponseSchema} from '../models/quota.schema';

/**
 * Service for managing host quotas and usage tracking.
 *
 * ARCHITECTURE:
 * - The global quota is an httpResource; `refresh()` reloads it.
 * - Bridges the API layer with Zod validation.
 */
@Service()
export class QuotaService {
  private readonly http = inject(HttpClient);
  private readonly logger = inject(LoggingService);
  private readonly baseUrl = resolveBasePath('/v2/quota');

  private readonly quotaResource = httpResource<Quota>(() => this.baseUrl, {
    parse: raw => QuotaResponseSchema.parse(raw),
  });

  /** The global quota; null until first load (or on load failure). */
  readonly currentQuota = computed(() =>
    this.quotaResource.hasValue() ? this.quotaResource.value() : null
  );

  readonly loading = this.quotaResource.isLoading;

  readonly error = computed(() =>
    this.quotaResource.error() ? 'Failed to load quota information' : null
  );

  constructor() {
    effect(() => {
      const err = this.quotaResource.error();
      if (err) {
        this.logger.error('Quota', 'Failed to load current quota', err);
      }
    });
  }

  /** Derived signal: Usage percentage (0-100). */
  readonly usagePercentage = computed(() => {
    const quota = this.currentQuota();
    return quota ? quota.percentage : 0;
  });

  /** Derived signal: True if usage >= 75%. */
  readonly isNearLimit = computed(() => this.usagePercentage() >= 75);

  /** Derived signal: True if usage >= 100% (quota exhausted). */
  readonly isExhausted = computed(() => this.usagePercentage() >= 100);

  /**
   * Updates the global platform quota limit.
   *
   * TRANSFORMATION: Maps `maxHosts` (camelCase) to `max_hosts` (snake_case).
   *
   * @param maxHosts - The new maximum number of allowed hosts across the platform.
   */
  updatePlatformQuota(maxHosts: number): Observable<Quota> {
    const _request: UpdateQuotaRequest = {maxHosts};
    return this.http.put<unknown>(`${this.baseUrl}/platform`, {max_hosts: maxHosts}).pipe(
      map(response => QuotaResponseSchema.parse(response)),
      catchError(err => {
        this.logger.error('Quota', 'Failed to update platform quota', err);
        throw err;
      })
    );
  }

  /**
   * Checks if there is sufficient capacity to provision new hosts.
   *
   * @param amount - Number of hosts requested (default: 1).
   * @returns True if `available >= amount` (or if quota not loaded yet).
   */
  canProvisionHosts(amount: number = 1): boolean {
    const quota = this.currentQuota();
    if (!quota) {
      // Fail open if quota data isn't loaded yet
      return true;
    }
    return quota.available >= amount;
  }

  /**
   * Re-fetches the global quota from the backend.
   */
  refresh(): void {
    this.quotaResource.reload();
  }
}
