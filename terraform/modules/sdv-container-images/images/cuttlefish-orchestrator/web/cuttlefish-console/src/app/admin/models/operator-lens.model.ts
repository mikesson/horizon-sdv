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

/** A device row in the reclaimable-capacity panel. */
export interface ReclaimableDevice {
  id: string;
  name: string;
  hostId: string;
  status: string;
  pinnedBy: string;
  pinnedAt: Date | null;
  pinnedReason: string;
  pinnedUntil: Date | null;
  updatedAt: Date;
}

/** A host row in the reclaimable-capacity panel. */
export interface ReclaimableHost {
  id: string;
  name: string;
  zone: string;
  status: string;
  currentDeviceCount: number;
  expiresAt: Date | null;
  updatedAt: Date;
}

/**
 * Reclaimable-capacity buckets from GET /v2/admin/reclaimable; each bucket is
 * capped at 50 rows server-side (a full bucket means at least that many).
 */
export interface Reclaimable {
  /** Rows across the four buckets, a floor while any bucket is full. */
  total: number;
  pinnedDevices: ReclaimableDevice[];
  stoppedDevices: ReclaimableDevice[];
  stoppedHosts: ReclaimableHost[];
  expiringHosts: ReclaimableHost[];
}

/** One resource type's recent provisioning record. */
export interface ProvisioningOutcome {
  created: number;
  errored: number;
}

/** 7-day provisioning outcomes from GET /v2/admin/provisioning-stats. */
export interface ProvisioningStats {
  windowDays: number;
  devices: ProvisioningOutcome;
  hosts: ProvisioningOutcome;
}

/** Success rate 0-100 for an outcome; null when nothing was created. */
export function successRate(outcome: ProvisioningOutcome): number | null {
  if (outcome.created === 0) {
    return null;
  }
  return ((outcome.created - outcome.errored) / outcome.created) * 100;
}
