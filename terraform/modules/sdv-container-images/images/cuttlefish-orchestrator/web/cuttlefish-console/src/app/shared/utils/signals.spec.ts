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

import {Injector, signal} from '@angular/core';
import {TestBed} from '@angular/core/testing';
import {describe, expect, it} from 'vitest';
import {waitForSignal} from './signals';

describe('waitForSignal', () => {
  const injector = (): Injector => TestBed.inject(Injector);

  it('resolves immediately when the projection is already defined', async () => {
    const state = signal('ready');
    const wait = waitForSignal(() => (state() === 'ready' ? state() : undefined), {
      injector: injector(),
    });
    TestBed.tick();
    await expect(wait).resolves.toBe('ready');
  });

  it('resolves once the watched signal reaches the projected state', async () => {
    const state = signal<'connecting' | 'connected'>('connecting');
    const wait = waitForSignal(() => (state() === 'connected' ? true : undefined), {
      injector: injector(),
    });
    TestBed.tick();

    state.set('connected');
    TestBed.tick();
    await expect(wait).resolves.toBe(true);
  });

  it('rejects when the projection throws', async () => {
    const state = signal<'connecting' | 'failed'>('connecting');
    const wait = waitForSignal(
      () => {
        if (state() === 'failed') {
          throw new Error('stream failed');
        }
        return undefined;
      },
      {injector: injector()}
    );
    TestBed.tick();

    state.set('failed');
    TestBed.tick();
    await expect(wait).rejects.toThrow('stream failed');
  });

  it('rejects with the timeout message when the state never settles', async () => {
    const state = signal(false);
    const wait = waitForSignal(() => (state() ? true : undefined), {
      injector: injector(),
      timeoutMs: 5,
      timeoutMessage: 'gave up',
    });
    TestBed.tick();
    await expect(wait).rejects.toThrow('gave up');
  });

  it('ignores state changes after settling', async () => {
    const state = signal(1);
    const wait = waitForSignal(() => (state() >= 2 ? state() : undefined), {
      injector: injector(),
    });
    TestBed.tick();

    state.set(2);
    TestBed.tick();
    state.set(3);
    TestBed.tick();
    await expect(wait).resolves.toBe(2);
  });
});
