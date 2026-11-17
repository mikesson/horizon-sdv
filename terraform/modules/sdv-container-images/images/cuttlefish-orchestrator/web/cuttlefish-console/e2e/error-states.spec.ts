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

import {expect, mockError, test} from './fixtures/api-mock';

test('host list shows an error banner when the API fails', async ({page}) => {
  await mockError(page, /\/v2\/hosts(\?|$)/, 500);
  await page.goto('/hosts');
  await expect(page.getByRole('alert')).toBeVisible();
});

test('host create surfaces a server error', async ({page}) => {
  await page.goto('/hosts/create');
  const create = page.getByRole('button', {name: 'Create', exact: true});
  await expect(create).toBeEnabled({timeout: 15_000});

  // The create page never GETs /v2/hosts, so failing the collection only hits
  // the POST. Registered now (after the page's loads) so it wins.
  await mockError(page, /\/v2\/hosts(\?|$)/, 400);
  await create.click();

  await expect(page.locator('.error-banner')).toBeVisible();
  await expect(page).toHaveURL(/\/hosts\/create/); // stayed on the form
});
