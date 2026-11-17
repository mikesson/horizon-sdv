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

import type {PlacementRequirements, Resolution} from '../../shared/models/requirements.model';

/** Derived batch lifecycle (server-side rollup over member devices). */
export type BatchStatus = 'provisioning' | 'ready' | 'degraded' | 'failed' | 'deleting';

/** Batch fill policy: atomic tears the whole batch down on member failure. */
export type BatchFillPolicy = 'best_effort' | 'atomic';

/** Placement: dedicated exclusive hosts vs packing existing capacity. */
export type BatchPlacement = 'isolated' | 'pack';

/**
 * A batch: N devices created and torn down as one unit. Status and member
 * counts are derived server-side from the member devices; the policy fields
 * mirror the persisted batch row.
 */
export interface Batch {
  id: string;
  namePrefix?: string;
  status: BatchStatus;
  total: number;
  running: number;
  errored: number;
  reconciling: boolean;
  fillPolicy?: BatchFillPolicy;
  placement?: BatchPlacement;
  readyTimeoutSeconds?: number;
  createdBy?: string;
  createdAt?: Date;
  readyAt?: Date;
  failedAt?: Date;
  failureReason?: string;
  /**
   * Admission facts, returned by the create call only. A list or get reads
   * the persisted row, which does not carry them.
   */
  admission?: BatchAdmission;
}

/**
 * What admission resolved: how many of the requested devices fit, how many
 * hosts that needs, and the build/placement the whole fleet shares. A dry run
 * returns exactly this and commits nothing.
 */
export interface BatchAdmission {
  requested: number;
  /**
   * Devices that fit. Lower than `requested` only when min_ready was lowered
   * below count. Host quota clamped the plan and the batch starts smaller.
   */
  admitted: number;
  newHosts: number;
  resolution?: Resolution;
  warnings: string[];
}

/** Payload for creating a batch (POST /v2/batches). */
export interface CreateBatchRequest {
  namePrefix: string;
  count: number;
  /** Admission floor; equals count under the atomic fill policy. */
  minReady: number;
  fillPolicy: BatchFillPolicy;
  placement: BatchPlacement;
  /** Atomic only: fail the batch unless every member runs within this window. */
  readyTimeoutSeconds?: number;
  ttlHours?: number;
  requirements: PlacementRequirements;
}

/** Filters for listing batches. */
export interface BatchListFilter {
  /** Server-side scope to the caller's own batches (the console default). */
  mine?: boolean;
  createdBy?: string;
}

/** One member device's connection info (the CI payload). */
export interface BatchMember {
  deviceId: string;
  name: string;
  status: string;
  hostId: string;
  adbSerial?: string;
  webrtcDeviceId?: string;
}

/** GET /v2/batches response. */
export interface BatchListResponse {
  items: Batch[];
  total: number;
}
