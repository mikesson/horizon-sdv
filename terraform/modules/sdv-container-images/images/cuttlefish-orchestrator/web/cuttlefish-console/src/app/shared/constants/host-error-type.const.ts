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

/**
 * The classifications a host's `error_type` can carry.
 *
 * These are GCE **provisioning** failures, produced by the backend's error
 * classifier when creating the VM fails. They do not cover Cuttlefish or
 * device errors. A CVD failure has no structured type at all, only a message.
 *
 * This list mirrors the `enum` the API publishes for `Host.error_type`, and
 * `npm run check:schema-drift` fails when the two diverge. The previous version
 * of this vocabulary was invented here rather than mirrored, and shared exactly
 * two values with what the backend sends, so every other real failure rendered
 * as a generic "Error".
 */
export const HOST_ERROR_TYPE = {
  ZONE_NOT_FOUND: 'zone_not_found',
  ZONE_DOWN: 'zone_down',
  MACHINE_TYPE_NOT_IN_ZONE: 'machine_type_not_in_zone',
  GPU_NOT_IN_ZONE: 'gpu_not_in_zone',
  GPU_COUNT_EXCEEDED: 'gpu_count_exceeded',
  GPU_NOT_ATTACHABLE: 'gpu_not_attachable',
  QUOTA_CPU_EXCEEDED: 'quota_cpu_exceeded',
  QUOTA_GPU_EXCEEDED: 'quota_gpu_exceeded',
  QUOTA_IP_EXCEEDED: 'quota_ip_exceeded',
  QUOTA_SSD_EXCEEDED: 'quota_ssd_exceeded',
  QUOTA_HYPERDISK_EXCEEDED: 'quota_hyperdisk_exceeded',
  QUOTA_INSTANCES_EXCEEDED: 'quota_instances_exceeded',
  PERMISSION_DENIED: 'permission_denied',
  RESOURCE_EXHAUSTED: 'resource_exhausted',
  RESOURCE_NOT_READY: 'resource_not_ready',
  RATE_LIMITED: 'rate_limited',
  NETWORK_NOT_FOUND: 'network_not_found',
  SUBNET_NOT_FOUND: 'subnet_not_found',
  IMAGE_NOT_FOUND: 'image_not_found',
  UNKNOWN: 'unknown',
} as const;

/** A classification the API can report for a failed host. */
export type HostErrorType = (typeof HOST_ERROR_TYPE)[keyof typeof HOST_ERROR_TYPE];

const KNOWN = new Set<string>(Object.values(HOST_ERROR_TYPE));

/**
 * Narrows an `error_type` off the wire, falling back to `unknown` for anything
 * unrecognised.
 *
 * A plain cast is what let the old vocabulary go unnoticed: TypeScript erases
 * at runtime, so an unmatched value flowed through, satisfied the type, and
 * then missed every branch keyed on it.
 */
export function toHostErrorType(value: string | undefined): HostErrorType {
  return value && KNOWN.has(value) ? (value as HostErrorType) : HOST_ERROR_TYPE.UNKNOWN;
}
