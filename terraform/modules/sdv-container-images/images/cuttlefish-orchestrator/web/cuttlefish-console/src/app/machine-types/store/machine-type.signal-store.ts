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
import {setAllEntities, withEntities} from '@ngrx/signals/entities';
import {rxMethod} from '@ngrx/signals/rxjs-interop';
import {pipe} from 'rxjs';
import {switchMap, tap} from 'rxjs/operators';

import {getErrorMessage} from '../../shared/utils';
import type {MachineType} from '../models';
import {MachineTypeApiService} from '../services';

interface MachineTypeState {
  loading: boolean;
  error: string | null;
  loaded: boolean;
}

const initialState: MachineTypeState = {
  loading: false,
  error: null,
  loaded: false,
};

/**
 * Signal store for machine types.
 * Caches machine types loaded from the backend API.
 */
export const MachineTypeStore = signalStore(
  {providedIn: 'root'},
  withState(initialState),
  withEntities<MachineType>(),
  withComputed(store => ({
    /**
     * The default machine type (n1-standard-8 typically).
     */
    defaultType: computed(() => store.entities().find(mt => mt.isDefault)),
  })),
  withMethods((store, api = inject(MachineTypeApiService)) => ({
    /**
     * Load all machine types from the backend.
     * Only loads if not already loaded (use forceReload to refresh).
     */
    loadAll: rxMethod<{forceReload?: boolean} | undefined>(
      pipe(
        tap(params => {
          // Skip if already loaded and not forcing reload
          if (store.loaded() && !params?.forceReload) {
            return;
          }
          patchState(store, {loading: true, error: null});
        }),
        switchMap(params => {
          // Skip API call if already loaded and not forcing
          if (store.loaded() && !params?.forceReload) {
            return [];
          }
          return api.list({enabled: true}).pipe(
            tapResponse({
              next: machineTypes =>
                patchState(store, setAllEntities(machineTypes), {
                  loading: false,
                  loaded: true,
                }),
              error: (err: unknown) =>
                patchState(store, {
                  loading: false,
                  error: getErrorMessage(err, 'Failed to load machine types'),
                }),
            })
          );
        })
      )
    ),
  }))
);
