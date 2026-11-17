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
import {safeParseList} from '../../shared/utils/schema-utils';
import type {ArtifactSource, Build, BuildSource} from './build.model';

/**
 * Zod schema for ArtifactSource in API responses.
 *
 * VALIDATION: Validates artifact source type (gcs/https) and URL format.
 * Used for URL-based builds that specify GCS or HTTPS artifact locations.
 */
export const ArtifactSourceSchema = z
  .object({
    type: z.enum(['gcs', 'https']),
    url: z.string().min(1),
    role: z
      .enum([
        'host_package',
        'default_build',
        'kernel_build',
        'boot_build',
        'bootloader_build',
        'system_image_build',
      ])
      .catch('default_build'),
  })
  .transform(
    (data): ArtifactSource => ({
      type: data.type,
      url: data.url,
      role: data.role,
    })
  );

/**
 * Zod schema for Build API response.
 *
 * VALIDATION: Performs runtime validation of the build artifact response.
 * TRANSFORMATION: Converts backend snake_case fields to frontend camelCase
 * to ensure consistency with TypeScript naming conventions.
 */
export const BuildSchema = z
  .object({
    id: z.string(),
    name: z.string(),
    source: z.string(),
    build_id: z.string().optional(),
    target: z.string().optional(),
    branch: z.string().optional(),
    flavor: z.string().optional(),
    artifact_specs: z.array(ArtifactSourceSchema).optional(),
    is_default: z.boolean().optional(),
    description: z.string().optional(),
    fingerprint: z.string().optional(),
    created_by: z.string().optional(),
    metadata: z.record(z.string(), z.unknown()).optional(),
    created_at: z.string().optional(),
    updated_at: z.string().optional(),
  })
  .transform((data): Build => {
    return {
      id: data.id,
      name: data.name,
      // Safe assertion: validated as string
      source: data.source as BuildSource,
      buildId: data.build_id,
      target: data.target,
      branch: data.branch,
      flavor: data.flavor,
      artifactSources: data.artifact_specs,
      isDefault: data.is_default,
      createdBy: data.created_by,
      description: data.description,
      fingerprint: data.fingerprint,
      createdAt: data.created_at ? new Date(data.created_at) : undefined,
      updatedAt: data.updated_at ? new Date(data.updated_at) : undefined,
    };
  });

/**
 * Zod schema for Build List API response.
 *
 * VALIDATION: Validates the paginated list structure and items array.
 * TRANSFORMATION: Maps each generic item through BuildSchema.
 */
export const BuildListResponseSchema = z
  .object({
    items: z.array(z.unknown()).nullable().default([]), // Backend may return null for empty list
    total: z.number().optional().default(0),
    limit: z.number(),
    offset: z.number(),
    // Empty on the last page; collectPages follows it.
    next_page_token: z.string().optional().default(''),
  })
  .transform(({next_page_token: nextPageToken, ...data}) => ({
    ...data,
    nextPageToken,
    items: safeParseList(data.items ?? [], BuildSchema, 'build'),
  }));
