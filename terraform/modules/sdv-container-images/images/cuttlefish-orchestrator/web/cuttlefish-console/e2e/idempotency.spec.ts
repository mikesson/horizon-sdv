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
 * Idempotency-Key on the create surfaces.
 *
 * Nothing else stops a duplicate. Resource names carry no unique constraint
 * and neither create service checks for a collision, so a create the user
 * re-sends after a response that only looked like a failure lands a second
 * device, and declaratively a second billed VM. Cloud Run has produced
 * exactly that shape here before, answering 503 and recycling the instance
 * while the handler's work completed unseen.
 *
 * The key is therefore what makes the retry safe, and these tests pin the two
 * halves of that: it must be STABLE across a retry of one intent, and it must
 * CHANGE when the body does (the backend fingerprints the request against the
 * key and 422s a reuse carrying different content).
 */

import type {Page} from './fixtures/api-mock';
import {expect, test} from './fixtures/api-mock';

/**
 * Fails every POST to pattern with a 503 and records the key each attempt
 * carried. A 503 is the failure the key exists for: from the client it is
 * indistinguishable from a create that committed and lost its response.
 */
async function failCreatesRecordingKeys(page: Page, pattern: RegExp): Promise<string[]> {
  const keys: string[] = [];
  await page.route(pattern, route => {
    const req = route.request();
    if (req.method() !== 'POST') {
      return route.fallback();
    }
    keys.push(req.headers()['idempotency-key'] ?? '');
    return route.fulfill({
      status: 503,
      contentType: 'application/problem+json',
      body: JSON.stringify({title: 'Service Unavailable', status: 503}),
    });
  });
  return keys;
}

test('a retried host create reuses its idempotency key', async ({page}) => {
  const keys = await failCreatesRecordingKeys(page, /\/v2\/hosts(\?|$)/);
  await page.goto('/hosts/create');
  await page.getByLabel('Host name').fill('e2e-retry');

  const create = page.getByRole('button', {name: 'Create', exact: true});
  await expect(create).toBeEnabled({timeout: 15_000});

  await create.click();
  await expect.poll(() => keys.length).toBe(1);
  expect(keys[0]).not.toBe('');

  // The failure re-enables the button, which is exactly how a user lands on
  // a second create for a host the server may already hold.
  await expect(create).toBeEnabled();
  await create.click();
  await expect.poll(() => keys.length).toBe(2);

  expect(keys[1]).toBe(keys[0]);
});

test('editing the host form between attempts mints a new key', async ({page}) => {
  const keys = await failCreatesRecordingKeys(page, /\/v2\/hosts(\?|$)/);
  await page.goto('/hosts/create');
  await page.getByLabel('Host name').fill('e2e-first');

  const create = page.getByRole('button', {name: 'Create', exact: true});
  await expect(create).toBeEnabled({timeout: 15_000});

  await create.click();
  await expect.poll(() => keys.length).toBe(1);

  // A different name is a different request. Reusing the key here would be
  // the backend's 422, so the console must mint a fresh one.
  await page.getByLabel('Host name').fill('e2e-second');
  await expect(create).toBeEnabled();
  await create.click();
  await expect.poll(() => keys.length).toBe(2);

  expect(keys[1]).not.toBe(keys[0]);
  expect(keys[1]).not.toBe('');
});

test('the device plan carries no key and the launch that follows does', async ({page, api}) => {
  await page.goto('/devices/create/simple');

  await page.getByLabel('Device name').fill('e2e-keyed');
  await page.getByRole('button', {name: 'Next'}).filter({visible: true}).click();
  await expect(page.getByRole('heading', {name: 'Usage'})).toBeVisible();

  await page.getByRole('radio', {name: /Headless/}).click();
  await page.getByRole('button', {name: 'Next'}).filter({visible: true}).click();
  await expect(page.getByRole('heading', {name: 'Set Lifecycle'})).toBeVisible();

  // Entering the Plan step fires the validate_only dry run.
  await page.getByRole('button', {name: 'See the plan'}).filter({visible: true}).click();
  await expect(page.getByRole('heading', {name: 'Plan'})).toBeVisible();
  await expect.poll(() => api.idempotencyKeys.device.length).toBe(1);
  expect(api.idempotencyKeys.device[0]).toBe('');

  await page.getByRole('button', {name: 'Launch Device'}).filter({visible: true}).click();
  await expect.poll(() => api.idempotencyKeys.device.length).toBe(2);
  expect(api.idempotencyKeys.device[1]).not.toBe('');
});
