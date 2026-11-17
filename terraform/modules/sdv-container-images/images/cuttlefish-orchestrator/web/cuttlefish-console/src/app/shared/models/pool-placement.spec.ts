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
 * The expectations here are the server's derivation, DerivePool in
 * internal/domain/host/pool.go. Nothing links the two copies, so a wizard that
 * promises a landing the create does not get is what this file catches.
 */

import {describe, expect, it} from 'vitest';
import type {PoolFacts, PoolPin} from './pool-placement';
import {POOL_NAMES, poolConsequence} from './pool-placement';

type Choice = Parameters<typeof poolConsequence>[0];

function choice(over: Partial<Choice> = {}): Choice {
  return {pool: 'auto', architecture: 'x86', gpuRequired: false, ...over};
}

/** A deployment offering all three pools, with an arm64 host image to build from. */
function facts(over: Partial<PoolFacts> = {}): PoolFacts {
  return {pools: [...POOL_NAMES], armImageReady: true, ...over};
}

describe('a pinned pool', () => {
  it('lands a create in the pool it named', () => {
    const result = poolConsequence(choice({pool: 'gpu'}), facts());

    expect(result.pool).toBe('gpu');
    expect(result.refused).toBe(false);
    expect(result.line).toContain('Lands in the gpu pool');
  });

  it('refuses a pin this deployment has not switched on', () => {
    const result = poolConsequence(choice({pool: 'gpu'}), facts({pools: ['default']}));

    expect(result.pool).toBe('gpu');
    expect(result.refused).toBe(true);
    expect(result.line).toContain('which this deployment cannot serve');
  });

  // A pool switched on that no machine can be built for is refused the same as
  // one switched off, and the line has to say which of the two it is.
  it('refuses the arm pool while no arm64 host image exists', () => {
    const result = poolConsequence(choice({pool: 'arm'}), facts({armImageReady: false}));

    expect(result.pool).toBe('arm');
    expect(result.refused).toBe(true);
    expect(result.line).toContain('Nothing can be built for this pool yet');
  });

  // POOL_NAMES is the console's copy of the server's registry. A name added to
  // one copy and not the other is reachable through the pin, and a pool nothing
  // knows the words for is refused rather than promised a landing.
  it('refuses a pin this console has no words for', () => {
    const invented: string = 'tpu';

    const result = poolConsequence(choice({pool: invented as PoolPin}), facts());

    expect(result.pool).toBe('tpu');
    expect(result.refused).toBe(true);
    expect(result.line).toContain('has no words for');
  });

  // The default pool is where every host naming none belongs, so it is served
  // whether or not an operator gave it bounds.
  it('lands in the default pool whatever the enabled set holds', () => {
    const result = poolConsequence(choice({pool: 'default'}), facts({pools: []}));

    expect(result.pool).toBe('default');
    expect(result.refused).toBe(false);
  });

  // arm64 hardware is the only hardware an arm64 guest runs on, so the pin is
  // refused instead of being overridden without a word.
  it('refuses an arm64 guest pinned to another pool', () => {
    const result = poolConsequence(choice({pool: 'gpu', architecture: 'arm'}), facts());

    expect(result.pool).toBe('gpu');
    expect(result.refused).toBe(true);
    expect(result.line).toContain('An arm64 guest runs in the arm pool and nowhere else');
    expect(result.line).toContain('Pin the arm pool, or build for x86.');
  });

  it('takes the arm pin for an arm64 guest', () => {
    const result = poolConsequence(choice({pool: 'arm', architecture: 'arm'}), facts());

    expect(result.pool).toBe('arm');
    expect(result.refused).toBe(false);
  });
});

describe('a derived pool', () => {
  it('routes an arm64 guest to the arm pool', () => {
    const result = poolConsequence(choice({architecture: 'arm'}), facts());

    expect(result.pool).toBe('arm');
    expect(result.refused).toBe(false);
    expect(result.line).toContain('Lands in the arm pool');
  });

  it('refuses an arm64 guest where the arm pool is switched off', () => {
    const result = poolConsequence(
      choice({architecture: 'arm'}),
      facts({pools: ['default', 'gpu']})
    );

    expect(result.pool).toBe('arm');
    expect(result.refused).toBe(true);
    expect(result.line).toContain('which this deployment cannot serve');
  });

  it('refuses an arm64 guest where no arm64 host image exists', () => {
    const result = poolConsequence(choice({architecture: 'arm'}), facts({armImageReady: false}));

    expect(result.refused).toBe(true);
    expect(result.line).toContain('Nothing can be built for this pool yet');
  });

  it('routes a hard GPU requirement to the gpu pool', () => {
    const result = poolConsequence(choice({gpuRequired: true}), facts());

    expect(result.pool).toBe('gpu');
    expect(result.refused).toBe(false);
  });

  // With the gpu pool switched off the requirement falls through to the default
  // pool, which serves it or misses it. Naming the gpu pool here, or refusing,
  // states an answer the server would not give.
  it('sends a hard GPU requirement to the default pool while the gpu pool is off', () => {
    const result = poolConsequence(choice({gpuRequired: true}), facts({pools: ['default']}));

    expect(result.pool).toBe('default');
    expect(result.refused).toBe(false);
    expect(result.line).toContain('Lands in the default pool');
  });

  // The architecture term is read first and never falls through, so an arm64
  // guest that also demands a GPU is an arm create.
  it('reads the architecture before the GPU requirement', () => {
    const result = poolConsequence(choice({architecture: 'arm', gpuRequired: true}), facts());

    expect(result.pool).toBe('arm');
    expect(result.refused).toBe(false);
  });

  it('lands a create that asks for nothing in the default pool', () => {
    const result = poolConsequence(choice(), facts({pools: ['default']}));

    expect(result.pool).toBe('default');
    expect(result.refused).toBe(false);
    expect(result.line).toContain('Lands in the default pool');
  });
});

describe('the fail-open facts', () => {
  // PoolFactsService falls back to this shape when the admin read fails or its
  // parse does, so a broken read can never block a create. Every answer lands
  // under it, and the server states the refusal at plan time.
  const failOpen: PoolFacts = {pools: [...POOL_NAMES], armImageReady: true};

  it('promises a landing for every pin', () => {
    for (const pool of POOL_NAMES) {
      const result = poolConsequence(choice({pool}), failOpen);

      expect(result.pool).toBe(pool);
      expect(result.refused).toBe(false);
    }
  });

  it('promises a landing for an arm64 guest and for a hard GPU requirement', () => {
    expect(poolConsequence(choice({architecture: 'arm'}), failOpen).refused).toBe(false);
    expect(poolConsequence(choice({gpuRequired: true}), failOpen).refused).toBe(false);
  });
});
