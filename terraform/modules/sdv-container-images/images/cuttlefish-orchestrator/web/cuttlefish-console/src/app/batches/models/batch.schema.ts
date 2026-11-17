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
import {ResolutionSchema} from '../../shared/models/resolution.schema';
import {safeParseList} from '../../shared/utils/schema-utils';
import type {
  Batch,
  BatchAdmission,
  BatchFillPolicy,
  BatchMember,
  BatchPlacement,
  BatchStatus,
} from './batch.model';

/**
 * The admission block POST /v2/batches returns for a create and for a
 * validate_only dry run. Declared once because both responses carry it, and
 * because omitting any of these from the entity schema would take them off the
 * drift check along with the console's guarantee that they still exist.
 */
const admissionFields = {
  requested: z.number().optional(),
  admitted: z.number().optional(),
  new_hosts: z.number().optional(),
  resolution: ResolutionSchema.optional(),
  warnings: z.array(z.string()).optional(),
};

function toAdmission(data: z.infer<z.ZodObject<typeof admissionFields>>): BatchAdmission {
  return {
    requested: data.requested ?? 0,
    admitted: data.admitted ?? 0,
    newHosts: data.new_hosts ?? 0,
    resolution: data.resolution,
    warnings: data.warnings ?? [],
  };
}

/**
 * Validates the validate_only response. A plan describes a batch that does not
 * exist, so it carries no batch_id and BatchSchema cannot parse it. This is
 * the same split the device create plan makes.
 */
export const BatchPlanSchema = z.object(admissionFields).transform(toAdmission);

/** Validates one batch summary and transforms snake_case to camelCase. */
export const BatchSchema = z
  .object({
    ...admissionFields,
    batch_id: z.string(),
    name_prefix: z.string().optional(),
    status: z.string().optional(),
    total: z.number().optional(),
    running: z.number().optional(),
    errored: z.number().optional(),
    reconciling: z.boolean().optional(),
    fill_policy: z.string().optional(),
    placement: z.string().optional(),
    ready_timeout_seconds: z.number().optional(),
    created_by: z.string().optional(),
    created_at: z.string().optional(),
    ready_at: z.string().optional(),
    failed_at: z.string().optional(),
    failure_reason: z.string().optional(),
  })
  .transform(
    (data): Batch => ({
      id: data.batch_id,
      namePrefix: data.name_prefix,
      // Safe assertion: server-derived enum; unknown values render as-is
      status: (data.status || 'provisioning') as BatchStatus,
      total: data.total ?? 0,
      running: data.running ?? 0,
      errored: data.errored ?? 0,
      reconciling: data.reconciling ?? false,
      fillPolicy: data.fill_policy as BatchFillPolicy | undefined,
      placement: data.placement as BatchPlacement | undefined,
      readyTimeoutSeconds: data.ready_timeout_seconds,
      createdBy: data.created_by,
      createdAt: data.created_at ? new Date(data.created_at) : undefined,
      readyAt: data.ready_at ? new Date(data.ready_at) : undefined,
      failedAt: data.failed_at ? new Date(data.failed_at) : undefined,
      failureReason: data.failure_reason,
      // Only a create response carries the admission block; `requested` is the
      // spec's own count, so its presence is what distinguishes one.
      admission: data.requested === undefined ? undefined : toAdmission(data),
    })
  );

/** Validates GET /v2/batches. */
export const BatchListResponseSchema = z
  .object({
    items: z.array(z.unknown()).nullable().default([]),
    total: z.number().optional().default(0),
  })
  .transform(data => ({
    items: safeParseList(data.items ?? [], BatchSchema, 'batch'),
    total: data.total,
  }));

/** Validates one member row of GET /v2/batches/{id}/devices. */
export const BatchMemberSchema = z
  .object({
    device_id: z.string(),
    name: z.string(),
    status: z.string(),
    host_id: z.string(),
    adb_serial: z.string().optional(),
    webrtc_device_id: z.string().optional(),
  })
  .transform(
    (data): BatchMember => ({
      deviceId: data.device_id,
      name: data.name,
      status: data.status,
      hostId: data.host_id,
      adbSerial: data.adb_serial,
      webrtcDeviceId: data.webrtc_device_id,
    })
  );

/** Validates GET /v2/batches/{id}/devices. */
export const BatchMembersResponseSchema = z
  .object({
    devices: z.array(z.unknown()).nullable().default([]),
  })
  .transform(data => safeParseList(data.devices ?? [], BatchMemberSchema, 'batch member'));
