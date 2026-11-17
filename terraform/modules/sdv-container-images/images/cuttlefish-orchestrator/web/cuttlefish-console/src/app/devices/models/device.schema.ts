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
import type {Resolution} from '../../shared/models/requirements.model';
import {ResolutionSchema} from '../../shared/models/resolution.schema';
import {resolveBasePath} from '../../shared/utils/base-url';
import {parseDate, safeParseList} from '../../shared/utils/schema-utils';
import type {
  BootObservation,
  BugreportArtifact,
  CreateMode,
  Device,
  DeviceLifecycle,
  DeviceStatus,
  GPUMode,
  LifecycleMode,
  PinReason,
  RecordingArtifact,
} from './device.model';

/**
 * Zod schema for DeviceLifecycle.
 *
 * VALIDATION: Performs runtime validation of lifecycle fields.
 * DEFAULTS: Sets default TTL (14400s) and 'ephemeral' mode if missing.
 */
export const DEVICE_LIFECYCLE_SCHEMA = z
  .object({
    // Mode can be empty string from DB, so allow any string and default
    mode: z.string().optional().default('ephemeral'),
    ttl_seconds: z.number().optional(),
    ttlSeconds: z.number().optional(),
    expires_at: z.string().optional(),
    expiresAt: z.string().optional(),
    pinned_at: z.string().optional(),
    pinnedAt: z.string().optional(),
    pinned_by: z.string().optional(),
    pinnedBy: z.string().optional(),
    pinned_reason: z.string().optional(),
    pinnedReason: z.string().optional(),
    pinned_reason_detail: z.string().optional(),
    pinnedReasonDetail: z.string().optional(),
    pinned_until: z.string().optional(),
    pinnedUntil: z.string().optional(),
    extend_count: z.number().optional(),
  })
  .transform((data): DeviceLifecycle => {
    // Default empty mode to ephemeral
    const mode = data.mode && data.mode !== '' ? (data.mode as LifecycleMode) : 'ephemeral';
    const ttlSeconds = data.ttl_seconds || data.ttlSeconds || 14400;

    let expiresAt: Date | undefined;
    const expiresAtStr = data.expires_at || data.expiresAt;
    if (expiresAtStr) {
      expiresAt = new Date(expiresAtStr);
    } else if (mode === 'ephemeral') {
      expiresAt = new Date(Date.now() + ttlSeconds * 1000);
    }

    const pinnedAtStr = data.pinned_at || data.pinnedAt;
    const pinnedUntilStr = data.pinned_until || data.pinnedUntil;

    return {
      mode,
      ttlSeconds,
      expiresAt,
      pinnedAt: pinnedAtStr ? new Date(pinnedAtStr) : mode === 'pinned' ? new Date() : undefined,
      pinnedBy: data.pinned_by || data.pinnedBy,
      // Safe assertion: API returns valid reason string
      pinnedReason: (data.pinned_reason || data.pinnedReason) as PinReason | undefined,
      pinnedReasonDetail: data.pinned_reason_detail || data.pinnedReasonDetail,
      pinnedUntil: pinnedUntilStr ? new Date(pinnedUntilStr) : undefined,
      extendedCount: data.extend_count || 0,
    };
  });

/**
 * Zod schema for the boot-progress observation sampled from a CVD's own
 * logs. Every field is optional on the wire. The struct exists only once a
 * plane has enriched it, and zero-valued counters are omitted.
 */
export const BOOT_OBSERVATION_SCHEMA = z
  .object({
    kernel_panics: z.number().optional(),
    guest_reboots: z.number().optional(),
    uboot_starts: z.number().optional(),
    boot_completed: z.boolean().optional(),
    boot_failed: z.boolean().optional(),
    kernel_log_bytes: z.number().optional(),
    last_panic: z.string().optional(),
    verdict: z.string().optional(),
    transitions: z.array(z.object({status: z.string(), at: z.string()})).optional(),
    transient_since: z.string().optional(),
    observed_at: z.string().optional(),
  })
  .transform(
    (data): BootObservation => ({
      kernelPanics: data.kernel_panics,
      guestReboots: data.guest_reboots,
      ubootStarts: data.uboot_starts,
      bootCompleted: data.boot_completed,
      bootFailed: data.boot_failed,
      kernelLogBytes: data.kernel_log_bytes,
      lastPanic: data.last_panic,
      verdict: data.verdict,
      transitions: data.transitions?.map(t => ({
        status: t.status,
        at: parseDate(t.at),
      })),
      transientSince: parseDate(data.transient_since),
      observedAt: parseDate(data.observed_at),
    })
  );

/**
 * One Android VM inside a device's CVD group, as the host reported it.
 *
 * The WebRTC device id is the host's own registration string
 * (`<group>-<name>-<instanceId>`) and is not derivable from the group and name,
 * so it is carried rather than rebuilt. The ADB port is provenance: the host
 * firewall opens the first instance's port only. The slot is host-global and
 * taken lowest-free-first, so a group's slots are routinely non-contiguous and
 * absent whenever the host published no port to recover one from.
 */
export const InstanceSchema = z.object({
  name: z.string(),
  state: z.string().optional(),
  webrtc_device_id: z.string().optional(),
  adb_port: z.number().optional(),
  slot: z.number().optional(),
  displays: z.array(z.string()).optional(),
});

/**
 * Zod schema for Device API response.
 *
 * VALIDATION: Performs runtime validation of the backend API response.
 * TRANSFORMATION: Converts backend snake_case fields to frontend camelCase
 * to maintain idiomatic TypeScript conventions.
 */
export const DeviceSchema = z
  .object({
    id: z.string(),
    name: z.string(),
    host_id: z.string().optional(),
    hostId: z.string().optional(),
    host_name: z.string().optional(),
    hostName: z.string().optional(),
    host_zone: z.string().optional(),
    hostZone: z.string().optional(),
    host_external_ip: z.string().optional(),
    hostExternalIp: z.string().optional(),
    host_internal_ip: z.string().optional(),
    hostInternalIp: z.string().optional(),
    status: z.string(),
    build_id: z.string().optional(),
    buildId: z.string().optional(),
    build_target: z.string().optional(),
    buildTarget: z.string().optional(),
    gpu_mode: z.string().optional(),
    gpuMode: z.string().optional(),
    adb_port: z.number().optional(),
    adbPort: z.number().optional(),
    webrtc_port: z.number().optional(),
    webrtcPort: z.number().optional(),
    created_by: z.string().optional(),
    createdBy: z.string().optional(),
    batch_id: z.string().optional(),
    batchId: z.string().optional(),
    error_message: z.string().optional(),
    errorMessage: z.string().optional(),
    created_at: z.string().optional(),
    createdAt: z.string().optional(),
    updated_at: z.string().optional(),
    updatedAt: z.string().optional(),
    started_at: z.string().optional(),
    startedAt: z.string().optional(),
    stopped_at: z.string().optional(),
    stoppedAt: z.string().optional(),
    // AIP-128 output-only: actively converging.
    reconciling: z.boolean().optional(),
    // The Android VMs inside this device's CVD group, as the host reported
    // them. Absent until the host answers, and a single-instance device
    // carries exactly one, holding the same values the flat columns above do.
    instances: z.array(InstanceSchema).optional(),
    // Health check fields
    health_status: z.string().optional(),
    healthStatus: z.string().optional(),
    last_health_check: z.string().optional(),
    lastHealthCheck: z.string().optional(),
    consecutive_fails: z.number().optional(),
    consecutiveFails: z.number().optional(),
    last_failure_reason: z.string().optional(),
    lastFailureReason: z.string().optional(),
    last_healthy_at: z.string().optional(),
    lastHealthyAt: z.string().optional(),
    // Reconcile/health internals (spec/observation + recovery budgets).
    cvd_status: z.string().optional(),
    boot_observation: z.record(z.string(), z.unknown()).optional(),
    recreate_count: z.number().optional(),
    recreate_window_start: z.string().optional(),
    restart_count: z.number().optional(),
    restart_window_start: z.string().optional(),
    // Creation mode (simple/advanced)
    mode: z.string().optional(),
    env_config: z.record(z.string(), z.unknown()).optional(),
    instance_count: z.number().optional(),
    // Metadata and lifecycle
    lifecycle: z.record(z.string(), z.unknown()).optional(), // Allow nested validation or raw object
    // Tags
    tags: z.array(z.string()).optional(),
  })
  .transform((data): Device => {
    // Handle lifecycle transformation: if lifecycle object exists, parse it; otherwise use defaults
    const lifecycleData = data.lifecycle
      ? DEVICE_LIFECYCLE_SCHEMA.parse(data.lifecycle)
      : DEVICE_LIFECYCLE_SCHEMA.parse({}); // Empty object gets defaults from schema

    return {
      id: data.id,
      name: data.name,
      hostId: data.host_id || data.hostId || '',
      hostName: data.host_name || data.hostName,
      hostZone: data.host_zone || data.hostZone,
      // Derive hostIp: prefer external IP, fallback to internal IP
      hostIp:
        data.host_external_ip ||
        data.hostExternalIp ||
        data.host_internal_ip ||
        data.hostInternalIp,
      hostExternalIp: data.host_external_ip || data.hostExternalIp,
      hostInternalIp: data.host_internal_ip || data.hostInternalIp,
      // Safe assertion: validated as string by Zod, matching DeviceStatus union
      status: data.status as DeviceStatus,
      reconciling: data.reconciling,
      buildId: data.build_id || data.buildId,
      buildTarget: data.build_target || data.buildTarget,
      // Safe assertion: defaulted to the platform default GPU mode
      gpuMode: (data.gpu_mode || data.gpuMode || 'auto') as GPUMode,
      adbPort: data.adb_port || data.adbPort,
      webrtcPort: data.webrtc_port || data.webrtcPort,
      createdBy: data.created_by || data.createdBy || 'unknown',
      batchId: data.batch_id || data.batchId,
      createdAt: new Date(data.created_at || data.createdAt || Date.now()),
      updatedAt: new Date(data.updated_at || data.updatedAt || Date.now()),
      startedAt: parseDate(data.started_at || data.startedAt),
      stoppedAt: parseDate(data.stopped_at || data.stoppedAt),
      lifecycle: lifecycleData,
      tags: data.tags,
      createMode: data.mode as CreateMode | undefined,
      envConfig: data.env_config,
      instanceCount: data.instance_count,
      instances: data.instances?.map(i => ({
        name: i.name,
        state: i.state,
        webrtcDeviceId: i.webrtc_device_id,
        adbPort: i.adb_port,
        slot: i.slot,
        displays: i.displays,
      })),
      cvdStatus: data.cvd_status,
      bootObservation: data.boot_observation
        ? BOOT_OBSERVATION_SCHEMA.parse(data.boot_observation)
        : undefined,
      recreateCount: data.recreate_count,
      recreateWindowStart: data.recreate_window_start
        ? new Date(data.recreate_window_start)
        : undefined,
      restartCount: data.restart_count,
      restartWindowStart: data.restart_window_start
        ? new Date(data.restart_window_start)
        : undefined,
    };
  });

/**
 * validate_only response. The would-be device rides the top level but has no
 * id (AIP-163 omits unknowable output), so DeviceSchema cannot parse it and
 * only the resolution is consumed; imperative plans carry no resolution.
 */
export const CreatePlanResponseSchema = z
  .object({resolution: ResolutionSchema.optional()})
  .transform((data): Resolution | null => data.resolution ?? null);

/**
 * Zod schema for Device List API response.
 *
 * VALIDATION: Validates pagination fields and array of items.
 * TRANSFORMATION: Parses each item in the list using DeviceSchema.
 */
export const DeviceListResponseSchema = z
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
    items: safeParseList(data.items ?? [], DeviceSchema, 'device'),
  }));

/** Zod schema for the bugreport capture response (snake_case → camelCase). */
export const BugreportArtifactSchema = z
  .object({
    bucket: z.string(),
    object: z.string(),
    gs_uri: z.string(),
    download_path: z.string(),
  })
  .transform(
    (d): BugreportArtifact => ({
      bucket: d.bucket,
      object: d.object,
      gsUri: d.gs_uri,
      downloadPath: resolveBasePath(d.download_path),
    })
  );

/** Zod schema for the fetched-recording response (snake_case -> camelCase). */
export const RecordingArtifactSchema = z
  .object({
    bucket: z.string(),
    object: z.string(),
    gs_uri: z.string(),
    download_path: z.string(),
  })
  .transform(
    (d): RecordingArtifact => ({
      bucket: d.bucket,
      object: d.object,
      gsUri: d.gs_uri,
      downloadPath: resolveBasePath(d.download_path),
    })
  );

/** Zod schema for the device screen-recordings listing. */
export const RecordingListSchema = z
  .object({
    recordings: z.array(z.string()).nullable().default([]),
  })
  .transform((d): string[] => d.recordings ?? []);
