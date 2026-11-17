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
 * Behavior net for the advanced device-create surface, written BEFORE its
 * signals/Signal-Forms rewrite to pin the existing contract: instance list
 * management, form -> JSON sync (via export), validity gating, the exact
 * create POST body, file import, and draft save/restore.
 */

import type {Page} from './fixtures/api-mock';
import {expect, test} from './fixtures/api-mock';

async function openAdvancedMode(page: Page): Promise<void> {
  await page.goto('/devices/create/simple');
  // Continue in Advanced performs a seeded eject: the interview's intent
  // (including its generated device name) becomes the Advanced starting
  // point. Pin the name first so instance names are deterministic in these
  // tests.
  await page.getByLabel('Device name').fill('device-1');
  await page.getByRole('button', {name: 'Continue in Advanced'}).click();
  await expect(page.getByRole('button', {name: 'Add Instance'})).toBeVisible();
}

/** Opens a build field's list and picks a registered build from it. */
async function pickBuild(page: Page, field: string, build: string): Promise<void> {
  await page.getByRole('combobox', {name: field, exact: true}).click();
  // Anchored, so the platform default option that names the same build is not a match.
  await page.getByRole('option', {name: new RegExp(`^${build}`)}).click();
}

async function exportedConfig(page: Page): Promise<Record<string, unknown>> {
  const download = page.waitForEvent('download');
  await page.getByRole('button', {name: 'Export'}).click();
  const stream = await (await download).createReadStream();
  const chunks: Buffer[] = [];
  for await (const chunk of stream) {
    chunks.push(chunk as Buffer);
  }
  return JSON.parse(Buffer.concat(chunks).toString('utf-8')) as Record<string, unknown>;
}

test.describe('advanced mode instance management', () => {
  test('add instance is reflected in the exported JSON config', async ({page}) => {
    await openAdvancedMode(page);

    await page.getByRole('button', {name: 'Add Instance'}).click();
    await expect(page.getByText('Instances (2/8)')).toBeVisible();

    const config = await exportedConfig(page);
    const instances = config.instances as Array<Record<string, unknown>>;
    expect(instances).toHaveLength(2);
    expect(instances[0]).toMatchObject({
      '@import': 'phone',
      name: 'device-1',
      vm: {cpus: 4, memory_mb: 4096, use_sdcard: true},
      graphics: {
        displays: [{width: 720, height: 1280, dpi: 320, refresh_rate_hertz: 60}],
        gpu_mode: 'gfxstream',
      },
      security: {guest_enforce_security: true},
    });
    expect(instances[1]).toMatchObject({name: 'device-2'});
  });

  test('a duplicate instance name disables Create and shows the uniqueness error', async ({
    page,
  }) => {
    await openAdvancedMode(page);

    // Select a build and a host so the config is otherwise valid. The instance
    // name is then the ONLY variable gating Create, so this isolates the
    // per-instance uniqueness rule rather than passing on some unrelated
    // still-invalid state.
    await pickBuild(page, 'Build', 'AOSP Master Phone');
    await page.locator('mat-button-toggle', {hasText: 'Choose a host'}).click();
    await page.locator('.host-card', {hasText: 'test-host'}).click();
    await page.getByRole('button', {name: 'Add Instance'}).click();

    // The added instance (device-2) is auto-selected and unique -> Create enabled.
    const name = page.getByLabel('Instance name');
    await expect(name).toHaveValue('device-2');
    // One device holding two instances, so the button counts instances rather
    // than devices. The create makes a single group either way.
    const create = page.getByRole('button', {name: 'Create device · 2 instances'});
    await expect(create).toBeEnabled();

    // Colliding with device-1 fails the uniqueness validator -> Create is gated.
    await name.fill('device-1');
    await expect(create).toBeDisabled();

    // Blurring surfaces the error to the user (Material shows it once touched).
    await name.blur();
    await expect(page.getByText('Instance names must be unique')).toBeVisible();

    // Restoring a unique name clears the error and re-enables Create, which
    // proves the collision was the gate and no other invalid state.
    await name.fill('device-2');
    await expect(page.getByText('Instance names must be unique')).toHaveCount(0);
    await expect(create).toBeEnabled();
  });

  test('template change re-applies template defaults', async ({page}) => {
    await openAdvancedMode(page);

    await page.locator('.template-card', {hasText: 'Android TV'}).click();

    const config = await exportedConfig(page);
    const instances = config.instances as Array<Record<string, unknown>>;
    expect(instances[0]).toMatchObject({
      '@import': 'tv',
      vm: {memory_mb: 2048},
      graphics: {displays: [{width: 1920, height: 1080, dpi: 213}]},
    });
  });
});

test.describe('advanced mode device name', () => {
  // The device name and the instance names are separate fields. Renaming the
  // device must not touch the instance the Simple name seeded.
  test('carries the Simple name in and submits the Advanced field', async ({page}) => {
    await page.goto('/devices/create/simple');
    await page.getByLabel('Device name').fill('from-simple');
    await page.getByRole('button', {name: 'Continue in Advanced'}).click();

    const name = page.getByLabel('Device name');
    await expect(name).toHaveValue('from-simple');
    await name.fill('renamed-device');

    const req = page.waitForRequest(r => r.url().endsWith('/v2/devices') && r.method() === 'POST');
    await page.getByRole('button', {name: 'Create device', exact: true}).click();
    const body = (await req).postDataJSON() as Record<string, unknown>;

    expect(body.name).toBe('renamed-device');
    const envConfig = (body.advanced_config as {env_config: Record<string, unknown>}).env_config;
    expect(envConfig.instances).toMatchObject([{name: 'from-simple'}]);
  });

  test('direct entry starts from a generated name and gates Create on it', async ({page}) => {
    await page.goto('/devices/create');
    await page.getByRole('link', {name: 'Advanced', exact: true}).click();

    const name = page.getByLabel('Device name');
    await expect(name).toHaveValue(/^device-[a-z0-9]{5}$/);
    const create = page.getByRole('button', {name: 'Create device', exact: true});
    await expect(create).toBeEnabled();

    await name.fill('Not Valid');
    await name.blur();
    await expect(create).toBeDisabled();
    await expect(page.getByText('Must start with a lowercase letter')).toBeVisible();

    await name.fill('direct-device');
    await expect(create).toBeEnabled();
    const req = page.waitForRequest(r => r.url().endsWith('/v2/devices') && r.method() === 'POST');
    await create.click();
    expect(((await req).postDataJSON() as Record<string, unknown>).name).toBe('direct-device');
  });
});

test.describe('advanced mode create submission', () => {
  test('POSTs the advanced payload (no disk fields; backend merges build)', async ({
    page,
    pageErrors,
  }) => {
    await openAdvancedMode(page);

    await pickBuild(page, 'Build', 'AOSP Master Phone');
    await page.locator('mat-button-toggle', {hasText: 'Choose a host'}).click();
    await page.locator('.host-card', {hasText: 'test-host'}).click();

    const req = page.waitForRequest(r => r.url().endsWith('/v2/devices') && r.method() === 'POST');
    await page.getByRole('button', {name: 'Create device', exact: true}).click();
    const body = (await req).postDataJSON() as Record<string, unknown>;

    expect(body).toMatchObject({
      host_id: 'host-1',
      name: 'device-1',
      gpu_mode: 'gfxstream',
      build_id: 'build-1',
      mode: 'advanced',
    });
    const envConfig = (body.advanced_config as {env_config: Record<string, unknown>}).env_config;
    const instances = envConfig.instances as Array<Record<string, unknown>>;
    expect(instances).toHaveLength(1);
    expect(instances[0]).toMatchObject({
      '@import': 'phone',
      name: 'device-1',
      vm: {cpus: 4, memory_mb: 4096, use_sdcard: true},
      security: {guest_enforce_security: true},
    });
    // A device-level build travels as build_id, so the submitted config
    // carries no disk/common build fields. The backend merges the build.
    expect(instances[0]).not.toHaveProperty('disk');
    expect(envConfig).not.toHaveProperty('common');
    expect(pageErrors, pageErrors.join('\n')).toHaveLength(0);
  });
});

test.describe('advanced mode builds', () => {
  test('one shared build needs no switch until a second instance exists', async ({page}) => {
    await openAdvancedMode(page);

    const toggle = page.getByRole('switch', {name: 'Same build for every instance'});
    await expect(toggle).toHaveCount(0);
    // Automatic placement offers the platform default as the field's value.
    const build = page.getByRole('combobox', {name: 'Build', exact: true});
    await expect(build).toHaveValue('Platform default: AOSP Master Phone');

    // A chosen host cannot use the platform default, so the value goes away.
    await page.locator('mat-button-toggle', {hasText: 'Choose a host'}).click();
    await page.locator('.host-card', {hasText: 'test-host'}).click();
    await expect(build).toHaveValue('');
    await expect(page.getByRole('button', {name: 'Create device', exact: true})).toBeDisabled();
    await page.locator('mat-button-toggle', {hasText: 'Automatic'}).click();
    await expect(build).toHaveValue('Platform default: AOSP Master Phone');

    await page.getByRole('button', {name: 'Add Instance'}).click();
    await expect(toggle).toBeChecked();

    const req = page.waitForRequest(r => r.url().endsWith('/v2/devices') && r.method() === 'POST');
    await page.getByRole('button', {name: 'Create device · 2 instances'}).click();
    const body = (await req).postDataJSON() as Record<string, unknown>;
    expect(body).not.toHaveProperty('build_id');
    expect(JSON.stringify(body.advanced_config)).not.toContain('default_build');
  });

  test('a pasted shared URL goes on every instance', async ({page}) => {
    await openAdvancedMode(page);
    await page.getByRole('button', {name: 'Add Instance'}).click();

    await page
      .getByRole('combobox', {name: 'Build', exact: true})
      .fill('gs://bucket/aosp/aosp_cf_x86_64_phone-img.zip');

    const req = page.waitForRequest(r => r.url().endsWith('/v2/devices') && r.method() === 'POST');
    await page.getByRole('button', {name: 'Create device · 2 instances'}).click();
    const body = (await req).postDataJSON() as Record<string, unknown>;
    const envConfig = (body.advanced_config as {env_config: Record<string, unknown>}).env_config;
    expect(envConfig.instances).toMatchObject([
      {disk: {default_build: 'gs://bucket/aosp/aosp_cf_x86_64_phone-img.zip'}},
      {disk: {default_build: 'gs://bucket/aosp/aosp_cf_x86_64_phone-img.zip'}},
    ]);
    expect(body).not.toHaveProperty('build_id');
  });

  test('turning the switch off asks for a build on each instance', async ({page}) => {
    await openAdvancedMode(page);
    await page.getByRole('button', {name: 'Add Instance'}).click();
    await pickBuild(page, 'Build', 'AOSP Master Phone');

    await page.getByRole('switch', {name: 'Same build for every instance'}).click();

    // The shared build is copied into both rows, so Create stays enabled.
    await expect(page.getByRole('combobox', {name: 'device-1', exact: true})).toHaveValue(
      'AOSP Master Phone'
    );
    await expect(page.getByRole('combobox', {name: 'device-2', exact: true})).toHaveValue(
      'AOSP Master Phone'
    );
    const create = page.getByRole('button', {name: 'Create device · 3 instances'});
    await page.getByRole('button', {name: 'Add Instance'}).click();
    // The added instance gets an empty row, which blocks Create.
    await expect(page.getByRole('combobox', {name: 'device-3', exact: true})).toHaveValue('');
    await expect(create).toBeDisabled();

    await page
      .getByRole('combobox', {name: 'device-3', exact: true})
      .fill('@ab/12345678/aosp_cf_x86_64_phone-userdebug');
    await expect(create).toBeEnabled();

    const req = page.waitForRequest(r => r.url().endsWith('/v2/devices') && r.method() === 'POST');
    await create.click();
    const body = (await req).postDataJSON() as Record<string, unknown>;
    const envConfig = (body.advanced_config as {env_config: Record<string, unknown>}).env_config;
    expect(envConfig.instances).toMatchObject([
      {'@build': 'build-1'},
      {'@build': 'build-1'},
      {disk: {default_build: '@ab/12345678/aosp_cf_x86_64_phone-userdebug'}},
    ]);
    expect(body).not.toHaveProperty('build_id');
  });
});

test.describe('advanced mode import and drafts', () => {
  test('imports a config file and rebuilds the instance list', async ({page}) => {
    await openAdvancedMode(page);

    await page.locator('input[type="file"]').setInputFiles({
      name: 'imported.json',
      mimeType: 'application/json',
      buffer: Buffer.from(
        JSON.stringify({
          instances: [{'@import': 'tablet', name: 'imported-dev', vm: {cpus: 8, memory_mb: 8192}}],
        })
      ),
    });

    await expect(page.getByText('Configuration imported successfully')).toBeVisible();
    await expect(page.getByLabel('Instance name')).toHaveValue('imported-dev');
    await expect(page.locator('.template-card', {hasText: 'Tablet'})).toHaveAttribute(
      'aria-selected',
      'true'
    );

    const config = await exportedConfig(page);
    const instances = config.instances as Array<Record<string, unknown>>;
    expect(instances[0]).toMatchObject({
      '@import': 'tablet',
      name: 'imported-dev',
      vm: {cpus: 8, memory_mb: 8192},
    });
  });

  test('Ctrl+S saves a draft; reload offers it; restore reapplies it', async ({page}) => {
    await openAdvancedMode(page);

    await page.getByLabel('Instance name').fill('my-draft-dev');
    await page.keyboard.press('Control+s');
    await expect(page.getByText('Draft saved')).toBeVisible();

    // The reload lands back in Advanced, where the stored draft is offered.
    await page.reload();

    await expect(page.getByText(/Unsaved draft from/)).toBeVisible();
    await page.getByRole('button', {name: 'Restore'}).click();
    await expect(page.getByText('Draft restored')).toBeVisible();
    await expect(page.getByLabel('Instance name')).toHaveValue('my-draft-dev');
  });
});

/**
 * A bucket the storage listing serves one level at a time, the way the backend
 * does: the objects directly under a prefix, with each subfolder as a folder
 * entry.
 */
const SDV_BUCKET: Record<string, Array<Record<string, unknown>>> = {
  '': [
    {name: 'sdv_core_cf/', display_name: 'sdv_core_cf', is_folder: true},
    {name: 'sdv_ivi_cf/', display_name: 'sdv_ivi_cf', is_folder: true},
  ],
  'sdv_core_cf/': [
    {
      name: 'sdv_core_cf/sdv_core_cf-img-user.zip',
      display_name: 'sdv_core_cf-img-user.zip',
      size: 2048,
    },
    {
      name: 'sdv_core_cf/cvd-host_package.tar.gz',
      display_name: 'cvd-host_package.tar.gz',
      size: 1024,
    },
  ],
  'sdv_ivi_cf/': [
    {
      name: 'sdv_ivi_cf/sdv_ivi_cf-img-user.zip',
      display_name: 'sdv_ivi_cf-img-user.zip',
      size: 2048,
    },
    {
      name: 'sdv_ivi_cf/cvd-host_package.tar.gz',
      display_name: 'cvd-host_package.tar.gz',
      size: 1024,
    },
  ],
};

async function mockSdvBucket(page: Page): Promise<void> {
  await page.route(/\/v2\/storage\/buckets(\?|$)/, route =>
    route.fulfill({json: {buckets: [{name: 'sdv-builds', location: 'EUROPE-WEST4'}]}})
  );
  await page.route(/\/v2\/storage\/buckets\/sdv-builds\/objects(\?|$)/, route => {
    const prefix = new URL(route.request().url()).searchParams.get('prefix') ?? '';
    const objects = (SDV_BUCKET[prefix] ?? []).map(o => ({...o, bucket: 'sdv-builds'}));
    return route.fulfill({json: {objects, prefixes: []}});
  });
}

test.describe('advanced mode storage browser', () => {
  test('Browse fills SDV builds with a folder and a zip, and the pair needs no host tools', async ({
    page,
    pageErrors,
  }) => {
    await mockSdvBucket(page);
    await page.goto('/devices/create');
    await page.getByRole('link', {name: 'AAOS SDV Core + IVI'}).click();

    // The preset asks for an on-demand host under automatic placement.
    await expect(page.getByRole('combobox', {name: 'Capacity'})).toContainText(
      'On-demand (never preempted)'
    );
    await expect(page.getByLabel('Host tools', {exact: true})).toBeVisible();

    // Core: open the bucket, open a build folder and pick the folder itself.
    await page.getByRole('button', {name: 'Browse Cloud Storage for core'}).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByText('sdv-builds').click();
    await dialog.getByText('sdv_core_cf', {exact: true}).click();
    await expect(dialog.getByText('sdv_core_cf-img-user.zip', {exact: true})).toBeVisible();
    await dialog.getByRole('button', {name: 'Select this folder'}).click();
    await expect(dialog).toHaveCount(0);

    const core = page.getByRole('combobox', {name: 'core', exact: true});
    await expect(core).toHaveValue('gs://sdv-builds/sdv_core_cf/');
    // cvd takes the host package from the first instance's build folder.
    await expect(page.getByLabel('Host tools (optional)')).toBeVisible();

    // IVI: the browser opens in the bucket the core build came from. A file
    // row selects the file.
    await page.getByRole('button', {name: 'Browse Cloud Storage for ivi'}).click();
    await dialog.getByText('sdv_ivi_cf', {exact: true}).click();
    await dialog.getByText('sdv_ivi_cf-img-user.zip', {exact: true}).click();
    await dialog.getByRole('button', {name: 'Select', exact: true}).click();
    await expect(page.getByRole('combobox', {name: 'ivi', exact: true})).toHaveValue(
      'gs://sdv-builds/sdv_ivi_cf/sdv_ivi_cf-img-user.zip'
    );

    const create = page.getByRole('button', {name: 'Create device · 2 instances'});
    await expect(create).toBeEnabled();
    const req = page.waitForRequest(r => r.url().endsWith('/v2/devices') && r.method() === 'POST');
    await create.click();
    const body = (await req).postDataJSON() as Record<string, unknown>;
    const envConfig = (body.advanced_config as {env_config: Record<string, unknown>}).env_config;
    expect(envConfig.instances).toMatchObject([
      {name: 'core', disk: {default_build: 'gs://sdv-builds/sdv_core_cf/'}},
      {name: 'ivi', disk: {default_build: 'gs://sdv-builds/sdv_ivi_cf/sdv_ivi_cf-img-user.zip'}},
    ]);
    expect(envConfig).not.toHaveProperty('common');
    expect(body.requirements).toMatchObject({capacity: 'standard'});
    expect(pageErrors, pageErrors.join('\n')).toHaveLength(0);
  });

  test('Browse fills host tools with a file, and capacity stays off the wire on Auto', async ({
    page,
  }) => {
    await mockSdvBucket(page);
    await openAdvancedMode(page);
    await expect(page.getByRole('combobox', {name: 'Capacity'})).toContainText('Auto');

    await page.getByRole('button', {name: 'Browse Cloud Storage for Host tools'}).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByText('sdv-builds').click();
    await dialog.getByText('sdv_ivi_cf', {exact: true}).click();
    // File mode never offers the folder itself.
    await expect(dialog.getByRole('button', {name: 'Select this folder'})).toHaveCount(0);
    await dialog.getByText('cvd-host_package.tar.gz', {exact: true}).click();
    await dialog.getByRole('button', {name: 'Select', exact: true}).click();
    await expect(page.getByLabel('Host tools (optional)')).toHaveValue(
      'gs://sdv-builds/sdv_ivi_cf/cvd-host_package.tar.gz'
    );

    const req = page.waitForRequest(r => r.url().endsWith('/v2/devices') && r.method() === 'POST');
    await page.getByRole('button', {name: 'Create device', exact: true}).click();
    const body = (await req).postDataJSON() as Record<string, unknown>;
    expect(body.requirements).not.toHaveProperty('capacity');
    const envConfig = (body.advanced_config as {env_config: Record<string, unknown>}).env_config;
    expect(envConfig.common).toEqual({
      host_package: 'gs://sdv-builds/sdv_ivi_cf/cvd-host_package.tar.gz',
    });
  });
});
