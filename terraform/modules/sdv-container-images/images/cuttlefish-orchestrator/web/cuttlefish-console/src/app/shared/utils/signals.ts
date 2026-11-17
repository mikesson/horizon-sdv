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

import type {EffectRef, Injector} from '@angular/core';
import {effect} from '@angular/core';

export interface WaitForSignalOptions {
  injector: Injector;
  /** Without a timeout the wait is unbounded. Pass one unless the watched
   * state is guaranteed to settle. */
  timeoutMs?: number;
  timeoutMessage?: string;
}

/**
 * Resolves with the first defined projection of reactive state.
 *
 * `read` runs inside an effect, so every signal it touches re-triggers it:
 * return a value to resolve, `undefined` to keep waiting, or throw to
 * reject. This is the bridge from signal state to one-shot promise flows.
 * Use it instead of polling signals with `setTimeout`.
 */
export function waitForSignal<R>(
  read: () => R | undefined,
  options: WaitForSignalOptions
): Promise<R> {
  return new Promise<R>((resolve, reject) => {
    let settled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;
    let watcher: EffectRef | null = null;

    const settle = (complete: () => void): void => {
      if (settled) {
        return;
      }
      settled = true;
      if (timer !== null) {
        clearTimeout(timer);
      }
      complete();
      // The effect may be mid-run when it settles; destroy it a tick later.
      queueMicrotask(() => watcher?.destroy());
    };

    watcher = effect(
      () => {
        try {
          const result = read();
          if (result !== undefined) {
            settle(() => resolve(result));
          }
        } catch (error: unknown) {
          settle(() => reject(error instanceof Error ? error : new Error(String(error))));
        }
      },
      {injector: options.injector}
    );

    if (options.timeoutMs !== undefined) {
      timer = setTimeout(() => {
        settle(() => reject(new Error(options.timeoutMessage ?? 'Timed out waiting for state')));
      }, options.timeoutMs);
    }
  });
}
