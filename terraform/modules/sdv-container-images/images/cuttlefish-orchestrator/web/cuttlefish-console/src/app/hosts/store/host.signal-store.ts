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
import {addEntity, removeEntities, removeEntity, updateEntity} from '@ngrx/signals/entities';
import {rxMethod} from '@ngrx/signals/rxjs-interop';
import {forkJoin, of, pipe} from 'rxjs';
import {catchError, exhaustMap, filter, map, switchMap, tap} from 'rxjs/operators';

import type {CatalogErrorDetails} from '../../shared/errors/api.error';
import {CatalogValidationError} from '../../shared/errors/api.error';
import {withEntityList} from '../../shared/store/entity-list.feature';
import {pollUntilTerminal} from '../../shared/store/entity-ops';
import {getErrorMessage} from '../../shared/utils';
import type {CreateHostRequest, Host, HostListFilter, RetryHostRequest} from '../models/host.model';
import {HostApiService} from '../services/host-api.service';

/** UI state layered on top of the shared entity-list feature. */
interface HostUiState {
  selectedId: string | null;
  catalogError: CatalogErrorDetails | null;
}

const initialUiState: HostUiState = {selectedId: null, catalogError: null};

/**
 * Stable statuses that end a create/retry status poll, meaning every state
 * that is not transitioning ('unreachable' keeps polling: the device controller
 * resolves it to running or error, bounded by the poll's tick budget).
 */
const HOST_TERMINAL = ['running', 'stopped', 'error', 'deleted', 'preempted'];

/**
 * The central state management store for the Hosts feature.
 *
 * Read-side (loading/error/total state, entity collection, loadAll/loadOne) is
 * the shared `withEntityList` feature; the Devices store mirrors it. This store
 * layers host-specific selection/catalog-error state, derived stats, and the
 * GCE lifecycle mutations (with optimistic transitions + revert-on-error) on top.
 */
export const HostStore = signalStore(
  {providedIn: 'root'},
  withState(initialUiState),
  withEntityList<Host, HostListFilter>({
    api: () => inject(HostApiService),
    label: 'hosts',
  }),

  withComputed(store => ({
    /** The currently selected host entity, or null. */
    selectedHost: computed(() => {
      const id = store.selectedId();
      return id ? store.entityMap()[id] : null;
    }),

    /** All hosts currently in 'running' state. */
    runningHosts: computed(() => store.entities().filter(h => h.status === 'running')),

    /** Hosts a device can be created on: running with free capacity. */
    availableForDevice: computed(() =>
      store.entities().filter(h => h.status === 'running' && h.currentDevices < h.maxDevices)
    ),

    /** Aggregated statistics about the host fleet. */
    stats: computed(() => {
      const entities = store.entities();
      return {
        total: entities.length,
        running: entities.filter(h => h.status === 'running').length,
        stopped: entities.filter(h => h.status === 'stopped').length,
        error: entities.filter(h => h.status === 'error').length,
        preempted: entities.filter(h => h.status === 'preempted').length,
        provisioning: entities.filter(h => h.status === 'provisioning').length,
        protected: entities.filter(h => h.lifecycle?.mode === 'protected').length,
        spotVMs: entities.filter(h => h.provisioningModel === 'SPOT').length,
        totalDevices: entities.reduce((sum, h) => sum + (h.currentDevices || 0), 0),
        maxDevices: entities.reduce((sum, h) => sum + (h.maxDevices || 0), 0),
      };
    }),

    /** Unique, sorted list of GCP zones where hosts are deployed. */
    zones: computed(() => {
      const zones = new Set(store.entities().map(h => h.zone));
      return Array.from(zones).sort();
    }),
  })),

  withMethods(store => {
    const hostApi = inject(HostApiService);
    const snackBar = inject(MatSnackBar);
    const router = inject(Router);

    // Undo an optimistic status change by refetching the authoritative row.
    const revert = (id: string): void => {
      hostApi.get(id).subscribe(h => patchState(store, updateEntity({id, changes: h})));
    };

    return {
      /** Sets the currently active host ID for detail views. */
      selectHost(id: string | null) {
        patchState(store, {selectedId: id});
      },

      /**
       * Keeps a detail view live while the host is transitional. It polls
       * until the status stabilizes, then stops. The store is root-scoped, so
       * the calling view must destroy the returned ref when it leaves. An
       * uncancelled watch keeps polling after navigation.
       */
      watch: rxMethod<string>(
        pipe(
          switchMap(id =>
            pollUntilTerminal(
              hostId => hostApi.get(hostId),
              id,
              HOST_TERMINAL,
              updated => patchState(store, updateEntity({id: updated.id, changes: updated}))
            )
          )
        )
      ),

      /**
       * Provisions a new GCE host, then polls its status until terminal so the
       * UI reflects provisioning -> running without a manual refresh.
       */
      create: rxMethod<{request: CreateHostRequest; idempotencyKey: string}>(
        // switchMap rather than exhaustMap. The trailing status poll keeps the
        // stream alive for the full provision, which would otherwise block new
        // creates. That also means a second create unsubscribes the first
        // while the server may already have committed it, and the caller's
        // Idempotency-Key is what keeps that from becoming a second billed VM.
        pipe(
          tap(() => patchState(store, {loading: true, error: null, catalogError: null})),
          switchMap(({request, idempotencyKey}) =>
            hostApi.create(request, idempotencyKey).pipe(
              tapResponse({
                next: (host: Host) => {
                  patchState(store, addEntity(host), {loading: false});
                  snackBar.open($localize`:@@hostCreated:Host "${host.name}" created`, 'Dismiss', {
                    duration: 5000,
                  });
                  // The detail page is the manual journey's next step: it shows
                  // provisioning progress and carries the Create Device button.
                  void router.navigate(['/hosts', host.id]);
                },
                error: (err: unknown) => {
                  if (err instanceof CatalogValidationError) {
                    patchState(store, {
                      loading: false,
                      error: err.message,
                      catalogError: err.details,
                    });
                  } else {
                    patchState(store, {
                      loading: false,
                      error: getErrorMessage(err, 'Failed to create host'),
                      catalogError: null,
                    });
                  }
                },
              }),
              switchMap((host: Host) =>
                pollUntilTerminal(
                  id => hostApi.get(id),
                  host.id,
                  HOST_TERMINAL,
                  updated => patchState(store, updateEntity({id: updated.id, changes: updated}))
                )
              )
            )
          )
        )
      ),

      /**
       * Deletes a host. Optimistically flips it to 'deleting'; a 404/503 means it
       * is already gone, so it is treated as success; a real error reverts the
       * optimistic status by refetching.
       */
      delete: rxMethod<string>(
        pipe(
          tap((id: string) => {
            patchState(store, {error: null}, updateEntity({id, changes: {status: 'deleting'}}));
          }),
          exhaustMap((id: string) =>
            hostApi.delete(id).pipe(
              tapResponse({
                next: () => {
                  patchState(store, removeEntity(id), {selectedId: null});
                  snackBar.open(
                    $localize`:@@hostDeleted:Host deleted`,
                    $localize`:@@dismiss:Dismiss`,
                    {duration: 3000}
                  );
                  void router.navigate(['/hosts']);
                },
                error: (err: unknown) => {
                  const message = getErrorMessage(err, 'Failed to delete host');
                  if (message.includes('404') || message.includes('503')) {
                    patchState(store, removeEntity(id), {selectedId: null});
                    snackBar.open(
                      $localize`:@@hostDeleted:Host deleted`,
                      $localize`:@@dismiss:Dismiss`,
                      {duration: 3000}
                    );
                    void router.navigate(['/hosts']);
                  } else {
                    patchState(store, {error: message});
                    revert(id);
                  }
                },
              })
            )
          )
        )
      ),

      /**
       * Bulk-deletes hosts by fanning out to the per-host endpoint (there is no
       * batch route). Each host optimistically flips to 'deleting'; a 404/503 or
       * success drops it, a real error reverts it. The toast summarizes; failed
       * rows stay so the user can retry.
       */
      deleteMany: rxMethod<string[]>(
        pipe(
          filter(ids => ids.length > 0),
          tap((ids: string[]) => {
            patchState(store, {error: null});
            for (const id of ids) {
              patchState(store, updateEntity({id, changes: {status: 'deleting'}}));
            }
          }),
          exhaustMap((ids: string[]) =>
            forkJoin(
              ids.map(id =>
                hostApi.delete(id).pipe(
                  map(() => ({id, ok: true})),
                  catchError((err: unknown) => {
                    const message = getErrorMessage(err, '');
                    const gone = message.includes('404') || message.includes('503');
                    if (!gone) {
                      revert(id);
                    }
                    return of({id, ok: gone});
                  })
                )
              )
            ).pipe(
              tap((results: Array<{id: string; ok: boolean}>) => {
                const deleted = results.filter(r => r.ok).map(r => r.id);
                const failed = results.length - deleted.length;
                patchState(store, removeEntities(deleted), {selectedId: null});
                snackBar.open(
                  failed === 0
                    ? `Deleted ${deleted.length} hosts`
                    : `Deleted ${deleted.length}, ${failed} failed`,
                  'Dismiss',
                  {duration: 4000}
                );
              })
            )
          )
        )
      ),

      /** Starts a stopped host VM; optimistically flips to 'starting'. */
      start: rxMethod<string>(
        pipe(
          tap((id: string) => patchState(store, updateEntity({id, changes: {status: 'starting'}}))),
          exhaustMap((id: string) =>
            hostApi.start(id).pipe(
              tapResponse({
                next: () =>
                  snackBar.open(
                    $localize`:@@hostStarting:Host starting...`,
                    $localize`:@@dismiss:Dismiss`,
                    {duration: 3000}
                  ),
                error: (err: unknown) => {
                  patchState(store, {error: getErrorMessage(err, 'Operation failed')});
                  revert(id);
                },
              })
            )
          )
        )
      ),

      /** Stops a running host VM; optimistically flips to 'stopping'. */
      stop: rxMethod<string>(
        pipe(
          tap((id: string) => patchState(store, updateEntity({id, changes: {status: 'stopping'}}))),
          exhaustMap((id: string) =>
            hostApi.stop(id).pipe(
              tapResponse({
                next: () =>
                  snackBar.open(
                    $localize`:@@hostStopping:Host stopping...`,
                    $localize`:@@dismiss:Dismiss`,
                    {duration: 3000}
                  ),
                error: (err: unknown) => {
                  patchState(store, {error: getErrorMessage(err, 'Operation failed')});
                  revert(id);
                },
              })
            )
          )
        )
      ),

      /**
       * Changes how many devices the host accepts. Not optimistic: the server
       * refuses a limit below the devices already on the host, and showing the
       * new number before that answer arrives would be showing a lie.
       */
      updateMaxDevices: rxMethod<{id: string; maxDevices: number}>(
        pipe(
          exhaustMap(({id, maxDevices}) =>
            hostApi.updateMaxDevices(id, maxDevices).pipe(
              tapResponse({
                next: (host: Host) => {
                  patchState(store, updateEntity({id, changes: {maxDevices: host.maxDevices}}));
                  snackBar.open(
                    $localize`:@@hostCapacityUpdated:Host capacity updated`,
                    $localize`:@@dismiss:Dismiss`,
                    {duration: 3000}
                  );
                },
                error: (err: unknown) => {
                  patchState(store, {error: getErrorMessage(err, 'Operation failed')});
                  snackBar.open(
                    getErrorMessage(err, 'Failed to change host capacity'),
                    $localize`:@@dismiss:Dismiss`,
                    {duration: 6000}
                  );
                },
              })
            )
          )
        )
      ),

      /**
       * Raises a reserved host's lease. Not optimistic, for the same reason as
       * the capacity change: the server may refuse, and its reason is shown
       * verbatim.
       */
      extend: rxMethod<{id: string; hours: number}>(
        pipe(
          exhaustMap(({id, hours}) =>
            hostApi.extend(id, hours).pipe(
              tapResponse({
                next: (host: Host) => {
                  patchState(store, {error: null}, updateEntity({id, changes: host}));
                  snackBar.open(
                    $localize`:@@hostExtended:Host lease extended`,
                    $localize`:@@dismiss:Dismiss`,
                    {duration: 3000}
                  );
                },
                error: (err: unknown) => {
                  patchState(store, {error: getErrorMessage(err, 'Operation failed')});
                  snackBar.open(
                    getErrorMessage(err, 'Failed to extend host'),
                    $localize`:@@dismiss:Dismiss`,
                    {duration: 6000}
                  );
                },
              })
            )
          )
        )
      ),

      /** Forces a host's state to sync with GCE. */
      sync: rxMethod<string>(
        pipe(
          tap(() => patchState(store, {loading: true})),
          exhaustMap((id: string) =>
            hostApi.sync(id).pipe(
              tapResponse({
                next: () => {
                  patchState(store, {loading: false});
                  snackBar.open(
                    $localize`:@@hostSyncStarted:Host sync started`,
                    $localize`:@@dismiss:Dismiss`,
                    {duration: 3000}
                  );
                },
                error: (err: unknown) =>
                  patchState(store, {
                    loading: false,
                    error: getErrorMessage(err, 'Operation failed'),
                  }),
              })
            )
          )
        )
      ),

      /**
       * Retries a preempted or failed host, then polls until terminal.
       * Optimistically flips to 'provisioning'; a real error reverts.
       */
      retry: rxMethod<{id: string; request?: RetryHostRequest}>(
        pipe(
          tap(({id}) =>
            patchState(store, {error: null}, updateEntity({id, changes: {status: 'provisioning'}}))
          ),
          switchMap(({id, request}) =>
            hostApi.retry(id, request).pipe(
              tapResponse({
                next: (host: Host) => {
                  patchState(store, updateEntity({id: host.id, changes: host}));
                  snackBar
                    .open(
                      $localize`:@@hostRetrying:Host retry initiated`,
                      $localize`:@@view:View`,
                      {duration: 5000}
                    )
                    .onAction()
                    .subscribe(() => void router.navigate(['/hosts', host.id]));
                },
                error: (err: unknown) => {
                  patchState(store, {error: getErrorMessage(err, 'Operation failed')});
                  revert(id);
                },
              }),
              switchMap((host: Host) =>
                pollUntilTerminal(
                  id => hostApi.get(id),
                  host.id,
                  HOST_TERMINAL,
                  updated => patchState(store, updateEntity({id: updated.id, changes: updated}))
                )
              )
            )
          )
        )
      ),
    };
  })
);
