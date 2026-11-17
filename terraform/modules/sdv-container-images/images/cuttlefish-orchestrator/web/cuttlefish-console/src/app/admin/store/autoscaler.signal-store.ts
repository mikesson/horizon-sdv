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
import {MatSnackBar} from '@angular/material/snack-bar';
import {tapResponse} from '@ngrx/operators';
import {patchState, signalStore, withComputed, withMethods, withState} from '@ngrx/signals';
import {rxMethod} from '@ngrx/signals/rxjs-interop';
import type {Observable} from 'rxjs';
import {exhaustMap, filter, pipe, switchMap, timer} from 'rxjs';
import {getErrorMessage} from '../../shared/utils';
import type {
  AutoscalerSnapshot,
  LadderRung,
  StandbyRecommendation,
} from '../models/autoscaler.model';
import type {
  AutoscaleMode,
  FleetSchedule,
  FleetSettings,
  FleetTier,
} from '../models/control-plane.model';
import {DEFAULT_FLEET_TIER} from '../models/control-plane.model';
import {AutoscalerApiService} from '../services/autoscaler-api.service';

/** A shape order the server refused, kept beside the pool whose order it was. */
interface LadderRefusal {
  pool: string;
  message: string;
}

/**
 * What a pool nobody has switched on asks for: nothing, of either kind, with no
 * windows and the catalog's own choice of shape. A card for such a pool is an
 * offer rather than a blank form, so it needs a baseline to draw and to compare
 * an edit against.
 */
export const UNSET_POOL_INTENT: FleetTier = {standby: 0, floor: 0, ladder: [], schedules: []};

/** Poll cadence while the page is visible and live, matching Control Plane. */
export const AUTOSCALER_POLL_MS = 5000;

interface AutoscalerState {
  snapshot: AutoscalerSnapshot | null;
  /** The stored policy: the controls' baseline, reloaded after every write. */
  policy: FleetSettings | null;
  recommendation: StandbyRecommendation | null;

  /**
   * Three errors, never one. Each band loads and fails alone. One dead endpoint
   * must not blank a page whose other two thirds are live, and the
   * recommendation in particular is a multi-day scan that can fail while the
   * fleet picture is perfectly healthy.
   */
  error: string | null;
  policyError: string | null;
  recommendationError: string | null;

  /**
   * The last refusal of a shape-order write, kept until the next attempt, with
   * the pool whose order was refused.
   *
   * Its own field, beside the snackbar every failed write already raises,
   * because this refusal names a SHAPE: "shape_ladder[1] is disabled in the
   * machine-type catalog" is unreadable six seconds after it appears and
   * nowhere near the list it is about. The snackbar says a save failed; this is
   * what says which shape and why, beside the shape. The pool travels with it
   * because every pool has an order of its own, and a refusal drawn under all of
   * them would blame orders nobody wrote.
   */
  ladderError: LadderRefusal | null;

  loading: boolean;
  saving: boolean;
  live: boolean;
}

const initialState: AutoscalerState = {
  snapshot: null,
  policy: null,
  recommendation: null,
  error: null,
  policyError: null,
  recommendationError: null,
  ladderError: null,
  loading: true,
  saving: false,
  live: true,
};

/**
 * The Autoscaler page's state. Provided at the component level so polling stops
 * when the page is left.
 */
export const AutoscalerStore = signalStore(
  withState(initialState),
  withComputed(store => ({
    /**
     * What each pool's operator stored, keyed by pool. A pool with no entry is
     * one nobody has switched on, and its intent is the zero one, which is what
     * makes a card for it render as an offer rather than as a blank form.
     */
    intents: computed<Record<string, FleetTier>>(() => store.policy()?.tiers ?? {}),
    /** The machines the DEFAULT pool is asked to keep free, 0 before the policy lands. */
    standby: computed(() => store.policy()?.tiers[DEFAULT_FLEET_TIER]?.standby ?? 0),
    /** The machines the DEFAULT pool is asked to keep running. */
    floor: computed(() => store.policy()?.tiers[DEFAULT_FLEET_TIER]?.floor ?? 0),
    /**
     * Each pool's order judged against what provisioning knows right now: what
     * each shape would hold, where it can go, and which one is being built.
     * Keyed to the STORED order, so it says nothing about an unsaved edit.
     */
    servedLadders: computed<Record<string, LadderRung[]>>(() =>
      Object.fromEntries((store.snapshot()?.pools ?? []).map(pool => [pool.pool, pool.ladder]))
    ),
    /** The shape a machine is built as when its pool stores no order. */
    defaultShape: computed(() => store.snapshot()?.densityMachineType ?? ''),
    /** Whether an arm64 machine can be built here, which the arm card states. */
    armImageReady: computed(() => store.snapshot()?.armImageReady ?? false),
    /**
     * Device slots per host of target, server-derived. The page never
     * multiplies two figures from two endpoints; this only names the one the
     * server already computed so a control can render "3 hosts = 9 slots".
     */
    density: computed(() => store.snapshot()?.effectiveDensity ?? 0),
  })),
  withMethods(store => {
    const api = inject(AutoscalerApiService);
    const snackBar = inject(MatSnackBar);

    const fetchSnapshot = (): Observable<unknown> =>
      api.snapshot().pipe(
        tapResponse({
          next: snapshot => patchState(store, {snapshot, loading: false, error: null}),
          error: (err: unknown) =>
            patchState(store, {
              loading: false,
              error: getErrorMessage(err, 'Failed to load the autoscaler snapshot'),
            }),
        })
      );

    const fetchPolicy = (): Observable<unknown> =>
      api.policy().pipe(
        tapResponse({
          next: policy => patchState(store, {policy, policyError: null}),
          error: (err: unknown) =>
            patchState(store, {
              policyError: getErrorMessage(err, 'Failed to load the autoscaler policy'),
            }),
        })
      );

    /**
     * Every write reloads the policy rather than trusting its own response: a
     * save makes values stored, and only the server can say which. The snapshot
     * refreshes with it so the target and the fleet catch up in one step
     * instead of on the next poll tick.
     */
    const applied = (
      write: Observable<unknown>,
      failure: string,
      onFailure?: (message: string) => void
    ): void => {
      patchState(store, {saving: true});
      write
        .pipe(
          tapResponse({
            next: () => patchState(store, {saving: false}),
            error: (err: unknown) => {
              patchState(store, {saving: false});
              const message = getErrorMessage(err, failure);
              onFailure?.(message);
              snackBar.open(message, 'Close', {duration: 6000});
            },
          }),
          // Reached on success only: tapResponse swallows the failure above.
          switchMap(() => fetchPolicy()),
          switchMap(() => fetchSnapshot())
        )
        .subscribe();
    };

    return {
      startPolling: rxMethod<void>(
        pipe(
          switchMap(() =>
            timer(0, AUTOSCALER_POLL_MS).pipe(
              filter(() => store.live() && !document.hidden),
              exhaustMap(fetchSnapshot)
            )
          )
        )
      ),

      refresh: rxMethod<void>(pipe(exhaustMap(fetchSnapshot))),

      loadPolicy: rxMethod<void>(pipe(exhaustMap(fetchPolicy))),

      loadRecommendation: rxMethod<void>(
        pipe(
          exhaustMap(() =>
            api.recommendation().pipe(
              tapResponse({
                next: recommendation =>
                  patchState(store, {recommendation, recommendationError: null}),
                error: (err: unknown) =>
                  patchState(store, {
                    recommendationError: getErrorMessage(
                      err,
                      'Failed to read the observation window'
                    ),
                  }),
              })
            )
          )
        )
      ),

      /**
       * The mode and one pool's bounds. Writing bounds for a pool is what
       * switches it on, so this is also how a pool is enabled, and why the
       * caller has to name which pool it is editing rather than the store
       * assuming the default one.
       */
      saveAutoscaler(mode: AutoscaleMode, pool: string, standby: number, floor: number): void {
        applied(api.saveAutoscaler(mode, pool, standby, floor), 'Failed to change the autoscaler');
      },

      /** A pool switched off: its bounds, windows and order go together. */
      disablePool(pool: string): void {
        applied(api.disablePool(pool), `Failed to switch the ${pool} pool off`);
      },

      saveTuning(demandHorizonSec: number, drainGraceSec: number): void {
        applied(api.saveTuning(demandHorizonSec, drainGraceSec), 'Failed to save the tuning');
      },

      saveSchedules(pool: string, schedules: FleetSchedule[]): void {
        applied(api.saveSchedules(pool, schedules), 'Failed to save the windows');
      },

      /**
       * The refusal is kept as well as raised: a shape named in a snackbar is a
       * shape the operator has to remember while looking back at the list.
       * Cleared on the way in, so a stale refusal never outlives the order it
       * refused.
       */
      saveLadder(pool: string, rungs: string[]): void {
        patchState(store, {ladderError: null});
        applied(api.saveLadder(pool, rungs), 'Failed to save the shape order', message =>
          patchState(store, {ladderError: {pool, message}})
        );
      },

      toggleLive(): void {
        patchState(store, {live: !store.live()});
      },
    };
  })
);
