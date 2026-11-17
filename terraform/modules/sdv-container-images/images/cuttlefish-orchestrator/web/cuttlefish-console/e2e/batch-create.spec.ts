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

// The stepper keeps every step in the DOM, so a bare role query can resolve to
// the outgoing step's button. Click the visible one, then wait for arrival.
async function goToStep(page: Page, button: string, arrivalHeading: string): Promise<void> {
  await page.getByRole('button', {name: button}).filter({visible: true}).click();
  await expect(page.getByRole('heading', {name: arrivalHeading})).toBeVisible();
}

/** Template → Usage → Shape, stopping on the Plan step. */
async function fillToPlan(page: Page, prefix: string): Promise<void> {
  await page.goto('/batches/create');
  await page.getByLabel('Name prefix').fill(prefix);
  await goToStep(page, 'Next', 'Usage');
  await goToStep(page, 'Next', 'Shape');
}

test('plans and launches a batch, and the plan names the fleet it would build', async ({page}) => {
  await fillToPlan(page, 'e2e-batch');

  await page.getByRole('button', {name: '10', exact: true}).click();

  const planReq = page.waitForRequest(
    req =>
      req.url().endsWith('/v2/batches') &&
      req.method() === 'POST' &&
      req.postDataJSON()?.validate_only === true
  );
  await goToStep(page, 'See the plan', 'Plan');

  // The dry run carries the whole spec, with the defaults the wizard applies.
  expect((await planReq).postDataJSON()).toMatchObject({
    name_prefix: 'e2e-batch',
    count: 10,
    min_ready: 10,
    fill_policy: 'best_effort',
    placement: 'isolated',
    validate_only: true,
    requirements: {interaction: 'headless'},
  });

  // The plan reports the fleet, its member names, and the hosts it provisions.
  // Scoped to the step: the stepper keeps the earlier steps' DOM, and both the
  // name-prefix hint and the Launch button echo these strings.
  const plan = page.locator('app-batch-plan-step');
  await expect(plan.getByText('10 devices')).toBeVisible();
  await expect(plan.getByText(/e2e-batch-000 … e2e-batch-009/)).toBeVisible();
  await expect(plan.getByText('3 new hosts')).toBeVisible();
  await expect(plan.getByText('Will provision n1-standard-8 in europe-west4-a')).toBeVisible();

  // The zone ranking's trace is available without leaving the step.
  await plan.getByText('Why this zone').click();
  await expect(plan.getByText('most free host capacity')).toBeVisible();

  const createReq = page.waitForRequest(
    req =>
      req.url().endsWith('/v2/batches') &&
      req.method() === 'POST' &&
      req.postDataJSON()?.validate_only !== true
  );
  await page.getByRole('button', {name: 'Launch 10 devices'}).click();

  // The create is keyed, which is what makes a retry resume rather than
  // provision a second fleet.
  const created = await createReq;
  expect(created.headers()['idempotency-key']).toMatch(/^[0-9a-f-]{36}$/);
  expect(created.postDataJSON()).not.toHaveProperty('validate_only');

  await expect(page).toHaveURL(/\/batches\/batch-2$/);
});

// The GPU constraint is the one answer that can get a batch rejected outright,
// and it was the only requirement the plan step never said back. Both halves
// are checked here: that it reaches the wire, and that the review echoes it.
test('a required GPU reaches the wire and is echoed on the plan', async ({page}) => {
  await page.goto('/batches/create');
  await page.getByLabel('Name prefix').fill('ci-gpu');
  await goToStep(page, 'Next', 'Usage');
  await page.getByRole('switch', {name: 'Require GPU hosts'}).click();
  await goToStep(page, 'Next', 'Shape');

  const planReq = page.waitForRequest(
    req =>
      req.url().endsWith('/v2/batches') &&
      req.method() === 'POST' &&
      req.postDataJSON()?.validate_only === true
  );
  await goToStep(page, 'See the plan', 'Plan');

  expect((await planReq).postDataJSON().requirements).toMatchObject({
    interaction: 'headless',
    gpu_required: true,
  });

  const plan = page.locator('app-batch-plan-step');
  await expect(plan.getByText(/GPU required/)).toBeVisible();
  await expect(plan.getByText(/· Headless ·/)).toBeVisible();
});

// A pinned pool overrides the derivation for the whole fleet and travels as
// requirements.pool. The mocked deployment offers the default pool alone, so
// the cluster says the plan will refuse it. It is sent anyway, because the
// refusal belongs to the server.
test('pins a fleet pool for the whole batch and sends it on the wire', async ({page}) => {
  await page.goto('/batches/create');
  await page.getByLabel('Name prefix').fill('ci-pinned');
  await goToStep(page, 'Next', 'Usage');

  await page.getByLabel('Fleet pool').click();
  await page.getByRole('option', {name: /^gpu/}).click();
  await expect(page.locator('.consequence')).toContainText('this deployment cannot serve');

  const planReq = page.waitForRequest(
    req =>
      req.url().endsWith('/v2/batches') &&
      req.method() === 'POST' &&
      req.postDataJSON()?.validate_only === true
  );
  await goToStep(page, 'Next', 'Shape');
  await goToStep(page, 'See the plan', 'Plan');

  expect((await planReq).postDataJSON().requirements).toMatchObject({pool: 'gpu'});
  await expect(page.locator('app-batch-plan-step').getByText(/gpu pool/)).toBeVisible();
});

// Automatic is the default answer: no pool travels, the server derives one, and
// the plan names the pool it resolved.
test('automatic placement sends no pool and the plan names the resolved one', async ({page}) => {
  await fillToPlan(page, 'ci-auto');

  const planReq = page.waitForRequest(
    req => req.url().endsWith('/v2/batches') && req.method() === 'POST'
  );
  await goToStep(page, 'See the plan', 'Plan');

  expect((await planReq).postDataJSON().requirements).not.toHaveProperty('pool');
  await expect(page.locator('app-batch-plan-step').getByText(/default pool/)).toBeVisible();
});

// An arm64 guest needs an arm64 host image, and this deployment has none. The
// toggle is off and says why in the fleet's own words.
test('the ARM toggle is disabled, with the reason, where no arm64 image exists', async ({page}) => {
  await page.goto('/batches/create');
  await page.getByLabel('Name prefix').fill('ci-arm');
  await goToStep(page, 'Next', 'Usage');

  // A single-selection button toggle exposes its buttons as radios.
  await expect(
    page.locator('.architecture-toggle').getByRole('radio', {name: 'ARM'})
  ).toBeDisabled();
  await expect(
    page.getByText('no arm64 machine family carries an enabled host image', {exact: false})
  ).toBeVisible();
});

test('a plan clamped by host quota says how much of the fleet will start', async ({page, api}) => {
  api.hostQuota = 6;

  await fillToPlan(page, 'ci-wide');
  await page.getByRole('button', {name: '25', exact: true}).click();
  // Lower the floor: while every device must be admitted the backend rejects
  // rather than clamping, so the short-fleet case would never be reachable.
  await page.getByRole('radio', {name: 'At least some'}).click();
  await page.getByRole('slider').fill('4');

  await goToStep(page, 'See the plan', 'Plan');

  const plan = page.locator('app-batch-plan-step');
  await expect(plan.getByText('6 of 25 devices')).toBeVisible();
  await expect(plan.getByText(/Host quota fits only 6 of the 25 devices requested/)).toBeVisible();
  // The button commits to the size that was actually admitted.
  await expect(page.getByRole('button', {name: 'Launch 6 devices'})).toBeVisible();
});

test('a batch that cannot reach its floor is rejected, not shrunk', async ({page, api}) => {
  api.hostQuota = 2;

  await fillToPlan(page, 'ci-doomed');
  await page.getByRole('button', {name: '25', exact: true}).click();
  await goToStep(page, 'See the plan', 'Plan');

  await expect(page.getByText('This batch would be rejected')).toBeVisible();
  await expect(page.getByText(/insufficient capacity/)).toBeVisible();
  await expect(page.getByRole('button', {name: /^Launch/})).toBeDisabled();
});

test('the atomic policy replaces the admission floor with a ready deadline', async ({page}) => {
  await fillToPlan(page, 'ci-atomic');

  // The default policy admits the whole fleet: neither the floor slider nor
  // the deadline is on offer, because neither applies to it.
  await expect(page.getByRole('slider')).toBeHidden();
  await expect(page.getByLabel('Ready deadline (seconds)')).toBeHidden();

  await page.getByRole('radio', {name: 'At least some'}).click();
  await expect(page.getByRole('slider')).toBeVisible();

  await page.getByRole('radio', {name: 'Every device, or none'}).click();

  await expect(page.getByRole('slider')).toBeHidden();
  await page.getByLabel('Ready deadline (seconds)').fill('600');

  const planReq = page.waitForRequest(
    req => req.url().endsWith('/v2/batches') && req.method() === 'POST'
  );
  await goToStep(page, 'See the plan', 'Plan');

  // Atomic pins the floor to the full count and carries the deadline; the
  // backend rejects either half of that pair on its own.
  expect((await planReq).postDataJSON()).toMatchObject({
    count: 5,
    min_ready: 5,
    fill_policy: 'atomic',
    ready_timeout_seconds: 600,
  });
  await expect(
    page.locator('app-batch-plan-step').getByText('Every device, or none')
  ).toBeVisible();
});

// The floor is derived from the policy rather than stored beside the count,
// so raising the count cannot leave a stale floor behind it.
test('raising the count carries the admission floor with it', async ({page}) => {
  await fillToPlan(page, 'ci-grow');

  await page.getByRole('button', {name: '10', exact: true}).click();
  await page.getByRole('button', {name: '25', exact: true}).click();

  const planReq = page.waitForRequest(
    req => req.url().endsWith('/v2/batches') && req.method() === 'POST'
  );
  await goToStep(page, 'See the plan', 'Plan');
  expect((await planReq).postDataJSON()).toMatchObject({count: 25, min_ready: 25});
});

test('the batch list offers the wizard, and an empty list leads to it', async ({page, api}) => {
  await page.goto('/batches');
  await page.locator('app-page-header').getByRole('button', {name: 'Create batch'}).click();
  await expect(page).toHaveURL(/\/batches\/create$/);
  await expect(page.getByRole('heading', {name: 'Template'})).toBeVisible();

  // With nothing to list, the empty state carries the same action rather than
  // naming a CLI the reader would have to go and find.
  api.batches = [];
  await page.goto('/batches');
  await expect(page.getByText('No batches')).toBeVisible();
  await page.locator('app-empty-state').getByRole('button', {name: 'Create batch'}).click();
  await expect(page).toHaveURL(/\/batches\/create$/);
});
