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
import {
  addEntity,
  removeEntity,
  setAllEntities,
  updateEntity,
  withEntities,
} from '@ngrx/signals/entities';
import {rxMethod} from '@ngrx/signals/rxjs-interop';
import {pipe} from 'rxjs';
import {switchMap, tap} from 'rxjs/operators';

import {getErrorMessage} from '../../shared/utils';
import type {
  CreateHostImageConfigRequest,
  HostImageConfig,
  MachineFamilyCategory,
  UpdateHostImageConfigRequest,
} from '../models/host-image-config.model';
import {getMachineFamilyCategory} from '../models/host-image-config.model';
import {HostImageConfigApiService} from '../services/host-image-config-api.service';

interface HostImageConfigState {
  loading: boolean;
  error: string | null;
  loaded: boolean;
}

const initialState: HostImageConfigState = {
  loading: false,
  error: null,
  loaded: false,
};

/**
 * Group of configs by category for UI display.
 */
export interface ConfigsByCategory {
  category: MachineFamilyCategory;
  label: string;
  configs: HostImageConfig[];
}

/**
 * Signal store for host image configurations.
 * Manages the state for the host image config admin UI.
 */
export const HostImageConfigStore = signalStore(
  {providedIn: 'root'},
  withState(initialState),
  withEntities<HostImageConfig>(),
  withComputed(store => ({
    /**
     * Statistics about the configurations.
     */
    stats: computed(() => {
      const entities = store.entities();
      return {
        total: entities.length,
        enabled: entities.filter(c => c.enabled).length,
        disabled: entities.filter(c => !c.enabled).length,
      };
    }),

    /**
     * Configurations grouped by category for tabbed display.
     */
    byCategory: computed((): ConfigsByCategory[] => {
      const entities = store.entities();
      const categoryLabels: Record<MachineFamilyCategory, string> = {
        general_purpose: 'General Purpose',
        compute_optimized: 'Compute Optimized',
        memory_optimized: 'Memory Optimized',
        gpus: 'GPU Accelerated',
        arm: 'ARM',
      };

      const categories: MachineFamilyCategory[] = [
        'general_purpose',
        'compute_optimized',
        'memory_optimized',
        'gpus',
        'arm',
      ];

      return categories
        .map(category => ({
          category,
          label: categoryLabels[category],
          configs: entities
            .filter(c => getMachineFamilyCategory(c.machineFamily) === category)
            .sort((a, b) => a.machineFamily.localeCompare(b.machineFamily)),
        }))
        .filter(g => g.configs.length > 0);
    }),

    /**
     * Only enabled configurations.
     */
    enabledConfigs: computed(() => store.entities().filter(c => c.enabled)),

    /**
     * Check if data has been loaded.
     */
    isLoaded: computed(() => store.loaded()),
  })),
  withMethods((store, api = inject(HostImageConfigApiService)) => ({
    /**
     * Load all host image configurations.
     */
    loadAll: rxMethod<{forceReload?: boolean} | undefined>(
      pipe(
        tap(params => {
          if (store.loaded() && !params?.forceReload) {
            return;
          }
          patchState(store, {loading: true, error: null});
        }),
        switchMap(params => {
          if (store.loaded() && !params?.forceReload) {
            return [];
          }
          return api.list().pipe(
            tapResponse({
              next: response =>
                patchState(store, setAllEntities(response.items), {
                  loading: false,
                  loaded: true,
                }),
              error: (err: unknown) =>
                patchState(store, {
                  loading: false,
                  error: getErrorMessage(err, 'Failed to load host image configurations'),
                }),
            })
          );
        })
      )
    ),

    /**
     * Create a new host image configuration.
     */
    create: rxMethod<CreateHostImageConfigRequest>(
      pipe(
        tap(() => patchState(store, {loading: true, error: null})),
        switchMap(request =>
          api.create(request).pipe(
            tapResponse({
              next: config => patchState(store, addEntity(config), {loading: false}),
              error: (err: unknown) =>
                patchState(store, {
                  loading: false,
                  error: getErrorMessage(err, 'Failed to create host image configuration'),
                }),
            })
          )
        )
      )
    ),

    /**
     * Update an existing host image configuration.
     */
    update: rxMethod<{machineFamily: string; changes: UpdateHostImageConfigRequest}>(
      pipe(
        tap(() => patchState(store, {loading: true, error: null})),
        switchMap(({machineFamily, changes}) =>
          api.update(machineFamily, changes).pipe(
            tapResponse({
              next: config =>
                patchState(
                  store,
                  updateEntity({
                    id: machineFamily,
                    changes: config,
                  }),
                  {loading: false}
                ),
              error: (err: unknown) =>
                patchState(store, {
                  loading: false,
                  error: getErrorMessage(err, 'Failed to update host image configuration'),
                }),
            })
          )
        )
      )
    ),

    /**
     * Delete a host image configuration.
     */
    delete: rxMethod<string>(
      pipe(
        tap(() => patchState(store, {loading: true, error: null})),
        switchMap(machineFamily =>
          api.delete(machineFamily).pipe(
            tapResponse({
              next: () => patchState(store, removeEntity(machineFamily), {loading: false}),
              error: (err: unknown) =>
                patchState(store, {
                  loading: false,
                  error: getErrorMessage(err, 'Failed to delete host image configuration'),
                }),
            })
          )
        )
      )
    ),

    /**
     * Get a configuration by machine family from the cache.
     */
    getByFamily(family: string): HostImageConfig | undefined {
      return store.entityMap()[family];
    },

    /**
     * Clear any error state.
     */
    clearError(): void {
      patchState(store, {error: null});
    },
  }))
);
