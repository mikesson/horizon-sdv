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
import {
  HOST_IMAGE_REASONS,
  type HostImageConfig,
  type HostImageConfigListResponse,
  type HostImageStatus,
} from './host-image-config.model';

/** The resolved block every host image config read carries. */
export const HostImageStatusSchema = z
  .object({
    ok: z.boolean(),
    project: z.string(),
    image_name: z.string().optional(),
    self_link: z.string().optional(),
    architecture: z.string().optional(),
    reason: z.enum(HOST_IMAGE_REASONS).optional(),
    message: z.string().optional(),
  })
  .transform(
    (data): HostImageStatus => ({
      ok: data.ok,
      project: data.project,
      imageName: data.image_name,
      selfLink: data.self_link,
      architecture: data.architecture,
      reason: data.reason,
      message: data.message,
    })
  );

/**
 * Zod schema for a single host image configuration.
 * Transforms snake_case (backend) to camelCase (frontend).
 */
export const HostImageConfigSchema = z
  .object({
    machine_family: z.string(),
    image_family: z.string(),
    source_project: z.string().nullable().optional(),
    description: z.string().nullable().optional(),
    enabled: z.boolean(),
    created_at: z.string(),
    updated_at: z.string(),
    created_by: z.string().nullable().optional(),
    updated_by: z.string().nullable().optional(),
    resolved: HostImageStatusSchema.optional(),
  })
  .transform(
    (data): HostImageConfig => ({
      id: data.machine_family, // Use machineFamily as the entity ID
      machineFamily: data.machine_family,
      imageFamily: data.image_family,
      sourceProject: data.source_project ?? undefined,
      description: data.description ?? undefined,
      enabled: data.enabled,
      createdAt: new Date(data.created_at),
      updatedAt: new Date(data.updated_at),
      createdBy: data.created_by ?? undefined,
      updatedBy: data.updated_by ?? undefined,
      resolved: data.resolved,
    })
  );

/**
 * Zod schema for list host image configs response.
 */
export const HostImageConfigListResponseSchema = z
  .object({
    items: z.array(HostImageConfigSchema).nullable(),
    limit: z.number(),
    offset: z.number(),
    total: z.number(),
  })
  .transform(
    (data): HostImageConfigListResponse => ({
      items: data.items ?? [],
      limit: data.limit,
      offset: data.offset,
      total: data.total,
    })
  );
