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

// Async verbs were visible one device at a time; this is the only fleet-wide
// view of what is running.
test('the control plane lists in-flight operations across resources', async ({page}) => {
  const listing = page.waitForRequest(
    r => r.url().includes('/v2/operations') && r.url().includes('done=false')
  );
  await page.goto('/admin/control-plane');
  await listing;

  const panel = page.locator('app-operations-panel');
  await expect(panel.getByRole('heading', {name: 'In-flight operations'})).toBeVisible();
  await expect(panel.getByText('restart')).toBeVisible();
  await expect(panel.getByText('powerwash')).toBeVisible();

  // A retrying operation says so, with the cause the task recorded.
  await expect(panel.getByText('attempt 3')).toBeVisible();
  await expect(panel.getByText('host unreachable')).toBeVisible();

  // Each row deep-links to the resource it is acting on.
  await panel.getByRole('link', {name: 'device-1'}).click();
  await expect(page).toHaveURL(/\/devices\/device-1$/);
});

test('an empty fleet says nothing is running rather than showing a blank panel', async ({
  page,
  api,
}) => {
  api.openOperations = [];
  await page.goto('/admin/control-plane');

  const panel = page.locator('app-operations-panel');
  await expect(panel.getByText('Nothing running right now')).toBeVisible();
});

// The operations leg is tolerated: it must not blank the page the way a failed
// snapshot does, because the snapshot is what the rest of the page renders.
test('a failing operations read degrades only its own panel', async ({page}) => {
  await page.route(/\/v2\/operations(\?|$)/, route => route.fulfill({status: 503, body: '{}'}));
  await page.goto('/admin/control-plane');

  await expect(page.locator('app-operations-panel').getByText(/Could not refresh/)).toBeVisible();
  // The working set beside it still rendered from the snapshot.
  await expect(page.getByRole('heading', {name: 'Working set'})).toBeVisible();
});
