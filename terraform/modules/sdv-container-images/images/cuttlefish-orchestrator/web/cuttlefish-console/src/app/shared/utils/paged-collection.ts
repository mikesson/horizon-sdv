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

import {type Observable, of} from 'rxjs';
import {switchMap} from 'rxjs/operators';

/** One page of a list endpoint's response. An empty token ends the list. */
export interface ListPage<T> {
  items: T[];
  total: number;
  nextPageToken: string;
}

/**
 * A whole server-side collection, assembled from as many pages as it took.
 *
 * `complete` false means the page budget ran out first, so `items` is a
 * prefix of `total` and any count derived from it describes the prefix only.
 * The list views say so instead of presenting the count as the whole fleet.
 */
export interface Collection<T> {
  items: T[];
  total: number;
  complete: boolean;
}

/** The largest page every /v2 list endpoint accepts. */
export const MAX_PAGE_SIZE = 100;

/**
 * Pages per collection. 5000 rows is far beyond any fleet this deployment's
 * quota permits; it exists so a server that never stops handing out a token
 * ends the walk instead of looping forever.
 */
export const MAX_PAGES = 50;

/**
 * The line a list view shows when its store holds less than the server has.
 *
 * Empty when the collection is whole, which is the normal case, so the note
 * appearing at all means a count on the page describes a prefix. The two
 * causes read differently. An incomplete walk is the page budget and says so.
 * A whole walk that still came up short lost rows to response validation,
 * which no filter change will bring back.
 */
export function collectionNote(
  loaded: number,
  total: number,
  complete: boolean,
  plural: string
): string {
  if (total <= loaded) {
    return '';
  }
  const shown = `Showing ${loaded} of ${total} ${plural}`;
  return complete ? shown : `${shown} (the load stopped at ${MAX_PAGES * MAX_PAGE_SIZE} rows)`;
}

/**
 * Reads a paginated list endpoint to the end and returns it as one collection.
 *
 * The console's stores hold whole collections. KPI strips, host and build
 * pickers and select-all all count what is in the store, so a single page
 * truncates the table and makes those numbers and option lists wrong too.
 * Completion happens here, at the transport edge, instead of at the twenty
 * call sites that assume it.
 *
 * The walk follows the server's next_page_token until it comes back empty.
 * The token names the last row returned, so a row created or deleted between
 * requests cannot shift a later page onto rows already seen or past rows not
 * yet seen, which an offset walk does. The page's item count decides nothing:
 * response validation drops malformed rows (`safeParseList`), so a short page
 * does not mean the collection ended.
 */
export function collectPages<T>(
  fetchPage: (page: {limit: number; pageToken?: string}) => Observable<ListPage<T>>,
  pageSize = MAX_PAGE_SIZE
): Observable<Collection<T>> {
  const step = (pageToken: string, seen: T[], pages: number): Observable<Collection<T>> =>
    fetchPage(pageToken ? {limit: pageSize, pageToken} : {limit: pageSize}).pipe(
      switchMap(page => {
        const items = seen.concat(page.items);
        if (!page.nextPageToken) {
          return of({items, total: page.total, complete: true});
        }
        if (pages + 1 >= MAX_PAGES) {
          return of({items, total: page.total, complete: false});
        }
        return step(page.nextPageToken, items, pages + 1);
      })
    );

  return step('', [], 0);
}
