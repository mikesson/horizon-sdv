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
import type {PlatformStats} from './platform-stats.model';

export const PlatformStatsSchema = z
  .object({
    hosts: z.object({
      total: z.number(),
      active: z.number(),
      max_quota: z.number(),
    }),
    devices: z.object({
      total: z.number(),
      active: z.number(),
    }),
    hosts_by_status: z.record(z.string(), z.number()),
    devices_by_status: z.record(z.string(), z.number()),
    timestamp: z.string(),
  })
  .transform(
    (data): PlatformStats => ({
      hosts: {
        total: data.hosts.total,
        active: data.hosts.active,
        maxQuota: data.hosts.max_quota,
      },
      devices: {
        total: data.devices.total,
        active: data.devices.active,
      },
      hostsByStatus: data.hosts_by_status,
      devicesByStatus: data.devices_by_status,
      timestamp: new Date(data.timestamp),
    })
  );
