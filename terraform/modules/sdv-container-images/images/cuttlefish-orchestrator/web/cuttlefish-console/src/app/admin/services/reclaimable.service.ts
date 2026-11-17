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
import type {Reclaimable} from '../models/operator-lens.model';
import {ReclaimableSchema} from '../models/operator-lens.schema';

/** Reclaimable-capacity buckets for the Capacity page. */
@Service()
export class ReclaimableService {
  private readonly resource = httpResource<Reclaimable>(
    () => resolveBasePath('/v2/admin/reclaimable'),
    {
      parse: raw => ReclaimableSchema.parse(raw),
    }
  );

  readonly reclaimable = computed(() => (this.resource.hasValue() ? this.resource.value() : null));

  readonly loading = this.resource.isLoading;

  readonly error = computed(() => {
    const err = this.resource.error();
    return err ? getErrorMessage(err, 'Failed to load the reclaimable worklist') : null;
  });

  refresh(): void {
    this.resource.reload();
  }
}
