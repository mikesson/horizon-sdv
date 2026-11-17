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

import {httpResource} from '@angular/common/http';
import {computed, Service} from '@angular/core';
import {resolveBasePath} from '../../shared/utils';
import type {PlatformStats} from '../models/platform-stats.model';
import {PlatformStatsSchema} from '../models/platform-stats.schema';

/**
 * Server-truth platform statistics (hosts/devices by status + quota ceiling),
 * shared by the dashboard and the Capacity page.
 */
@Service()
export class PlatformStatsService {
  private readonly resource = httpResource<PlatformStats>(
    () => resolveBasePath('/v2/admin/stats'),
    {
      parse: raw => PlatformStatsSchema.parse(raw),
    }
  );

  /** Latest snapshot; null until first load (or on load failure). */
  readonly stats = computed(() => (this.resource.hasValue() ? this.resource.value() : null));

  readonly loading = this.resource.isLoading;

  readonly error = computed(() =>
    this.resource.error() ? 'Failed to load platform statistics' : null
  );

  refresh(): void {
    this.resource.reload();
  }
}
