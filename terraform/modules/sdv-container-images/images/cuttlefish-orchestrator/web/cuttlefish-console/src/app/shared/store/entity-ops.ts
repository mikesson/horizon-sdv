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

import type {MatSnackBar} from '@angular/material/snack-bar';
import {EMPTY, type Observable, timer} from 'rxjs';
import {catchError, filter, switchMap, take, tap} from 'rxjs/operators';

import {getErrorMessage} from '../utils';

/**
 * Shared building blocks for the device/host signal stores so the two slices
 * stay true mirror images instead of drifting apart. Store-specific concerns
 * (selection, navigation, status machines) stay in each store; these cover the
 * bits that were byte-identical.
 */

/**
 * Surfaces an operation failure the same way everywhere: derives a human message
 * (falling back to `fallback`), shows the standard error toast, and returns the
 * message so the caller can also patch it into store `error` state.
 */
export function fail(snackBar: MatSnackBar, fallback: string, err: unknown): string {
  const message = getErrorMessage(err, fallback);
  snackBar.open(message, 'Close', {duration: 5000});
  return message;
}

/**
 * Upper bound on a status poll's lifetime (ticks every 2s → 30 minutes). A
 * status that never stabilizes must not leave a poll running forever.
 */
export const MAX_POLL_TICKS = 900;

/**
 * Polls `get(id)` every 2s until the entity reaches a stable status (one of
 * `terminal`), pushing each interim value through `onUpdate`. A failed tick is
 * swallowed per-poll and the next tick refetches, so a transient error neither
 * kills the poll nor leaks out. Completes on the first stable value or after
 * MAX_POLL_TICKS. Used to reflect create/retry progress without a manual
 * refresh; callers that outlive a route (root stores) must destroy the
 * returned subscription on navigation.
 */
export function pollUntilTerminal<E extends {status: string}>(
  get: (id: string) => Observable<E>,
  id: string,
  terminal: readonly string[],
  onUpdate: (entity: E) => void
): Observable<E> {
  return timer(0, 2000).pipe(
    take(MAX_POLL_TICKS),
    switchMap(() => get(id).pipe(catchError(() => EMPTY))),
    tap(onUpdate),
    filter(entity => terminal.includes(entity.status)),
    take(1)
  );
}
