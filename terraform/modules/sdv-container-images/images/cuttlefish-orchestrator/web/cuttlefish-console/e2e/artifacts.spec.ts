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
 * Device artifacts: on-demand bugreport capture into the configured GCS
 * bucket, listed on the device detail page; bucket settings managed on the
 * admin Artifacts page.
 */

import {expect, test} from './fixtures/api-mock';

test('device detail captures a bugreport and lists it', async ({page}) => {
  await page.goto('/devices/device-1');
  await expect(page.getByText('No artifacts captured for this device yet.')).toBeVisible();

  const captureReq = page.waitForRequest(
    r => r.url().endsWith('/v2/devices/device-1/bugreport') && r.method() === 'POST'
  );
  await page.getByRole('button', {name: 'Capture bugreport'}).click();
  await captureReq;

  // Scope to the table cell: the success snackbar also echoes the gs:// URI
  // (…/20260615-100000.zip), so a bare text match races two elements.
  await expect(page.getByRole('cell', {name: '20260615-100000.zip', exact: true})).toBeVisible();
  await expect(page.getByText('4.01 MB')).toBeVisible();
});

// An artifact is the only copy of why a device failed, so the delete confirms
// first. The row goes away because the bucket is refetched, not spliced.
test('deletes a captured artifact from the bucket', async ({page}) => {
  await page.goto('/devices/device-1');
  await page.getByRole('button', {name: 'Capture bugreport'}).click();
  await expect(page.getByRole('cell', {name: '20260615-100000.zip', exact: true})).toBeVisible();

  const deleteReq = page.waitForRequest(
    r => r.url().includes('/v2/storage/buckets/') && r.method() === 'DELETE'
  );
  await page.getByRole('button', {name: 'Delete 20260615-100000.zip'}).click();
  await expect(page.getByText('Delete this artifact?')).toBeVisible();
  // Scoped to the dialog: the device's own Delete action shares the label.
  await page.getByRole('dialog').getByRole('button', {name: 'Delete', exact: true}).click();
  await deleteReq;

  await expect(page.getByRole('cell', {name: '20260615-100000.zip', exact: true})).toBeHidden();
  await expect(page.getByText('No artifacts captured for this device yet.')).toBeVisible();
});

test('device detail lists recordings and fetches one to storage', async ({page}) => {
  await page.goto('/devices/device-1');
  // The recordings card lists the host-local recording from the fixture.
  await expect(page.getByText('screen_record_0.webm')).toBeVisible();

  // Recordings are instance-addressed: every per-instance route goes through
  // targetPath, so the device id alone never names one.
  const fetchReq = page.waitForRequest(
    r =>
      /\/v2\/devices\/device-1\/instances\/[^/]+\/recordings\/.+\/fetch$/.test(r.url()) &&
      r.method() === 'POST'
  );
  await page.getByRole('button', {name: 'Fetch'}).click();
  await fetchReq;
});
