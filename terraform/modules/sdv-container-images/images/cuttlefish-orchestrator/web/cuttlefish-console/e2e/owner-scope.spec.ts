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
 * Owner-scoped visibility: the list pages default to the caller's own
 * resources (server-side mine=true), the Mine/All toggle refetches, and
 * deleting someone else's resource surfaces a creator warning in the
 * confirm dialog.
 */

import {batch, device, expect, host, type Page, test} from './fixtures/api-mock';

// Each row's kebab is a matMenuTriggerFor button. It is targeted by its
// accessible name rather than by the Material trigger class: the topbar carries
// a menu trigger of its own for the appearance switcher, so `.first()` on the
// class picks that instead.
async function openRowMenu(page: Page): Promise<void> {
  await page
    .getByRole('button', {name: /actions$/})
    .first()
    .click();
}

test('device list defaults to mine and reveals foreign devices on All', async ({page, api}) => {
  api.devices.push(
    device({id: 'device-2', name: 'foreign-device', created_by: 'other@example.com'})
  );

  const mineReq = page.waitForRequest(r => {
    const url = new URL(r.url());
    return url.pathname.endsWith('/v2/devices') && url.searchParams.get('mine') === 'true';
  });
  await page.goto('/devices');
  await mineReq;
  await expect(page.getByText('my-device')).toBeVisible();
  await expect(page.getByText('foreign-device')).toHaveCount(0);

  const allReq = page.waitForRequest(r => {
    const url = new URL(r.url());
    return (
      url.pathname.endsWith('/v2/devices') &&
      r.method() === 'GET' &&
      url.searchParams.get('mine') === null
    );
  });
  await page.getByText('My devices only').click();
  await allReq;
  await expect(page.getByText('foreign-device')).toBeVisible();
});

test('deleting a foreign device shows the creator warning', async ({page, api}) => {
  api.devices = [device({created_by: 'other@example.com'})];
  await page.goto('/devices');
  // The foreign device is hidden behind the default Mine scope.
  await expect(page.getByText('my-device')).toHaveCount(0);
  await page.getByText('My devices only').click();
  await expect(page.getByText('my-device')).toBeVisible();

  await openRowMenu(page);
  await page.getByRole('menuitem', {name: 'Delete'}).click();
  await expect(page.getByText('Created by other@example.com, another user.')).toBeVisible();

  // The warning is advisory, and confirming still deletes.
  const req = page.waitForRequest(
    r => r.url().endsWith('/v2/devices/device-1') && r.method() === 'DELETE'
  );
  await page.getByRole('button', {name: 'Delete'}).click();
  await req;
});

test('host list toggle reveals foreign hosts', async ({page, api}) => {
  api.hosts.push(host({id: 'host-2', name: 'foreign-host', created_by: 'other@example.com'}));
  await page.goto('/hosts');
  // Exact, because the row renders the display name and, under it, the derived
  // instance name that contains it. A substring match would hit both.
  await expect(page.getByText('test-host', {exact: true})).toBeVisible();
  await expect(page.getByText('foreign-host', {exact: true})).toHaveCount(0);

  await page.getByText('My hosts only').click();
  await expect(page.getByText('foreign-host', {exact: true})).toBeVisible();
});

test('batch list toggle reveals foreign batches', async ({page, api}) => {
  api.batches.push(
    batch({
      id: 'batch-2',
      batch_id: 'batch-2',
      name_prefix: 'foreign-run',
      created_by: 'other@example.com',
    })
  );
  await page.goto('/batches');
  await expect(page.getByText('ci-run', {exact: true})).toBeVisible();
  await expect(page.getByText('foreign-run')).toHaveCount(0);

  await page.getByText('My batches only').click();
  await expect(page.getByText('foreign-run')).toBeVisible();
});
