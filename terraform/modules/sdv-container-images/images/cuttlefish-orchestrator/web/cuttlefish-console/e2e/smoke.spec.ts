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

import {expect, test} from './fixtures/api-mock';

// Each route loads behind the auth guard, fetches its feature data, and renders.
// `text` is a value from the mocked backend that must appear once data binds.
// It proves the page wired the API. A page that only avoided crashing fails it.
const ROUTES: Array<{path: string; name: string; text?: string}> = [
  {path: '/', name: 'dashboard', text: 'tester@example.com'},
  {path: '/hosts', name: 'hosts', text: 'test-host'},
  {path: '/devices', name: 'devices', text: 'my-device'},
  {path: '/batches', name: 'batches', text: 'ci-run'},
  {path: '/batches/batch-1', name: 'batch-detail', text: 'ci-run-000'},
  {path: '/builds', name: 'builds', text: 'AOSP Master Phone'},
  {path: '/admin/capacity', name: 'admin-capacity', text: 'europe-west4-a'},
  {path: '/admin/control-plane', name: 'admin-control-plane', text: 'device.create_cvd'},
  {path: '/admin/catalog', name: 'admin-catalog', text: 'cco-host'},
  // Pre-catalog deep link; must redirect into the catalog page.
  {path: '/admin/host-images', name: 'admin-host-images', text: 'cco-host'},
  // Pre-rename deep link, from before the API took the "operations" noun for
  // AIP-151 handles; must redirect into the control-plane page.
  {path: '/admin/operations', name: 'admin-operations-redirect', text: 'device.create_cvd'},
  // The zones tab moved to Capacity; a bookmark of it must land on the board
  // rather than silently on the first remaining tab.
  {path: '/admin/catalog?tab=zones', name: 'admin-catalog-zones-redirect', text: 'Zone board'},
  {path: '/admin/activity', name: 'admin-activity', text: 'quota.update'},
  // Field values sit in inputs, where getByText cannot see them, so the
  // smoke asserts a card title; settings.spec.ts asserts the values.
  {path: '/admin/settings', name: 'admin-settings', text: 'Session and lease lifetimes'},
  // Both consolidated routes were bookmarkable, so both must still land here.
  {path: '/admin/artifacts', name: 'admin-artifacts-redirect', text: 'test-artifacts'},
  {path: '/admin/placement', name: 'admin-placement-redirect', text: 'Session and lease lifetimes'},
];

for (const route of ROUTES) {
  test(`renders ${route.name}`, async ({page, pageErrors}, testInfo) => {
    await page.goto(route.path);

    // The auth guard sends failed identity loads here; reaching it means the
    // mocked /v2/me didn't take.
    await expect(page).not.toHaveURL(/\/unauthorized/);

    // App shell rendered (nav rail present on every authenticated route).
    // exact: the dashboard also has a "Hosts" quick-access card.
    await expect(page.getByRole('link', {name: 'Hosts', exact: true})).toBeVisible();

    if (route.text) {
      await expect(page.getByText(route.text).first()).toBeVisible();
    }

    await testInfo.attach(`${route.name}.png`, {
      body: await page.screenshot({fullPage: true}),
      contentType: 'image/png',
    });

    expect(pageErrors, pageErrors.join('\n')).toHaveLength(0);
  });
}
