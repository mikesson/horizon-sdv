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

import {inject} from '@angular/core';
import {MatSnackBar} from '@angular/material/snack-bar';
import {tapResponse} from '@ngrx/operators';
import {patchState, signalStore, withMethods, withState} from '@ngrx/signals';
import {rxMethod} from '@ngrx/signals/rxjs-interop';
import {exhaustMap, pipe, switchMap, tap} from 'rxjs';
import {fail} from '../../shared/store/entity-ops';
import {getErrorMessage} from '../../shared/utils';
import type {FailedTask} from '../models/task-admin.model';
import {TaskAdminApiService} from '../services/task-admin-api.service';

interface TaskAdminState {
  tasks: FailedTask[];
  loading: boolean;
  error: string | null;
}

const initialState: TaskAdminState = {
  tasks: [],
  loading: false,
  error: null,
};

/** Dead-letter queue state for the Operations page. */
export const TaskAdminStore = signalStore(
  {providedIn: 'root'},
  withState(initialState),
  withMethods(store => {
    const api = inject(TaskAdminApiService);
    const snackBar = inject(MatSnackBar);

    const removeTask = (id: string): void =>
      patchState(store, {tasks: store.tasks().filter(t => t.id !== id)});

    return {
      loadAll: rxMethod<void>(
        pipe(
          tap(() => patchState(store, {loading: true, error: null})),
          switchMap(() =>
            api.listFailed().pipe(
              tapResponse({
                next: tasks => patchState(store, {tasks, loading: false}),
                error: err =>
                  patchState(store, {
                    loading: false,
                    error: getErrorMessage(err, 'Failed to load dead-lettered tasks'),
                  }),
              })
            )
          )
        )
      ),

      retry: rxMethod<FailedTask>(
        pipe(
          exhaustMap(task =>
            api.retry(task.id).pipe(
              tapResponse({
                next: () => {
                  removeTask(task.id);
                  api.refreshStats();
                  snackBar.open(`Requeued ${task.type} task`, 'Close', {duration: 3000});
                },
                error: err => {
                  fail(snackBar, 'Failed to retry task', err);
                },
              })
            )
          )
        )
      ),

      delete: rxMethod<FailedTask>(
        pipe(
          exhaustMap(task =>
            api.delete(task.id).pipe(
              tapResponse({
                next: () => {
                  removeTask(task.id);
                  api.refreshStats();
                  snackBar.open('Task deleted', 'Close', {duration: 3000});
                },
                error: err => {
                  fail(snackBar, 'Failed to delete task', err);
                },
              })
            )
          )
        )
      ),
    };
  })
);
