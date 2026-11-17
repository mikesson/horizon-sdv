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
 * Wide content scrolls inside itself; the page it sits on does not move.
 *
 * The devices table used to take the whole frame sideways with it (header,
 * filters and all) because nine columns do not fit a laptop and nothing
 * contained them. The fix was two changes in different files. These tests
 * assert the outcome instead, so any future arrangement that delivers it
 * passes and any that loses it goes red.
 */

import {expect, test} from './fixtures/api-mock';

// Every route a signed-in user can reach, including the two detail pages,
// whose supporting pane splits the width the content had to itself.
const ROUTES = [
  '/',
  '/devices',
  '/devices/device-1',
  '/hosts',
  '/hosts/host-1',
  '/batches',
  '/builds',
  '/admin/capacity',
  '/admin/control-plane',
  '/admin/catalog',
  '/admin/activity',
  '/admin/settings',
];

// The width the original defect showed up at, and the commonest laptop there
// is. Wider hides it; narrower is not what anyone runs the console on.
const LAPTOP = {width: 1280, height: 800};

for (const route of ROUTES) {
  test(`the page frame stays put on ${route}`, async ({page}) => {
    await page.setViewportSize(LAPTOP);
    await page.goto(route);
    // The rail is on every authenticated route, so its presence means the
    // shell and the route's data have both rendered and the measurement is
    // taken against a real page rather than an empty outlet.
    await expect(page.getByRole('link', {name: 'Hosts', exact: true})).toBeVisible();

    const overflow = await page.locator('.content').evaluate(el => el.scrollWidth - el.clientWidth);

    // A pixel of slack for subpixel layout; 159 was the original defect.
    expect(overflow, `the page frame scrolls ${overflow}px sideways`).toBeLessThanOrEqual(1);
  });
}

/**
 * The devices table is the one thing in the console with a hard width floor,
 * so it is the one that has to scroll itself. The sticky columns exist to
 * anchor the identifier. A value at the far right is worthless if you cannot
 * see which row it belongs to.
 *
 * Measured on rendered geometry rather than on the authored CSS, because
 * `position: sticky` on a `td` has to survive the `border-collapse: collapse`
 * the table mixin sets, and reading the stylesheet cannot tell you whether it
 * did.
 */
test('the name column stays anchored while the table scrolls under it', async ({page}) => {
  await page.setViewportSize({width: 1100, height: 800});
  await page.goto('/devices');

  const scroller = page.locator('.table-scroll');
  const name = page.locator('td.mat-column-name').first();
  const created = page.locator('td.mat-column-created').first();
  await expect(name).toBeVisible();

  // Without this the rest passes on a table that never scrolled: every cell
  // stays where it was, including the ones that were supposed to move.
  const scrollable = await scroller.evaluate(el => el.scrollWidth - el.clientWidth);
  expect(scrollable, 'the table did not overflow, so nothing below is tested').toBeGreaterThan(200);

  const nameBefore = await name.boundingBox();
  const createdBefore = await created.boundingBox();

  const SHIFT = 200;
  await scroller.evaluate((el, x) => el.scrollTo({left: x}), SHIFT);
  await expect.poll(() => scroller.evaluate(el => el.scrollLeft)).toBe(SHIFT);

  const nameAfter = await name.boundingBox();
  const createdAfter = await created.boundingBox();

  expect(createdBefore?.x, 'the unpinned column never moved').toBeCloseTo(
    (createdAfter?.x ?? 0) + SHIFT,
    0
  );
  expect(nameAfter?.x, 'the name column scrolled away with the rest').toBeCloseTo(
    nameBefore?.x ?? 0,
    0
  );

  // Anchored but transparent reads as two rows of text on top of each other.
  // Nothing in this component paints the sticky cells; Material's own
  // `background: inherit` does, off the opaque colour on the table.
  const fill = await name.evaluate(el => getComputedStyle(el).backgroundColor);
  expect(fill, 'the sticky cell is transparent, so the columns show through').not.toMatch(
    /rgba\(.*,\s*0\)$/
  );
});
