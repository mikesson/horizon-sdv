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

/**
 * The platform lifetime policy: the operator-settable defaults plus the
 * compiled caps and the teardown grace, served by
 * GET /v2/admin/system-config/lifetimes. The caps and grace come down the
 * wire so no console surface retypes them. The 8-vs-16 device clamp shipped
 * exactly that way.
 */
export interface PlatformLifetimes {
  deviceTtlHours: number;
  extendHours: number;
  extendCount: number;
  hostTtlHours: number;
  isSet: boolean;
  maxDeviceTtlHours: number;
  maxExtendHours: number;
  maxExtendCount: number;
  maxHostTtlHours: number;
  maxDevicesPerHost: number;
  /**
   * Instance slots the host image preallocates a TAP set for. Equal to the cap
   * today and kept separate from it. The cap is policy. This number is what
   * the image can physically back, and a slot past it launches into a TAP
   * device nobody created.
   */
  hostImageBackedSlots: number;
  gracePeriodSeconds: number;
}

export const PlatformLifetimesSchema = z
  .object({
    device_ttl_hours: z.number(),
    extend_hours: z.number(),
    extend_count: z.number(),
    host_ttl_hours: z.number(),
    is_set: z.boolean().optional().default(false),
    max_device_ttl_hours: z.number(),
    max_extend_hours: z.number(),
    max_extend_count: z.number(),
    max_host_ttl_hours: z.number(),
    max_devices_per_host: z.number(),
    host_image_backed_slots: z.number(),
    grace_period_seconds: z.number(),
  })
  .transform(
    (d): PlatformLifetimes => ({
      deviceTtlHours: d.device_ttl_hours,
      extendHours: d.extend_hours,
      extendCount: d.extend_count,
      hostTtlHours: d.host_ttl_hours,
      isSet: d.is_set,
      maxDeviceTtlHours: d.max_device_ttl_hours,
      maxExtendHours: d.max_extend_hours,
      maxExtendCount: d.max_extend_count,
      maxHostTtlHours: d.max_host_ttl_hours,
      maxDevicesPerHost: d.max_devices_per_host,
      hostImageBackedSlots: d.host_image_backed_slots,
      gracePeriodSeconds: d.grace_period_seconds,
    })
  );

/**
 * The compiled defaults, used only until the fetch lands (or when it fails).
 * They are the same answer the backend serves when nothing is stored. The
 * authority is the wire. These exist so a create wizard never renders empty
 * bounds.
 */
export const COMPILED_LIFETIMES: PlatformLifetimes = {
  deviceTtlHours: 4,
  extendHours: 2,
  extendCount: 3,
  hostTtlHours: 10,
  isSet: false,
  maxDeviceTtlHours: 8,
  maxExtendHours: 4,
  maxExtendCount: 10,
  maxHostTtlHours: 168,
  // The host image preallocates one TAP set per slot and cvd's allocator does
  // not know the ceiling, so capacity beyond this is not real capacity. It is
  // a launch that dies looking for a TAP device nobody created.
  maxDevicesPerHost: 10,
  hostImageBackedSlots: 10,
  gracePeriodSeconds: 900,
};
