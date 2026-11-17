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

// Create Device lands on the chooser, not inside one of the modes, and each
// card is a link the browser's Back undoes.
test('Create Device opens the chooser and Back returns to it from a mode', async ({page}) => {
  await page.goto('/devices');
  await page.getByRole('button', {name: 'Create Device'}).first().click();

  await expect(page).toHaveURL(/\/devices\/create$/);
  await expect(page.getByRole('link', {name: 'Simple', exact: true})).toBeVisible();
  await expect(page.getByRole('link', {name: 'Advanced', exact: true})).toBeVisible();
  await expect(page.getByRole('link', {name: 'AAOS SDV Core + IVI'})).toBeVisible();

  await page.getByRole('link', {name: 'Simple', exact: true}).click();
  await expect(page).toHaveURL(/\/devices\/create\/simple$/);
  await expect(page.getByLabel('Device name')).toBeVisible();

  await page.goBack();
  await expect(page).toHaveURL(/\/devices\/create$/);
  await expect(page.getByRole('link', {name: 'Advanced', exact: true})).toBeVisible();
});

// The cards are links, so the keyboard reaches them in order and Enter
// follows one.
test('a card opens from the keyboard', async ({page}) => {
  await page.goto('/devices/create');

  const advanced = page.getByRole('link', {name: 'Advanced', exact: true});
  await advanced.focus();
  await expect(advanced).toBeFocused();
  await page.keyboard.press('Enter');

  await expect(page).toHaveURL(/\/devices\/create\/advanced$/);
});

// Host detail's Create Device names its host. The chooser passes it on, and
// the interview arrives with the host question answered.
test('a handoff host travels through the chooser into Simple', async ({page}) => {
  await page.goto('/devices/create?hostId=host-1');
  await page.getByRole('link', {name: 'Simple', exact: true}).click();

  await expect(page).toHaveURL(/\/devices\/create\/simple\?hostId=host-1$/);
  await expect(
    page.locator('.mat-button-toggle-checked', {hasText: 'Choose a host'})
  ).toBeVisible();

  // Change mode goes back to the chooser and keeps the host.
  await page.getByRole('link', {name: 'Change mode'}).click();
  await expect(page).toHaveURL(/\/devices\/create\?hostId=host-1$/);
});

test('a handoff host is pre-selected in Advanced', async ({page}) => {
  await page.goto('/devices/create?hostId=host-1');
  await page.getByRole('link', {name: 'Advanced', exact: true}).click();

  await expect(page).toHaveURL(/\/devices\/create\/advanced\?hostId=host-1$/);
  await expect(
    page.locator('.placement-toggle .mat-button-toggle-checked', {hasText: 'Choose a host'})
  ).toBeVisible();
});

// An SDV pair takes a build per instance, so the SDV card drops a handoff
// build. As the device build it would fill both instances' gaps. The host
// stays, because the SDV host warning reads it.
test('the SDV card keeps a handoff host and drops the build', async ({page}) => {
  await page.goto('/devices/create?hostId=host-1&buildId=build-1');
  await page.getByRole('link', {name: 'AAOS SDV Core + IVI'}).click();

  await expect(page).toHaveURL(/\/devices\/create\/advanced\?hostId=host-1$/);
  await expect(page.getByText('Instances (2/8)')).toBeVisible();
  await expect(
    page.locator('.placement-toggle .mat-button-toggle-checked', {hasText: 'Choose a host'})
  ).toBeVisible();
  // No device build: the pair's switch is off, one row per instance.
  await expect(page.getByRole('switch', {name: 'Same build for every instance'})).not.toBeChecked();
  await expect(page.getByRole('combobox', {name: 'core', exact: true})).toHaveValue('');
  await expect(page.getByRole('combobox', {name: 'ivi', exact: true})).toHaveValue('');
});

// The SDV card opens Advanced with the pair in place, then takes the preset out
// of the URL so a reload does not lay it over what the user has edited.
// Each instance must name its build. Under automatic placement an empty row
// would boot the platform default phone build with SDV bootconfig.
test('the SDV pair waits for a build on both instances', async ({page}) => {
  await page.goto('/devices/create');
  await page.getByRole('link', {name: 'AAOS SDV Core + IVI'}).click();

  const create = page.getByRole('button', {name: 'Create device · 2 instances'});
  await expect(create).toBeDisabled();

  await page
    .getByRole('combobox', {name: 'core', exact: true})
    .fill('gs://bucket/sdv_core_cf/T/sdv_core_cf-img-user.zip');
  await expect(create).toBeDisabled();

  await page.getByRole('combobox', {name: 'ivi', exact: true}).click();
  await page.getByRole('option', {name: /^AOSP Master Phone/}).click();
  // A registered build carries host tools, so the pair needs no host tools URL.
  await expect(page.getByLabel('Host tools (optional)')).toBeVisible();
  await expect(create).toBeEnabled();

  const req = page.waitForRequest(r => r.url().endsWith('/v2/devices') && r.method() === 'POST');
  await create.click();
  const body = (await req).postDataJSON() as Record<string, unknown>;
  const envConfig = (body.advanced_config as {env_config: Record<string, unknown>}).env_config;
  const [core, ivi] = envConfig.instances as Array<Record<string, unknown>>;
  expect(core).toMatchObject({
    name: 'core',
    disk: {default_build: 'gs://bucket/sdv_core_cf/T/sdv_core_cf-img-user.zip'},
  });
  expect(core).not.toHaveProperty('@build');
  expect(ivi).toMatchObject({name: 'ivi', '@build': 'build-1'});
  expect(ivi).not.toHaveProperty('disk');
  expect(body).not.toHaveProperty('build_id');
  const requirements = body.requirements as Record<string, unknown>;
  expect(requirements.build).toBeUndefined();
  // A Spot preemption would kill both guests mid-session.
  expect(requirements.capacity).toBe('standard');
});

test('the SDV card opens Advanced with the pair, once', async ({page}) => {
  await page.goto('/devices/create');
  await page.getByRole('link', {name: 'AAOS SDV Core + IVI'}).click();

  await expect(page).toHaveURL(/\/devices\/create\/advanced$/);
  await expect(page.getByText('Instances (2/8)')).toBeVisible();
  await expect(
    page.getByText('Core image zip or build folder, for example sdv_core_cf-img-user.zip')
  ).toBeVisible();

  await page.getByRole('button', {name: 'Add Instance'}).click();
  await expect(page.getByText('Instances (3/8)')).toBeVisible();
  await page.keyboard.press('Control+s');
  await expect(page.getByText('Draft saved')).toBeVisible();

  await page.reload();

  await expect(page.getByText('Instances (1/8)')).toBeVisible();
  await page.getByRole('button', {name: 'Restore'}).click();
  await expect(page.getByText('Instances (3/8)')).toBeVisible();
});
