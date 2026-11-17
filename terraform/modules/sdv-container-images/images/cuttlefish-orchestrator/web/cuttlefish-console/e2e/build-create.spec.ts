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

test('registers a build via Quick Start', async ({page}) => {
  await page.goto('/builds/create');

  // Quick Start is the default mode and pre-fills a valid public build, so the
  // form is submittable immediately.
  const register = page.getByRole('button', {name: 'Register Build'});
  await expect(register).toBeEnabled();

  const postReq = page.waitForRequest(
    req => req.url().includes('/v2/builds') && req.method() === 'POST'
  );
  await register.click();

  const body = (await postReq).postDataJSON();
  // aosp-android-latest-release is the branch that publishes fetchable CF
  // artifacts under the 2026 trunk-stable model (aosp-main is dead).
  expect(body).toMatchObject({
    name: 'AOSP Latest Release Phone',
    source: 'android_build',
    branch: 'aosp-android-latest-release',
  });
  expect(body.target).toBe('aosp_cf_x86_64_only_phone-userdebug');

  // Success surfaces as a snackbar; the new build also lands in the list.
  await expect(page.getByText(/registered/i)).toBeVisible();
});
