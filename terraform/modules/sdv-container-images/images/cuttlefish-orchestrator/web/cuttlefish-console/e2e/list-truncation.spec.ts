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

/**
 * A fleet larger than one page must arrive whole: the console follows the
 * server's page tokens until the collection is complete, because the table, the
 * KPI strip and select-all all count what the store holds. When it still ends
 * up short of the server's count, the page says so rather than presenting a
 * prefix as the fleet.
 */

import type {Route} from '@playwright/test';
import {device, expect, host, test} from './fixtures/api-mock';

const MAX_PAGE = 100;

/**
 * Serves `total` rows a page at a time. The token here is the next row's
 * position, which is enough for a mock; the backend's names the last row.
 */
function pagedRoute(total: number, row: (i: number) => object): (route: Route) => Promise<void> {
  return async route => {
    const params = new URL(route.request().url()).searchParams;
    const limit = Number(params.get('limit') ?? MAX_PAGE);
    const start = Number(params.get('page_token') ?? 0);
    const items = Array.from({length: Math.max(0, Math.min(limit, total - start))}, (_, i) =>
      row(start + i)
    );
    const next = start + limit < total ? String(start + limit) : '';
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({items, total, limit, offset: 0, next_page_token: next}),
    });
  };
}

test('device list loads every page of a fleet larger than one page', async ({page}) => {
  const tokens: Array<string | null> = [];
  page.on('request', r => {
    const url = new URL(r.url());
    if (url.pathname.endsWith('/v2/devices')) {
      tokens.push(url.searchParams.get('page_token'));
    }
  });
  // Registered after the default mocks, so it wins.
  await page.route(
    /\/v2\/devices(\?|$)/,
    pagedRoute(137, i => device({id: `device-${i}`, name: `device-${i}`}))
  );

  await page.goto('/devices');

  // The KPI counts the store, so it reads the fleet only if both pages landed.
  await expect(page.getByText('137', {exact: true}).first()).toBeVisible();
  expect(tokens).toEqual([null, '100']);
  // Nothing is missing, so there is nothing to disclose.
  await expect(page.getByText(/Showing \d+ of/)).toBeHidden();
});

test('device list says so when it holds fewer rows than the server counts', async ({page}) => {
  // One row served against a total of 137: what a response-validation drop
  // looks like from the page's side.
  await page.route(/\/v2\/devices(\?|$)/, route =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({items: [device()], total: 137, limit: MAX_PAGE, offset: 0}),
    })
  );

  await page.goto('/devices');

  await expect(page.getByText('Showing 1 of 137 devices')).toBeVisible();
});

test('host list loads every page of a fleet larger than one page', async ({page}) => {
  const tokens: Array<string | null> = [];
  page.on('request', r => {
    const url = new URL(r.url());
    if (url.pathname.endsWith('/v2/hosts')) {
      tokens.push(url.searchParams.get('page_token'));
    }
  });
  await page.route(
    /\/v2\/hosts(\?|$)/,
    pagedRoute(105, i => host({id: `host-${i}`, name: `host-${i}`}))
  );

  await page.goto('/hosts');

  await expect(page.getByText('105', {exact: true}).first()).toBeVisible();
  expect(tokens).toEqual([null, '100']);
  await expect(page.getByText(/Showing \d+ of/)).toBeHidden();
});

test('host list says so when it holds fewer rows than the server counts', async ({page}) => {
  await page.route(/\/v2\/hosts(\?|$)/, route =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({items: [host()], total: 42, limit: MAX_PAGE, offset: 0}),
    })
  );

  await page.goto('/hosts');

  await expect(page.getByText('Showing 1 of 42 hosts')).toBeVisible();
});
