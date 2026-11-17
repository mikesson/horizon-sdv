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

// The stepper animates between steps and keeps every step's content in the
// DOM, so a bare role query can transiently resolve to the outgoing step's
// button. Click the visible one, then wait for the destination heading.
async function goToStep(page: Page, button: string, arrivalHeading: string): Promise<void> {
  await page.getByRole('button', {name: button}).filter({visible: true}).click();
  await expect(page.getByRole('heading', {name: arrivalHeading})).toBeVisible();
}

// Drives the interview: Android → Usage → Duration → Plan, on the default
// automatic host. The plan is an AIP-163 validate_only dry run; Launch posts
// the declarative intent.
test('launches a device through the interview with automatic placement', async ({page}) => {
  await page.goto('/devices/create/simple');

  // Step 1, Android. The platform default build renders as a statement;
  // the host question is pre-answered with Automatic. (Scope to the
  // statement: the Plan step's DOM exists eagerly in the stepper.)
  const statement = page.locator('.statement');
  await expect(statement.getByText('the platform default build')).toBeVisible();
  await expect(statement.getByText('AOSP Master Phone')).toBeVisible();
  await page.getByLabel('Device name').fill('e2e-device');
  await goToStep(page, 'Next', 'Usage');

  // Step 2, Usage. Interactive is preselected; pick Headless.
  await page.getByRole('radio', {name: /Headless/}).click();
  await goToStep(page, 'Next', 'Set Lifecycle');

  // Step 3, Duration. Defaults are valid. Entering the Plan step fires the
  // validate_only dry run.
  const planReq = page.waitForRequest(
    req =>
      req.url().endsWith('/v2/devices') &&
      req.method() === 'POST' &&
      req.postDataJSON()?.validate_only === true
  );
  await goToStep(page, 'See the plan', 'Plan');
  const planBody = (await planReq).postDataJSON();
  expect(planBody).toMatchObject({
    name: 'e2e-device',
    validate_only: true,
    requirements: {interaction: 'headless'},
  });

  // Step 4, Plan. It renders the server resolution.
  await expect(page.getByText('Placed on existing capacity')).toBeVisible();
  await expect(page.getByText('test-host')).toBeVisible();

  // Launch posts the same intent without validate_only.
  const createReq = page.waitForRequest(
    req =>
      req.url().endsWith('/v2/devices') &&
      req.method() === 'POST' &&
      !req.postDataJSON()?.validate_only
  );
  await page.getByRole('button', {name: 'Launch Device'}).click();
  const body = (await createReq).postDataJSON();
  expect(body).toMatchObject({name: 'e2e-device', requirements: {interaction: 'headless'}});
  expect(body.host_id).toBeUndefined();
  expect(body.gpu_mode).toBeUndefined(); // backend default (auto)

  // On success the store navigates to the new device's detail page.
  await expect(page).toHaveURL(/\/devices\/device-\d+/);
});

// The Android step can track an AOSP branch/target (latest on every launch)
// instead of pinning a registered build. It reaches the wire as
// requirements.build.track.
test('tracks an AOSP branch through the interview', async ({page}) => {
  await page.goto('/devices/create/simple');
  await page.getByLabel('Device name').fill('tracked-device');

  // Open the build picker and switch to branch tracking.
  await page.locator('.statement').getByRole('button', {name: 'Choose different'}).click();
  await page.locator('.build-mode-toggle').getByText('Track a branch').click();
  await page.getByLabel('Branch').fill('aosp-main');
  await page.getByLabel('Target').fill('aosp_cf_x86_64_auto-userdebug');
  await page.locator('.statement').getByRole('button', {name: 'Done'}).click();

  await expect(
    page.locator('.statement').getByText('aosp-main / aosp_cf_x86_64_auto-userdebug')
  ).toBeVisible();

  await goToStep(page, 'Next', 'Usage');
  await goToStep(page, 'Next', 'Set Lifecycle');
  await goToStep(page, 'See the plan', 'Plan');

  const createReq = page.waitForRequest(
    req =>
      req.url().endsWith('/v2/devices') &&
      req.method() === 'POST' &&
      !req.postDataJSON()?.validate_only
  );
  await page.getByRole('button', {name: 'Launch Device'}).click();
  const body = (await createReq).postDataJSON();
  expect(body.requirements.build).toEqual({
    track: {branch: 'aosp-main', target: 'aosp_cf_x86_64_auto-userdebug'},
  });
});

// Zero builds, zero hosts. The interview arrives valid, with no dead-ends.
// The plan shows what the platform will register and provision.
test('plans register + provision from a completely empty fleet', async ({page, api}) => {
  api.builds = [];
  api.hosts = [];
  await page.goto('/devices/create/simple');

  // The old empty-state dead-ends are gone: statements instead.
  await expect(page.getByText(/latest AOSP phone build/)).toBeVisible();
  await expect(
    page.getByText(/places the device on existing capacity or provisions/)
  ).toBeVisible();

  await page.getByLabel('Device name').fill('bootstrap-device');
  await goToStep(page, 'Next', 'Usage');
  await goToStep(page, 'Next', 'Set Lifecycle');
  await goToStep(page, 'See the plan', 'Plan');

  await expect(page.getByText('Will be registered in Builds', {exact: false})).toBeVisible();
  await expect(page.getByText(/Will provision n1-standard-8 in europe-west4-a/)).toBeVisible();

  const createReq = page.waitForRequest(
    req =>
      req.url().endsWith('/v2/devices') &&
      req.method() === 'POST' &&
      !req.postDataJSON()?.validate_only
  );
  await page.getByRole('button', {name: 'Launch Device'}).click();
  const body = (await createReq).postDataJSON();
  expect(body.requirements).toMatchObject({interaction: 'interactive'});
  expect(body.requirements.build).toBeUndefined(); // platform default
});

// Host detail's "Create Device" arrives with ?hostId, so the host question
// is pre-answered with that specific host, making the create imperative.
test('?hostId pre-answers the host question with a chosen host', async ({page}) => {
  await page.goto('/devices/create/simple?hostId=host-1');

  // The toggle sits on "Choose a host" with the host selected.
  await expect(
    page.locator('.mat-button-toggle-checked', {hasText: 'Choose a host'})
  ).toBeVisible();
  await expect(page.locator('.host-card', {hasText: 'test-host'})).toBeVisible();

  await page.getByLabel('Device name').fill('j2-device');
  await goToStep(page, 'Next', 'Usage');
  await goToStep(page, 'Next', 'Set Lifecycle');
  await goToStep(page, 'See the plan', 'Plan');

  // Imperative plan: the chosen host renders from the fixture state.
  await expect(page.getByText('capacity and quota verified')).toBeVisible();

  const createReq = page.waitForRequest(
    req =>
      req.url().endsWith('/v2/devices') &&
      req.method() === 'POST' &&
      !req.postDataJSON()?.validate_only
  );
  await page.getByRole('button', {name: 'Launch Device'}).click();
  const body = (await createReq).postDataJSON();
  // A chosen host is imperative: host_id + the default build's id.
  expect(body).toMatchObject({name: 'j2-device', host_id: 'host-1', build_id: 'build-1'});
  expect(body.requirements).toBeUndefined();
});

test('lifecycle choices are carried in the create request', async ({page}) => {
  await page.goto('/devices/create/simple');

  await page.getByLabel('Device name').fill('lifecycle-device');
  await goToStep(page, 'Next', 'Usage');
  await goToStep(page, 'Next', 'Set Lifecycle');

  // Step 3, Duration. TTL preset, pin with reason, one tag.
  await page.getByRole('button', {name: '8 hours'}).click();
  await page.getByRole('checkbox', {name: /Pin this device/}).click();
  await page.getByLabel('Reason for Pinning').click();
  await page.getByRole('option', {name: 'Demo Preparation'}).click();
  await page.getByPlaceholder('Type and press Enter...').fill('smoke');
  await page.getByPlaceholder('Type and press Enter...').press('Enter');
  await goToStep(page, 'See the plan', 'Plan');

  // The plan summarizes the device line (pinned, usage)...
  const planCards = page.locator('.plan-cards');
  await expect(planCards.getByText(/pinned/)).toBeVisible();

  // ...and the create request carries all of it.
  const createReq = page.waitForRequest(
    req =>
      req.url().endsWith('/v2/devices') &&
      req.method() === 'POST' &&
      !req.postDataJSON()?.validate_only
  );
  await page.getByRole('button', {name: 'Launch Device'}).click();
  const body = (await createReq).postDataJSON() as Record<string, unknown>;
  expect(body).toMatchObject({
    name: 'lifecycle-device',
    ttl_hours: 8,
    pinned: true,
    pinned_reason: 'demo_preparation',
    tags: ['smoke'],
  });
});

// gpu_required turns off the silent fallback to software rendering, and
// pinned_until bounds a born-pinned device. Neither had a control before.
test('sends a hard GPU constraint and a pin expiry', async ({page}) => {
  await page.goto('/devices/create/simple');
  await page.getByLabel('Device name').fill('e2e-strict');
  await goToStep(page, 'Next', 'Usage');

  // Interactive is preselected, so the constraint is on offer straight away.
  await page.getByRole('switch', {name: 'Require a GPU host'}).click();
  await goToStep(page, 'Next', 'Set Lifecycle');

  await page.getByRole('checkbox', {name: /Pin/}).first().check();
  await page.getByLabel('Reason for Pinning').click();
  await page.getByRole('option').first().click();
  await page.getByLabel('Unpin automatically at (optional)').fill('2027-01-01T09:00');

  const planReq = page.waitForRequest(
    req =>
      req.url().endsWith('/v2/devices') &&
      req.method() === 'POST' &&
      req.postDataJSON()?.validate_only === true
  );
  await goToStep(page, 'See the plan', 'Plan');
  const body = (await planReq).postDataJSON();
  expect(body.requirements).toMatchObject({gpu_required: true});
  expect(body.pinned).toBe(true);
  expect(body.pinned_until).toMatch(/^2027-01-01T/);
});

// Pinning a pool overrides the derivation, and it travels as requirements.pool.
// The mocked deployment offers the default pool alone, so the cluster says the
// plan will refuse this one. It is sent anyway, because the refusal is the
// server's to make.
test('pins a fleet pool and sends it on the wire', async ({page}) => {
  await page.goto('/devices/create/simple');
  await page.getByLabel('Device name').fill('e2e-pinned-pool');
  await goToStep(page, 'Next', 'Usage');

  await page.getByLabel('Fleet pool').click();
  await page.getByRole('option', {name: /^gpu/}).click();
  await expect(page.locator('.consequence')).toContainText('this deployment cannot serve');

  const planReq = page.waitForRequest(
    req =>
      req.url().endsWith('/v2/devices') &&
      req.method() === 'POST' &&
      req.postDataJSON()?.validate_only === true
  );
  await goToStep(page, 'Next', 'Set Lifecycle');
  await goToStep(page, 'See the plan', 'Plan');
  expect((await planReq).postDataJSON().requirements).toMatchObject({pool: 'gpu'});
});

// Automatic is the default answer, and it sends no pool at all: the server
// derives one, and the plan says which.
test('automatic placement sends no pool and the plan names the one it resolved', async ({page}) => {
  await page.goto('/devices/create/simple');
  await page.getByLabel('Device name').fill('e2e-auto-pool');
  await goToStep(page, 'Next', 'Usage');

  await expect(page.locator('.consequence')).toContainText('Lands in the default pool');

  const planReq = page.waitForRequest(
    req =>
      req.url().endsWith('/v2/devices') &&
      req.method() === 'POST' &&
      req.postDataJSON()?.validate_only === true
  );
  await goToStep(page, 'Next', 'Set Lifecycle');
  await goToStep(page, 'See the plan', 'Plan');
  expect((await planReq).postDataJSON().requirements).not.toHaveProperty('pool');

  // The resolution's pool is the answer the server gave, not a client echo.
  await expect(page.locator('.plan-cards').getByText(/default pool/)).toBeVisible();
});

// An arm64 guest needs an arm64 host image, and this deployment has none. The
// toggle is off and says why in the fleet's own words, rather than accepting an
// answer the create would be refused for.
test('the ARM toggle is disabled, with the reason, where no arm64 image exists', async ({page}) => {
  await page.goto('/devices/create/simple');
  await page.getByLabel('Device name').fill('e2e-arm');
  await goToStep(page, 'Next', 'Usage');

  // A single-selection button toggle exposes its buttons as radios.
  await expect(
    page.locator('.architecture-toggle').getByRole('radio', {name: 'ARM'})
  ).toBeDisabled();
  await expect(
    page.getByText('no arm64 machine family carries an enabled host image', {exact: false})
  ).toBeVisible();
});

// The constraint only applies to interactive intent; leaving it set while
// switching to headless would send a requirement the step stops showing.
test('drops the GPU constraint when usage turns headless', async ({page}) => {
  await page.goto('/devices/create/simple');
  await page.getByLabel('Device name').fill('e2e-headless');
  await goToStep(page, 'Next', 'Usage');

  await page.getByRole('switch', {name: 'Require a GPU host'}).click();
  await page.getByRole('radio', {name: /Headless/}).click();
  await expect(page.getByRole('switch', {name: 'Require a GPU host'})).toBeHidden();

  const planReq = page.waitForRequest(
    req =>
      req.url().endsWith('/v2/devices') &&
      req.method() === 'POST' &&
      req.postDataJSON()?.validate_only === true
  );
  await goToStep(page, 'Next', 'Set Lifecycle');
  await goToStep(page, 'See the plan', 'Plan');
  expect((await planReq).postDataJSON().requirements).not.toHaveProperty('gpu_required');
});
