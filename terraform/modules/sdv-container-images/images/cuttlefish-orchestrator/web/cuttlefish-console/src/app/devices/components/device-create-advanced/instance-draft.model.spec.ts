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

import {describe, expect, it} from 'vitest';
import type {Build} from '../../../builds/models/build.model';
import type {AdvancedDraft, InstanceDraft} from './instance-draft.model';
import {
  applyTemplateDefaults,
  buildValueError,
  buildValueLabel,
  displayDraftsFrom,
  instanceBuildLabel,
  instanceDraftFromTemplate,
  isBuildUrl,
  isGcsFolder,
  isHostPackageUrl,
  isRegisteredBuildId,
  MAX_DISPLAYS,
  templateSwitchDiscardsDisplayEdits,
} from './instance-draft.model';

describe('instanceDraftFromTemplate', () => {
  it('seeds every screen of a multi-display template', () => {
    expect(instanceDraftFromTemplate('auto', 'car-1').displays).toEqual([
      {width: 1080, height: 600, dpi: 120, refreshRate: 60},
      {width: 400, height: 600, dpi: 120, refreshRate: 60},
    ]);
    expect(instanceDraftFromTemplate('foldable', 'fold-1').displays).toEqual([
      {width: 1768, height: 2208, dpi: 374, refreshRate: 60},
      {width: 832, height: 2268, dpi: 387, refreshRate: 60},
    ]);
  });

  it('seeds one screen for a single-display template', () => {
    expect(instanceDraftFromTemplate('phone', 'device-1').displays).toEqual([
      {width: 720, height: 1280, dpi: 320, refreshRate: 60},
    ]);
  });

  it('never seeds more screens than the backend accepts', () => {
    for (const template of [
      'phone',
      'tablet',
      'tv',
      'wearable',
      'auto',
      'slim',
      'go',
      'foldable',
    ] as const) {
      expect(instanceDraftFromTemplate(template, 'd').displays.length).toBeLessThanOrEqual(
        MAX_DISPLAYS
      );
    }
  });
});

describe('applyTemplateDefaults', () => {
  it('replaces the whole screen list, in both directions', () => {
    const phone = instanceDraftFromTemplate('phone', 'device-1');
    const asAuto = applyTemplateDefaults(phone, 'auto');
    expect(asAuto.displays).toEqual(instanceDraftFromTemplate('auto', 'device-1').displays);

    const backToPhone = applyTemplateDefaults(asAuto, 'phone');
    expect(backToPhone.displays).toEqual(phone.displays);
  });

  it('re-applies the VM defaults and keeps every other edit', () => {
    const edited = {...instanceDraftFromTemplate('phone', 'device-1'), cpus: 16, uuid: 'u-1'};
    const asWearable = applyTemplateDefaults(edited, 'wearable');
    expect(asWearable).toMatchObject({memoryMb: 1536, useSdcard: false, cpus: 16, uuid: 'u-1'});
  });
});

describe('templateSwitchDiscardsDisplayEdits', () => {
  it('is false while the screens still match the current template', () => {
    const phone = instanceDraftFromTemplate('phone', 'device-1');
    expect(templateSwitchDiscardsDisplayEdits(phone, 'auto')).toBe(false);
  });

  it('is true once a screen is edited, added or removed', () => {
    const auto = instanceDraftFromTemplate('auto', 'car-1');
    const resized = {...auto, displays: [{...auto.displays[0], width: 1920}, auto.displays[1]]};
    expect(templateSwitchDiscardsDisplayEdits(resized, 'phone')).toBe(true);

    const dropped = {...auto, displays: [auto.displays[0]]};
    expect(templateSwitchDiscardsDisplayEdits(dropped, 'phone')).toBe(true);

    const added = {...auto, displays: [...auto.displays, {...auto.displays[0]}]};
    expect(templateSwitchDiscardsDisplayEdits(added, 'phone')).toBe(true);
  });
});

describe('displayDraftsFrom', () => {
  it('yields one default screen for an absent or empty list', () => {
    const fallback = [{width: 720, height: 1280, dpi: 320, refreshRate: 60}];
    expect(displayDraftsFrom(undefined)).toEqual(fallback);
    expect(displayDraftsFrom([])).toEqual(fallback);
  });
});

function groupOf(
  sameBuild: boolean,
  deviceBuild: string,
  ...instances: InstanceDraft[]
): AdvancedDraft {
  return {
    instances,
    sameBuild,
    deviceBuild,
    hostPackage: '',
    netsimBt: false,
    netsimUwb: false,
    capacity: 'auto',
  };
}

describe('isRegisteredBuildId', () => {
  it('reads a bare id as registered and a URL or @ab/ reference as not', () => {
    expect(isRegisteredBuildId('build-1')).toBe(true);
    expect(isRegisteredBuildId(' 0f3c2a9e-1b2c-4d5e-8f90-123456789abc ')).toBe(true);
    expect(isRegisteredBuildId('gs://b/img.zip')).toBe(false);
    expect(isRegisteredBuildId('https://example.com/img.zip')).toBe(false);
    expect(isRegisteredBuildId('@ab/12345678/target')).toBe(false);
    // A half-typed URL is still a URL, so it is checked as one.
    expect(isRegisteredBuildId('gs:')).toBe(false);
    expect(isRegisteredBuildId('  ')).toBe(false);
  });
});

describe('buildValueError', () => {
  const catalog: Build[] = [{id: 'b-1', name: 'SDV Core', source: 'url'}];

  it('accepts a registered id the catalog lists and a well-formed URL', () => {
    expect(buildValueError('b-1', catalog)).toBeNull();
    expect(buildValueError('gs://b/sdv_core_cf/T/', catalog)).toBeNull();
    expect(buildValueError('@ab/12345678/aosp_cf_x86_64_phone-userdebug', catalog)).toBeNull();
    expect(buildValueError('', catalog)).toBeNull();
  });

  it('refuses an id the loaded catalog does not list', () => {
    expect(buildValueError('gone', catalog)?.kind).toBe('unregistered');
    expect(buildValueError('SDV Co', catalog)?.kind).toBe('unregistered');
  });

  it('cannot call an id missing before the catalog is known', () => {
    expect(buildValueError('gone', null)).toBeNull();
  });

  it('refuses a URL the server would refuse', () => {
    expect(buildValueError('http://b/img.zip', catalog)?.kind).toBe('url');
    expect(buildValueError('gs://', catalog)?.kind).toBe('url');
    expect(buildValueError('@ab/', null)?.kind).toBe('url');
  });
});

describe('buildValueLabel', () => {
  it('names a registered build and shows anything else as typed', () => {
    const builds: Build[] = [{id: 'b-1', name: 'SDV Core', source: 'url'}];
    expect(buildValueLabel('b-1', builds)).toBe('SDV Core');
    expect(buildValueLabel('gone', builds)).toBe('gone');
    expect(buildValueLabel(' gs://b/img.zip ', builds)).toBe('gs://b/img.zip');
  });
});

describe('instanceBuildLabel', () => {
  const base = instanceDraftFromTemplate('auto', 'core');
  const builds: Build[] = [{id: 'b-1', name: 'SDV Core', source: 'url'}];

  it('names the shared build on every card while the switch is on', () => {
    expect(instanceBuildLabel(groupOf(true, 'b-1', base), base, builds, true)).toBe(
      'Device build: SDV Core'
    );
    expect(instanceBuildLabel(groupOf(true, '', base), base, builds, true)).toBe(
      'Platform default'
    );
    // A chosen host has no platform default to fall back on.
    expect(instanceBuildLabel(groupOf(true, '', base), base, builds, false)).toBe(
      'No build picked'
    );
    expect(
      instanceBuildLabel(
        groupOf(true, 'gs://B/T/sdv_core_cf-img-user.zip', base),
        base,
        builds,
        true
      )
    ).toBe('sdv_core_cf-img-user.zip');
  });

  it('names each instance build while the switch is off', () => {
    const own = (build: string): InstanceDraft => ({...base, build});
    const label = (inst: InstanceDraft): string =>
      instanceBuildLabel(groupOf(false, 'ignored', inst), inst, builds, true);
    expect(label(own('b-1'))).toBe('SDV Core');
    expect(label(own('gs://B/sdv_core_cf/T/sdv_core_cf-img-user.zip'))).toBe(
      'sdv_core_cf-img-user.zip'
    );
    expect(label(own('gs://B/T/'))).toBe('T');
    expect(label(own('gone'))).toBe('gone');
    expect(label(own(''))).toBe('No build picked');
  });
});

describe('isBuildUrl', () => {
  it('accepts gs:// and https:// sources, including a gs:// directory', () => {
    expect(isBuildUrl('gs://b/sdv_core_cf/T/sdv_core_cf-img-user.zip')).toBe(true);
    expect(isBuildUrl('gs://b/sdv_core_cf/T/')).toBe(true);
    expect(isBuildUrl(' https://example.com/img.zip ')).toBe(true);
  });

  it('refuses cleartext http, local paths and empty values', () => {
    expect(isBuildUrl('http://example.com/img.zip')).toBe(false);
    expect(isBuildUrl('/tmp/img.zip')).toBe(false);
    expect(isBuildUrl('gs://')).toBe(false);
    expect(isBuildUrl('')).toBe(false);
  });

  it('accepts an Android Build reference, which the server takes in disk.default_build', () => {
    expect(isBuildUrl('@ab/12345678/aosp_cf_x86_64_auto-trunk_staging-userdebug')).toBe(true);
    expect(isBuildUrl(' @ab/aosp-main/aosp_cf_x86_64_phone-userdebug ')).toBe(true);
    expect(isBuildUrl('@ab/')).toBe(false);
    expect(isBuildUrl('ab/12345678/target')).toBe(false);
  });
});

describe('isGcsFolder', () => {
  it('is true only for a gs:// URL that ends in a slash', () => {
    expect(isGcsFolder('gs://b/sdv_core_cf/T/')).toBe(true);
    expect(isGcsFolder(' gs://b/T/ ')).toBe(true);
    expect(isGcsFolder('gs://b/sdv_core_cf/T/sdv_core_cf-img-user.zip')).toBe(false);
    expect(isGcsFolder('https://example.com/T/')).toBe(false);
    expect(isGcsFolder('b-core')).toBe(false);
    expect(isGcsFolder('')).toBe(false);
  });
});

describe('isHostPackageUrl', () => {
  it('accepts only gs:// and https:// URLs', () => {
    expect(isHostPackageUrl('gs://b/sdv_ivi_cf/T/cvd-host_package.tar.gz')).toBe(true);
    expect(isHostPackageUrl(' https://example.com/cvd-host_package.tar.gz ')).toBe(true);
    expect(isHostPackageUrl('@ab/12345678/aosp_cf_x86_64_auto-trunk_staging-userdebug')).toBe(
      false
    );
    expect(isHostPackageUrl('http://example.com/cvd-host_package.tar.gz')).toBe(false);
    expect(isHostPackageUrl('/tmp/cvd-host_package.tar.gz')).toBe(false);
  });
});
