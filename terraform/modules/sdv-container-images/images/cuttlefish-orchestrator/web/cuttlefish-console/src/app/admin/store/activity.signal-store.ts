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
import {concatMap, pipe, switchMap, tap} from 'rxjs';
import {getErrorMessage} from '../../shared/utils';
import type {AuditEntry, AuditFilter} from '../models/audit.model';
import {AuditApiService} from '../services/audit-api.service';

/** Entries fetched per request; "Load more" appends another page. */
const PAGE_SIZE = 50;

interface ActivityState {
  entries: AuditEntry[];
  total: number;
  filter: AuditFilter;
  loading: boolean;
  error: string | null;
}

const initialState: ActivityState = {
  entries: [],
  total: 0,
  filter: {},
  loading: false,
  error: null,
};

/**
 * Audit-log state for the Activity page. Bespoke (not withEntityList): a log
 * view appends pages under a server-side filter instead of replacing on load.
 */
export const ActivityStore = signalStore(
  {providedIn: 'root'},
  withState(initialState),
  withComputed(store => ({
    hasMore: computed(() => store.entries().length < store.total()),
  })),
  withMethods(store => {
    const api = inject(AuditApiService);

    return {
      /** Applies a filter and reloads from the top. */
      load: rxMethod<AuditFilter>(
        pipe(
          tap(filter =>
            patchState(store, {filter, entries: [], total: 0, loading: true, error: null})
          ),
          switchMap(filter =>
            api.list(filter, PAGE_SIZE, 0).pipe(
              tapResponse({
                next: page =>
                  patchState(store, {entries: page.items, total: page.total, loading: false}),
                error: err =>
                  patchState(store, {
                    loading: false,
                    error: getErrorMessage(err, 'Failed to load activity'),
                  }),
              })
            )
          )
        )
      ),

      /** Appends the next page under the current filter. */
      loadMore: rxMethod<void>(
        pipe(
          concatMap(() => {
            patchState(store, {loading: true, error: null});
            return api.list(store.filter(), PAGE_SIZE, store.entries().length).pipe(
              tapResponse({
                next: page =>
                  patchState(store, {
                    entries: [...store.entries(), ...page.items],
                    total: page.total,
                    loading: false,
                  }),
                error: err =>
                  patchState(store, {
                    loading: false,
                    error: getErrorMessage(err, 'Failed to load more activity'),
                  }),
              })
            );
          })
        )
      ),
    };
  })
);
