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

import {build, device, expect, host, type Page, test} from './fixtures/api-mock';

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

test.describe('host lifecycle', () => {
  test('start a stopped host', async ({page, api}) => {
    api.hosts = [host({status: 'stopped'})];
    await page.goto('/hosts');
    await openRowMenu(page);
    const req = page.waitForRequest(
      r => r.url().endsWith('/v2/hosts/host-1/start') && r.method() === 'POST'
    );
    await page.getByRole('menuitem', {name: 'Start'}).click(); // no confirm dialog
    await req;
  });

  test('stop a running host (with confirm)', async ({page}) => {
    await page.goto('/hosts'); // default host is running
    await openRowMenu(page);
    await page.getByRole('menuitem', {name: 'Stop'}).click();
    const req = page.waitForRequest(
      r => r.url().endsWith('/v2/hosts/host-1/stop') && r.method() === 'POST'
    );
    await page.getByRole('button', {name: 'Stop'}).click(); // dialog confirm
    await req;
  });

  test('delete a host (row disappears)', async ({page}) => {
    await page.goto('/hosts');
    await openRowMenu(page);
    await page.getByRole('menuitem', {name: 'Delete'}).click();
    const req = page.waitForRequest(
      r => r.url().endsWith('/v2/hosts/host-1') && r.method() === 'DELETE'
    );
    await page.getByRole('button', {name: 'Delete'}).click();
    await req;
    await expect(page.getByText('test-host')).toHaveCount(0);
  });

  test('bulk-delete selected hosts (both rows disappear)', async ({page, api}) => {
    api.hosts = [host({id: 'host-1', name: 'host-a'}), host({id: 'host-2', name: 'host-b'})];
    await page.goto('/hosts');

    await page.getByRole('checkbox', {name: 'Select all hosts'}).check();
    await page.getByRole('button', {name: /Delete \(2\)/}).click();
    const del1 = page.waitForRequest(
      r => r.url().endsWith('/v2/hosts/host-1') && r.method() === 'DELETE'
    );
    const del2 = page.waitForRequest(
      r => r.url().endsWith('/v2/hosts/host-2') && r.method() === 'DELETE'
    );
    await page.getByRole('button', {name: 'Delete', exact: true}).click(); // dialog confirm
    await Promise.all([del1, del2]);

    await expect(page.getByText('host-a')).toHaveCount(0);
    await expect(page.getByText('host-b')).toHaveCount(0);
  });
});

test.describe('device lifecycle', () => {
  test('start a stopped device', async ({page, api}) => {
    api.devices = [device({status: 'stopped'})];
    await page.goto('/devices');
    await openRowMenu(page);
    const req = page.waitForRequest(
      r => r.url().endsWith('/v2/devices/device-1/start') && r.method() === 'POST'
    );
    await page.getByRole('menuitem', {name: 'Start'}).click();
    await req;
  });

  test('delete a device (with confirm)', async ({page}) => {
    await page.goto('/devices');
    await openRowMenu(page);
    await page.getByRole('menuitem', {name: 'Delete'}).click();
    const req = page.waitForRequest(
      r => r.url().endsWith('/v2/devices/device-1') && r.method() === 'DELETE'
    );
    await page.getByRole('button', {name: 'Delete'}).click();
    await req;
    await expect(page.getByText('my-device')).toHaveCount(0);
  });

  test('bulk-delete selected devices (both rows disappear)', async ({page, api}) => {
    api.devices = [
      device({id: 'device-1', name: 'dev-a'}),
      device({id: 'device-2', name: 'dev-b'}),
    ];
    await page.goto('/devices');

    await page.getByRole('checkbox', {name: 'Select all devices'}).check();
    await page.getByRole('button', {name: /Delete \(2\)/}).click();
    const del1 = page.waitForRequest(
      r => r.url().endsWith('/v2/devices/device-1') && r.method() === 'DELETE'
    );
    const del2 = page.waitForRequest(
      r => r.url().endsWith('/v2/devices/device-2') && r.method() === 'DELETE'
    );
    await page.getByRole('button', {name: 'Delete', exact: true}).click(); // dialog confirm
    await Promise.all([del1, del2]);

    await expect(page.getByText('dev-a')).toHaveCount(0);
    await expect(page.getByText('dev-b')).toHaveCount(0);
  });
});

test('set a build as default', async ({page, api}) => {
  api.builds = [build({is_default: false})];
  await page.goto('/builds');
  await openRowMenu(page);
  const req = page.waitForRequest(
    r => r.url().endsWith('/v2/builds/build-1') && r.method() === 'PATCH'
  );
  await page.getByRole('menuitem', {name: 'Set as Default'}).click(); // no dialog
  const body = (await req).postDataJSON();
  expect(body.is_default).toBe(true);
});

// The build row's launch action opens the mode chooser, and the build travels
// on into the interview, which arrives with the Android question answered.
test('build-list launch action pre-answers the interview', async ({page}) => {
  await page.goto('/builds');
  await page.getByRole('link', {name: 'Create device with this build'}).click();

  await expect(page).toHaveURL(/\/devices\/create\?buildId=build-1/);
  await page.getByRole('link', {name: 'Simple', exact: true}).click();
  await expect(page).toHaveURL(/\/devices\/create\/simple\?buildId=build-1/);
  await expect(
    page.locator('.statement').getByText('Using AOSP Master Phone', {exact: false})
  ).toBeVisible();
});

// A branch-only registration tracks the newest build on its branch; the
// Builds page says so instead of leaving the semantics invisible.
test('tracking builds show the Tracks-latest chip', async ({page, api}) => {
  api.builds = [
    build(), // branch-only fixture -> tracking
    build({id: 'build-2', name: 'Pinned Build', build_id: '12345678'}),
  ];
  await page.goto('/builds');

  await expect(page.locator('.tracking-chip')).toHaveCount(1);
  await expect(page.getByText('12345678')).toBeVisible();
});

// The player is one click from the list for running devices.
test('running device rows link straight to the player', async ({page}) => {
  await page.goto('/devices');
  const playerLink = page.locator('a.open-player').first();
  await expect(playerLink).toBeVisible();
  await expect(playerLink).toHaveAttribute('href', '/devices/device-1/player');
});
