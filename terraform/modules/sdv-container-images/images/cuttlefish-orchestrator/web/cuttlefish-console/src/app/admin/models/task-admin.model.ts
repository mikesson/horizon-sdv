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

/** A dead-lettered background task from GET /v2/admin/tasks/failed. */
export interface FailedTask {
  id: string;
  /** Task type, e.g. "device.create_cvd" or "host.provision". */
  type: string;
  /** ID of the device/host the task operates on. */
  resourceId: string;
  attempts: number;
  maxAttempts: number;
  lastError: string;
  updatedAt: Date;
}

/** Point-in-time task queue depth from GET /v2/admin/tasks/stats. */
export interface QueueStats {
  pending: number;
  leased: number;
  failed: number;
  oldestPendingAgeSeconds: number;
}

/**
 * Route to the resource a task operates on, derived from the task-type
 * prefix ("device." / "host."). Null for unrecognized types or when the
 * task carries no resource.
 */
export function taskResourceRoute(task: {type: string; resourceId: string}): string[] | null {
  if (!task.resourceId) {
    return null;
  }
  if (task.type.startsWith('device.')) {
    return ['/devices', task.resourceId];
  }
  if (task.type.startsWith('host.')) {
    return ['/hosts', task.resourceId];
  }
  return null;
}
