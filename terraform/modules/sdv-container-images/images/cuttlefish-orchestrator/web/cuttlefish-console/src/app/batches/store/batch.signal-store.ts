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
import {addEntity, removeEntity} from '@ngrx/signals/entities';
import {rxMethod} from '@ngrx/signals/rxjs-interop';
import {pipe} from 'rxjs';
import {exhaustMap, switchMap, tap} from 'rxjs/operators';
import {withEntityList} from '../../shared/store/entity-list.feature';
import {getErrorMessage} from '../../shared/utils';
import type {Batch, BatchListFilter, BatchMember, CreateBatchRequest} from '../models/batch.model';
import {BatchApiService} from '../services/batch-api.service';

interface BatchUiState {
  /** Members of the batch currently open in the detail view. */
  members: BatchMember[];
  membersLoading: boolean;
}

/**
 * Batch feature state: the shared entity read-side plus the detail view's
 * member list. Every number is server-derived (the rollup comes from the
 * batch API, never from client counting).
 */
export const BatchStore = signalStore(
  {providedIn: 'root'},
  withState<BatchUiState>({members: [], membersLoading: false}),
  withEntityList<Batch, BatchListFilter>({
    api: () => inject(BatchApiService),
    label: 'batches',
  }),
  withComputed(store => ({
    liveCount: computed(() => store.entities().filter(b => b.status !== 'failed').length),
    failedCount: computed(() => store.entities().filter(b => b.status === 'failed').length),
  })),
  withMethods(store => {
    const api = inject(BatchApiService);
    const snackBar = inject(MatSnackBar);
    const router = inject(Router);
    return {
      create: rxMethod<{request: CreateBatchRequest; idempotencyKey: string}>(
        // exhaustMap rather than switchMap. Admission provisions hosts, so a
        // second submit landing while the first is in flight must be dropped
        // so the two never race. The key would make the retry converge, but only
        // if the caller reused it, and dropping does not depend on that.
        pipe(
          tap(() => patchState(store, {loading: true, error: null})),
          exhaustMap(({request, idempotencyKey}) =>
            api.create(request, idempotencyKey).pipe(
              tapResponse({
                next: batch => {
                  patchState(store, addEntity(batch), {loading: false});
                  snackBar.open(`Batch "${batch.namePrefix ?? batch.id}" created`, 'Close', {
                    duration: 3000,
                  });
                  void router.navigate(['/batches', batch.id]);
                },
                error: (err: unknown) =>
                  patchState(store, {
                    loading: false,
                    error: getErrorMessage(err, 'Failed to create batch'),
                  }),
              })
            )
          )
        )
      ),
      loadMembers: rxMethod<string>(
        pipe(
          tap(() => patchState(store, {members: [], membersLoading: true})),
          switchMap(id =>
            api.members(id).pipe(
              tapResponse({
                next: members => patchState(store, {members, membersLoading: false}),
                error: (err: unknown) =>
                  patchState(store, {
                    membersLoading: false,
                    error: getErrorMessage(err, 'Failed to load batch members'),
                  }),
              })
            )
          )
        )
      ),
      delete: rxMethod<string>(
        pipe(
          switchMap(id =>
            api.delete(id).pipe(
              tapResponse({
                next: () => patchState(store, removeEntity(id)),
                error: (err: unknown) =>
                  patchState(store, {error: getErrorMessage(err, 'Failed to delete batch')}),
              })
            )
          )
        )
      ),
    };
  })
);
