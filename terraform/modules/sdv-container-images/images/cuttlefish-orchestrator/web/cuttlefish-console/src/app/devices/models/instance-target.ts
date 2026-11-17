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

import {resolveBasePath} from '../../shared/utils';
import type {Device} from './device.model';

/**
 * One Android VM inside a device's CVD group. This is what every interactive
 * surface addresses.
 *
 * A device is the leased, placed and billed unit; the screen, ADB, displays and
 * recordings all belong to one VM inside it. Streaming sessions and ADB
 * sessions are cached per target rather than per device, so two instances of
 * one device are two sessions.
 */
export interface InstanceTarget {
  readonly deviceId: string;
  readonly instance: string;
}

/**
 * The cache key for a target.
 *
 * '/' cannot appear in either half. Device ids are UUIDs, and instance names
 * are gated at admission to letters, digits, underscores and single hyphens,
 * so the join is unambiguous without escaping.
 */
export function targetKey(target: InstanceTarget): string {
  return `${target.deviceId}/${target.instance}`;
}

/** The API route prefix for a target. */
export function targetPath(target: InstanceTarget): string {
  return resolveBasePath(
    `/v2/devices/${encodeURIComponent(target.deviceId)}/instances/${encodeURIComponent(target.instance)}`
  );
}

/**
 * The instance a device-scoped surface acts on until the caller names one.
 *
 * Every device the host has answered for carries at least one instance, and
 * until the instance grid exists a device page shows exactly one screen, the
 * first, which is what it has always shown. Returns '' for a device whose
 * create has not reached the host yet; callers that need a target should not
 * build one for a device with no instances.
 */
export function primaryInstance(device: Device): string {
  return device.instances?.[0]?.name ?? '';
}
