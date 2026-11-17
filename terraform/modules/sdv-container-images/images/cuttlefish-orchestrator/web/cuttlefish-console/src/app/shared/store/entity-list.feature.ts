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

import {tapResponse} from '@ngrx/operators';
import {patchState, signalStoreFeature, withMethods, withState} from '@ngrx/signals';
import {setAllEntities, setEntity, withEntities} from '@ngrx/signals/entities';
import {rxMethod} from '@ngrx/signals/rxjs-interop';
import {type Observable, pipe} from 'rxjs';
import {switchMap, tap} from 'rxjs/operators';

import {getErrorMessage} from '../utils';
import type {Collection} from '../utils/paged-collection';

/** The read-side API a store's list feature needs. */
export interface EntityListApi<E, F> {
  listAll(filter?: F): Observable<Collection<E>>;
  get(id: string): Observable<E>;
}

/**
 * Shared read-side of an entity signal store: `{loading, error, total,
 * complete}` state, a normalized entity collection, and `loadAll`/`loadOne`
 * under one resilient idiom (tapResponse, upsert on load). Compose it into a
 * store and layer the entity-specific computed selectors + mutation methods on
 * top; it keeps the device/host stores true mirrors instead of two drifting
 * copies.
 *
 * `loadAll` holds the whole server-side collection rather than its first page.
 * Every consumer of `entities()` counts it or offers it as a choice.
 * `complete` is false only when the collection outran the page budget, and the
 * list views say so instead of presenting a prefix as the whole fleet.
 *
 * `label` is the lowercase plural used in error messages ("devices"/"hosts").
 */
// The return is an inferred SignalStoreFeature; an explicit annotation would
// restate the whole feature shape (state + entities + methods) with no benefit.
// eslint-disable-next-line @typescript-eslint/explicit-function-return-type
export function withEntityList<E extends {id: string}, F>(config: {
  api: () => EntityListApi<E, F>;
  label: string;
}) {
  return signalStoreFeature(
    withState({loading: false, error: null as string | null, total: 0, complete: true}),
    withEntities<E>(),
    withMethods(store => {
      const api = config.api();
      // Load failures set `error` state, which both slices render as a banner;
      // no toast (the banner is the surface, and a toast would double it).
      const onError = (err: unknown): void =>
        patchState(store, {
          loading: false,
          error: getErrorMessage(err, `Failed to load ${config.label}`),
        });
      return {
        loadAll: rxMethod<F | undefined>(
          pipe(
            tap(() => patchState(store, {loading: true, error: null})),
            switchMap(filter =>
              api.listAll(filter).pipe(
                tapResponse({
                  next: res =>
                    patchState(store, setAllEntities(res.items), {
                      loading: false,
                      total: res.total,
                      complete: res.complete,
                    }),
                  error: onError,
                })
              )
            )
          )
        ),
        loadOne: rxMethod<string>(
          pipe(
            tap(() => patchState(store, {loading: true, error: null})),
            switchMap((id: string) =>
              api.get(id).pipe(
                tapResponse({
                  next: (entity: E) => patchState(store, setEntity(entity), {loading: false}),
                  error: onError,
                })
              )
            )
          )
        ),
      };
    })
  );
}
