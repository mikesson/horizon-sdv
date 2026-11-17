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
 * Machine curation, now one decision per family. The page's subject is the
 * series an operator admits; what it holds is discovery's answer, shown and not
 * edited.
 */

import type {Locator, Page} from '@playwright/test';
import {expect, test} from './fixtures/api-mock';

const MACHINE_TYPES_TAB = '/admin/catalog?tab=machine-types';

/** The family row, located by its series rather than by its display name. */
function familyRow(page: Page, family: string): Locator {
  return page.locator('.family').filter({has: page.locator('.fname', {hasText: family})});
}

test('one row per family, with the two counts that differ', async ({page}) => {
  await page.goto(MACHINE_TYPES_TAB);

  await expect(page.locator('.family')).toHaveCount(3);
  await expect(familyRow(page, 'n1')).toContainText('2 shapes · 2 viable');
  // The C4A row is the one a listing built from the served catalog could not
  // carry: withdrawn, and holding a shape too small for the fleet to offer.
  await expect(familyRow(page, 'c4a')).toContainText('2 shapes · 1 viable');
});

test('a withdrawn family is still a row, with its switch off', async ({page}) => {
  await page.goto(MACHINE_TYPES_TAB);

  await expect(familyRow(page, 'n1').getByRole('switch')).toBeChecked();
  await expect(familyRow(page, 'c4a').getByRole('switch')).not.toBeChecked();
});

/**
 * Admitting a family is one call naming it. The request-body assertion is what
 * catches a regression that sent the family's whole curation along and
 * silently rewrote fields nobody touched.
 */
test('admitting a family patches only its enablement', async ({page}) => {
  await page.goto(MACHINE_TYPES_TAB);

  const patch = page.waitForRequest(
    r => r.url().endsWith('/v2/admin/machine-families/c4a') && r.method() === 'PATCH'
  );
  await familyRow(page, 'c4a').getByRole('switch').click();
  const request = await patch;

  expect(request.postDataJSON()).toEqual({is_enabled: true});
  await expect(familyRow(page, 'c4a').getByRole('switch')).toBeChecked();
});

test('the shape matrix states each shape size and what it holds', async ({page}) => {
  await page.goto(MACHINE_TYPES_TAB);
  await familyRow(page, 'n1').getByRole('button', {name: 'Shapes in n1'}).click();

  const matrix = familyRow(page, 'n1').locator('table');
  await expect(matrix).toBeVisible();
  // The width is the row and the variant is the column, so the cell states the
  // memory and the server's device count.
  await expect(matrix).toContainText('30 GB');
  await expect(matrix).toContainText('3 devices');
  await expect(matrix).toContainText('104 GB');
  await expect(matrix).toContainText('12 devices');
});

/**
 * A shape below the floor is reported and muted rather than dropped. The
 * class assertion is what makes this fail on a page that merely omitted it.
 * A text check for the size would pass on any rendering that kept the row.
 */
test('a shape too small to run a device is shown, muted, with the reason', async ({page}) => {
  await page.goto(MACHINE_TYPES_TAB);
  await familyRow(page, 'c4a').getByRole('button', {name: 'Shapes in c4a'}).click();

  const muted = familyRow(page, 'c4a').locator('td.muted');
  await expect(muted).toHaveCount(1);
  await expect(muted).toContainText('4 GB');
  await expect(muted).toContainText('no room for a device');
});

/**
 * The exclusion collapses to a count because the usual answer is none. N2
 * withholds one shape, so the summary must say so before it is opened.
 */
test('exclusions are a count until opened, and re-admitting writes the shape patch', async ({
  page,
}) => {
  await page.goto(MACHINE_TYPES_TAB);
  const row = familyRow(page, 'n2');
  await row.getByRole('button', {name: 'Shapes in n2'}).click();

  await expect(row.getByText('1 shape excluded')).toBeVisible();
  await row.getByText('1 shape excluded').click();

  const patch = page.waitForRequest(
    r => r.url().endsWith('/v2/admin/machine-types/n2-standard-16') && r.method() === 'PATCH'
  );
  await row.getByRole('button', {name: 'Return n2-standard-16 to the catalog'}).click();
  const request = await patch;

  expect(request.postDataJSON()).toEqual({is_enabled: true});
});

/**
 * The builder asks Compute Engine about the exact name and renders its answer.
 * A refusal is the interesting half. The console has no rule of its own about
 * what a family will build, so what appears must be the server's sentence.
 */
test('the custom shape builder renders Compute Engine refusal and resolution', async ({page}) => {
  await page.goto(MACHINE_TYPES_TAB);
  await page.getByRole('button', {name: 'Custom shape'}).click();

  const memory = page.getByLabel('Memory (MB)');
  await memory.fill('32700');
  await expect(page.getByText('Memory should be a multiple of 256MiB')).toBeVisible();
  await expect(page.getByRole('button', {name: 'Use shape'})).toBeDisabled();

  await memory.fill('32768');
  await expect(page.getByText('8 vCPU · 32 GB · holds 3 devices')).toBeVisible();
  await expect(page.getByRole('button', {name: 'Use shape'})).toBeEnabled();
});
