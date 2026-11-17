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

// The whole history of one resource is the incident question Activity exists
// to answer; the filter is sent server-side, not applied to the loaded page.
test('filters the audit trail to a single resource id', async ({page}) => {
  await page.goto('/admin/activity');
  await expect(page.getByText('device.create')).toBeVisible();
  await expect(page.getByText('quota.update')).toBeVisible();

  const filtered = page.waitForRequest(
    req => req.url().includes('/v2/admin/audit') && req.url().includes('resource_id=device-1')
  );
  await page.getByLabel('Resource ID').fill('device-1');
  await page.getByRole('button', {name: 'Apply'}).click();
  await filtered;

  await expect(page.getByText('device.create')).toBeVisible();
  await expect(page.getByText('quota.update')).toBeHidden();

  // Clear drops it from the query rather than leaving it pinned.
  const cleared = page.waitForRequest(
    req => req.url().includes('/v2/admin/audit') && !req.url().includes('resource_id')
  );
  await page.getByRole('button', {name: 'Clear'}).click();
  await cleared;
  await expect(page.getByText('quota.update')).toBeVisible();
});

// The category select is what replaced the settings page's recent-changes
// strip: one click narrows the trail to settings changes, server-side.
test('filters the audit trail to settings changes by category', async ({page}) => {
  await page.goto('/admin/activity');
  await expect(page.getByText('device.create')).toBeVisible();

  const filtered = page.waitForRequest(
    req => req.url().includes('/v2/admin/audit') && req.url().includes('kind=system_config')
  );
  await page.getByLabel('Category').click();
  await page.getByRole('option', {name: 'Settings changes'}).click();
  await page.getByRole('button', {name: 'Apply'}).click();
  await filtered;

  const table = page.locator('app-activity-table');
  await expect(table.getByText('zone-pin', {exact: true})).toBeVisible();
  await expect(table.getByText('device.create')).toBeHidden();
  // Deep-linkable, like every other filter on this page.
  expect(page.url()).toContain('kind=system_config');
});
