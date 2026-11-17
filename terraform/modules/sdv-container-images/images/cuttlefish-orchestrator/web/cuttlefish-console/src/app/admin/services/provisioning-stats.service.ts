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
import type {ProvisioningStats} from '../models/operator-lens.model';
import {ProvisioningStatsSchema} from '../models/operator-lens.schema';

/** 7-day provisioning outcomes for the Operations success-rate strip. */
@Service()
export class ProvisioningStatsService {
  private readonly resource = httpResource<ProvisioningStats>(
    () => resolveBasePath('/v2/admin/provisioning-stats'),
    {parse: raw => ProvisioningStatsSchema.parse(raw)}
  );

  readonly stats = computed(() => (this.resource.hasValue() ? this.resource.value() : null));

  readonly loading = this.resource.isLoading;

  refresh(): void {
    this.resource.reload();
  }
}
