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
 * Zone curation, now in the row it informs: the enablement toggle lives on the
 * board's tiles, and the zones Compute Engine offers that the fleet has never
 * placed into are one disclosure below it.
 */

import {expect, test} from './fixtures/api-mock';

test('the board carries the enablement toggle in the tile it belongs to', async ({page}) => {
  await page.goto('/admin/capacity');

  const westA = page
    .locator('.tile')
    .filter({has: page.getByText('europe-west4-a', {exact: true})});
  await expect(westA.getByRole('switch')).toBeChecked();

  const off = page.locator('.tile').filter({has: page.getByText('us-central1-a', {exact: true})});
  await expect(off.getByRole('switch')).not.toBeChecked();
});

/**
 * Enablement is one stored set, so switching one zone has to carry the zones it
 * is not touching along with it. A regression that sent only the toggled zone
 * would disable the rest of the placement space, which is what the request-body
 * assertion catches: the mock applies the PUT rather than echoing it.
 */
test('switching a zone off keeps the zones it did not touch enabled', async ({page}) => {
  await page.goto('/admin/capacity');

  const westB = page
    .locator('.tile')
    .filter({has: page.getByText('europe-west4-b', {exact: true})});
  await expect(westB.getByRole('switch')).toBeChecked();

  const put = page.waitForRequest(
    r => r.url().endsWith('/v2/admin/zones/enabled') && r.method() === 'PUT'
  );
  await westB.getByRole('switch').click();
  const request = await put;

  expect(request.postDataJSON()).toEqual({zones: ['europe-west4-a', 'us-east5-a']});
});

/**
 * The tile answers from the stored set, so the write has to land before the
 * snapshot is re-read: a refetch fired beside the PUT rather than after it
 * renders the pre-toggle board and stops there.
 */
test('switching a zone off moves its tile, not only its toggle', async ({page}) => {
  await page.goto('/admin/capacity');

  const westB = page
    .locator('.tile')
    .filter({has: page.getByText('europe-west4-b', {exact: true})});
  await expect(westB.locator('.word')).toContainText('Cooling');

  // Held open long enough that an unordered refetch would answer first.
  await page.route(/\/v2\/admin\/zones\/enabled/, async route => {
    await new Promise(resolve => setTimeout(resolve, 500));
    await route.fallback();
  });

  await westB.getByRole('switch').click();
  await expect(westB.getByRole('switch')).not.toBeChecked();
  await expect(westB.locator('.word')).toHaveText('Disabled');
});

/**
 * The disclosure is the only way into the placement space for a zone nothing
 * has run in. A zone the catalog snapshot said nothing about must not read as
 * offering none of the fleet's shapes. Those are different facts with
 * different fixes.
 */
test('the offered-zone list holds what the board does not, and can bring it in', async ({page}) => {
  await page.goto('/admin/capacity');

  const offered = page.locator('.offered');
  await expect(offered).toContainText('1 more zone Compute Engine offers');
  // A closed disclosure keeps its rows out of the accessibility tree, so the
  // switch is unreachable until the summary is opened.
  await offered.locator('summary').click();
  const row = offered.locator('.offered-row').filter({hasText: 'us-central1-f'});
  // Counted against the fleet's curated shapes, of which this zone stocks two.
  await expect(row).toContainText('2 of 3 shapes');
  // A zone the board already holds is not offered twice.
  await expect(offered.locator('.offered-row').filter({hasText: 'us-central1-a'})).toHaveCount(0);

  const put = page.waitForRequest(
    r => r.url().endsWith('/v2/admin/zones/enabled') && r.method() === 'PUT'
  );
  await row.getByRole('switch').click();
  expect((await put).postDataJSON()).toEqual({
    zones: ['europe-west4-a', 'europe-west4-b', 'us-east5-a', 'us-central1-f'],
  });

  // What the row reads back is the stored set, not the click that changed it.
  await page.reload();
  await page.locator('.offered summary').click();
  await expect(
    page.locator('.offered-row').filter({hasText: 'us-central1-f'}).getByRole('switch')
  ).toBeChecked();
});

test('the fleet ceiling is edited here and stated on the autoscaler', async ({page}) => {
  await page.goto('/admin/capacity');
  await expect(page.getByText("fleet ceiling, CCO's own admission quota")).toBeVisible();

  await page.goto('/admin/autoscaler');
  await expect(page.getByText('Fleet ceiling')).toBeVisible();
  await expect(page.getByRole('link', {name: 'Edit on Capacity'})).toBeVisible();
});
