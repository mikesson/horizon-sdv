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
import {computed, inject, Service} from '@angular/core';
import {toSignal} from '@angular/core/rxjs-interop';
import {of} from 'rxjs';
import {catchError, map} from 'rxjs/operators';
import {z} from 'zod';
import type {PoolFacts} from '../models/pool-placement';
import {POOL_NAMES} from '../models/pool-placement';
import {resolveBasePath} from '../utils';

/**
 * A thin pick of the autoscaler snapshot rather than the admin page's schema.
 * The wizards read two fields, and a create must not start failing because a
 * field the fleet page draws changed shape.
 *
 * The enabled pools are the policy's tier keys, never the snapshot's `pools`
 * list. That list is the union of the pools an operator configured and the
 * pools the fleet still holds machines in, so a pool switched off while its
 * hosts run their leases out is still in it. The server's derivation reads the
 * configured set alone, and this has to read what it reads.
 */
const PoolFactsSchema = z
  .object({
    fleet: z.object({
      policy: z.object({tiers: z.record(z.string(), z.unknown()).nullable()}),
    }),
    arm_image_ready: z.boolean(),
  })
  .transform(
    (d): PoolFacts => ({
      pools: Object.keys(d.fleet.policy.tiers ?? {}),
      armImageReady: d.arm_image_ready,
    })
  );

const EVERYTHING_OFFERED: PoolFacts = {pools: [...POOL_NAMES], armImageReady: true};

/**
 * Two facts off the autoscaler snapshot: which pools this deployment offers,
 * and whether an arm64 host can be built here. Fetched once per app session and
 * shared by both create wizards, which say where a create will land before it
 * is sent.
 *
 * A failed fetch or parse fails OPEN (every pool offered, arm64 allowed), so a
 * broken admin read can never block a create. Whether a deployment serves a
 * pool is the server's refusal to make, and it makes it at plan time.
 */
@Service()
export class PoolFactsService {
  private readonly http = inject(HttpClient);

  private readonly facts = toSignal(
    this.http.get<unknown>(resolveBasePath('/v2/admin/autoscaler')).pipe(
      map((raw): PoolFacts => {
        const parsed = PoolFactsSchema.safeParse(raw);
        return parsed.success ? parsed.data : EVERYTHING_OFFERED;
      }),
      catchError(() => of(EVERYTHING_OFFERED))
    ),
    {initialValue: EVERYTHING_OFFERED}
  );

  readonly pools = computed(() => this.facts().pools);
  readonly armImageReady = computed(() => this.facts().armImageReady);
}
