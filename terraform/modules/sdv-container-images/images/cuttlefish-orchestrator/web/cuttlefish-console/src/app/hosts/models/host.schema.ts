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
import {toHostErrorType} from '../../shared/constants';
import {parseDate, safeParseList} from '../../shared/utils/schema-utils';
import type {
  Host,
  HostClass,
  HostError,
  HostLifecycle,
  HostOrigin,
  ProvisioningModel,
} from './host.model';

/**
 * Zod schema for HostLifecycle.
 *
 * VALIDATION: Validates lifecycle fields from the API.
 * DEFAULTS: Sets default TTL (36000s) and 'ephemeral' mode if missing.
 *
 * Host modes are 'ephemeral' or 'protected' (different from device modes).
 */
export const HOST_LIFECYCLE_SCHEMA = z
  .object({
    // Mode can be empty string from DB, so use transform to default
    mode: z.string().optional().default('ephemeral'),
    ttl_seconds: z.number().optional(),
    ttlSeconds: z.number().optional(),
    expires_at: z.string().nullable().optional(),
    expiresAt: z.string().nullable().optional(),
    protected_device_count: z.number().optional(),
    protectedDeviceCount: z.number().optional(),
  })
  .transform((data): HostLifecycle => {
    const ttlSeconds = data.ttl_seconds || data.ttlSeconds || 36000;
    const expiresAtStr = data.expires_at || data.expiresAt;
    // Default empty mode to ephemeral
    const mode = data.mode && data.mode !== '' ? data.mode : 'ephemeral';

    return {
      mode: mode as 'ephemeral' | 'protected',
      ttlSeconds,
      expiresAt: expiresAtStr ? new Date(expiresAtStr) : undefined,
      protectedDeviceCount: data.protected_device_count || data.protectedDeviceCount || 0,
    };
  });

/**
 * Zod schema for Host API response.
 *
 * VALIDATION: Runtime validation of backend response.
 * TRANSFORMATION: Converts backend snake_case fields to frontend camelCase.
 */
export const HostSchema = z
  .object({
    id: z.string(),
    name: z.string(),
    zone: z.string(),
    // Placement intent: requested_zone is the pinned zone or location hint
    // (absent = auto); zone above is the physical binding placement chose.
    requested_zone: z.string().optional(),
    zone_pinned: z.boolean().optional(),
    gce_instance_name: z.string().optional(),
    gceInstanceName: z.string().optional(),
    gce_project: z.string().optional(),
    gceProject: z.string().optional(),
    machine_type: z.string().optional(),
    machineType: z.string().optional(),
    status: z.string(),
    // AIP-128 output-only: actively converging.
    reconciling: z.boolean().optional(),
    gpu_type: z.string().optional(),
    gpuType: z.string().optional(),
    gpu_count: z.number().optional(),
    gpuCount: z.number().optional(),
    min_cpu_platform: z.string().optional(),
    boot_disk_size_gb: z.number().optional(),
    boot_image: z.string().optional(),
    provisioning_model: z.string().optional(),
    provisioningModel: z.string().optional(),
    max_devices: z.number().optional(),
    maxDevices: z.number().optional(),
    exclusive: z.boolean().optional(),
    foreign_cvds: z.boolean().optional(),
    batch_id: z.string().optional(),
    // Provenance and the class derived from it. Both absent on a host created
    // before the column existed, which the backend reads as 'reserved'.
    origin: z.string().optional(),
    class: z.string().optional(),
    current_device_count: z.number().optional(),
    host_orchestrator_port: z.number().optional(),
    hostOrchestratorPort: z.number().optional(),
    service_account: z.string().optional(),
    serviceAccount: z.string().optional(),
    ip_address: z.string().optional(),
    internal_ip: z.string().optional(),
    internalIP: z.string().optional(),
    created_by: z.string().optional(),
    createdBy: z.string().optional(),
    error_message: z.string().optional(),
    errorMessage: z.string().optional(),
    error_type: z.string().optional(),
    errorType: z.string().optional(),
    error_code: z.string().optional(),
    errorCode: z.string().optional(),
    error_suggestion: z.string().optional(),
    errorSuggestion: z.string().optional(),
    error_alternative_zones: z.array(z.string()).optional(),
    errorAlternativeZones: z.array(z.string()).optional(),
    error_retryable: z.boolean().optional(),
    errorRetryable: z.boolean().optional(),
    error_occurred_at: z.string().optional(),
    errorOccurredAt: z.string().optional(),
    stopped_at: z.string().optional(),
    stoppedAt: z.string().optional(),
    preempted_at: z.string().optional(),
    preemptedAt: z.string().optional(),
    preemption_count: z.number().optional(),
    last_preemption_id: z.string().optional(),
    preemptionCount: z.number().optional(),
    preempted_incarnation: z.number().optional(),
    // Spec/observation split, incarnation, and the poison/stall conditions.
    desired_state: z.string().optional(),
    vm_status: z.string().optional(),
    vm_observed_at: z.string().optional(),
    vm_terminates_at: z.string().optional(),
    incarnation: z.number().optional(),
    stalled_reason: z.string().optional(),
    ho_ready_at: z.string().optional(),
    consecutive_create_fails: z.number().optional(),
    last_create_fail_reason: z.string().optional(),
    degraded_at: z.string().optional(),
    degraded_reason: z.string().optional(),
    provision_blocked_at: z.string().optional(),
    provision_blocked_reason: z.string().optional(),
    created_at: z.string().optional(),
    createdAt: z.string().optional(),
    updated_at: z.string().optional(),
    updatedAt: z.string().optional(),
    lifecycle: z.record(z.string(), z.unknown()).optional(),
  })
  .transform((data): Host => {
    return {
      id: data.id,
      name: data.name,
      zone: data.zone,
      requestedZone: data.requested_zone,
      zonePinned: data.zone_pinned ?? false,
      gceInstanceName: data.gce_instance_name || data.gceInstanceName,
      machineType: data.machine_type || data.machineType || '',
      // Safe assertion: validated as string, matches union
      status: data.status as Host['status'],
      reconciling: data.reconciling,
      gpuType: data.gpu_type || data.gpuType || '',
      gpuCount: data.gpu_count || data.gpuCount || 0,
      minCpuPlatform: data.min_cpu_platform,
      bootDiskSizeGb: data.boot_disk_size_gb,
      bootImage: data.boot_image,
      provisioningModel: (data.provisioning_model ||
        data.provisioningModel ||
        'STANDARD') as ProvisioningModel,
      maxDevices: data.max_devices || data.maxDevices || 0,
      exclusive: data.exclusive ?? false,
      foreignCvds: data.foreign_cvds ?? false,
      batchId: data.batch_id,
      // Safe assertions: validated as strings, and both vocabularies are
      // published in the OpenAPI document the drift gate checks these against.
      origin: data.origin ? (data.origin as HostOrigin) : undefined,
      hostClass: data.class ? (data.class as HostClass) : undefined,
      currentDevices: data.current_device_count || 0,
      externalIP: data.ip_address,
      internalIP: data.internal_ip || data.internalIP,
      createdBy: data.created_by || data.createdBy || '',
      createdAt: new Date(data.created_at || data.createdAt || Date.now()),
      updatedAt: new Date(data.updated_at || data.updatedAt || Date.now()),
      lifecycle: data.lifecycle ? HOST_LIFECYCLE_SCHEMA.parse(data.lifecycle) : undefined,
      error: buildHostError(data),
      preemptedAt: parseDate(data.preempted_at || data.preemptedAt),
      preemptionCount: data.preemption_count || data.preemptionCount || 0,
      lastPreemptionId: data.last_preemption_id,
      preemptedIncarnation: data.preempted_incarnation,
      desiredState: data.desired_state,
      vmStatus: data.vm_status,
      vmObservedAt: data.vm_observed_at ? new Date(data.vm_observed_at) : undefined,
      vmTerminatesAt: data.vm_terminates_at ? new Date(data.vm_terminates_at) : undefined,
      incarnation: data.incarnation,
      stalledReason: data.stalled_reason,
      hoReadyAt: data.ho_ready_at ? new Date(data.ho_ready_at) : undefined,
      consecutiveCreateFails: data.consecutive_create_fails,
      lastCreateFailReason: data.last_create_fail_reason,
      degradedAt: data.degraded_at ? new Date(data.degraded_at) : undefined,
      degradedReason: data.degraded_reason,
      provisionBlockedAt: data.provision_blocked_at
        ? new Date(data.provision_blocked_at)
        : undefined,
      provisionBlockedReason: data.provision_blocked_reason,
    };
  });

/**
 * Build a HostError object from API response data.
 * Returns undefined if no error information is present.
 */
function buildHostError(data: {
  error_message?: string;
  errorMessage?: string;
  error_type?: string;
  errorType?: string;
  error_code?: string;
  errorCode?: string;
  error_suggestion?: string;
  errorSuggestion?: string;
  error_alternative_zones?: string[];
  errorAlternativeZones?: string[];
  error_retryable?: boolean;
  errorRetryable?: boolean;
  error_occurred_at?: string;
  errorOccurredAt?: string;
}): HostError | undefined {
  const message = data.error_message || data.errorMessage;
  const type = data.error_type || data.errorType;

  // Only create error object if there's an error message or type
  if (!(message || type)) {
    return undefined;
  }

  const occurredAtStr = data.error_occurred_at || data.errorOccurredAt;

  return {
    message: message || 'Unknown error',
    type: toHostErrorType(type),
    code: data.error_code || data.errorCode,
    suggestion: data.error_suggestion || data.errorSuggestion,
    alternativeZones: data.error_alternative_zones ?? data.errorAlternativeZones,
    retryable: data.error_retryable ?? data.errorRetryable ?? false,
    occurredAt: occurredAtStr ? new Date(occurredAtStr) : undefined,
  };
}

/**
 * Zod schema for Host List API response.
 *
 * VALIDATION: Validates pagination structure and item array.
 * TRANSFORMATION: Parses each item using HostSchema.
 */
export const HostListResponseSchema = z
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
    items: safeParseList(data.items ?? [], HostSchema, 'host'),
  }));
