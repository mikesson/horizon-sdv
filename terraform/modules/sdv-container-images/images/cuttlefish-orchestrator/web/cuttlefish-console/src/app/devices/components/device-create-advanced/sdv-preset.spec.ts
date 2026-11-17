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
import {EnvironmentSpecificationSchema} from '../../models/cvd-config.schema';
import {toSpec} from './advanced-draft.mapper';
import type {AdvancedDraft} from './instance-draft.model';
import {instanceDraftFromTemplate} from './instance-draft.model';
import {
  isSdvDraft,
  sdvBuildHint,
  sdvHostWarning,
  sdvNeedsHostPackage,
  sdvPairDraft,
} from './sdv-preset';

describe('sdvPairDraft', () => {
  it('emits the proven pair with explicit gpu_mode and displays on both instances', () => {
    const spec = toSpec(sdvPairDraft());

    expect(spec.instances.map(inst => inst.name)).toEqual(['core', 'ivi']);
    const [core, ivi] = spec.instances;

    expect(core.vm).toMatchObject({cpus: 2, memory_mb: 2096, use_sdcard: false});
    expect(core.graphics?.gpu_mode).toBe('swiftshader');
    expect(core.graphics?.displays).toEqual([
      {width: 720, height: 480, dpi: 160, refresh_rate_hertz: 60},
    ]);
    expect(core.boot?.extra_bootconfig_args).toBe(
      'androidboot.sdv.instance_name=instance1 androidboot.virt.address=3 androidboot.sdv.boot_mode=unlocked'
    );

    expect(ivi.vm).toMatchObject({cpus: 4, memory_mb: 4096});
    expect(ivi.graphics?.gpu_mode).toBe('swiftshader');
    expect(ivi.graphics?.displays).toEqual([
      {width: 1080, height: 600, dpi: 120, refresh_rate_hertz: 60},
      {width: 400, height: 600, dpi: 120, refresh_rate_hertz: 60},
    ]);
    expect(ivi.boot?.extra_bootconfig_args).toBe(
      'androidboot.sdv.instance_name=instance2 androidboot.virt.address=4 androidboot.sdv.boot_mode=unlocked'
    );
  });

  it('asks for a build per instance and leaves them and the host package empty', () => {
    const draft = sdvPairDraft();
    // With the switch on, automatic placement would fill both instances from
    // the platform default phone build: two phones with SDV bootconfig.
    expect(draft.sameBuild).toBe(false);
    expect(draft.deviceBuild).toBe('');
    expect(draft.instances.map(inst => inst.build)).toEqual(['', '']);
    expect(draft.hostPackage).toBe('');
  });

  it('asks for an on-demand host, since a Spot preemption kills the pair', () => {
    expect(sdvPairDraft().capacity).toBe('standard');
  });

  it('names the image each instance expects', () => {
    const [core, ivi] = sdvPairDraft().instances;
    expect(sdvBuildHint(core)).toContain('sdv_core_cf-img-user.zip');
    expect(sdvBuildHint(ivi)).toContain('sdv_ivi_cf-img-user.zip');
    expect(sdvBuildHint(instanceDraftFromTemplate('phone', 'device-1'))).toBe('');
  });

  it('produces a config the editor schema accepts', () => {
    expect(EnvironmentSpecificationSchema.safeParse(toSpec(sdvPairDraft())).success).toBe(true);
  });

  it('gives each instance a fresh client id', () => {
    const [core, ivi] = sdvPairDraft().instances;
    expect(core.id).not.toBe(ivi.id);
    expect(sdvPairDraft().instances[0].id).not.toBe(core.id);
  });
});

describe('isSdvDraft', () => {
  it('recognises the pair by its bootconfig, so a pasted config counts', () => {
    expect(isSdvDraft(sdvPairDraft())).toBe(true);
  });

  it('is false for an ordinary device', () => {
    expect(
      isSdvDraft({
        instances: [instanceDraftFromTemplate('auto', 'car-1')],
        sameBuild: true,
        deviceBuild: '',
        hostPackage: '',
        netsimBt: false,
        netsimUwb: false,
        capacity: 'auto',
      })
    ).toBe(false);
  });
});

describe('sdvHostWarning', () => {
  it('warns when the chosen host already runs devices', () => {
    expect(sdvHostWarning(sdvPairDraft(), {currentDevices: 1})).toContain(
      'already runs one device'
    );
    expect(sdvHostWarning(sdvPairDraft(), {currentDevices: 3})).toContain('already runs 3 devices');
  });

  it('stays quiet for an empty host, no host, or a device that is not SDV', () => {
    expect(sdvHostWarning(sdvPairDraft(), {currentDevices: 0})).toBe('');
    expect(sdvHostWarning(sdvPairDraft(), undefined)).toBe('');
    expect(
      sdvHostWarning(
        {
          instances: [instanceDraftFromTemplate('phone', 'device-1')],
          sameBuild: true,
          deviceBuild: '',
          hostPackage: '',
          netsimBt: false,
          netsimUwb: false,
          capacity: 'auto',
        },
        {currentDevices: 2}
      )
    ).toBe('');
  });
});

describe('sdvNeedsHostPackage', () => {
  /** The pair with both instances on image URLs, which carry no host tools. */
  function urlPair(): AdvancedDraft {
    const draft = sdvPairDraft();
    return {
      ...draft,
      instances: draft.instances.map(inst => ({
        ...inst,
        build: `gs://b/sdv_${inst.name}_cf/T/sdv_${inst.name}_cf-img-user.zip`,
      })),
    };
  }

  it('is true for an SDV pair with no registered build anywhere', () => {
    expect(sdvNeedsHostPackage(urlPair())).toBe(true);
    expect(sdvNeedsHostPackage(sdvPairDraft())).toBe(true);
    // The platform default does not count: the editor cannot see what it is.
    expect(sdvNeedsHostPackage({...urlPair(), sameBuild: true, deviceBuild: ''})).toBe(true);
    expect(
      sdvNeedsHostPackage({...urlPair(), sameBuild: true, deviceBuild: 'gs://b/img.zip'})
    ).toBe(true);
  });

  it('is false once a registered device or instance build can supply it', () => {
    expect(sdvNeedsHostPackage({...urlPair(), sameBuild: true, deviceBuild: 'b-ivi'})).toBe(false);
    const [core, ivi] = urlPair().instances;
    expect(sdvNeedsHostPackage({...urlPair(), instances: [core, {...ivi, build: 'b-ivi'}]})).toBe(
      false
    );
  });

  it('is false when the first instance boots from a gs:// build folder', () => {
    const [core, ivi] = urlPair().instances;
    expect(
      sdvNeedsHostPackage({
        ...urlPair(),
        instances: [{...core, build: 'gs://b/sdv_core_cf/T/'}, ivi],
      })
    ).toBe(false);
    expect(
      sdvNeedsHostPackage({...urlPair(), sameBuild: true, deviceBuild: 'gs://b/sdv_core_cf/T/'})
    ).toBe(false);
  });

  it('is true when only a later instance boots from a build folder', () => {
    // cvd takes the host package from the first instance's folder only.
    const [core, ivi] = urlPair().instances;
    expect(
      sdvNeedsHostPackage({
        ...urlPair(),
        instances: [core, {...ivi, build: 'gs://b/sdv_ivi_cf/T/'}],
      })
    ).toBe(true);
  });

  it('is false for a device that is not SDV', () => {
    expect(
      sdvNeedsHostPackage({
        instances: [{...instanceDraftFromTemplate('phone', 'device-1'), build: 'gs://b/img.zip'}],
        sameBuild: true,
        deviceBuild: '',
        hostPackage: '',
        netsimBt: false,
        netsimUwb: false,
        capacity: 'auto',
      })
    ).toBe(false);
  });
});
