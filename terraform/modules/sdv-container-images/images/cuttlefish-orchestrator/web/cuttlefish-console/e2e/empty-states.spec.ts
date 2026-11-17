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

// Clearing the collection before navigating makes the list endpoint return [].
test('hosts list empty state', async ({page, api}) => {
  api.hosts = [];
  await page.goto('/hosts');
  await expect(page.getByText('No hosts found')).toBeVisible();
});

test('devices list empty state', async ({page, api}) => {
  api.devices = [];
  await page.goto('/devices');
  await expect(page.getByText('No devices found')).toBeVisible();
});

test('builds list empty state', async ({page, api}) => {
  api.builds = [];
  await page.goto('/builds');
  await expect(page.getByText('No builds registered')).toBeVisible();
});
