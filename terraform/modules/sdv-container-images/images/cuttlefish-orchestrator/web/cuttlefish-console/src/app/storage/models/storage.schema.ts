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
import type {
  AccessCheckResult,
  Bucket,
  ListObjectsResponse,
  StorageObject,
  VerifyAccessResponse,
} from './storage.model';

/**
 * Zod schema for a GCS bucket from the storage API.
 *
 * TRANSFORMATION: Converts backend snake_case fields to frontend camelCase.
 */
export const BucketSchema = z
  .object({
    name: z.string(),
    location: z.string().optional(),
    created_at: z.string().optional(),
  })
  .transform(
    (data): Bucket => ({
      name: data.name,
      location: data.location,
      createdAt: data.created_at ? new Date(data.created_at) : undefined,
    })
  );

/**
 * Zod schema for the bucket list response; unwraps to the bucket array.
 */
export const ListBucketsResponseSchema = z
  .object({
    buckets: z.array(BucketSchema).nullable().default([]),
  })
  .transform((data): Bucket[] => data.buckets ?? []);

/**
 * Zod schema for a GCS object (file or synthesized folder prefix).
 *
 * TRANSFORMATION: Converts backend snake_case fields to frontend camelCase;
 * guarantees the model's required displayName/gsUri via fallbacks.
 */
export const StorageObjectSchema = z
  .object({
    name: z.string(),
    display_name: z.string().optional(),
    bucket: z.string(),
    size: z.number().optional(),
    content_type: z.string().optional(),
    is_folder: z.boolean().optional(),
    updated_at: z.string().optional(),
    gs_uri: z.string().optional(),
    public_url: z.string().optional(),
    generation: z.number().optional(),
  })
  .transform(
    (data): StorageObject => ({
      name: data.name,
      displayName: data.display_name || data.name,
      bucket: data.bucket,
      size: data.size,
      contentType: data.content_type,
      isFolder: Boolean(data.is_folder),
      updatedAt: data.updated_at ? new Date(data.updated_at) : undefined,
      gsUri: data.gs_uri || `gs://${data.bucket}/${data.name}`,
      publicUrl: data.public_url,
      generation: data.generation,
    })
  );

/**
 * Zod schema for the object list response.
 */
export const ListObjectsResponseSchema = z
  .object({
    objects: z.array(StorageObjectSchema).nullable().default([]),
    prefixes: z.array(z.string()).nullable().optional(),
    next_page_token: z.string().optional(),
  })
  .transform(
    (data): ListObjectsResponse => ({
      objects: data.objects ?? [],
      prefixes: data.prefixes ?? undefined,
      nextPageToken: data.next_page_token,
    })
  );

/**
 * Zod schema for a single service-account access check.
 */
export const AccessCheckResultSchema = z
  .object({
    service_account: z.string().optional().default(''),
    has_access: z.boolean().optional().default(false),
    permissions: z.array(z.string()).nullable().optional(),
    missing_perms: z.array(z.string()).nullable().optional(),
    error: z.string().optional(),
    checked_at: z.string().optional().default(''),
  })
  .transform(
    (data): AccessCheckResult => ({
      serviceAccount: data.service_account,
      hasAccess: data.has_access,
      permissions: data.permissions ?? undefined,
      missingPerms: data.missing_perms ?? undefined,
      error: data.error,
      checkedAt: data.checked_at,
    })
  );

/**
 * Zod schema for the verify-access response.
 */
export const VerifyAccessResponseSchema = z
  .object({
    gs_uri: z.string().optional().default(''),
    orchestrator_access: AccessCheckResultSchema,
    host_access: AccessCheckResultSchema.optional(),
    can_proceed: z.boolean().optional().default(false),
    message: z.string().optional(),
  })
  .transform(
    (data): VerifyAccessResponse => ({
      gsUri: data.gs_uri,
      orchestratorAccess: data.orchestrator_access,
      hostAccess: data.host_access,
      canProceed: data.can_proceed,
      message: data.message,
    })
  );
