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
 * What each pool is, in words. It is the one thing about a pool that is not a
 * served number.
 *
 * The masthead row, the region header on the drawing, the settings card and the
 * create wizards all say it. Written once here so a pool cannot be described as
 * one kind of machine in one place and another kind two screens down.
 */

interface PoolWords {
  /** What kind of machine stands in it, short enough to sit beside the name. */
  readonly family: string;
  /** What a create has to want to be routed here. */
  readonly purpose: string;
  /** What switching it off does, said before it is done. */
  readonly disabling: string;
}

const POOL_WORDS: Record<string, PoolWords> = {
  default: {
    family: 'x86 · no accelerator',
    purpose:
      'Every create that says nothing about capacity lands here, and this pool cannot be switched off.',
    disabling: '',
  },
  gpu: {
    family: 'accelerated',
    purpose:
      'A create that must have a GPU is routed here, and is placed on these machines alone. A create that merely prefers one is served by the default pool, because a preference is met by software rendering.',
    disabling:
      'Switched off, a create that must have a GPU falls through to the default pool, which may serve it or may miss it. These machines are not torn down. They stop being bought for and run their leases out.',
  },
  arm: {
    family: 'arm64 · bare metal',
    purpose:
      'A create built for the arm64 architecture is routed here, onto bare-metal machines, the only hardware an arm64 guest runs on.',
    disabling:
      'Switched off, a create requiring arm64 is refused because the pool is not offered here. These machines are not torn down. They stop being bought for and run their leases out.',
  },
};

const UNKNOWN: PoolWords = {
  family: 'machines',
  purpose: 'This deployment holds machines in a pool this console has no words for.',
  disabling: '',
};

function wordsFor(pool: string): PoolWords {
  return POOL_WORDS[pool] ?? UNKNOWN;
}

/** What kind of machine a pool holds. The phrase that sits beside its name. */
export function poolFamily(pool: string): string {
  return wordsFor(pool).family;
}

/**
 * What a create has to want to be routed into this pool. The arm pool's answer
 * depends on a served fact. With no arm64 host image, the accurate sentence
 * says nothing can be built here, so that is what it says.
 */
export function poolPurpose(pool: string, armImageReady: boolean): string {
  if (pool === 'arm' && !armImageReady) {
    return 'Nothing can be built for this pool yet. No arm64 machine family carries an enabled host image, so a create requiring arm64 is refused and nothing is ever bought here.';
  }
  return wordsFor(pool).purpose;
}

/** What switching this pool off does; empty for a pool that cannot be. */
export function poolDisabling(pool: string): string {
  return wordsFor(pool).disabling;
}
