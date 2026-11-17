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
  ProvisioningStats,
  Reclaimable,
  ReclaimableDevice,
  ReclaimableHost,
} from './operator-lens.model';

export const ReclaimableDeviceSchema = z
  .object({
    id: z.string(),
    name: z.string(),
    host_id: z.string(),
    status: z.string(),
    pinned_by: z.string().optional(),
    pinned_at: z.string().optional(),
    pinned_reason: z.string().optional(),
    pinned_until: z.string().optional(),
    updated_at: z.string(),
  })
  .transform(
    (data): ReclaimableDevice => ({
      id: data.id,
      name: data.name,
      hostId: data.host_id,
      status: data.status,
      pinnedBy: data.pinned_by ?? '',
      pinnedAt: data.pinned_at ? new Date(data.pinned_at) : null,
      pinnedReason: data.pinned_reason ?? '',
      pinnedUntil: data.pinned_until ? new Date(data.pinned_until) : null,
      updatedAt: new Date(data.updated_at),
    })
  );

export const ReclaimableHostSchema = z
  .object({
    id: z.string(),
    name: z.string(),
    zone: z.string(),
    status: z.string(),
    current_device_count: z.number(),
    expires_at: z.string().optional(),
    updated_at: z.string(),
  })
  .transform(
    (data): ReclaimableHost => ({
      id: data.id,
      name: data.name,
      zone: data.zone,
      status: data.status,
      currentDeviceCount: data.current_device_count,
      expiresAt: data.expires_at ? new Date(data.expires_at) : null,
      updatedAt: new Date(data.updated_at),
    })
  );

export const ReclaimableSchema = z
  .object({
    total: z.number(),
    pinned_devices: z.array(ReclaimableDeviceSchema).nullable(),
    stopped_devices: z.array(ReclaimableDeviceSchema).nullable(),
    stopped_hosts: z.array(ReclaimableHostSchema).nullable(),
    expiring_hosts: z.array(ReclaimableHostSchema).nullable(),
  })
  .transform(
    (data): Reclaimable => ({
      total: data.total,
      pinnedDevices: data.pinned_devices ?? [],
      stoppedDevices: data.stopped_devices ?? [],
      stoppedHosts: data.stopped_hosts ?? [],
      expiringHosts: data.expiring_hosts ?? [],
    })
  );

export const ProvisioningOutcomeSchema = z.object({
  created: z.number(),
  errored: z.number(),
});

export const ProvisioningStatsSchema = z
  .object({
    window_days: z.number(),
    devices: ProvisioningOutcomeSchema,
    hosts: ProvisioningOutcomeSchema,
  })
  .transform(
    (data): ProvisioningStats => ({
      windowDays: data.window_days,
      devices: data.devices,
      hosts: data.hosts,
    })
  );
