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
 * The shell's surface and text roles, against the values sampled from the live
 * Cloud Console.
 *
 * Asserted as computed style rather than sampled pixels. The console
 * measurements had to come from screenshots and those were Display P3, so every
 * saturated value read raw was wrong. The computed value avoids that conversion
 * entirely. It is also the authored colour (before compositing), which is what
 * these tokens are.
 *
 * The canvas is *lighter* than the page surface, and the chrome is lighter
 * than both. This matches the Cloud Console's layering and is intended.
 */

import {expect, test} from './fixtures/api-mock';

// These assert the dark palette, so the OS scheme has to be pinned: with
// the appearance switcher in place the console follows it, and Playwright
// defaults to light.
test.use({colorScheme: 'dark'});

interface SurfaceProbe {
  readonly label: string;
  readonly selector: string;
  readonly property: 'background-color' | 'color';
  readonly expected: string;
}

const PROBES: readonly SurfaceProbe[] = [
  // The shell continues the chrome behind the rounded content frame; there is
  // no darker outer gutter in either console theme.
  {
    label: 'app shell',
    selector: '.app-shell',
    property: 'background-color',
    expected: 'rgb(40, 44, 51)',
  },
  {
    label: 'top bar',
    selector: '.topbar',
    property: 'background-color',
    expected: 'rgb(40, 44, 51)',
  },
  {
    label: 'nav rail',
    selector: '.navrail',
    property: 'background-color',
    expected: 'rgb(40, 44, 51)',
  },
  {
    label: 'canvas behind cards',
    selector: '.content',
    property: 'background-color',
    expected: 'rgb(31, 33, 35)',
  },
  {label: 'body text', selector: 'body', property: 'color', expected: 'rgb(232, 234, 237)'},
];

test('shell surfaces and text match the console palette', async ({page}) => {
  await page.goto('/devices');
  await expect(page.getByRole('link', {name: 'Hosts', exact: true})).toBeVisible();

  for (const probe of PROBES) {
    await expect
      .soft(
        page.locator(probe.selector),
        `${probe.label}: nothing matched "${probe.selector}", so this probe asserts nothing`
      )
      .not.toHaveCount(0);

    const value = await page
      .locator(probe.selector)
      .first()
      .evaluate((el, property) => getComputedStyle(el).getPropertyValue(property), probe.property);
    expect.soft(value, `${probe.label} (${probe.property})`).toBe(probe.expected);
  }
});

test('secondary text is the brighter console value, not the old grey', async ({page}) => {
  await page.goto('/devices');
  await expect(page.getByRole('link', {name: 'Hosts', exact: true})).toBeVisible();

  // Read off a rendered element, not the custom property. The property now
  // holds a light-dark() pair, and its text says nothing about which branch a
  // viewer actually gets.
  const subtitle = await page.locator('.page-subtitle').evaluate(el => getComputedStyle(el).color);
  expect(subtitle).toBe('rgb(190, 192, 198)');
});
