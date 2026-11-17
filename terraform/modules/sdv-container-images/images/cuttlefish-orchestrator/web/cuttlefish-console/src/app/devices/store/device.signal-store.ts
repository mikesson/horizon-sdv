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
import {Router} from '@angular/router';
import {tapResponse} from '@ngrx/operators';
import {patchState, signalStore, withComputed, withMethods, withState} from '@ngrx/signals';
import {
  addEntity,
  removeEntities,
  removeEntity,
  setEntity,
  updateEntity,
} from '@ngrx/signals/entities';
import {rxMethod} from '@ngrx/signals/rxjs-interop';
import {EMPTY, forkJoin, of, pipe, timer} from 'rxjs';
import {catchError, exhaustMap, filter, map, switchMap, take, tap} from 'rxjs/operators';

import {withEntityList} from '../../shared/store/entity-list.feature';
import {fail, MAX_POLL_TICKS, pollUntilTerminal} from '../../shared/store/entity-ops';
import {getErrorMessage} from '../../shared/utils';
import type {
  CreateDeviceRequest,
  Device,
  DeviceListFilter,
  ExtendRequest,
  PinRequest,
} from '../models/device.model';
import type {InstanceTarget} from '../models/instance-target';
import type {Operation} from '../models/operation.model';
import type {Snapshot} from '../models/snapshot.model';
import {DeviceApiService} from '../services/device-api.service';

/** UI state layered on top of the shared entity-list feature. */
interface DeviceUiState {
  selectedId: string | null;
  filter: DeviceListFilter;
  /**
   * The selected device's recent operation handles, newest first.
   *
   * These are the only place a restart or powerwash outcome can appear: the
   * device row is identical before and after, so the status poll can never
   * change. A durable operation nobody looks at is a silently swallowed error,
   * and before handles the user at least got a loud 5xx, so surfacing them is
   * the mitigation rather than polish.
   */
  operations: Operation[];
  /**
   * The selected device's snapshot inventory, newest first. Server state
   * only: a verb pushes its operation handle and re-reads the list, so every
   * row here (including `creating`) is what the backend recorded.
   */
  snapshots: Snapshot[];
}

const initialUiState: DeviceUiState = {selectedId: null, filter: {}, operations: [], snapshots: []};

/**
 * Stable statuses that end a status poll, meaning every state that is not
 * actively transitioning. 'stopped' matters: without it, watching a stopped
 * device polls until the MAX_POLL_TICKS limit.
 */
const DEVICE_TERMINAL = ['running', 'stopped', 'error', 'deleted'];

/**
 * The central state management store for the Devices feature.
 *
 * Read-side (loading/error/total state, entity collection, loadAll/loadOne) is
 * the shared `withEntityList` feature; the Hosts store mirrors it. This store
 * layers device-specific selection/filter state, derived stats, and the CVD
 * lifecycle mutations on top.
 */
export const DeviceStore = signalStore(
  {providedIn: 'root'},
  withState(initialUiState),
  withEntityList<Device, DeviceListFilter>({
    api: () => inject(DeviceApiService),
    label: 'devices',
  }),

  withComputed(store => ({
    /** The currently selected device entity, or null. */
    selectedDevice: computed(() => {
      const id = store.selectedId();
      return id ? (store.entityMap()[id] ?? null) : null;
    }),

    /** Operations still in flight on the selected device. */
    activeOperations: computed(() => store.operations().filter(op => !op.done)),

    /**
     * Operations that settled as failures. Surfaced separately because this is
     * the signal that replaced a loud 5xx: a failed powerwash has no other
     * observable trace.
     */
    failedOperations: computed(() => store.operations().filter(op => op.done && !!op.error)),

    /** Aggregated statistics for the current device collection. */
    stats: computed(() => {
      const devices = store.entities();
      const expiringSoon = devices.filter(d => {
        if (d.lifecycle?.mode !== 'ephemeral' || !d.lifecycle.expiresAt) {
          return false;
        }
        const expiresAt =
          d.lifecycle.expiresAt instanceof Date
            ? d.lifecycle.expiresAt
            : new Date(d.lifecycle.expiresAt);
        const remainingMs = expiresAt.getTime() - Date.now();
        // Warn if expiration is within 30 minutes.
        return remainingMs > 0 && remainingMs < 30 * 60 * 1000;
      }).length;

      return {
        total: devices.length,
        running: devices.filter(d => d.status === 'running').length,
        stopped: devices.filter(d => d.status === 'stopped').length,
        error: devices.filter(d => d.status === 'error').length,
        creating: devices.filter(d => d.status === 'creating').length,
        pinned: devices.filter(d => d.lifecycle?.mode === 'pinned').length,
        ephemeral: devices.filter(d => d.lifecycle?.mode === 'ephemeral').length,
        expiringSoon,
      };
    }),
  })),

  withMethods(store => {
    const deviceApi = inject(DeviceApiService);
    const snackBar = inject(MatSnackBar);
    const router = inject(Router);

    return {
      /** Updates the list filter criteria. */
      updateFilter(filter: DeviceListFilter) {
        patchState(store, {filter: {...store.filter(), ...filter}});
      },

      /** Sets the currently selected device ID (or null to deselect). */
      selectDevice(id: string | null) {
        patchState(store, {selectedId: id});
      },

      /**
       * Keeps a detail view live while the device is transitional: polls until
       * the status stabilizes, then stops. The store is root-scoped, so the
       * calling view must destroy the returned ref when it leaves. An
       * uncancelled watch keeps polling after navigation.
       */
      watch: rxMethod<string>(
        pipe(
          switchMap(id =>
            pollUntilTerminal(
              deviceId => deviceApi.get(deviceId),
              id,
              DEVICE_TERMINAL,
              updated => patchState(store, updateEntity({id: updated.id, changes: updated}))
            )
          )
        )
      ),

      /**
       * Creates a device, then polls its status until terminal so the UI
       * reflects creating -> running without a manual refresh.
       */
      create: rxMethod<{request: CreateDeviceRequest; idempotencyKey: string}>(
        // switchMap (not exhaustMap): the trailing status poll keeps the stream
        // alive for minutes, so exhaustMap would block a second create until the
        // first device finished provisioning. That also means a second create
        // unsubscribes the first while the server may already have committed
        // it. The caller's Idempotency-Key is what keeps that from becoming a
        // second device.
        pipe(
          tap(() => patchState(store, {loading: true, error: null})),
          switchMap(({request, idempotencyKey}) =>
            deviceApi.create(request, idempotencyKey).pipe(
              tapResponse({
                next: device => {
                  patchState(store, addEntity(device), {loading: false});
                  snackBar.open(`Device "${device.name}" created`, 'Close', {duration: 3000});
                  void router.navigate(['/devices', device.id]);
                },
                error: (err: unknown) =>
                  patchState(store, {
                    loading: false,
                    error: fail(snackBar, 'Failed to create device', err),
                  }),
              }),
              switchMap(device =>
                pollUntilTerminal(
                  id => deviceApi.get(id),
                  device.id,
                  DEVICE_TERMINAL,
                  updated => patchState(store, updateEntity({id: updated.id, changes: updated}))
                )
              )
            )
          )
        )
      ),

      /**
       * Deletes a device. A 404/503 means it is already gone or mid-teardown, so
       * it is treated as a successful delete rather than surfaced as an error.
       */
      delete: rxMethod<string>(
        pipe(
          tap(() => patchState(store, {loading: true, error: null})),
          exhaustMap(id =>
            deviceApi.delete(id).pipe(
              tapResponse({
                next: () => {
                  patchState(store, removeEntity(id), {loading: false});
                  snackBar.open('Device deleted', 'Close', {duration: 3000});
                },
                error: (err: unknown) => {
                  const message = getErrorMessage(err, 'Failed to delete device');
                  if (message.includes('404') || message.includes('503')) {
                    patchState(store, removeEntity(id), {loading: false});
                    snackBar.open('Device deleted', 'Close', {duration: 3000});
                  } else {
                    patchState(store, {loading: false, error: message});
                    snackBar.open(message, 'Close', {duration: 5000});
                  }
                },
              })
            )
          )
        )
      ),

      /**
       * Bulk-deletes devices by fanning out to the per-device endpoint (there is
       * no batch route). Each 404/503 counts as already-gone; successes leave the
       * collection and the toast summarizes; failures stay so the user can retry.
       */
      deleteMany: rxMethod<string[]>(
        pipe(
          filter(ids => ids.length > 0),
          tap(() => patchState(store, {loading: true, error: null})),
          exhaustMap(ids =>
            forkJoin(
              ids.map(id =>
                deviceApi.delete(id).pipe(
                  map(() => ({id, ok: true})),
                  catchError((err: unknown) => {
                    const message = getErrorMessage(err, '');
                    const gone = message.includes('404') || message.includes('503');
                    return of({id, ok: gone});
                  })
                )
              )
            ).pipe(
              tap(results => {
                const deleted = results.filter(r => r.ok).map(r => r.id);
                const failed = results.length - deleted.length;
                patchState(store, removeEntities(deleted), {loading: false});
                snackBar.open(
                  failed === 0
                    ? `Deleted ${deleted.length} devices`
                    : `Deleted ${deleted.length}, ${failed} failed`,
                  'Close',
                  {duration: 4000}
                );
              })
            )
          )
        )
      ),

      /** Starts a stopped device. */
      start: rxMethod<string>(
        pipe(
          exhaustMap(id =>
            deviceApi.start(id).pipe(
              tapResponse({
                next: () => {
                  patchState(
                    store,
                    updateEntity({id, changes: {status: 'running', startedAt: new Date()}})
                  );
                  snackBar.open('Device starting...', 'Close', {duration: 3000});
                },
                error: (err: unknown) => fail(snackBar, 'Failed to start device', err),
              })
            )
          )
        )
      ),

      /** Stops a running device. */
      stop: rxMethod<string>(
        pipe(
          exhaustMap(id =>
            deviceApi.stop(id).pipe(
              tapResponse({
                next: () => {
                  patchState(
                    store,
                    updateEntity({id, changes: {status: 'stopped', stoppedAt: new Date()}})
                  );
                  snackBar.open('Device stopping...', 'Close', {duration: 3000});
                },
                error: (err: unknown) => fail(snackBar, 'Failed to stop device', err),
              })
            )
          )
        )
      ),

      /** Loads the selected device's operation handles once. */
      loadOperations: rxMethod<string>(
        pipe(
          switchMap(id =>
            deviceApi.listOperations(id).pipe(
              tapResponse({
                next: (operations: Operation[]) => patchState(store, {operations}),
                // A failed operations read must not disrupt the page: the device
                // itself still renders, and the next poll retries.
                error: () => undefined,
              })
            )
          )
        )
      ),

      /**
       * Keeps a detail view's operations live while any verb is in flight.
       *
       * This cannot ride the device `watch`: that poll stops once the device
       * status is terminal, and `running` IS terminal, which is exactly the
       * state a restart leaves the device in for its whole duration. So the
       * operation poll needs its own stop condition: every operation settled.
       *
       * The store is root-scoped, so the calling view must destroy the returned
       * ref when it leaves.
       */
      watchOperations: rxMethod<string>(
        pipe(
          switchMap(id =>
            timer(0, 2000).pipe(
              take(MAX_POLL_TICKS),
              switchMap(() => deviceApi.listOperations(id).pipe(catchError(() => EMPTY))),
              tap((operations: Operation[]) => patchState(store, {operations})),
              filter((operations: Operation[]) => operations.every(op => op.done)),
              take(1)
            )
          )
        )
      ),

      /**
       * Relaunches a running device's CVD in place; the row stays running.
       *
       * The response is a handle, not a status. It is pushed into state
       * immediately so the card shows the verb as in flight before the next
       * poll. The alternative is a three-second snackbar and then nothing.
       */
      restart: rxMethod<InstanceTarget>(
        pipe(
          exhaustMap(target =>
            deviceApi.restart(target).pipe(
              tapResponse({
                next: (op: Operation) => {
                  patchState(store, {operations: [op, ...store.operations()]});
                  snackBar.open('Device restarting...', 'Close', {duration: 3000});
                },
                error: (err: unknown) => fail(snackBar, 'Failed to restart device', err),
              })
            )
          )
        )
      ),

      /** Resets a running device to first-boot state; the row stays running. */
      powerwash: rxMethod<InstanceTarget>(
        pipe(
          exhaustMap(target =>
            deviceApi.powerwash(target).pipe(
              tapResponse({
                next: (op: Operation) => {
                  patchState(store, {operations: [op, ...store.operations()]});
                  snackBar.open('Device powerwashing...', 'Close', {duration: 3000});
                },
                error: (err: unknown) => fail(snackBar, 'Failed to powerwash device', err),
              })
            )
          )
        )
      ),

      /** Loads the selected device's snapshot inventory. */
      loadSnapshots: rxMethod<string>(
        pipe(
          switchMap(id =>
            deviceApi.listSnapshots(id).pipe(
              tapResponse({
                next: (snapshots: Snapshot[]) => patchState(store, {snapshots}),
                // A failed read must not disrupt the page; the next action or
                // refresh re-reads.
                error: () => undefined,
              })
            )
          )
        )
      ),

      /** Removes one of a device's snapshots. */
      deleteSnapshot: rxMethod<{deviceId: string; snapshotId: string}>(
        pipe(
          exhaustMap(({deviceId, snapshotId}) =>
            deviceApi.deleteSnapshot(deviceId, snapshotId).pipe(
              tap((op: Operation) => {
                patchState(store, {operations: [op, ...store.operations()]});
                snackBar.open('Deleting snapshot...', 'Close', {duration: 3000});
              }),
              switchMap(() => deviceApi.listSnapshots(deviceId).pipe(catchError(() => EMPTY))),
              tap((snapshots: Snapshot[]) => patchState(store, {snapshots})),
              catchError((err: unknown) => {
                fail(snackBar, 'Failed to delete snapshot', err);
                return EMPTY;
              })
            )
          )
        )
      ),

      /** Presses one instance's power button. */
      powerbtn: rxMethod<InstanceTarget>(
        pipe(
          exhaustMap(target =>
            deviceApi.powerbtn(target).pipe(
              tapResponse({
                next: () => {
                  snackBar.open('Power button pressed', 'Close', {duration: 3000});
                },
                error: (err: unknown) => fail(snackBar, 'Failed to press power button', err),
              })
            )
          )
        )
      ),

      /** Starts a screen recording; the row stays running. */
      recordStart: rxMethod<InstanceTarget>(
        pipe(
          exhaustMap(target =>
            deviceApi.recordStart(target).pipe(
              tapResponse({
                next: () => {
                  snackBar.open('Recording started', 'Close', {duration: 3000});
                },
                error: (err: unknown) => fail(snackBar, 'Failed to start recording', err),
              })
            )
          )
        )
      ),

      /** Stops the screen recording; the row stays running. */
      recordStop: rxMethod<InstanceTarget>(
        pipe(
          exhaustMap(target =>
            deviceApi.recordStop(target).pipe(
              tapResponse({
                next: () => {
                  snackBar.open('Recording stopped', 'Close', {duration: 3000});
                },
                error: (err: unknown) => fail(snackBar, 'Failed to stop recording', err),
              })
            )
          )
        )
      ),

      /** Pins a device (prevents auto-reaping and protects its host). */
      pin: rxMethod<{id: string; request: PinRequest}>(
        pipe(
          tap(() => patchState(store, {loading: true, error: null})),
          exhaustMap(({id, request}) =>
            deviceApi.pin(id, request).pipe(
              tapResponse({
                next: device => {
                  patchState(store, setEntity(device), {loading: false});
                  snackBar.open('Device pinned - host is now protected', 'Close', {duration: 3000});
                },
                error: (err: unknown) =>
                  patchState(store, {
                    loading: false,
                    error: fail(snackBar, 'Failed to pin device', err),
                  }),
              })
            )
          )
        )
      ),

      /** Unpins a device (restores auto-reaping). */
      unpin: rxMethod<string>(
        pipe(
          tap(() => patchState(store, {loading: true, error: null})),
          exhaustMap(id =>
            deviceApi.unpin(id).pipe(
              tapResponse({
                next: device => {
                  patchState(store, setEntity(device), {loading: false});
                  snackBar.open('Device unpinned - TTL timer resumed', 'Close', {duration: 3000});
                },
                error: (err: unknown) =>
                  patchState(store, {
                    loading: false,
                    error: fail(snackBar, 'Failed to unpin device', err),
                  }),
              })
            )
          )
        )
      ),

      /** Extends the TTL of an ephemeral device. */
      extend: rxMethod<{id: string; request?: ExtendRequest}>(
        pipe(
          tap(() => patchState(store, {loading: true, error: null})),
          exhaustMap(({id, request}) =>
            deviceApi.extend(id, request).pipe(
              tapResponse({
                next: device => {
                  patchState(store, setEntity(device), {loading: false});
                  const remaining = device.lifecycle?.expiresAt
                    ? new Date(device.lifecycle.expiresAt).toLocaleTimeString()
                    : 'extended';
                  snackBar.open(`TTL extended until ${remaining}`, 'Close', {duration: 3000});
                },
                error: (err: unknown) =>
                  patchState(store, {
                    loading: false,
                    error: fail(snackBar, 'Failed to extend device TTL', err),
                  }),
              })
            )
          )
        )
      ),
    };
  })
);
