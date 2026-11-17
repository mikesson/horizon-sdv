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

import type {Page} from './fixtures/api-mock';
import {expect, test} from './fixtures/api-mock';

// The advanced editor is a large, separate surface (CVD JSON config). These
// cover the two ways in and that the advanced component instantiates without
// error. Submission is device-create-advanced-flows' job.
test('the chooser opens Advanced mode', async ({page, pageErrors}) => {
  await page.goto('/devices/create');
  await page.getByRole('link', {name: 'Advanced', exact: true}).click();

  await expect(page).toHaveURL(/\/devices\/create\/advanced$/);
  await expect(page.getByText('Advanced mode', {exact: true})).toBeVisible();
  await expect(page.getByRole('button', {name: 'Add Instance'})).toBeVisible();
  expect(pageErrors, pageErrors.join('\n')).toHaveLength(0);
});

test('Simple continues into Advanced from its header', async ({page, pageErrors}) => {
  await page.goto('/devices/create/simple');

  // The Plan step's DOM also exists in the stepper, so scope to the statement.
  await expect(page.locator('.statement').getByText('AOSP Master Phone')).toBeVisible();

  await page.getByRole('button', {name: 'Continue in Advanced'}).click();

  await expect(page).toHaveURL(/\/devices\/create\/advanced$/);
  await expect(page.getByText('Advanced mode', {exact: true})).toBeVisible();
  expect(pageErrors, pageErrors.join('\n')).toHaveLength(0);
});

// The SDV preset puts a long bootconfig line in the JSON editor. A bare 1fr
// grid track grows to fit such a line, which would squeeze the form to a
// sliver, so the line has to scroll inside the editor instead.
test.describe('advanced layout with a long JSON line', () => {
  async function openSdvPair(page: Page): Promise<void> {
    await page.goto('/devices/create');
    await page.getByRole('link', {name: 'AAOS SDV Core + IVI'}).click();
    // The editor loads when its pane enters the viewport, which on a narrow
    // screen is below the form.
    await page.locator('.preview-panel').scrollIntoViewIfNeeded();
    await expect(page.locator('app-cvd-json-editor .cm-content')).toBeVisible();
    // Without this the rest passes on a config with no long line.
    const lineOverflow = await page.locator('app-cvd-json-editor').evaluate(el => {
      const content = el.querySelector('.cm-content');
      const wrapper = el.querySelector('.editor-wrapper');
      return (content?.scrollWidth ?? 0) - (wrapper?.clientWidth ?? 0);
    });
    expect(lineOverflow, 'no JSON line is wider than the editor').toBeGreaterThan(100);
  }

  async function box(
    page: Page,
    selector: string
  ): Promise<{x: number; y: number; width: number; height: number}> {
    const rect = await page.locator(selector).boundingBox();
    if (!rect) {
      throw new Error(`${selector} did not render`);
    }
    return rect;
  }

  function frameOverflow(page: Page): Promise<number> {
    return page.locator('.content').evaluate(el => el.scrollWidth - el.clientWidth);
  }

  test('keeps the form and the editor side by side at equal width', async ({page}) => {
    await page.setViewportSize({width: 1280, height: 800});
    await openSdvPair(page);

    const form = await box(page, '.config-panel');
    const editor = await box(page, '.preview-panel');
    expect(editor.x, 'the panes stacked at laptop width').toBeGreaterThan(form.x + form.width);
    expect(Math.abs(form.width - editor.width)).toBeLessThanOrEqual(1);
    // A pixel of slack for subpixel layout.
    expect(await frameOverflow(page)).toBeLessThanOrEqual(1);
  });

  test('stacks the panes at full width on a narrow screen', async ({page}) => {
    await page.setViewportSize({width: 760, height: 900});
    await openSdvPair(page);

    const form = await box(page, '.config-panel');
    const editor = await box(page, '.preview-panel');
    expect(editor.y, 'the panes are not stacked').toBeGreaterThanOrEqual(form.y + form.height);
    expect(Math.abs(form.width - editor.width)).toBeLessThanOrEqual(1);
    expect(await frameOverflow(page)).toBeLessThanOrEqual(1);
  });
});
