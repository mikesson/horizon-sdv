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
import {getErrorMessage, resolveBasePath} from '../../shared/utils';
import type {CapacitySnapshot} from '../models/capacity.model';
import {CapacitySnapshotSchema} from '../models/capacity.schema';

/**
 * The capacity snapshot behind the Capacity page: the placement space, the
 * quota headroom under it, and the fleet's Spot capacity, in one server-composed
 * read.
 *
 * Not polled. Every input is a TTL'd or event-written row, so two reads between
 * syncs render identically; an operator refreshes when they act, and the page
 * refetches itself after a write it made.
 */
@Service()
export class CapacityService {
  private readonly resource = httpResource<CapacitySnapshot>(
    () => resolveBasePath('/v2/admin/capacity'),
    {
      parse: raw => CapacitySnapshotSchema.parse(raw),
    }
  );

  readonly snapshot = computed(() => (this.resource.hasValue() ? this.resource.value() : null));

  readonly loading = this.resource.isLoading;

  readonly error = computed(() => {
    const err = this.resource.error();
    return err ? getErrorMessage(err, 'Failed to load the capacity snapshot') : null;
  });

  refresh(): void {
    this.resource.reload();
  }
}
