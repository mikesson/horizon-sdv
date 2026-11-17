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
 * The Capacity page's Spot card. It shows the fleet's model split, 7-day
 * preemption churn, and whether the advice steering placement is fresh, all
 * read from the capacity snapshot's spot block.
 */

import {expect, test} from './fixtures/api-mock';

test('capacity page shows the Spot capacity card', async ({page}) => {
  await page.goto('/admin/capacity');

  const card = page.locator('section.card').filter({hasText: 'Spot capacity'});
  await expect(card).toBeVisible();

  // 2 Spot of 4 machines (the mock's fleet census).
  await expect(card.getByText('2 Spot · 2 on-demand')).toBeVisible();

  // Churn: 3 preempted, all recovered, with the typical duration beside it.
  await expect(card.getByText('Taken back by Google, last 7 days')).toBeVisible();
  await expect(card.getByText('34s')).toBeVisible();

  // Advice health: 36 fresh cells.
  await expect(card.getByText(/fresh · 36 cells · synced .* ago/)).toBeVisible();
});

test('the control plane no longer answers for the placement space', async ({page}) => {
  await page.goto('/admin/control-plane');

  await expect(page.getByRole('heading', {name: 'Reconciliation loops'})).toBeVisible();
  await expect(page.getByRole('heading', {name: 'Placement space'})).toHaveCount(0);
  await expect(page.getByRole('heading', {name: 'Spot capacity'})).toHaveCount(0);
});
