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
import type {AdminZone, AdminZoneList, CatalogFreshness} from './catalog-admin.model';

export const AdminZoneSchema = z
  .object({
    zone: z.string(),
    region: z.string(),
    status: z.string(),
    is_enabled: z.boolean(),
    offered_shapes: z.array(z.string()).nullable(),
    shapes_reported: z.boolean(),
  })
  .transform(
    (data): AdminZone => ({
      zone: data.zone,
      region: data.region,
      status: data.status,
      isEnabled: data.is_enabled,
      offeredShapes: data.offered_shapes ?? [],
      shapesReported: data.shapes_reported,
    })
  );

export const AdminZoneListSchema = z
  .object({
    zones: z.array(AdminZoneSchema).nullable(),
    curated_shapes: z.array(z.string()).nullable(),
  })
  .transform(
    (data): AdminZoneList => ({
      zones: data.zones ?? [],
      curatedShapes: data.curated_shapes ?? [],
    })
  );

export const EnabledZonesSchema = z
  .object({
    zones: z.array(z.string()).nullable(),
  })
  .transform((data): string[] => data.zones ?? []);

export const CatalogFreshnessSchema = z
  .object({
    built_at: z.string().optional(),
    partial: z.array(z.string()).nullable(),
  })
  .transform(
    (data): CatalogFreshness => ({
      builtAt: data.built_at ? new Date(data.built_at) : null,
      partial: data.partial ?? [],
    })
  );
