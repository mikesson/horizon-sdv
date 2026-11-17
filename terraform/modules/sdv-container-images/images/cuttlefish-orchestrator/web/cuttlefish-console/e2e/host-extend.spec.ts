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

import {expect, extendedHostBody, leasedHost, problemBody, test} from './fixtures/api-mock';

const EXTEND_URL = /\/v2\/hosts\/[^/?]+\/extend$/;

test('extends a reserved host and shows the raised lease', async ({page, api}) => {
  const leased = leasedHost();
  api.hosts = [leased];
  const extended = extendedHostBody(leased, '2099-01-03T12:00:00Z');
  await page.route(EXTEND_URL, route =>
    route.fulfill({status: 200, contentType: 'application/json', body: JSON.stringify(extended)})
  );

  await page.goto('/hosts/host-1');
  await expect(page.getByText('Jan 1, 2099')).toBeVisible();
  await page.getByRole('button', {name: 'Extend', exact: true}).click();

  const dialog = page.getByRole('dialog');
  const hours = dialog.getByLabel('Hours to add');
  await hours.fill('169');
  await expect(dialog.getByText('Enter a whole number from 1 to 168')).toBeVisible();
  await expect(dialog.getByRole('button', {name: 'Extend'})).toBeDisabled();

  await hours.fill('48');
  // Counted from the current expiry, which is later than now.
  await expect(dialog.getByText(/New expiry Jan 3, 2099/)).toBeVisible();

  const postReq = page.waitForRequest(req => EXTEND_URL.test(req.url()) && req.method() === 'POST');
  await dialog.getByRole('button', {name: 'Extend'}).click();
  expect((await postReq).postDataJSON()).toEqual({hours: 48});

  await expect(page.getByText('Host lease extended')).toBeVisible();
  await expect(page.getByText('Jan 3, 2099')).toBeVisible();
});

test('shows the server refusal verbatim', async ({page, api}) => {
  api.hosts = [leasedHost()];
  const detail =
    'host host-1 lease 2099-01-21T12:00:00Z would pass the VM stop time 2099-01-20T12:00:00Z';
  await page.route(EXTEND_URL, route =>
    route.fulfill({
      status: 409,
      contentType: 'application/problem+json',
      body: JSON.stringify(problemBody(detail, 409)),
    })
  );

  await page.goto('/hosts/host-1');
  await page.getByRole('button', {name: 'Extend', exact: true}).click();
  await page.getByRole('dialog').getByRole('button', {name: 'Extend'}).click();

  await expect(page.locator('mat-snack-bar-container').getByText(detail)).toBeVisible();
});

test('shows when the VM stops and that a restart resets it', async ({page, api}) => {
  api.hosts = [leasedHost()];
  await page.goto('/hosts/host-1');
  await expect(page.getByText('VM stops')).toBeVisible();
  await expect(page.getByText('Jan 20, 2099')).toBeVisible();
  await expect(page.getByText('A host restart resets this time.')).toBeVisible();
});

test('offers no extend on a fleet host and no stop time when none is observed', async ({
  page,
  api,
}) => {
  api.hosts = [leasedHost({class: 'fleet', origin: 'pool', vm_terminates_at: undefined})];
  await page.goto('/hosts/host-1');
  // Wait for the page to render the host before asserting absences.
  await expect(page.getByText('Lease expires')).toBeVisible();
  await expect(page.getByRole('button', {name: 'Extend', exact: true})).toHaveCount(0);
  await expect(page.getByText('VM stops')).toHaveCount(0);
});
