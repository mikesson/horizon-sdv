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

// The three keys that had no UI at all before this page existed.
test('reaches every runtime setting, including the three that had no UI', async ({page}) => {
  await page.goto('/admin/settings');

  await expect(page.getByLabel('Zone pin')).toHaveValue('europe-west4-a');
  await expect(page.getByLabel('Quota reserve margin (%)')).toHaveValue('10');
  await expect(page.getByLabel('GPU host machine type')).toBeVisible();
  await expect(page.getByLabel('Upstream branch')).toHaveValue('aosp-android-latest-release');
  await expect(page.getByLabel('Upstream target')).toHaveValue(
    'aosp_cf_x86_64_only_phone-userdebug'
  );
  await expect(page.getByLabel('Bugreport bucket')).toBeVisible();
});

// The autoscaler settings moved to the Autoscaler page. Two editors for one
// value is a defect, so this page must offer none of them.
test('no longer edits the autoscaler', async ({page}) => {
  await page.goto('/admin/settings');
  await expect(page.getByLabel('Zone pin')).toHaveValue('europe-west4-a');

  await expect(page.getByLabel('Demand window (seconds)')).toHaveCount(0);
  await expect(page.getByLabel('Drain grace (seconds)')).toHaveCount(0);
  await expect(page.locator('app-schedule-editor')).toHaveCount(0);
});

// Without this the console cannot tell a margin of 10 an operator chose from
// the compiled 10 it would serve anyway.
test('says whether each value is stored or defaulted', async ({page}) => {
  await page.goto('/admin/settings');

  const placement = page.locator('app-settings-card').filter({hasText: 'Placement'}).first();
  // Zone pin is stored in the fixture; the margin is not.
  await expect(placement.getByText('stored')).toBeVisible();
  await expect(placement.getByText('default')).toBeVisible();

  const put = page.waitForRequest(
    r => r.url().endsWith('/system-config/quota-margin') && r.method() === 'PUT'
  );
  await page.getByLabel('Quota reserve margin (%)').fill('20');
  await placement.getByRole('button', {name: 'Save'}).click();
  await put;

  // Saved, so it is now an operator's value rather than the default.
  await expect(placement.getByText('default')).toBeHidden();
});

// Every PUT writes a config_change audit entry, so a page that writes on load,
// or a Save that re-sends untouched values, fills Activity with edits nobody
// made. The listener is installed before navigation so it sees the load too.
test('a no-change visit issues no PUT on any section', async ({page}) => {
  const puts: string[] = [];
  page.on('request', r => {
    if (r.method() === 'PUT') {
      puts.push(r.url());
    }
  });

  await page.goto('/admin/settings');
  await expect(page.getByLabel('Zone pin')).toHaveValue('europe-west4-a');

  const saves = await page.getByRole('button', {name: 'Save'}).all();
  expect(saves.length).toBe(5);
  for (const save of saves) {
    await expect(save).toBeDisabled();
  }
  expect(puts).toEqual([]);
});

// A switch applies on flip; it never shares a card with a Save button.
test('switches apply immediately and report it', async ({page, api}) => {
  await page.goto('/admin/settings');

  const put = page.waitForRequest(
    r => r.url().endsWith('/system-config/spot-auto-default') && r.method() === 'PUT'
  );
  const spotCard = page.locator('app-settings-card').filter({hasText: 'Spot by default'});
  await spotCard.getByRole('switch').click();
  await put;
  expect(api.settings.spot_auto_default).toBe(true);
  await expect(spotCard.getByRole('status')).toHaveText('Saved');
});

// Consolidating four pages into one must not turn one broken endpoint into a
// blank settings page. That would be worse than the pages it replaces.
test('one failing section leaves the others usable', async ({page}) => {
  await page.route(/\/v2\/admin\/system-config\/spot-min-uptime(\?|$)/, route =>
    route.fulfill({status: 503, body: '{}'})
  );
  await page.goto('/admin/settings');

  const spot = page.locator('app-settings-card').filter({hasText: 'Spot capacity'});
  await expect(spot.locator('app-error-banner')).toBeVisible();

  // The other sections rendered their values regardless.
  await expect(page.getByLabel('Zone pin')).toHaveValue('europe-west4-a');
  await expect(page.getByLabel('Upstream branch')).toBeVisible();
  await expect(page.getByLabel('Bugreport bucket')).toBeVisible();
  await expect(page.getByLabel('Device session (hours)')).toHaveValue('4');
});

// Carried over from the Artifacts page this replaces: IAM misconfiguration is
// invisible until a capture fails hours later, so the probe asks for write.
test('probes each bucket for write access from the Artifacts card', async ({page}) => {
  await page.goto('/admin/settings');

  // Both buckets get one, and the count is asserted: with only `.first()` a
  // deleted probe passes by falling through to its sibling.
  const probes = page.getByRole('button', {name: 'Verify write access'});
  await expect(probes).toHaveCount(2);

  const verify = page.waitForRequest(r => {
    if (!r.url().endsWith('/v2/storage/verify-access') || r.method() !== 'POST') {
      return false;
    }
    const body = r.postDataJSON() as {check_write?: boolean; bucket?: string};
    // Empty override, so the effective bucket is the deployment default.
    return body.check_write === true && body.bucket === 'test-artifacts';
  });
  await probes.first().click();
  await verify;
  await expect(page.getByRole('button', {name: 'Writable'})).toBeVisible();
});

test('saves a bucket override behind an explicit Save', async ({page, api}) => {
  await page.goto('/admin/settings');
  const artifacts = page.locator('app-settings-card').filter({hasText: 'Artifacts'}).first();
  await expect(artifacts.getByRole('button', {name: 'Save'})).toBeDisabled();

  await page.getByLabel('Bugreport bucket').fill('triage-bucket');
  const put = page.waitForRequest(
    r => r.url().endsWith('/system-config/artifact-buckets') && r.method() === 'PUT'
  );
  await artifacts.getByRole('button', {name: 'Save'}).click();
  await put;
  expect(api.artifactBuckets.bugreport_bucket).toBe('triage-bucket');
});

// The endpoint carries all three keys, so the flip must send the stored
// buckets, and never whatever text is sitting unsaved in the form above it.
test('the capture switch applies on flip without writing an unsaved bucket', async ({
  page,
  api,
}) => {
  await page.goto('/admin/settings');
  await expect(page.getByLabel('Bugreport bucket')).toBeVisible();
  await page.getByLabel('Bugreport bucket').fill('never-saved');

  const capture = page.locator('app-settings-card').filter({hasText: 'Automatic capture'});
  const put = page.waitForRequest(
    r => r.url().endsWith('/system-config/artifact-buckets') && r.method() === 'PUT'
  );
  await capture.getByRole('switch').click();
  await put;

  expect(api.artifactBuckets.capture_on_error).toBe(true);
  expect(api.artifactBuckets.bugreport_bucket).toBe('');
  await expect(capture.getByRole('status')).toHaveText('Saved');
  // Still the operator's to save or discard.
  await expect(page.getByLabel('Bugreport bucket')).toHaveValue('never-saved');
});

test('both consolidated routes redirect here', async ({page}) => {
  await page.goto('/admin/placement');
  await expect(page).toHaveURL(/\/admin\/settings$/);
  await page.goto('/admin/artifacts');
  await expect(page).toHaveURL(/\/admin\/settings$/);
});
