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

import {z} from 'zod';
import type {Quota, QuotaScope} from './quota.model';

/**
 * Zod schema for Quota API response.
 *
 * VALIDATION: Runtime check of quota fields from backend.
 * TRANSFORMATION: Maps snake_case JSON (e.g. `max_hosts`) to camelCase model (`maxHosts`).
 */
export const QuotaResponseSchema = z
  .object({
    scope: z.string(),
    scope_id: z.string(),
    max_hosts: z.number(),
    used_hosts: z.number(),
    available: z.number(),
    percentage: z.number(),
    high_water_used_hosts: z.number().optional(),
    high_water_at: z.string().optional(),
  })
  .transform(
    (data): Quota => ({
      scope: data.scope as QuotaScope,
      scopeId: data.scope_id,
      maxHosts: data.max_hosts,
      usedHosts: data.used_hosts,
      available: data.available,
      percentage: data.percentage,
      highWaterUsedHosts: data.high_water_used_hosts ?? 0,
      highWaterAt: data.high_water_at ? new Date(data.high_water_at) : null,
    })
  );
