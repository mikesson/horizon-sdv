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
 * The Capacity page's zone board. One tile per zone of the placement space with
 * a server-composed verdict, the region rails carrying the quota chip, and the
 * headroom bars underneath, all from the capacity snapshot.
 *
 * Locators are testids and exact matches on purpose. `hasText` is
 * case-insensitive substring matching, and this page now carries copy that
 * appears on the neighbouring pages too ("quota", "Spot", zone names).
 */

import type {Locator, Page} from './fixtures/api-mock';
import {expect, mockError, test} from './fixtures/api-mock';

/** The tile for one zone, addressed by the zone name its header carries. */
function tile(page: Page, zone: string): Locator {
  return page.locator('.tile').filter({has: page.getByText(zone, {exact: true})});
}

test('the board answers with a stripe and a word per zone', async ({page}) => {
  await page.goto('/admin/capacity');

  const clear = tile(page, 'europe-west4-a');
  await expect(clear).toHaveClass(/clear/);
  await expect(clear.locator('.word')).toHaveText('Clear');
  await expect(clear.locator('.count')).toHaveText('2 machines · 6 devices · 1 arriving');
  await expect(clear.locator('.sig')).toContainText('Spot high');

  // The cooling zone shows what is left of its cooldown, from the snapshot's
  // own clock rather than the browser's.
  const cooling = tile(page, 'europe-west4-b');
  await expect(cooling).toHaveClass(/cooling/);
  await expect(cooling.locator('.word')).toContainText('Cooling ·');

  // A quota wall condemns the zone and says which pool ran out.
  const blocked = tile(page, 'us-east5-a');
  await expect(blocked).toHaveClass(/blocked/);
  await expect(blocked.locator('.word')).toHaveText('Blocked · quota');
  await expect(blocked).toContainText('CPUS in us-east5 is spent to its reserve');

  // Disabled is grey and says so in words, not by colour alone.
  const off = tile(page, 'us-central1-a');
  await expect(off).toHaveClass(/disabled/);
  await expect(off.locator('.word')).toHaveText('Disabled');
});

test('the region rail carries the quota verdict its tiles inherit', async ({page}) => {
  await page.goto('/admin/capacity');

  const rails = page.locator('.rail');
  await expect(rails.filter({hasText: 'europe-west4'}).locator('.chip')).toHaveText(
    'quota comfortable'
  );
  // Grouped, like every other numeral on the page.
  await expect(rails.filter({hasText: 'us-east5'}).locator('.chip')).toContainText(
    'quota spent, CPUS 2,912 of 3,000'
  );
});

test('the strip states what the zones short of clear are doing', async ({page}) => {
  await page.goto('/admin/capacity');

  const clear = page.locator('.kpi').filter({hasText: 'enabled zones clear'});
  await expect(clear.locator('.v')).toHaveText('1 of 3');
  // The breakdown is the server's count of its own verdicts, not a tally the
  // page made of the tiles beside it.
  await expect(clear.locator('.note')).toHaveText('1 cooling · 1 blocked');
});

test('a pool with no series draws a dashed track, never a zero', async ({page}) => {
  await page.goto('/admin/capacity');

  const dimensioned = page.locator('.pool').filter({hasText: 'CPUS_PER_VM_FAMILY:C4'});
  await expect(dimensioned).toHaveClass(/unknown/);
  await expect(dimensioned).toContainText('no data until first use');
  // A judged pool in the same panel shows its numbers rather than a share.
  await expect(page.locator('.pool').filter({hasText: 'SSD_TOTAL_GB'})).toContainText(
    '1,600 of 40,960'
  );
});

test('clearing a lapsed regional cooldown warns about the region and drops the cell', async ({
  page,
  api,
}) => {
  await page.goto('/admin/capacity');

  await page.getByRole('button', {name: 'Cooldown actions for europe-west4-b'}).click();
  await page.getByRole('menuitem', {name: 'Clear the n2 CPU quota failures'}).click();

  // The quota cell repels the whole region, which the tile does not show.
  await expect(page.getByText(/reopens every zone in the region/)).toBeVisible();

  const del = page.waitForRequest(
    r => r.url().endsWith('/v2/admin/zone-health/europe-west4-b/n2') && r.method() === 'DELETE'
  );
  await page.getByRole('button', {name: 'Clear', exact: true}).click();
  await del;

  // Only the refetch can empty the cell, so the cleared entry leaving the menu
  // is what proves the board re-read itself. The fixture is asserted after
  // that, since checking it straight after the request went out would race the
  // mock's own handler.
  await page.getByRole('button', {name: 'Cooldown actions for europe-west4-b'}).click();
  await expect(page.getByRole('menuitem', {name: 'Clear the n2 CPU quota failures'})).toHaveCount(
    0
  );
  // The zonal cooldown is untouched: clearing is per cell, not per zone.
  await expect(page.getByRole('menuitem', {name: 'Clear the n1 capacity cooldown'})).toBeVisible();
  expect(api.zoneCooldowns).toHaveLength(1);
});

test('a failed reclaimable read says so rather than reading as nothing to reclaim', async ({
  page,
}) => {
  await mockError(page, /\/v2\/admin\/reclaimable(\?|$)/, 500);
  await page.goto('/admin/capacity');

  const card = page.locator('section.card').filter({hasText: 'Reclaimable now'});
  await expect(card.getByRole('alert')).toBeVisible();
  // A count of zero would be a measurement; the read never produced one.
  await expect(page.locator('.kpi').filter({hasText: 'reclaimable now'}).locator('.v')).toHaveText(
    '-'
  );
});

test('a zone with no cells offers no clear action', async ({page}) => {
  await page.goto('/admin/capacity');
  await expect(tile(page, 'europe-west4-a')).toBeVisible();
  await expect(page.getByRole('button', {name: 'Cooldown actions for europe-west4-a'})).toHaveCount(
    0
  );
});
