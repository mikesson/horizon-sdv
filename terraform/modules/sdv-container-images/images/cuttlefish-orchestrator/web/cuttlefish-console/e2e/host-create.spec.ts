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

import {expect, test} from './fixtures/api-mock';

test('creates a host VM', async ({page}) => {
  await page.goto('/hosts/create');

  // The field is pre-filled with a generated label; replace it so the
  // assertion below pins an exact name rather than a shape.
  await page.getByLabel('Host name').fill('e2e-host');

  // The machine type auto-selects from the default once /v2/machine-types loads;
  // the submit button stays disabled until the form is valid.
  const create = page.getByRole('button', {name: 'Create', exact: true});
  await expect(create).toBeEnabled({timeout: 15_000});

  const postReq = page.waitForRequest(
    req => req.url().endsWith('/v2/hosts') && req.method() === 'POST'
  );
  await create.click();

  const body = (await postReq).postDataJSON();
  // name is the display label the operator typed, verbatim. The GCE instance
  // name is derived server-side; a client that sends one composes "cf-cf-…"
  // and the API rejects it.
  expect(body.name).toBe('e2e-host');
  expect(body.name).not.toMatch(/^cf-/);
  expect(body).toMatchObject({
    zone: 'europe-west4-a',
    machine_type: 'n1-standard-8',
    max_devices: 4,
    ttl_hours: 10,
  });

  // Store navigates to the new host's DETAIL page, which is the manual
  // journey's next step (watch provisioning, then Create Device), with a
  // snackbar.
  await expect(page).toHaveURL(/\/hosts\/host-2$/);
  await expect(page.getByText('Host "e2e-host" created')).toBeVisible();
});

// The three shape fields the API has always taken and the form never sent.
// They live under Advanced options, which is collapsed by default.
test('sends the advanced shape knobs when they are set', async ({page}) => {
  await page.goto('/hosts/create');
  await page.getByLabel('Host name').fill('e2e-shaped');

  const create = page.getByRole('button', {name: 'Create', exact: true});
  await expect(create).toBeEnabled({timeout: 15_000});

  // Collapsed by default: none of these exist until Advanced is opened.
  await expect(page.getByLabel('Boot disk (GB)')).toBeHidden();
  await page.getByRole('button', {name: /Advanced options/}).click();

  await page.getByRole('switch', {name: 'Reserve this host'}).click();
  await page.getByLabel('Minimum CPU platform').fill('Intel Cascade Lake');
  await page.getByLabel('Boot disk (GB)').fill('200');

  // The summary on the collapsed header reports the fencing, which is the
  // one setting that affects every later create.
  await expect(page.getByText(/exclusive/)).toBeVisible();

  const postReq = page.waitForRequest(
    req => req.url().endsWith('/v2/hosts') && req.method() === 'POST'
  );
  await create.click();
  expect((await postReq).postDataJSON()).toMatchObject({
    exclusive: true,
    min_cpu_platform: 'Intel Cascade Lake',
    boot_disk_size_gb: 200,
  });
});

// 0 means "platform default" but the API rejects 1..9, so an unset field must
// not travel as a number and a too-small one must not reach the API at all.
test('omits an unset boot disk and blocks a too-small one', async ({page}) => {
  await page.goto('/hosts/create');
  await page.getByLabel('Host name').fill('e2e-default-disk');
  const create = page.getByRole('button', {name: 'Create', exact: true});
  await expect(create).toBeEnabled({timeout: 15_000});

  await page.getByRole('button', {name: /Advanced options/}).click();
  await page.getByLabel('Boot disk (GB)').fill('5');
  // mat-error only renders once the field is touched, and fill() does not blur.
  await page.getByLabel('Boot disk (GB)').blur();
  await expect(page.getByText('Use 0 for the default, or at least 10 GB')).toBeVisible();
  await expect(create).toBeDisabled();

  await page.getByLabel('Boot disk (GB)').fill('0');
  await expect(create).toBeEnabled();

  const postReq = page.waitForRequest(
    req => req.url().endsWith('/v2/hosts') && req.method() === 'POST'
  );
  await create.click();
  const body = (await postReq).postDataJSON();
  expect(body).not.toHaveProperty('boot_disk_size_gb');
  expect(body).not.toHaveProperty('exclusive');
});
