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

import {provideHttpClient} from '@angular/common/http';
import {provideHttpClientTesting} from '@angular/common/http/testing';
import {provideZonelessChangeDetection} from '@angular/core';
import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {MatDialog} from '@angular/material/dialog';
import {of} from 'rxjs';
import type {Mock} from 'vitest';
import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest';

// The editor this component embeds pulls in codemirror-json-schema, whose
// published ESM subpath imports do not resolve under the test runner. Nothing
// here exercises the editor, so stub the one symbol it imports.
vi.mock('codemirror-json-schema', () => ({jsonSchema: () => []}));

import type {MatChipInputEvent} from '@angular/material/chips';
import type {Host} from '../../../hosts/models/host.model';
import type {EnvironmentSpecification} from '../../models/cvd-config.schema';
import type {AdvancedSeed} from '../device-create-page/create-intent.mapper';
import {loadDraft, saveDraft} from './advanced-draft.storage';
import type {AdvancedSubmitEvent} from './device-create-advanced.component';
import {DeviceCreateAdvancedComponent} from './device-create-advanced.component';
import type {DisplayDraft} from './instance-draft.model';
import {instanceDraftFromTemplate} from './instance-draft.model';

/** The phone template's screen list, the seed every test below starts from. */
const PHONE_DISPLAYS: readonly DisplayDraft[] = instanceDraftFromTemplate(
  'phone',
  'device-1'
).displays;

/** A seed whose single phone instance carries the given screen list. */
function seedWithDisplays(displays: DisplayDraft[]): AdvancedSeed {
  return {
    draft: {
      instances: [{...instanceDraftFromTemplate('phone', 'device-1'), displays}],
      sameBuild: true,
      deviceBuild: '',
      hostPackage: '',
      netsimBt: false,
      netsimUwb: false,
      capacity: 'auto',
    },
    hostChoice: {kind: 'auto'},
    buildChoice: {kind: 'default'},
    lifecycle: {
      ttlHours: 4,
      pinned: false,
      pinnedReason: '',
      pinnedReasonDetail: '',
      pinnedUntil: '',
      tags: [],
    },
  };
}

/**
 * Opens with a fixed answer and records every request, so a test can assert
 * both that the confirmation was asked for and that it was not.
 */
interface Rendered {
  component: DeviceCreateAdvancedComponent;
  dialog: {open: Mock};
}

async function render(answer: boolean, seed: AdvancedSeed, hosts: Host[] = []): Promise<Rendered> {
  const dialog = {open: vi.fn(() => ({afterClosed: () => of(answer)}))};
  TestBed.resetTestingModule();
  await TestBed.configureTestingModule({
    imports: [DeviceCreateAdvancedComponent],
    providers: [
      provideZonelessChangeDetection(),
      provideHttpClient(),
      provideHttpClientTesting(),
      {provide: MatDialog, useValue: dialog},
    ],
  }).compileComponents();
  const fixture = TestBed.createComponent(DeviceCreateAdvancedComponent);
  fixture.componentRef.setInput('seed', seed);
  fixture.componentRef.setInput('availableHosts', hosts);
  fixture.detectChanges();
  return {component: fixture.componentInstance, dialog};
}

function displays(component: DeviceCreateAdvancedComponent): readonly DisplayDraft[] {
  return component.activeInstance()?.displays ?? [];
}

describe('DeviceCreateAdvancedComponent template switching', () => {
  beforeEach(() => {
    localStorage.clear(); // a stored draft would offer a restore over the seed
  });

  it('switches without asking when the screens are still the template defaults', async () => {
    const untouched = instanceDraftFromTemplate('phone', 'device-1').displays;
    const {component, dialog} = await render(true, seedWithDisplays([...untouched]));

    component.changeTemplate('auto');

    expect(dialog.open).not.toHaveBeenCalled();
    // 'auto' is one of the two multi-display templates: gaining a screen the
    // user never touched must not cost them a dialog.
    expect(displays(component)).toHaveLength(2);
  });

  it('asks before replacing edited screens, and applies on confirm', async () => {
    const edited = instanceDraftFromTemplate('phone', 'device-1').displays;
    const {component, dialog} = await render(true, seedWithDisplays([{...edited[0], width: 1234}]));

    component.changeTemplate('auto');

    expect(dialog.open).toHaveBeenCalledTimes(1);
    expect(displays(component)).toHaveLength(2);
    expect(displays(component)[0].width).toBe(1080);
  });

  it('keeps the edited screens when the confirmation is declined', async () => {
    const edited = instanceDraftFromTemplate('phone', 'device-1').displays;
    const {component, dialog} = await render(
      false,
      seedWithDisplays([{...edited[0], width: 1234}])
    );

    component.changeTemplate('auto');

    expect(dialog.open).toHaveBeenCalledTimes(1);
    // Declining leaves both the screens and the template alone. The picker is
    // a controlled input, so the model not moving is the whole revert.
    expect(displays(component)).toHaveLength(1);
    expect(displays(component)[0].width).toBe(1234);
    expect(component.activeInstance()?.template).toBe('phone');
  });
});

/**
 * The draft operations behind the editor's instance list and lifecycle
 * controls. These drive the model. Rendering is the e2e suite's job, and a
 * passing spec here says nothing about whether a button is wired to them.
 */
describe('DeviceCreateAdvancedComponent draft operations', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it('adds instances up to the backend ceiling and no further', async () => {
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]));

    const ceiling = component.maxInstances;
    for (let i = 0; i < ceiling + 2; i++) {
      component.addInstance();
    }

    expect(component.instances()).toHaveLength(ceiling);
    // Names must stay unique: they become the group's instance names, which
    // upstream requires to be distinct within a group.
    expect(new Set(component.instances().map(i => i.name)).size).toBe(ceiling);
  });

  it('refuses to remove the last instance', async () => {
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]));

    component.addInstance();
    const [first, second] = component.instances();
    component.removeInstance(second.id);
    expect(component.instances()).toHaveLength(1);

    component.removeInstance(first.id);
    expect(component.instances()).toHaveLength(1);
  });

  it('duplicates an instance under a fresh id and name', async () => {
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]));
    const [source] = component.instances();

    component.duplicateInstance(source.id);

    const copies = component.instances();
    expect(copies).toHaveLength(2);
    expect(copies[1].id).not.toBe(source.id);
    expect(copies[1].name).not.toBe(source.name);
    expect(copies[1].displays).toEqual(source.displays);
    // Duplicating selects the copy, so the settings panel follows the click.
    expect(component.activeInstanceId()).toBe(copies[1].id);
  });

  it('ignores a duplicate of an unknown instance', async () => {
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]));

    component.duplicateInstance('no-such-id');

    expect(component.instances()).toHaveLength(1);
  });

  it('records host, build and lifecycle answers', async () => {
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]));

    component.setHostKind('host');
    component.setTtl(6);

    expect(component.hostChoice()).toEqual({kind: 'host', id: ''});
    expect(component.buildChoice()).toEqual({kind: 'default'});
    expect(component.lifecycle().ttlHours).toBe(6);
    expect(component.instances()).toHaveLength(1);
  });

  it('adds a tag once and removes it by value', async () => {
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]));
    const chipInput = {clear: vi.fn()};
    const event = (value: string): MatChipInputEvent =>
      ({value, chipInput}) as unknown as MatChipInputEvent;

    component.addTag(event('nightly'));
    component.addTag(event('  nightly  '));
    component.addTag(event('   '));
    expect(component.lifecycle().tags).toEqual(['nightly']);

    component.removeTag('nightly');
    expect(component.lifecycle().tags).toEqual([]);
  });

  it('applies an imported config and ignores a failed parse', async () => {
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]));

    component.onJsonApply({
      success: true,
      config: {
        instances: [
          {name: 'imported-a', vm: {memory_mb: 2048}},
          {name: 'imported-b', vm: {memory_mb: 2048}},
        ],
      },
    });
    expect(component.instances().map(i => i.name)).toEqual(['imported-a', 'imported-b']);

    // A failed parse must not clear what the editor already holds. The import
    // surface is the one place a typo can destroy a whole draft.
    component.onJsonApply({success: false});
    expect(component.instances().map(i => i.name)).toEqual(['imported-a', 'imported-b']);
  });

  it('emits the draft on create and leaves no stored draft behind', async () => {
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]));
    const emitted: AdvancedSubmitEvent[] = [];
    component.submitted.subscribe(e => emitted.push(e));

    component.onCreate();

    expect(emitted).toHaveLength(1);
    expect(emitted[0].config.instances).toHaveLength(1);
    expect(emitted[0].hostChoice).toEqual({kind: 'auto'});
    // A draft that outlived its own submit would offer to restore the device
    // the user just created.
    expect(loadDraft()).toBeNull();
  });

  it('restores a stored draft, including its declarative host and build', async () => {
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]));
    saveDraft(
      JSON.stringify({instances: [{name: 'from-storage', vm: {memory_mb: 4096}}]}),
      '',
      '',
      true,
      'auto'
    );

    component.restoreDraft();

    expect(component.instances().map(i => i.name)).toEqual(['from-storage']);
    expect(component.hostChoice()).toEqual({kind: 'auto'});
    expect(component.buildChoice()).toEqual({kind: 'default'});
  });

  it('leaves the editor alone when there is no draft to restore', async () => {
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]));

    component.restoreDraft();

    expect(component.instances().map(i => i.name)).toEqual(['device-1']);
  });
});

describe('DeviceCreateAdvancedComponent seed', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  // A handoff link names a host or a build and nothing else. The parts it does
  // not name must keep the editor's own defaults, not arrive as undefined.
  it('applies only the parts a seed carries', async () => {
    const {component} = await render(true, {hostChoice: {kind: 'host', id: 'h1'}});

    expect(component.hostChoice()).toEqual({kind: 'host', id: 'h1'});
    expect(component.buildChoice()).toEqual({kind: 'default'});
    expect(component.instances().map(i => i.name)).toEqual(['device-1']);
    expect(component.lifecycle().tags).toEqual([]);
  });

  it('submits the device name the seed carries', async () => {
    const {component} = await render(true, {name: 'from-simple'});
    const emitted: AdvancedSubmitEvent[] = [];
    component.submitted.subscribe(e => emitted.push(e));

    component.onCreate();

    expect(emitted.map(e => e.name)).toEqual(['from-simple']);
  });

  // Direct entry from the chooser names no device. The editor must not send
  // an empty name or borrow an instance's.
  it('generates a valid device name when the seed has none', async () => {
    const {component} = await render(true, {});
    const emitted: AdvancedSubmitEvent[] = [];
    component.submitted.subscribe(e => emitted.push(e));

    expect(component.deviceName()).toMatch(/^device-[a-z0-9]{5}$/);
    expect(component.isValid()).toBe(true);

    component.deviceName.set('my-pair');
    component.onCreate();

    expect(emitted.map(e => e.name)).toEqual(['my-pair']);
  });

  it('gates Create on the device name rules', async () => {
    const {component} = await render(true, {name: 'ok-name'});
    expect(component.isValid()).toBe(true);

    component.deviceName.set('');
    expect(component.isValid()).toBe(false);

    component.deviceName.set('Has Spaces');
    expect(component.isValid()).toBe(false);

    component.deviceName.set('ok-name');
    expect(component.isValid()).toBe(true);
  });
});

/**
 * Each mode keeps its own draft now, and the restore banner is the only way
 * back to the Advanced one. A draft overwritten by the editor's opening state
 * two seconds after load is a draft the banner offers and cannot restore.
 */
describe('DeviceCreateAdvancedComponent autosave', () => {
  const stored = JSON.stringify({instances: [{name: 'kept-from-last-time'}]});

  beforeEach(() => {
    localStorage.clear();
    vi.useFakeTimers({toFake: ['setTimeout', 'clearTimeout']});
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('leaves the stored draft alone until the user edits', async () => {
    saveDraft(stored, 'h-old', '', true, 'auto');
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]));
    TestBed.tick();

    vi.advanceTimersByTime(5000);

    expect(loadDraft()?.config.instances.map(i => i.name)).toEqual(['kept-from-last-time']);
    expect(component.hasDraft()).toBe(true);
  });

  it('saves once the user edits', async () => {
    saveDraft(stored, 'h-old', '', true, 'auto');
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]));
    TestBed.tick();

    component.addInstance();
    TestBed.tick();
    vi.advanceTimersByTime(5000);

    expect(loadDraft()?.config.instances).toHaveLength(2);
  });
});

function hostWith(currentDevices: number): Host {
  const base: Partial<Host> = {
    id: `host-${currentDevices}`,
    name: `host-${currentDevices}`,
    machineType: 'n2-standard-16',
    currentDevices,
    maxDevices: 4,
  };
  return base as Host;
}

/** Two instances that each carry their own image URL. */
const OWN_BUILDS = {
  instances: [
    {name: 'core', disk: {default_build: 'gs://b/sdv_core_cf/T/sdv_core_cf-img-user.zip'}},
    {name: 'ivi', disk: {default_build: 'gs://b/sdv_ivi_cf/T/sdv_ivi_cf-img-user.zip'}},
  ],
};

/** A seed holding the given instance names and build settings. */
function seedWith(
  names: string[],
  builds: {sameBuild: boolean; deviceBuild?: string; rows?: string[]}
): AdvancedSeed {
  return {
    draft: {
      instances: names.map((name, i) => ({
        ...instanceDraftFromTemplate('phone', name),
        build: builds.rows?.[i] ?? '',
      })),
      sameBuild: builds.sameBuild,
      deviceBuild: builds.deviceBuild ?? '',
      hostPackage: '',
      netsimBt: false,
      netsimUwb: false,
      capacity: 'auto',
    },
  };
}

function created(component: DeviceCreateAdvancedComponent): AdvancedSubmitEvent[] {
  const emitted: AdvancedSubmitEvent[] = [];
  component.submitted.subscribe(e => emitted.push(e));
  component.onCreate();
  return emitted;
}

describe('DeviceCreateAdvancedComponent shared build', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it('leaves an automatic create on the platform default', async () => {
    const {component} = await render(true, seedWith(['a', 'b'], {sameBuild: true}));

    const [event] = created(component);

    expect(event.buildChoice).toEqual({kind: 'default'});
    expect(JSON.stringify(event.config)).not.toContain('default_build');
    expect(component.instanceCards().map(card => card.build)).toEqual([
      'Platform default',
      'Platform default',
    ]);
  });

  it('refuses the platform default for a chosen host', async () => {
    const empty = hostWith(0);
    const {component} = await render(true, seedWith(['a'], {sameBuild: true}), [empty]);
    component.selectHost(empty);

    expect(component.isValid()).toBe(false);
    expect(component.instanceCards()[0].build).toBe('No build picked');
  });

  it('sends a registered shared build as build_id', async () => {
    const empty = hostWith(0);
    const {component} = await render(
      true,
      seedWith(['a', 'b'], {sameBuild: true, deviceBuild: 'b-1'}),
      [empty]
    );
    component.selectHost(empty);
    expect(component.isValid()).toBe(true);

    const [event] = created(component);

    expect(event.buildChoice).toEqual({kind: 'registered', id: 'b-1'});
    expect(JSON.stringify(event.config)).not.toContain('@build');
  });

  it('sends a shared URL on every instance and no build_id', async () => {
    const empty = hostWith(0);
    const {component} = await render(
      true,
      seedWith(['a', 'b'], {sameBuild: true, deviceBuild: 'gs://b/img.zip'}),
      [empty]
    );
    component.selectHost(empty);
    expect(component.isValid()).toBe(true);

    const [event] = created(component);

    expect(event.buildChoice).toEqual({kind: 'default'});
    expect(event.config.instances.map(inst => inst.disk?.default_build)).toEqual([
      'gs://b/img.zip',
      'gs://b/img.zip',
    ]);
  });

  it('takes a handoff build as the shared build', async () => {
    const {component} = await render(true, {buildChoice: {kind: 'registered', id: 'b-1'}});
    expect(component.buildChoice()).toEqual({kind: 'registered', id: 'b-1'});
  });

  it('refuses a malformed shared URL', async () => {
    const {component} = await render(
      true,
      seedWith(['a'], {sameBuild: true, deviceBuild: 'http://b/img.zip'})
    );
    expect(component.isValid()).toBe(false);
  });
});

describe('DeviceCreateAdvancedComponent per-instance builds', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it('requires a build on every row, whatever the placement', async () => {
    const {component} = await render(
      true,
      seedWith(['core', 'ivi'], {sameBuild: false, rows: ['b-core', '']})
    );
    expect(component.isValid()).toBe(false);

    component.onJsonApply({success: true, config: OWN_BUILDS});
    expect(component.isValid()).toBe(true);
  });

  it('sends a registered row as @build and a URL row as disk.default_build', async () => {
    const empty = hostWith(0);
    const {component} = await render(
      true,
      seedWith(['core', 'ivi'], {sameBuild: false, rows: ['b-core', 'gs://b/ivi/img.zip']}),
      [empty]
    );
    component.selectHost(empty);

    const [event] = created(component);

    expect(event.buildChoice).toEqual({kind: 'default'});
    expect(event.config.instances[0]['@build']).toBe('b-core');
    expect(event.config.instances[0]).not.toHaveProperty('disk');
    expect(event.config.instances[1].disk).toEqual({default_build: 'gs://b/ivi/img.zip'});
    expect(event.config.instances[1]).not.toHaveProperty('@build');
  });

  it('refuses an instance URL the server would refuse', async () => {
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]));

    component.onJsonApply({
      success: true,
      config: {instances: [{name: 'core', disk: {default_build: 'http://b/img.zip'}}]},
    });

    expect(component.isValid()).toBe(false);
  });

  it('accepts an Android Build reference as an instance build', async () => {
    const empty = hostWith(0);
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]), [empty]);
    component.selectHost(empty);

    component.onJsonApply({
      success: true,
      config: {
        instances: [
          {
            name: 'core',
            disk: {default_build: '@ab/12345678/aosp_cf_x86_64_auto-trunk_staging-userdebug'},
          },
        ],
      },
    });

    expect(component.isValid()).toBe(true);
  });

  it('keeps the host package to gs:// and https:// URLs', async () => {
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]));

    component.onJsonApply({
      success: true,
      config: {
        instances: [{name: 'device-1'}],
        common: {host_package: '@ab/12345678/aosp_cf_x86_64_phone-userdebug'},
      },
    });

    expect(component.isValid()).toBe(false);
  });

  it('shows the submitted config, not a merged copy, in the preview', async () => {
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]));

    component.onJsonApply({
      success: true,
      config: {...OWN_BUILDS, common: {host_package: 'gs://b/cvd-host_package.tar.gz'}},
    });

    const preview = JSON.parse(component.configJson()) as Record<string, unknown>;
    expect(preview['common']).toEqual({host_package: 'gs://b/cvd-host_package.tar.gz'});
    expect(component.instanceCards().map(card => card.build)).toEqual([
      'sdv_core_cf-img-user.zip',
      'sdv_ivi_cf-img-user.zip',
    ]);
  });

  it('gives an added instance an empty row while the switch is off', async () => {
    const {component} = await render(
      true,
      seedWith(['core'], {sameBuild: false, rows: ['gs://b/core.zip']})
    );

    component.addInstance();

    expect(component.instances().map(inst => inst.build)).toEqual(['gs://b/core.zip', '']);
    expect(component.isValid()).toBe(false);
  });
});

describe('DeviceCreateAdvancedComponent build switch', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it('copies the shared build into every row when turned off', async () => {
    const {component} = await render(
      true,
      seedWith(['a', 'b'], {sameBuild: true, deviceBuild: 'gs://b/img.zip'})
    );

    component.setSameBuild(false);

    expect(component.instances().map(inst => inst.build)).toEqual([
      'gs://b/img.zip',
      'gs://b/img.zip',
    ]);
    expect(component.buildChoice()).toEqual({kind: 'default'});
  });

  it('copies the platform default as empty rows', async () => {
    const {component} = await render(true, seedWith(['a', 'b'], {sameBuild: true}));

    component.setSameBuild(false);

    expect(component.instances().map(inst => inst.build)).toEqual(['', '']);
    expect(component.isValid()).toBe(false);
  });

  it('keeps the first row when turned on, and says when others were replaced', async () => {
    const {component} = await render(
      true,
      seedWith(['core', 'ivi'], {sameBuild: false, rows: ['b-core', 'gs://b/ivi.zip']})
    );

    component.setSameBuild(true);

    expect(component.buildChoice()).toEqual({kind: 'registered', id: 'b-core'});
    expect(JSON.stringify(component.configJson())).not.toContain('ivi.zip');
    expect(component.instanceCards().map(card => card.build)).toEqual([
      'Device build: b-core',
      'Device build: b-core',
    ]);
    expect(component.buildsNote()).toContain('core');
  });

  it('says nothing when every row already matched', async () => {
    const {component} = await render(
      true,
      seedWith(['a', 'b'], {sameBuild: false, rows: ['b-1', 'b-1']})
    );

    component.setSameBuild(true);

    expect(component.buildsNote()).toBe('');
  });

  it('keeps the switch where it is when the preview is applied unchanged', async () => {
    const {component} = await render(
      true,
      seedWith(['a', 'b'], {sameBuild: true, deviceBuild: 'gs://b/img.zip'})
    );
    const before = component.configJson();

    component.onJsonApply({
      success: true,
      config: JSON.parse(before) as EnvironmentSpecification,
    });

    expect(component.buildChoice()).toEqual({kind: 'default'});
    expect(component.instances().map(inst => inst.build)).toEqual(['', '']);
    expect(component.configJson()).toBe(before);
  });
});

describe('DeviceCreateAdvancedComponent SDV preset', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it('replaces the instances with the pair once confirmed', async () => {
    const {component, dialog} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]));
    component.onJsonApply({
      success: true,
      config: {instances: [{name: 'device-1'}], common: {host_package: 'gs://b/host.tar.gz'}},
    });

    component.startFromSdvPair();

    expect(dialog.open).toHaveBeenCalledTimes(1);
    expect(component.instances().map(i => i.name)).toEqual(['core', 'ivi']);
    expect(component.isSdvPair()).toBe(true);
    // Only the instance list and the builds are replaced; a host package
    // already typed stays.
    const preview = JSON.parse(component.configJson()) as Record<string, unknown>;
    expect(preview['common']).toEqual({host_package: 'gs://b/host.tar.gz'});
  });

  it('asks automatic placement for an on-demand host', async () => {
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]));
    expect(component.capacity()).toBe('auto');

    component.startFromSdvPair();
    component.onJsonApply({
      success: true,
      config: {
        instances: component.instances().map((inst, i) => ({
          name: inst.name,
          disk: {default_build: `gs://b/sdv_${inst.name}_cf/T/`},
          boot: {extra_bootconfig_args: component.instances()[i].extraBootconfigArgs},
        })),
      },
    });

    expect(component.capacity()).toBe('standard');
    const [event] = created(component);
    expect(event.capacity).toBe('standard');
  });

  it('needs no host tools when the first instance boots from a build folder', async () => {
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]));
    component.startFromSdvPair();
    const [core, ivi] = component.instances();
    const pair = (coreBuild: string): EnvironmentSpecification => ({
      instances: [
        {
          name: core.name,
          disk: {default_build: coreBuild},
          boot: {extra_bootconfig_args: core.extraBootconfigArgs},
        },
        {
          name: ivi.name,
          disk: {default_build: 'gs://b/sdv_ivi_cf/T/sdv_ivi_cf-img-user.zip'},
          boot: {extra_bootconfig_args: ivi.extraBootconfigArgs},
        },
      ],
    });

    component.onJsonApply({success: true, config: pair('gs://b/sdv_core_cf/T/')});
    expect(component.hostPackageRequired()).toBe(false);
    expect(component.isValid()).toBe(true);

    component.onJsonApply({
      success: true,
      config: pair('gs://b/sdv_core_cf/T/sdv_core_cf-img-user.zip'),
    });
    expect(component.hostPackageRequired()).toBe(true);
    expect(component.isValid()).toBe(false);
  });

  it('turns the switch off, so Create waits for both builds', async () => {
    const {component} = await render(true, seedWith(['a'], {sameBuild: true, deviceBuild: 'b-1'}));

    component.startFromSdvPair();

    expect(component.buildChoice()).toEqual({kind: 'default'});
    expect(component.instances().map(inst => inst.build)).toEqual(['', '']);
    expect(component.isValid()).toBe(false);
  });

  it('keeps the current instances when the confirmation is declined', async () => {
    const {component} = await render(false, seedWithDisplays([...PHONE_DISPLAYS]));

    component.startFromSdvPair();

    expect(component.instances().map(i => i.name)).toEqual(['device-1']);
  });

  it('requires a host package for a URL-built pair with no registered build', async () => {
    const empty = hostWith(0);
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]), [empty]);
    component.startFromSdvPair();
    const [core, ivi] = component.instances();
    const urlPair = (hostPackage: string, iviBuild?: string): EnvironmentSpecification => ({
      instances: [
        {
          name: core.name,
          disk: {default_build: 'gs://b/sdv_core_cf/T/sdv_core_cf-img-user.zip'},
          boot: {extra_bootconfig_args: core.extraBootconfigArgs},
        },
        {
          name: ivi.name,
          ...(iviBuild
            ? {'@build': iviBuild}
            : {disk: {default_build: 'gs://b/sdv_ivi_cf/T/sdv_ivi_cf-img-user.zip'}}),
          boot: {extra_bootconfig_args: ivi.extraBootconfigArgs},
        },
      ],
      ...(hostPackage && {common: {host_package: hostPackage}}),
    });
    component.selectHost(empty);

    component.onJsonApply({success: true, config: urlPair('')});
    expect(component.hostPackageRequired()).toBe(true);
    expect(component.isValid()).toBe(false);

    component.onJsonApply({
      success: true,
      config: urlPair('gs://b/sdv_ivi_cf/T/cvd-host_package.tar.gz'),
    });
    expect(component.isValid()).toBe(true);

    // A registered instance build supplies the host package itself.
    component.onJsonApply({success: true, config: urlPair('', 'b-ivi')});
    expect(component.hostPackageRequired()).toBe(false);
  });

  it('keeps the host package optional outside an SDV pair', async () => {
    const empty = hostWith(0);
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]), [empty]);
    component.selectHost(empty);

    component.onJsonApply({success: true, config: OWN_BUILDS});

    expect(component.hostPackageRequired()).toBe(false);
    expect(component.isValid()).toBe(true);
  });

  it('warns when the chosen host already runs devices, and only then', async () => {
    const busy = hostWith(2);
    const empty = hostWith(0);
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]), [busy, empty]);
    component.startFromSdvPair();

    component.selectHost(busy);
    expect(component.sdvWarning()).toContain('already runs 2 devices');

    component.selectHost(empty);
    expect(component.sdvWarning()).toBe('');

    // Automatic placement provisions a dedicated host for a multi-instance
    // device, so there is nothing to warn about.
    component.selectHost(busy);
    component.setHostKind('auto');
    expect(component.sdvWarning()).toBe('');
  });
});

describe('DeviceCreateAdvancedComponent capacity', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it('starts on auto and submits the choice', async () => {
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]));
    expect(created(component)[0].capacity).toBe('auto');

    component.setCapacity('spot');

    expect(created(component)[0].capacity).toBe('spot');
  });

  it('keeps the choice across a JSON apply, which carries no capacity', async () => {
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]));
    component.setCapacity('standard');

    component.onJsonApply({success: true, config: OWN_BUILDS});

    expect(component.capacity()).toBe('standard');
  });

  it('restores the choice a stored draft carries', async () => {
    const {component} = await render(true, seedWithDisplays([...PHONE_DISPLAYS]));
    saveDraft(JSON.stringify({instances: [{name: 'from-storage'}]}), '', '', true, 'standard');

    component.restoreDraft();

    expect(component.capacity()).toBe('standard');
  });

  it('shows the choice only under automatic placement', async () => {
    const {fixture, component} = await renderWithBrowser(undefined);
    const field = (): Element | null => fixture.nativeElement.querySelector('app-capacity-field');
    expect(field()).not.toBeNull();

    component.setHostKind('host');
    fixture.detectChanges();

    expect(field()).toBeNull();
  });
});

interface RenderedWithBrowser extends Rendered {
  fixture: ComponentFixture<DeviceCreateAdvancedComponent>;
}

/** Renders with a storage browser that answers every Browse with `pick`. */
async function renderWithBrowser(
  pick: {gsUri: string} | undefined,
  seed: AdvancedSeed = seedWithDisplays([...PHONE_DISPLAYS])
): Promise<RenderedWithBrowser> {
  const dialog: {open: Mock} = {open: vi.fn(() => ({afterClosed: () => of(pick)}))};
  TestBed.resetTestingModule();
  await TestBed.configureTestingModule({
    imports: [DeviceCreateAdvancedComponent],
    providers: [
      provideZonelessChangeDetection(),
      provideHttpClient(),
      provideHttpClientTesting(),
      {provide: MatDialog, useValue: dialog},
    ],
  }).compileComponents();
  const fixture = TestBed.createComponent(DeviceCreateAdvancedComponent);
  fixture.componentRef.setInput('seed', seed);
  fixture.detectChanges();
  return {fixture, component: fixture.componentInstance, dialog};
}

describe('DeviceCreateAdvancedComponent Browse', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  function browseButtons(root: HTMLElement): HTMLButtonElement[] {
    return Array.from(root.querySelectorAll<HTMLButtonElement>('button.browse-button'));
  }

  it('fills the shared build with a picked folder', async () => {
    const {fixture, component, dialog} = await renderWithBrowser({gsUri: 'gs://b/sdv/core/'});
    const [build, hostTools] = browseButtons(fixture.nativeElement);
    expect(hostTools).toBeDefined();

    build.click();
    fixture.detectChanges();

    expect(dialog.open).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({data: expect.objectContaining({selectMode: 'folder'})})
    );
    expect(component.instanceCards()[0].build).toBe('core');
    const [event] = created(component);
    expect(event.config.instances[0].disk).toEqual({default_build: 'gs://b/sdv/core/'});
  });

  it('fills an instance row and opens where the other builds live', async () => {
    const {fixture, component, dialog} = await renderWithBrowser(
      {gsUri: 'gs://b/sdv/ivi/sdv_ivi_cf-img-user.zip'},
      seedWith(['core', 'ivi'], {sameBuild: false, rows: ['gs://b/sdv/core/', '']})
    );
    const [, iviRow] = browseButtons(fixture.nativeElement);

    iviRow.click();

    expect(dialog.open).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({
        data: expect.objectContaining({initialBucket: 'b', initialPath: 'sdv/'}),
      })
    );
    expect(component.instances().map(inst => inst.build)).toEqual([
      'gs://b/sdv/core/',
      'gs://b/sdv/ivi/sdv_ivi_cf-img-user.zip',
    ]);
  });

  it('picks host tools as a file and leaves the field alone on cancel', async () => {
    const {fixture, component, dialog} = await renderWithBrowser({
      gsUri: 'gs://b/sdv/ivi/cvd-host_package.tar.gz',
    });
    const hostTools = browseButtons(fixture.nativeElement).at(-1);

    hostTools?.click();

    expect(dialog.open).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({data: expect.objectContaining({selectMode: 'file'})})
    );
    const preview = JSON.parse(component.configJson()) as Record<string, unknown>;
    expect(preview['common']).toEqual({host_package: 'gs://b/sdv/ivi/cvd-host_package.tar.gz'});

    dialog.open.mockReturnValue({afterClosed: () => of(undefined)});
    hostTools?.click();
    expect(JSON.parse(component.configJson())).toHaveProperty('common');
  });
});
