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
 * Light, dark and same-as-device.
 *
 * Every assertion is on a computed colour rather than the stored preference or
 * the data attribute. Those only say what was *asked for*; a light-dark() pair
 * that resolves the same in both schemes would satisfy them and still ship one
 * theme, which is the exact shape of gate this arc has already been caught by.
 */

import {expect, test} from './fixtures/api-mock';

const CANVAS_DARK = 'rgb(31, 33, 35)';
const CANVAS_LIGHT = 'rgb(249, 250, 251)';

function canvas(page: import('@playwright/test').Page): Promise<string> {
  return page
    .locator('.content')
    .evaluate(el => getComputedStyle(el).getPropertyValue('background-color'));
}

test.describe('follows the operating system when nothing is chosen', () => {
  test.use({colorScheme: 'dark'});

  test('a dark OS gives the dark canvas', async ({page}) => {
    await page.goto('/devices');
    await expect(page.getByRole('link', {name: 'Hosts', exact: true})).toBeVisible();
    expect(await canvas(page)).toBe(CANVAS_DARK);
  });
});

test.describe('follows a light operating system too', () => {
  test.use({colorScheme: 'light'});

  test('a light OS gives the light canvas', async ({page}) => {
    await page.goto('/devices');
    await expect(page.getByRole('link', {name: 'Hosts', exact: true})).toBeVisible();
    expect(await canvas(page)).toBe(CANVAS_LIGHT);
  });
});

test.describe('an explicit choice overrides the operating system', () => {
  test.use({colorScheme: 'dark'});

  test('choosing Light flips the canvas with no reload', async ({page}) => {
    await page.goto('/devices');
    await expect(page.getByRole('link', {name: 'Hosts', exact: true})).toBeVisible();
    expect(await canvas(page)).toBe(CANVAS_DARK);

    // Pin the document so a reload would be detectable rather than assumed.
    await page.evaluate(() => {
      (window as unknown as {ccoAlive: boolean}).ccoAlive = true;
    });

    await page.getByRole('button', {name: 'Appearance'}).click();
    await page.getByRole('menuitem', {name: 'Light'}).click();

    await expect.poll(() => canvas(page)).toBe(CANVAS_LIGHT);

    const survived = await page.evaluate(
      () => (window as unknown as {ccoAlive?: boolean}).ccoAlive === true
    );
    expect(survived, 'the page reloaded, and the console should not').toBe(true);
  });

  test('the choice survives a reload, and Same as device gives it back', async ({page}) => {
    await page.goto('/devices');
    await expect(page.getByRole('link', {name: 'Hosts', exact: true})).toBeVisible();

    await page.getByRole('button', {name: 'Appearance'}).click();
    await page.getByRole('menuitem', {name: 'Light'}).click();
    await expect.poll(() => canvas(page)).toBe(CANVAS_LIGHT);

    await page.reload();
    await expect(page.getByRole('link', {name: 'Hosts', exact: true})).toBeVisible();
    expect(await canvas(page), 'the stored choice did not survive').toBe(CANVAS_LIGHT);

    await page.getByRole('button', {name: 'Appearance'}).click();
    await page.getByRole('menuitem', {name: 'Same as device'}).click();
    await expect.poll(() => canvas(page)).toBe(CANVAS_DARK);
  });
});
