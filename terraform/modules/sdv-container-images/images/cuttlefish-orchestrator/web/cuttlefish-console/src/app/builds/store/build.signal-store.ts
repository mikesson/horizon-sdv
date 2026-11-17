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
  removeEntity,
  setAllEntities,
  updateEntity,
  withEntities,
} from '@ngrx/signals/entities';
import {rxMethod} from '@ngrx/signals/rxjs-interop';
import {pipe} from 'rxjs';
import {switchMap, tap} from 'rxjs/operators';

import {getErrorMessage} from '../../shared/utils';
import type {Build, BuildListFilter, CreateBuildRequest} from '../models/build.model';
import {BuildApiService} from '../services/build-api.service';

/**
 * State definition for the Build vertical slice.
 */
interface BuildState {
  loading: boolean;
  error: string | null;
  selectedId: string | null;
  total: number;
  /** False when the catalog outran the page budget; the list view says so. */
  complete: boolean;
}

const initialState: BuildState = {
  loading: false,
  error: null,
  selectedId: null,
  total: 0,
  complete: true,
};

/**
 * The central state management store for Android Builds.
 *
 * ARCHITECTURE:
 * Implements the NgRx Signal Store pattern with normalized entity management.
 * Provides a reactive source of truth for the device creation wizard and build catalog.
 *
 * @remarks
 * Uses `withEntities` to manage the collection of `Build` artifacts.
 */
export const BuildStore = signalStore(
  {providedIn: 'root'},
  withState(initialState),
  withEntities<Build>(),
  withComputed(store => ({
    /**
     * Selector: The currently selected build entity.
     */
    selectedBuild: computed(() => {
      const id = store.selectedId();
      return id ? store.entityMap()[id] : null;
    }),

    /**
     * Selector: List of builds available for device provisioning (every
     * registered build; a deleted build has no row).
     */
    availableBuildsForDevice: computed(() => store.entities()),

    /**
     * Selector: The build marked as system default.
     */
    defaultBuild: computed(() => store.entities().find(b => b.isDefault)),

    /**
     * Selector: Aggregated statistics by build source.
     */
    stats: computed(() => {
      const entities = store.entities();
      return {
        total: entities.length,
        official: entities.filter(b => b.source === 'android_build').length,
        custom: entities.filter(b => b.source === 'url').length,
      };
    }),
  })),
  withMethods(
    (
      store,
      buildApi = inject(BuildApiService),
      snackBar = inject(MatSnackBar),
      router = inject(Router)
    ) => ({
      /**
       * Loads the whole build catalog matching the filter. Every picker in
       * the console offers what is in this collection, so it holds all of it
       * rather than the first page.
       */
      loadAll: rxMethod<{filter?: BuildListFilter} | undefined>(
        pipe(
          tap(() => patchState(store, {loading: true, error: null})),
          switchMap(req =>
            buildApi.listAll(req ? req.filter : undefined).pipe(
              tapResponse({
                next: res =>
                  patchState(store, setAllEntities(res.items), {
                    loading: false,
                    total: res.total,
                    complete: res.complete,
                  }),
                error: (err: unknown) =>
                  patchState(store, {
                    loading: false,
                    error: getErrorMessage(err, 'Failed to load builds'),
                  }),
              })
            )
          )
        )
      ),

      /**
       * Reactive method to register a new build image.
       */
      create: rxMethod<CreateBuildRequest>(
        pipe(
          tap(() => patchState(store, {loading: true, error: null})),
          switchMap((req: CreateBuildRequest) =>
            buildApi.create(req).pipe(
              tapResponse({
                next: (build: Build) => {
                  patchState(store, addEntity(build), {loading: false});
                  // The snackbar action hands off to the interview with the
                  // Android question pre-answered by this build.
                  snackBar
                    .open(`Build "${build.name}" registered`, 'Create device', {duration: 6000})
                    .onAction()
                    .subscribe(
                      () =>
                        void router.navigate(['/devices/create'], {
                          queryParams: {buildId: build.id},
                        })
                    );
                  void router.navigate(['/builds']);
                },
                error: (err: unknown) =>
                  patchState(store, {
                    loading: false,
                    error: getErrorMessage(err, 'Failed to create build'),
                  }),
              })
            )
          )
        )
      ),

      /**
       * Reactive method to update metadata for an existing build.
       */
      update: rxMethod<{id: string; request: Partial<CreateBuildRequest>}>(
        pipe(
          tap(() => patchState(store, {loading: true, error: null})),
          switchMap(({id, request}) =>
            buildApi.update(id, request).pipe(
              tapResponse({
                next: (build: Build) => {
                  patchState(store, updateEntity({id, changes: build}), {loading: false});
                  snackBar.open(`Build "${build.name}" updated`, 'Dismiss', {duration: 3000});
                },
                error: (err: unknown) =>
                  patchState(store, {
                    loading: false,
                    error: getErrorMessage(err, 'Failed to update build'),
                  }),
              })
            )
          )
        )
      ),

      /**
       * Reactive method to delete a build record.
       */
      delete: rxMethod<string>(
        pipe(
          tap((_id: string) => {
            // Implementation note: could add optimistic UI update here
          }),
          switchMap((id: string) =>
            buildApi.delete(id).pipe(
              tapResponse({
                next: () => {
                  patchState(store, removeEntity(id));
                  snackBar.open('Build deleted', 'Dismiss', {duration: 3000});
                },
                error: (err: unknown) => {
                  patchState(store, {error: getErrorMessage(err, 'Failed to delete build')});
                },
              })
            )
          )
        )
      ),
    })
  )
);
