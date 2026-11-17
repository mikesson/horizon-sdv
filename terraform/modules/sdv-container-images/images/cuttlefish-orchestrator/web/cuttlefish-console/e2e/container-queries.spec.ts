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
 * Pages respond to the space they have, not to the viewport.
 *
 * The distinction only shows up when the two disagree, which is why this
 * collapses the nav rail at a fixed viewport width: the window never changes
 * size, so a viewport media query cannot produce the effect being asserted. Any
 * rule that survives this test is reading its container.
 */

import {expect, test} from './fixtures/api-mock';

test('the content container tracks the nav rail, not the window', async ({page}) => {
  await page.setViewportSize({width: 1100, height: 800});
  await page.goto('/devices');
  await expect(page.getByRole('link', {name: 'Hosts', exact: true})).toBeVisible();

  const width = (): Promise<number> =>
    page.locator('.content').evaluate(el => Math.round(el.getBoundingClientRect().width));

  const expanded = await width();

  await page.getByRole('button', {name: 'Toggle navigation'}).click();
  await expect(page.locator('.navrail')).toHaveAttribute('inert', '');

  // The rail collapses over a width transition, so poll rather than sampling
  // once. A single read lands mid-flight and reports no change at all.
  await expect
    .poll(async () => (await width()) - expanded, {message: 'the container never widened'})
    .toBeGreaterThan(200);

  // And the container query sees it: a container declared on .content reports a
  // query container, which is what makes the @container rules resolve at all.
  const isContainer = await page
    .locator('.content')
    .evaluate(el => getComputedStyle(el).containerType);
  expect(isContainer, '.content is not a query container').toBe('inline-size');
});

/**
 * The test above proves the container exists and resizes. It does not prove any
 * `@container` rule reads it. A rule naming a container that does not exist,
 * or sitting at a threshold nothing ever crosses, passes it unchanged. This one
 * asserts a rendered property flips, driven only by the rail.
 *
 * 1200px is chosen so the 999px threshold falls *between* the two states: with
 * the rail open the page has ~945px and the supporting pane sits under the
 * content, with it closed ~1185px and the pane moves beside it. Any viewport
 * where both states sit on the same side of the threshold would pass while
 * asserting nothing.
 */
test('a @container rule flips on the rail alone, at a fixed viewport', async ({page}) => {
  await page.setViewportSize({width: 1200, height: 900});
  await page.goto('/devices/device-1');

  const body = page.locator('.page-layout-body.variant-detail');
  await expect(body, 'the detail layout never rendered, so nothing can be asserted').toHaveCount(1);

  const direction = (): Promise<string> => body.evaluate(el => getComputedStyle(el).flexDirection);

  expect(await direction(), 'the pane should start stacked with the rail open').toBe('column');

  await page.getByRole('button', {name: 'Toggle navigation'}).click();
  await expect(page.locator('.navrail')).toHaveAttribute('inert', '');

  await expect
    .poll(direction, {
      message: 'the pane never moved beside the content, so no @container rule read the rail',
    })
    .toBe('row');

  // The window never moved, so nothing here could have come from a media query.
  expect(page.viewportSize()?.width).toBe(1200);
});

/**
 * Which side of the content the pane lands on once it stacks.
 *
 * It goes above: stacked, the summary holds the only pin control on the page,
 * and under the content that sits past every card the device has. Asserted on
 * geometry rather than on the `order` declaration, because the property is only
 * a means. A later grid or a reordered projection that puts the pane on top
 * should pass this, and only losing the position should fail it.
 */
test('the stacked summary sits above the content, not under it', async ({page}) => {
  await page.setViewportSize({width: 1200, height: 900});
  await page.goto('/devices/device-1');

  const body = page.locator('.page-layout-body.variant-detail');
  const aside = page.locator('.page-layout-sidebar');
  const content = page.locator('.page-layout-content');
  await expect(aside).toBeVisible();

  // With the rail open this viewport gives the page ~945px, under the 999px
  // threshold. Without this the comparison below is between two panes sitting
  // side by side, where a top edge says nothing about reading order.
  expect(
    await body.evaluate(el => getComputedStyle(el).flexDirection),
    'the panes are side by side, so there is no stacking order to assert'
  ).toBe('column');

  const asideBox = await aside.boundingBox();
  const contentBox = await content.boundingBox();
  expect(asideBox?.y, 'the summary stacked under the content').toBeLessThan(contentBox?.y ?? 0);

  // Reading order moved and the tab order stayed as it was. The pane still
  // follows the content in the DOM, and this pins that half of the trade so a
  // later "fix" for the mismatch cannot land unnoticed.
  const domOrder = await body.evaluate(el =>
    [...el.children].map(c => (c.classList.contains('page-layout-sidebar') ? 'aside' : 'content'))
  );
  expect(domOrder, 'the pane moved in the DOM, which changes the tab order').toEqual([
    'content',
    'aside',
  ]);
});
