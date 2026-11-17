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

import type {Observable} from 'rxjs';
import {of} from 'rxjs';
import {describe, expect, it} from 'vitest';
import type {Collection, ListPage} from './paged-collection';
import {collectionNote, collectPages, MAX_PAGES} from './paged-collection';

interface PageRequest {
  limit: number;
  pageToken?: string;
}

/**
 * A keyset server over the rows in `store`, newest first. A token names the
 * last row returned, so a page resumes after that row wherever it now sits,
 * which is what the backend's next_page_token does.
 */
function server(store: number[]): {
  fetch: (p: PageRequest) => Observable<ListPage<number>>;
  requests: PageRequest[];
} {
  const requests: PageRequest[] = [];
  return {
    requests,
    fetch: (p: PageRequest) => {
      requests.push(p);
      const after = p.pageToken === undefined ? -1 : store.indexOf(Number(p.pageToken));
      const items = store.slice(after + 1, after + 1 + p.limit);
      const more = after + 1 + p.limit < store.length;
      return of({
        items,
        total: store.length,
        nextPageToken: more ? String(items[items.length - 1]) : '',
      });
    },
  };
}

function rows(n: number): number[] {
  return Array.from({length: n}, (_, i) => i);
}

function collect<T>(source: Observable<Collection<T>>): Collection<T> {
  let result: Collection<T> | undefined;
  source.subscribe((value: Collection<T>): void => {
    result = value;
  });
  if (!result) {
    throw new Error('collectPages did not emit');
  }
  return result;
}

describe('collectPages', () => {
  it('reads a collection that fits in one page with one request', () => {
    const {fetch, requests} = server(rows(7));

    const result = collect(collectPages(fetch, 10));

    expect(requests).toEqual([{limit: 10}]);
    expect(result.items).toEqual([0, 1, 2, 3, 4, 5, 6]);
    expect(result.complete).toBe(true);
  });

  it('follows next_page_token through a collection larger than one page', () => {
    const {fetch, requests} = server(rows(25));

    const result = collect(collectPages(fetch, 10));

    expect(requests).toEqual([
      {limit: 10},
      {limit: 10, pageToken: '9'},
      {limit: 10, pageToken: '19'},
    ]);
    expect(result.items).toEqual(rows(25));
    expect(result.total).toBe(25);
    expect(result.complete).toBe(true);
  });

  // The defect an offset walk has: a row deleted after the first page moves
  // every later row up by one, so offset 10 lands one row late and the row
  // that moved into position 9 is never read. The token resumes after the
  // last row the walk saw, so nothing that still exists is skipped.
  it('misses no row when one is deleted between pages', () => {
    const store = rows(25);
    const {fetch: serve, requests} = server(store);
    const fetch = (p: PageRequest): Observable<ListPage<number>> => {
      if (requests.length === 1) {
        store.splice(store.indexOf(3), 1);
      }
      return serve(p);
    };

    const result = collect(collectPages(fetch, 10));

    // Row 3 was read before it went; every other row is read once.
    expect(result.items).toEqual(rows(25));
    expect(result.total).toBe(24);
    expect(result.complete).toBe(true);
  });

  // The page's own count decides nothing: validation drops malformed rows, so
  // a short page with a token still has more behind it.
  it('keeps walking when a page comes back short but carries a token', () => {
    const pages: Array<ListPage<number>> = [
      {items: [1, 2], total: 30, nextPageToken: 'a'},
      {items: [3], total: 30, nextPageToken: ''},
    ];
    const requests: PageRequest[] = [];
    const fetch = (p: PageRequest): Observable<ListPage<number>> => {
      requests.push(p);
      return of(pages[requests.length - 1]);
    };

    const result = collect(collectPages(fetch, 10));

    expect(requests).toEqual([{limit: 10}, {limit: 10, pageToken: 'a'}]);
    expect(result.items).toEqual([1, 2, 3]);
    expect(result.total).toBe(30);
    expect(result.complete).toBe(true);
  });

  // A full page with no token is the last page, whatever total says: walking
  // on from a count would ask for an offset the server no longer serves.
  it('ends the walk on an empty token even when total is larger', () => {
    const requests: PageRequest[] = [];
    const fetch = (p: PageRequest): Observable<ListPage<number>> => {
      requests.push(p);
      return of({items: rows(10), total: 137, nextPageToken: ''});
    };

    const result = collect(collectPages(fetch, 10));

    expect(requests).toHaveLength(1);
    expect(result.items).toHaveLength(10);
    expect(result.total).toBe(137);
    expect(result.complete).toBe(true);
  });

  it('stops at the page budget and reports the collection incomplete', () => {
    const {fetch, requests} = server(rows(10_000));

    const result = collect(collectPages(fetch, 10));

    expect(requests).toHaveLength(MAX_PAGES);
    expect(result.items).toHaveLength(MAX_PAGES * 10);
    expect(result.total).toBe(10_000);
    expect(result.complete).toBe(false);
  });

  it('makes one request for an empty collection', () => {
    const {fetch, requests} = server([]);

    const result = collect(collectPages(fetch, 10));

    expect(requests).toEqual([{limit: 10}]);
    expect(result.items).toEqual([]);
    expect(result.complete).toBe(true);
  });

  it('defaults to the largest page the API accepts', () => {
    const {fetch, requests} = server(rows(1));

    collect(collectPages(fetch));

    expect(requests).toEqual([{limit: 100}]);
  });
});

describe('collectionNote', () => {
  it('says nothing when the collection is whole', () => {
    expect(collectionNote(42, 42, true, 'devices')).toBe('');
  });

  // A row the response schema dropped: the walk finished, the count is still
  // short, and no page budget was involved.
  it('reports a shortfall a completed walk still has', () => {
    expect(collectionNote(41, 42, true, 'devices')).toBe('Showing 41 of 42 devices');
  });

  it('names the page budget when that is what stopped the walk', () => {
    expect(collectionNote(5000, 7000, false, 'devices')).toBe(
      'Showing 5000 of 7000 devices (the load stopped at 5000 rows)'
    );
  });

  // total is 0 whenever Huma omits it, which must not read as "1 of 0".
  it('says nothing when the server reports no total', () => {
    expect(collectionNote(3, 0, true, 'builds')).toBe('');
  });
});
