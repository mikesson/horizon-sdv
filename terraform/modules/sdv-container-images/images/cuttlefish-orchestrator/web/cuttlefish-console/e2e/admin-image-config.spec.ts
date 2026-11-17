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

import {expect, type Page, test} from './fixtures/api-mock';

// The edit trigger is an icon-only mat-icon-button (aria-hidden icon, no
// accessible name); the seeded n1 card renders first, ahead of the arm group.
async function openEditDialog(page: Page): Promise<void> {
  await page.goto('/admin/host-images');
  await page.locator('.config-card button').first().click();
}

// The shared fixture only mocks the list route; the item PUT must return a
// Zod-valid config (the store parses it with HostImageConfigSchema).
async function mockUpdate(page: Page, overrides: Record<string, unknown>): Promise<void> {
  const now = new Date().toISOString();
  await page.route(/\/v2\/admin\/host-image-configs\/n1$/, route =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        machine_family: 'n1',
        image_family: 'cco-host',
        source_project: 'example-image-project',
        description: 'Default Cuttlefish host image',
        enabled: true,
        created_at: now,
        updated_at: now,
        ...overrides,
      }),
    })
  );
}

test.describe('host image config edit dialog', () => {
  test('validates image family (required + pattern)', async ({page}) => {
    await openEditDialog(page);
    const dialog = page.getByRole('dialog');
    const imageFamily = dialog.getByLabel('Image Family');
    const save = dialog.getByRole('button', {name: 'Save Changes'});

    await expect(imageFamily).toHaveValue('cco-host');
    await expect(save).toBeEnabled();

    await imageFamily.fill('Not-Valid');
    await imageFamily.blur();
    await expect(dialog.getByText('Must be lowercase letters, numbers, and hyphens')).toBeVisible();
    await expect(save).toBeDisabled();

    await imageFamily.fill('');
    await imageFamily.blur();
    await expect(dialog.getByText('Image family is required')).toBeVisible();
    await expect(save).toBeDisabled();

    await imageFamily.fill('cco-host');
    await expect(save).toBeEnabled();
  });

  test('saves edited values including the enabled toggle', async ({page}) => {
    await mockUpdate(page, {image_family: 'cco-host-v2', enabled: false});
    await openEditDialog(page);
    const dialog = page.getByRole('dialog');

    await dialog.getByLabel('Image Family').fill('cco-host-v2');
    await dialog.getByRole('switch', {name: 'Enabled'}).click();

    const req = page.waitForRequest(
      r => r.url().endsWith('/v2/admin/host-image-configs/n1') && r.method() === 'PUT'
    );
    await dialog.getByRole('button', {name: 'Save Changes'}).click();
    expect((await req).postDataJSON()).toMatchObject({
      image_family: 'cco-host-v2',
      enabled: false,
    });
    await expect(page.getByText('Configuration for n1 updated')).toBeVisible();
  });
});

test.describe('host image status', () => {
  test('shows the image each family resolves to, or why it has none', async ({page}) => {
    await page.goto('/admin/host-images');

    await expect(page.getByText('cco-host-20261018')).toBeVisible();
    await expect(page.getByText('not_found')).toBeVisible();
    await expect(page.getByText('Default Image Project')).toHaveCount(0);
  });
});
