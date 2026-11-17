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
import {fromSpec, instanceToSpec, toSpec, uniqueInstanceName} from './advanced-draft.mapper';
import type {AdvancedDraft, DisplayDraft, InstanceDraft} from './instance-draft.model';

function makeDraft(overrides: Partial<InstanceDraft> = {}): InstanceDraft {
  return {
    id: 'test-id',
    template: 'phone',
    name: 'device-1',
    build: '',
    cpus: 4,
    memoryMb: 4096,
    useSdcard: true,
    displays: [{width: 720, height: 1280, dpi: 320, refreshRate: 60}],
    gpuMode: 'gfxstream',
    recordScreen: false,
    enableBootAnimation: true,
    extraBootconfigArgs: '',
    serialNumber: '',
    useRandomSerial: false,
    guestEnforceSecurity: true,
    vsockGuestGroup: '',
    vhostUserVsock: 'auto',
    streamingDeviceId: '',
    setupWizardMode: 'DISABLED',
    uuid: '',
    crosvmSandbox: false,
    ...overrides,
  };
}

const THREE_DISPLAYS: DisplayDraft[] = [
  {width: 1080, height: 600, dpi: 120, refreshRate: 60},
  {width: 400, height: 600, dpi: 120, refreshRate: 90},
  {width: 1920, height: 1080, dpi: 213, refreshRate: 120},
];

function makeAdvancedDraft(overrides: Partial<AdvancedDraft> = {}): AdvancedDraft {
  return {
    instances: [makeDraft()],
    sameBuild: true,
    deviceBuild: '',
    hostPackage: '',
    netsimBt: false,
    netsimUwb: false,
    capacity: 'auto',
    ...overrides,
  };
}

describe('instanceToSpec', () => {
  it('maps a default phone draft to the exact legacy shape', () => {
    expect(instanceToSpec(makeDraft())).toEqual({
      '@import': 'phone',
      name: 'device-1',
      vm: {cpus: 4, memory_mb: 4096, use_sdcard: true},
      graphics: {
        displays: [{width: 720, height: 1280, dpi: 320, refresh_rate_hertz: 60}],
        gpu_mode: 'gfxstream',
      },
      security: {guest_enforce_security: true},
    });
  });

  it('falls back to "device" for an empty name', () => {
    expect(instanceToSpec(makeDraft({name: ''})).name).toBe('device');
  });

  it('omits boot by default, includes it when animation is disabled or args set', () => {
    expect(instanceToSpec(makeDraft())).not.toHaveProperty('boot');
    expect(instanceToSpec(makeDraft({enableBootAnimation: false})).boot).toEqual({
      enable_bootanimation: false,
    });
    expect(instanceToSpec(makeDraft({extraBootconfigArgs: 'foo=bar'})).boot).toEqual({
      enable_bootanimation: true,
      extra_bootconfig_args: 'foo=bar',
    });
  });

  it('omits security entirely when all security fields are falsy', () => {
    const spec = instanceToSpec(makeDraft({guestEnforceSecurity: false}));
    expect(spec).not.toHaveProperty('security');
  });

  it('includes serial and random-serial fields when set', () => {
    expect(
      instanceToSpec(makeDraft({serialNumber: 'SN1', useRandomSerial: true})).security
    ).toEqual({
      serial_number: 'SN1',
      use_random_serial: true,
      guest_enforce_security: true,
    });
  });

  it('maps the advanced VM extras onto vm', () => {
    expect(
      instanceToSpec(makeDraft({setupWizardMode: 'OPTIONAL', uuid: 'abc', crosvmSandbox: true})).vm
    ).toEqual({
      cpus: 4,
      memory_mb: 4096,
      use_sdcard: true,
      setupwizard_mode: 'OPTIONAL',
      uuid: 'abc',
      crosvm: {enable_sandbox: true},
    });
  });

  it('maps connectivity and streaming only when set', () => {
    const spec = instanceToSpec(
      makeDraft({vsockGuestGroup: 'g1+g2', streamingDeviceId: 'stream-1'})
    );
    expect(spec.connectivity).toEqual({vsock: {guest_group: 'g1+g2'}});
    expect(spec.streaming).toEqual({device_id: 'stream-1'});
    expect(instanceToSpec(makeDraft())).not.toHaveProperty('connectivity');
    expect(instanceToSpec(makeDraft())).not.toHaveProperty('streaming');
  });

  it('includes record_screen only when enabled', () => {
    expect(instanceToSpec(makeDraft({recordScreen: true})).graphics?.record_screen).toBe(true);
    expect(instanceToSpec(makeDraft()).graphics).not.toHaveProperty('record_screen');
  });

  it('emits every screen of a multi-display instance', () => {
    const spec = instanceToSpec(makeDraft({displays: THREE_DISPLAYS}));
    expect(spec.graphics?.displays).toEqual([
      {width: 1080, height: 600, dpi: 120, refresh_rate_hertz: 60},
      {width: 400, height: 600, dpi: 120, refresh_rate_hertz: 90},
      {width: 1920, height: 1080, dpi: 213, refresh_rate_hertz: 120},
    ]);
  });
});

describe('toSpec', () => {
  it('includes netsim flags only when enabled', () => {
    expect(toSpec(makeAdvancedDraft())).not.toHaveProperty('netsim_bt');
    expect(toSpec(makeAdvancedDraft({netsimBt: true, netsimUwb: true}))).toMatchObject({
      netsim_bt: true,
      netsim_uwb: true,
    });
  });

  it('carries no build fields for the platform default', () => {
    const json = JSON.stringify(toSpec(makeAdvancedDraft()));
    expect(json).not.toContain('"disk"');
    expect(json).not.toContain('"common"');
    expect(json).not.toContain('"@build"');
  });

  it('sends a shared registered build as build_id, outside the config', () => {
    const spec = toSpec(
      makeAdvancedDraft({deviceBuild: 'b-1', instances: [makeDraft(), makeDraft({name: 'two'})]})
    );
    expect(JSON.stringify(spec)).not.toContain('"@build"');
    expect(JSON.stringify(spec)).not.toContain('"disk"');
  });

  it('puts a shared URL on every instance, since build_id cannot carry one', () => {
    const spec = toSpec(
      makeAdvancedDraft({
        deviceBuild: ' @ab/12345678/aosp_cf_x86_64_auto-userdebug ',
        instances: [makeDraft(), makeDraft({name: 'two'})],
      })
    );
    expect(spec.instances.map(inst => inst.disk)).toEqual([
      {default_build: '@ab/12345678/aosp_cf_x86_64_auto-userdebug'},
      {default_build: '@ab/12345678/aosp_cf_x86_64_auto-userdebug'},
    ]);
    expect(JSON.stringify(spec)).not.toContain('"@build"');
  });

  it('emits a registered instance build as @build and a URL as disk.default_build', () => {
    const spec = toSpec(
      makeAdvancedDraft({
        sameBuild: false,
        instances: [
          makeDraft({name: 'core', build: 'b-core'}),
          makeDraft({name: 'ivi', build: ' gs://b/ivi/img.zip '}),
          makeDraft({name: 'rest'}),
        ],
      })
    );
    expect(spec.instances[0]['@build']).toBe('b-core');
    expect(spec.instances[0]).not.toHaveProperty('disk');
    expect(spec.instances[1].disk).toEqual({default_build: 'gs://b/ivi/img.zip'});
    expect(spec.instances[1]).not.toHaveProperty('@build');
    expect(spec.instances[2]).not.toHaveProperty('@build');
    expect(spec.instances[2]).not.toHaveProperty('disk');
  });

  it('sends no device build while the switch is off, and no rows while it is on', () => {
    const off = toSpec(
      makeAdvancedDraft({
        sameBuild: false,
        deviceBuild: 'gs://b/shared.zip',
        instances: [makeDraft()],
      })
    );
    expect(off.instances[0]).not.toHaveProperty('disk');

    const on = toSpec(makeAdvancedDraft({instances: [makeDraft({build: 'gs://b/row.zip'})]}));
    expect(on.instances[0]).not.toHaveProperty('disk');
  });

  it('emits the group host package as common.host_package', () => {
    expect(
      toSpec(makeAdvancedDraft({hostPackage: ' gs://b/ivi/cvd-host_package.tar.gz '})).common
    ).toEqual({host_package: 'gs://b/ivi/cvd-host_package.tar.gz'});
  });

  it('keeps the console GPU vocabulary on the wire', () => {
    expect(instanceToSpec(makeDraft({gpuMode: 'swiftshader'})).graphics?.gpu_mode).toBe(
      'swiftshader'
    );
  });
});

describe('fromSpec', () => {
  it('round-trips every editor-exposed field through toSpec', () => {
    const original = makeAdvancedDraft({
      instances: [
        makeDraft({
          template: 'tablet',
          name: 'tab-1',
          cpus: 8,
          memoryMb: 8192,
          useSdcard: false,
          displays: [{width: 2560, height: 1800, dpi: 320, refreshRate: 90}],
          gpuMode: 'swiftshader',
          recordScreen: true,
          enableBootAnimation: false,
          extraBootconfigArgs: 'x=1',
          serialNumber: 'SN9',
          useRandomSerial: true,
          vsockGuestGroup: 'g1',
          streamingDeviceId: 'sid',
          setupWizardMode: 'REQUIRED',
          uuid: 'u-1',
          crosvmSandbox: true,
        }),
      ],
      netsimBt: true,
    });

    const roundTripped = fromSpec(toSpec(original));
    const {id: _a, ...expected} = original.instances[0];
    const {id: _b, ...actual} = roundTripped.instances[0];
    expect(actual).toEqual(expected);
    expect(roundTripped.netsimBt).toBe(true);
    expect(roundTripped.netsimUwb).toBe(false);
  });

  it('round-trips per-instance builds and the host package', () => {
    const original = makeAdvancedDraft({
      sameBuild: false,
      instances: [
        makeDraft({name: 'core', build: 'b-core'}),
        makeDraft({name: 'ivi', build: 'gs://b/sdv_ivi_cf/'}),
      ],
      hostPackage: 'https://example.com/cvd-host_package.tar.gz',
    });

    const roundTripped = fromSpec(toSpec(original));

    expect(roundTripped.sameBuild).toBe(false);
    expect(roundTripped.deviceBuild).toBe('');
    expect(roundTripped.instances.map(inst => inst.build)).toEqual([
      'b-core',
      'gs://b/sdv_ivi_cf/',
    ]);
    expect(roundTripped.hostPackage).toBe('https://example.com/cvd-host_package.tar.gz');
  });

  it('reads a mixed config as per-instance builds with the gaps left empty', () => {
    const draft = fromSpec({
      instances: [{name: 'core', '@build': 'b-core'}, {name: 'ivi'}],
    });
    expect(draft.sameBuild).toBe(false);
    expect(draft.instances.map(inst => inst.build)).toEqual(['b-core', '']);
    // The empty row stays empty on the wire, so validation has to catch it.
    expect(toSpec(draft).instances[1]).not.toHaveProperty('disk');
  });

  it('turns the switch on for a config with no instance builds', () => {
    const draft = fromSpec({instances: [{name: 'a'}, {name: 'b'}]});
    expect(draft.sameBuild).toBe(true);
    expect(draft.deviceBuild).toBe('');
  });

  it('takes the device build that travels beside a config', () => {
    const draft = fromSpec({instances: [{name: 'a'}]}, {sameBuild: true, buildId: 'b-1'});
    expect(draft).toMatchObject({sameBuild: true, deviceBuild: 'b-1'});
  });

  it('keeps the switch off when the hint says so, even with no builds yet', () => {
    const draft = fromSpec({instances: [{name: 'a'}, {name: 'b'}]}, {sameBuild: false});
    expect(draft.sameBuild).toBe(false);
    expect(draft.instances.map(inst => inst.build)).toEqual(['', '']);
  });

  it('round-trips a shared URL when the hint says the switch was on', () => {
    const original = makeAdvancedDraft({
      deviceBuild: 'gs://b/img.zip',
      instances: [makeDraft({name: 'a'}), makeDraft({name: 'b'})],
    });
    const spec = toSpec(original);

    const restored = fromSpec(spec, {sameBuild: true});
    expect(restored.sameBuild).toBe(true);
    expect(restored.deviceBuild).toBe('gs://b/img.zip');
    expect(restored.instances.map(inst => inst.build)).toEqual(['', '']);
    expect(toSpec(restored)).toEqual(spec);

    // Without the hint, the same config reads as two instance builds.
    expect(fromSpec(spec).sameBuild).toBe(false);
  });

  it('reads the builds of a pasted config written for cvd', () => {
    const draft = fromSpec({
      common: {host_package: 'gs://B/sdv_ivi_cf/T/cvd-host_package.tar.gz'},
      instances: [
        {name: 'core', disk: {default_build: 'gs://B/sdv_core_cf/T/sdv_core_cf-img-user.zip'}},
        {name: 'ivi', '@build': 'b-ivi'},
      ],
    });
    expect(draft.hostPackage).toBe('gs://B/sdv_ivi_cf/T/cvd-host_package.tar.gz');
    expect(draft.instances.map(inst => inst.build)).toEqual([
      'gs://B/sdv_core_cf/T/sdv_core_cf-img-user.zip',
      'b-ivi',
    ]);
  });

  it('keeps @build over disk.default_build, so the resubmitted config has one build', () => {
    const draft = fromSpec({
      instances: [{name: 'core', '@build': 'b-core', disk: {default_build: 'gs://b/img.zip'}}],
    });
    expect(draft.instances[0].build).toBe('b-core');

    const resubmitted = toSpec(draft).instances[0];
    expect(resubmitted['@build']).toBe('b-core');
    expect(resubmitted).not.toHaveProperty('disk');
  });

  it('imports guest_swiftshader, the cvd spelling, as swiftshader', () => {
    const draft = fromSpec({instances: [{graphics: {gpu_mode: 'guest_swiftshader'}}]});
    expect(draft.instances[0].gpuMode).toBe('swiftshader');
    expect(toSpec(draft).instances[0].graphics?.gpu_mode).toBe('swiftshader');
  });

  it('keeps all three screens of a multi-display instance through a round trip', () => {
    const original = makeAdvancedDraft({instances: [makeDraft({displays: THREE_DISPLAYS})]});
    expect(fromSpec(toSpec(original)).instances[0].displays).toEqual(THREE_DISPLAYS);
  });

  it('applies the legacy fixed fallbacks for a minimal instance', () => {
    const draft = fromSpec({instances: [{'@import': 'tv'}]});
    // Fallbacks are fixed values, NOT template defaults (tv would be 2048).
    expect(draft.instances[0]).toMatchObject({
      template: 'tv',
      name: 'device-1',
      cpus: 4,
      memoryMb: 4096,
      displays: [{width: 720, height: 1280, dpi: 320, refreshRate: 60}],
      gpuMode: 'gfxstream',
      guestEnforceSecurity: true,
      setupWizardMode: 'DISABLED',
      build: '',
    });
  });

  it('gives an instance whose screens are half-specified the fixed per-field fallbacks', () => {
    const draft = fromSpec({instances: [{graphics: {displays: [{width: 1920}]}}]});
    expect(draft.instances[0].displays).toEqual([
      {width: 1920, height: 1280, dpi: 320, refreshRate: 60},
    ]);
  });

  it('generates unique names for unnamed instances', () => {
    const draft = fromSpec({instances: [{}, {name: 'device-2'}, {}]});
    expect(draft.instances.map(i => i.name)).toEqual(['device-1', 'device-2', 'device-3']);
  });
});

describe('uniqueInstanceName', () => {
  it('returns the first free numbered name', () => {
    expect(uniqueInstanceName('device', new Set())).toBe('device-1');
    expect(uniqueInstanceName('device', new Set(['device-1', 'device-2']))).toBe('device-3');
    expect(uniqueInstanceName('tab', new Set(['device-1']))).toBe('tab-1');
  });
});
