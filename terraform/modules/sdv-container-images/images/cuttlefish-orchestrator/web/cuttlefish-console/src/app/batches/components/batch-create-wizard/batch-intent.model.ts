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

import {DEFAULT_TRACK_BRANCH, DEFAULT_TRACK_TARGET} from '../../../shared/constants';
import {COMPILED_LIFETIMES} from '../../../shared/models/platform-lifetimes.schema';
import type {PoolPin} from '../../../shared/models/pool-placement';
import type {
  CapacityModel,
  GuestArchitecture,
  Interaction,
} from '../../../shared/models/requirements.model';
import type {BatchPlacement, CreateBatchRequest} from '../../models/batch.model';

/** Upper bound on a batch, mirroring the backend's own cap. */
export const MAX_BATCH_COUNT = 100;

/**
 * Longest usable name prefix: a member is named "<prefix>-000" and a device
 * name may not exceed 63 characters.
 */
export const MAX_NAME_PREFIX = 59;

/** Per-member session length; the batch is reaped with its members. */
const DEFAULT_BATCH_TTL_HOURS = COMPILED_LIFETIMES.deviceTtlHours;

/**
 * How much of the fleet has to arrive. The backend spells this as a fill
 * policy plus a min_ready floor, but only three of those combinations are
 * legal and each means something different, so the interview asks once:
 *
 * - `atomic`:  every device, and the batch tears down if one later dies
 * - `all`:     every device must be admitted, but later failures are survived
 * - `partial`: start with whatever clears the floor
 */
export type AdmissionPolicy = 'atomic' | 'all' | 'partial';

/**
 * The batch interview's draft. Flat by design: signal forms bind and validate
 * fields directly, so the build choice is a discriminator plus its operands
 * rather than a union.
 */
export interface BatchIntent {
  namePrefix: string;
  buildKind: 'default' | 'registered' | 'track';
  /** Set when buildKind is 'registered'. */
  buildId: string;
  trackBranch: string;
  trackTarget: string;
  usage: Interaction;
  /** Hard constraint: fail admission rather than place the fleet without GPUs. */
  gpuRequired: boolean;
  /** Architecture of the guests; arm64 is served by the arm pool alone. */
  architecture: GuestArchitecture;
  /**
   * The pool to pin the batch's hosts to; 'auto' lets the server derive one
   * from the needs above (architecture, then a hard GPU requirement, then the
   * default).
   */
  pool: PoolPin;
  /** '' = no preference. One region for the batch, whose hosts share a zone. */
  preferredRegion: string;
  capacity: CapacityModel;
  count: number;
  admission: AdmissionPolicy;
  /** The floor, read only under the 'partial' policy. */
  minReady: number;
  placement: BatchPlacement;
  /** Read only under the 'atomic' policy; 0 = no deadline. */
  readyTimeoutSeconds: number;
  ttlHours: number;
}

export function defaultBatchIntent(): BatchIntent {
  return {
    namePrefix: '',
    buildKind: 'default',
    buildId: '',
    trackBranch: DEFAULT_TRACK_BRANCH,
    trackTarget: DEFAULT_TRACK_TARGET,
    // Batches are fleets for tests and automation; the device wizard's
    // interactive default would put every member on scarce GPU capacity.
    usage: 'headless',
    gpuRequired: false,
    architecture: 'x86',
    pool: 'auto',
    preferredRegion: '',
    capacity: 'auto',
    count: 5,
    admission: 'all',
    minReady: 5,
    placement: 'isolated',
    readyTimeoutSeconds: 0,
    ttlHours: DEFAULT_BATCH_TTL_HOURS,
  };
}

/** The floor the policy implies, which only 'partial' reads off the slider. */
export function effectiveMinReady(intent: BatchIntent): number {
  return intent.admission === 'partial' ? Math.min(intent.minReady, intent.count) : intent.count;
}

/**
 * Maps the intent to the wire request.
 *
 * This is where the backend's cross-field rules are made true instead of
 * merely validated. A ready timeout exists only under atomic, an unset one is
 * absent rather than zero, and the floor is derived from the policy instead of
 * stored beside it, which is what stops a raised count from leaving a stale
 * floor behind.
 */
export function toCreateBatchRequest(intent: BatchIntent): CreateBatchRequest {
  const atomic = intent.admission === 'atomic';
  return {
    namePrefix: intent.namePrefix,
    count: intent.count,
    minReady: effectiveMinReady(intent),
    fillPolicy: atomic ? 'atomic' : 'best_effort',
    placement: intent.placement,
    readyTimeoutSeconds:
      atomic && intent.readyTimeoutSeconds > 0 ? intent.readyTimeoutSeconds : undefined,
    ttlHours: intent.ttlHours,
    requirements: {
      interaction: intent.usage,
      gpuRequired: intent.gpuRequired || undefined,
      build: toBuildRequirement(intent),
      preferredRegion: intent.preferredRegion || undefined,
      capacity: intent.capacity !== 'auto' ? intent.capacity : undefined,
      // x86 and an unpinned pool are what the server assumes, so neither
      // travels. An omitted field and its default mean the same thing there.
      architecture: intent.architecture === 'arm' ? 'arm' : undefined,
      pool: intent.pool !== 'auto' ? intent.pool : undefined,
    },
  };
}

function toBuildRequirement(intent: BatchIntent): CreateBatchRequest['requirements']['build'] {
  if (intent.buildKind === 'registered' && intent.buildId) {
    return {kind: 'registered', id: intent.buildId};
  }
  if (intent.buildKind === 'track') {
    return {kind: 'track', branch: intent.trackBranch, target: intent.trackTarget};
  }
  return undefined;
}

/** The member names admission will assign, as a range ("ci-000 … ci-004"). */
export function memberNameRange(prefix: string, count: number): string {
  const name = (i: number): string => `${prefix}-${String(i).padStart(3, '0')}`;
  return count === 1 ? name(0) : `${name(0)} … ${name(count - 1)}`;
}
