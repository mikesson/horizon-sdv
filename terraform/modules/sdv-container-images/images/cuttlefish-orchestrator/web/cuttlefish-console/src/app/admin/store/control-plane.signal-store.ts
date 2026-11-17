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

import {computed, inject} from '@angular/core';
import {tapResponse} from '@ngrx/operators';
import {patchState, signalStore, withComputed, withMethods, withState} from '@ngrx/signals';
import {rxMethod} from '@ngrx/signals/rxjs-interop';
import type {Observable} from 'rxjs';
import {catchError, exhaustMap, filter, forkJoin, of, pipe, switchMap, timer} from 'rxjs';
import type {Operation} from '../../devices/models/operation.model';
import {OperationsApiService} from '../../shared/services/operations-api.service';
import {getErrorMessage} from '../../shared/utils';
import type {ControlPlaneSnapshot} from '../models/control-plane.model';
import {isRetrying} from '../models/control-plane.model';
import {TaskAdminApiService} from '../services/task-admin-api.service';

/** Poll cadence while the page is visible and live. */
export const POLL_INTERVAL_MS = 5000;

/**
 * Rows the in-flight panel shows. The listing is index-backed, so the bound is
 * about the panel staying readable rather than about query cost.
 */
const OPEN_OPERATIONS_LIMIT = 25;

/** Ring-buffer length for the session sparkline (~10 min at 5s). */
const MAX_SAMPLES = 120;

/** One sparkline sample of queue depth, taken per poll. */
export interface QueueSample {
  t: number;
  pending: number;
  leased: number;
  failed: number;
}

/**
 * Which pipeline edges saw movement between the last two snapshots. Drives
 * the flow diagram's animation: motion means "work flowed here just now".
 */
export interface EdgeActivity {
  /** A new user-intent task (edge priority) appeared. */
  intent: boolean;
  /** A new reconciler-enqueued task (resync priority) appeared. */
  reconcile: boolean;
  /** A worker claimed a task (pending → leased). */
  claim: boolean;
  /** A task finished and left the queue (delete-on-complete). */
  complete: boolean;
  /** A task came back from a worker to wait out retry backoff. */
  retry: boolean;
  /** A task was dead-lettered. */
  dead: boolean;
}

const NO_ACTIVITY: EdgeActivity = {
  intent: false,
  reconcile: false,
  claim: false,
  complete: false,
  retry: false,
  dead: false,
};

interface ControlPlaneState {
  snapshot: ControlPlaneSnapshot | null;
  /** The fleet's in-flight async verbs, newest first. */
  openOperations: Operation[];
  /** Set when only the operations leg failed; the rest of the page is live. */
  operationsError: string | null;
  history: QueueSample[];
  edgeActivity: EdgeActivity;
  /** serverNow − clientNow at receive time; makes countdowns skew-proof. */
  clockOffsetMs: number;
  live: boolean;
  loading: boolean;
  error: string | null;
}

const initialState: ControlPlaneState = {
  snapshot: null,
  openOperations: [],
  operationsError: null,
  history: [],
  edgeActivity: NO_ACTIVITY,
  clockOffsetMs: 0,
  live: true,
  loading: true,
  error: null,
};

/**
 * Polling control-plane snapshot for the Operations page. Provided at the
 * component level so polling stops when the page is left.
 */
export const ControlPlaneStore = signalStore(
  withState(initialState),
  withComputed(store => ({
    inFlight: computed(() => store.snapshot()?.liveTasks.filter(t => t.state === 'leased') ?? []),
    waiting: computed(() => store.snapshot()?.liveTasks.filter(t => t.state === 'pending') ?? []),
    retryingCount: computed(() => store.snapshot()?.liveTasks.filter(isRetrying).length ?? 0),
    /** Distinct instances seen holding queue or leader leases (a lower bound). */
    knownInstances: computed(() => {
      const s = store.snapshot();
      if (!s) {
        return 0;
      }
      const holders = new Set<string>();
      for (const t of s.liveTasks) {
        if (t.leaseHolder) {
          holders.add(t.leaseHolder);
        }
      }
      for (const l of s.leaders) {
        if (l.holder) {
          holders.add(l.holder);
        }
      }
      return holders.size;
    }),
  })),
  withMethods(store => {
    const api = inject(TaskAdminApiService);
    const operationsApi = inject(OperationsApiService);

    const apply = (snapshot: ControlPlaneSnapshot, operations: Operation[] | null): void => {
      const prev = store.snapshot();
      const sample: QueueSample = {
        t: Date.now(),
        pending: snapshot.queue.pending,
        leased: snapshot.queue.leased,
        failed: snapshot.queue.failed,
      };
      patchState(store, {
        snapshot,
        openOperations: operations ?? store.openOperations(),
        // A failed leg keeps the last good list and says so, rather than
        // showing an empty panel that reads as "nothing is running".
        operationsError: operations === null ? 'Could not refresh in-flight operations' : null,
        history: [...store.history(), sample].slice(-MAX_SAMPLES),
        edgeActivity: prev ? diffActivity(prev, snapshot) : NO_ACTIVITY,
        clockOffsetMs: snapshot.now.getTime() - Date.now(),
        loading: false,
        error: null,
      });
    };

    // The operations leg is tolerated when it fails (previous values stay);
    // the snapshot leg failing is the page's error state.
    const fetchOnce = (): Observable<unknown> =>
      forkJoin({
        snapshot: api.controlPlane(),
        operations: operationsApi.listOpen(OPEN_OPERATIONS_LIMIT).pipe(catchError(() => of(null))),
      }).pipe(
        tapResponse({
          next: ({snapshot, operations}) => apply(snapshot, operations),
          error: err =>
            patchState(store, {
              loading: false,
              error: getErrorMessage(err, 'Failed to load the control-plane snapshot'),
            }),
        })
      );

    return {
      startPolling: rxMethod<void>(
        pipe(
          switchMap(() =>
            timer(0, POLL_INTERVAL_MS).pipe(
              filter(() => store.live() && !document.hidden),
              exhaustMap(fetchOnce)
            )
          )
        )
      ),

      refresh: rxMethod<void>(pipe(exhaustMap(fetchOnce))),

      toggleLive(): void {
        patchState(store, {live: !store.live()});
      },
    };
  })
);

/** Edge movement between two consecutive snapshots, keyed by task identity. */
function diffActivity(prev: ControlPlaneSnapshot, cur: ControlPlaneSnapshot): EdgeActivity {
  const prevById = new Map(prev.liveTasks.map(t => [t.id, t]));
  const curIds = new Set(cur.liveTasks.map(t => t.id));

  let intent = false;
  let reconcile = false;
  let claim = false;
  let retry = false;
  for (const t of cur.liveTasks) {
    const before = prevById.get(t.id);
    if (!before) {
      if (t.priority < 0) {
        reconcile = true;
      } else if (t.attempts === 0) {
        intent = true;
      }
      if (t.state === 'leased') {
        claim = true;
      }
      continue;
    }
    if (t.state === 'leased' && before.state === 'pending') {
      claim = true;
    }
    if (t.state === 'pending' && before.state === 'leased') {
      retry = true;
    }
  }

  const deadDelta = Math.max(0, cur.queue.failed - prev.queue.failed);
  let vanished = 0;
  for (const id of prevById.keys()) {
    if (!curIds.has(id)) {
      vanished++;
    }
  }
  return {
    intent,
    reconcile,
    claim,
    retry,
    complete: vanished > deadDelta,
    dead: deadDelta > 0,
  };
}
