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
import {safeParseList} from '../utils/schema-utils';

/**
 * Zone availability information from the GCE catalog.
 */
export interface ZoneAvailability {
  zone: string;
  region: string;
  status: 'available' | 'unavailable' | 'degraded';
  isEnabled: boolean;
  machineTypeAvailable?: boolean;
  gpuAvailable?: boolean;
  gpuMaxCount?: number;
}

/**
 * Zod schema for zone availability API response.
 */
export const ZoneAvailabilitySchema = z
  .object({
    zone: z.string(),
    region: z.string(),
    status: z.string(),
    is_enabled: z.boolean().optional().default(true),
    isEnabled: z.boolean().optional(),
    machine_type_available: z.boolean().optional(),
    machineTypeAvailable: z.boolean().optional(),
    gpu_available: z.boolean().optional(),
    gpuAvailable: z.boolean().optional(),
    gpu_max_count: z.number().optional(),
    gpuMaxCount: z.number().optional(),
  })
  .transform(
    (data): ZoneAvailability => ({
      zone: data.zone,
      region: data.region,
      status: (data.status as ZoneAvailability['status']) || 'available',
      isEnabled: data.is_enabled ?? data.isEnabled ?? true,
      machineTypeAvailable: data.machine_type_available ?? data.machineTypeAvailable,
      gpuAvailable: data.gpu_available ?? data.gpuAvailable,
      gpuMaxCount: data.gpu_max_count ?? data.gpuMaxCount,
    })
  );

/**
 * Zod schema for the zone availability list envelope.
 */
export const ZoneAvailabilityListSchema = z
  .object({
    zones: z.array(z.unknown()).nullable().default([]),
  })
  .transform(data => ({
    zones: safeParseList(data.zones ?? [], ZoneAvailabilitySchema, 'zone'),
  }));
