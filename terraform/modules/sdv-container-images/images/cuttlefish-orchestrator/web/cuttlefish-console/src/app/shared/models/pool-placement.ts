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
 * Which pool a create lands in, said back to the person answering the
 * interview.
 *
 * The derivation is the server's, in the server's order (the pool the create
 * named, then its architecture, then a hard GPU requirement, then the
 * default), so the line the wizard shows is the answer the create will get
 * rather than a guess at it. What this deployment cannot serve is stated and
 * never enforced. The refusal belongs to the server, and it arrives verbatim
 * on the Plan step.
 */

import {poolPurpose} from './pool-copy';
import type {GuestArchitecture} from './requirements.model';

/** The pools a create can be pinned to, in the order the derivation reads them. */
export const POOL_NAMES = ['default', 'gpu', 'arm'] as const;

/** What a create says about its pool; 'auto' lets the needs above decide. */
export type PoolPin = 'auto' | (typeof POOL_NAMES)[number];

/** What this deployment offers, as the autoscaler snapshot reports it. */
export interface PoolFacts {
  /** Pools an operator has switched on; the default pool is always served. */
  pools: readonly string[];
  /** Whether an arm64 host can be built here at all. */
  armImageReady: boolean;
}

/** The needs a pool is derived from. */
interface PoolChoice {
  pool: PoolPin;
  architecture: GuestArchitecture;
  gpuRequired: boolean;
}

interface PoolConsequence {
  /** The pool the derivation answers. */
  pool: string;
  /** The one line the placement cluster shows. */
  line: string;
  /** True when this deployment cannot serve the answer, so the plan refuses it. */
  refused: boolean;
}

export function poolConsequence(choice: PoolChoice, facts: PoolFacts): PoolConsequence {
  const arm = choice.architecture === 'arm';
  if (choice.pool !== 'auto') {
    // An arm64 guest runs on arm64 hardware and nowhere else, so a pin
    // anywhere else is refused instead of being overridden without a word.
    if (arm && choice.pool !== 'arm') {
      return {
        pool: choice.pool,
        refused: true,
        line:
          `An arm64 guest runs in the arm pool and nowhere else, so a create pinned to the ` +
          `${choice.pool} pool is refused. Pin the arm pool, or build for x86.`,
      };
    }
    return landing(choice.pool, facts);
  }
  if (arm) {
    return landing('arm', facts);
  }
  // With the GPU pool switched off a hard requirement falls through to the
  // default pool, which serves it or misses it; it is not refused.
  if (choice.gpuRequired && facts.pools.includes('gpu')) {
    return landing('gpu', facts);
  }
  return landing('default', facts);
}

function landing(pool: string, facts: PoolFacts): PoolConsequence {
  const purpose = poolPurpose(pool, facts.armImageReady);
  if (servable(pool, facts)) {
    return {pool, refused: false, line: `Lands in the ${pool} pool. ${purpose}`};
  }
  return {
    pool,
    refused: true,
    line:
      `Would land in the ${pool} pool, which this deployment cannot serve, ` +
      `so the plan will refuse this create. ${purpose}`,
  };
}

/** Switched on here, and able to hold a machine at all. */
function servable(pool: string, facts: PoolFacts): boolean {
  if (pool === 'arm' && !facts.armImageReady) {
    return false;
  }
  return pool === 'default' || facts.pools.includes(pool);
}
