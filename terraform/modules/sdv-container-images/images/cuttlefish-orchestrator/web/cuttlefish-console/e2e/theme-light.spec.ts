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
 * The light theme's surfaces, text and status roles.
 *
 * `theme-surfaces.spec.ts` and `theme-accent.spec.ts` both pin the OS to dark,
 * and `appearance.spec.ts` reads one property on one element, so before this
 * file the light theme was asserted by a single background colour. Everything
 * that only shows up on a light surface was unguarded, and four such roles were
 * shipping. That covers a dark-ramp value used raw and a foreground that does
 * not invert with its container.
 *
 * Each probe names the dark value it must not be, because that is the failure
 * mode: a `light-dark()` pair resolving to the wrong branch, or a colour that
 * never became a pair at all, both land on the dark value here.
 */

import {device, expect, test} from './fixtures/api-mock';

test.use({colorScheme: 'light'});

interface Probe {
  readonly label: string;
  readonly selector: string;
  readonly property: 'background-color' | 'color';
  readonly expected: string;
  /** What renders here if the role never became a light-dark() pair. */
  readonly darkValue: string;
}

const SHELL: readonly Probe[] = [
  {
    label: 'chrome, top bar',
    selector: '.topbar',
    property: 'background-color',
    expected: 'rgb(240, 245, 254)',
    darkValue: 'rgb(40, 44, 51)',
  },
  {
    label: 'chrome, nav rail',
    selector: '.navrail',
    property: 'background-color',
    expected: 'rgb(240, 245, 254)',
    darkValue: 'rgb(40, 44, 51)',
  },
  {
    label: 'canvas behind cards',
    selector: '.content',
    property: 'background-color',
    expected: 'rgb(249, 250, 251)',
    darkValue: 'rgb(31, 33, 35)',
  },
  {
    label: 'body text',
    selector: 'body',
    property: 'color',
    expected: 'rgb(32, 33, 36)',
    darkValue: 'rgb(232, 234, 237)',
  },
  {
    label: 'secondary text',
    selector: '.page-subtitle',
    property: 'color',
    expected: 'rgb(96, 98, 105)',
    darkValue: 'rgb(190, 192, 198)',
  },
];

async function check(page: import('@playwright/test').Page, probe: Probe): Promise<void> {
  // Also the wait for the route to render: without it a computed read samples
  // an empty page and the probe passes having asserted on nothing.
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

  expect
    .soft(value, `${probe.label} (${probe.property}), dark value is ${probe.darkValue}`)
    .toBe(probe.expected);
}

test('shell surfaces and text take the light ramp', async ({page}) => {
  await page.goto('/devices');
  await expect(page.getByRole('link', {name: 'Hosts', exact: true})).toBeVisible();

  for (const probe of SHELL) {
    await check(page, probe);
  }
});

test('the accent inverts to the light-theme blue', async ({page}) => {
  await page.goto('/devices');
  await expect(page.getByRole('link', {name: 'Hosts', exact: true})).toBeVisible();

  await check(page, {
    label: 'filled button container',
    selector: 'button.mat-mdc-unelevated-button:not(.mat-warn):not(.mat-accent):not(:disabled)',
    property: 'background-color',
    expected: 'rgb(14, 103, 224)',
    darkValue: 'rgb(138, 180, 248)',
  });
});

/**
 * A status dot, which reaches the token layer through a component's own scoped
 * rule rather than the global sheet. Every component that draws one carries
 * its own ramp, and those rules win. `running` is the probe because its light
 * and dark values are far apart; `creating` would not be, since the component
 * paints it with `--cco-accent`, whose light value is the same blue the status
 * token resolves to. A probe both candidates satisfy asserts nothing.
 */
test('status roles have light counterparts', async ({page, api}) => {
  api.devices = [device({id: 'device-1', name: 'running-device', status: 'running'})];

  await page.goto('/devices');
  await expect(page.getByText('running-device')).toBeVisible();

  await check(page, {
    label: 'running status dot',
    selector: '.status-indicator.running',
    property: 'background-color',
    expected: 'rgb(13, 101, 45)',
    darkValue: 'rgb(129, 201, 149)',
  });
});

test('the status chip label inverts with its surface', async ({page, api}) => {
  api.devices = [device({id: 'device-1', status: 'creating'})];

  await page.goto('/devices/device-1');

  await check(page, {
    label: 'creating chip label',
    selector: '.mat-mdc-chip.creating',
    property: 'color',
    expected: 'rgb(14, 103, 224)',
    darkValue: 'rgb(138, 180, 248)',
  });
});
