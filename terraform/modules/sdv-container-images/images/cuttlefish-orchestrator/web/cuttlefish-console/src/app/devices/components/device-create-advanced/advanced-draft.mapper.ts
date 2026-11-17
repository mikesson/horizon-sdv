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
 * Pure mapping between the advanced editor's draft model and the CVD
 * EnvironmentSpecification wire shape. Single source for the conversion the
 * component previously duplicated across JSON apply, file import, and submit.
 */

import type {CapacityModel} from '../../../shared/models/requirements.model';
import type {
  EnvironmentSpecification,
  GpuMode,
  Instance,
  WireGpuMode,
} from '../../models/cvd-config.schema';
import type {AdvancedDraft, InstanceDraft} from './instance-draft.model';
import {displayDraftsFrom, isBuildUrl, isRegisteredBuildId} from './instance-draft.model';

/** `build` is the picker value this instance boots, '' for none of its own. */
export function instanceToSpec(draft: InstanceDraft, build = draft.build): Instance {
  return {
    '@import': draft.template,
    name: draft.name || 'device',
    ...instanceBuildToSpec(build),
    vm: {
      cpus: draft.cpus,
      memory_mb: draft.memoryMb,
      use_sdcard: draft.useSdcard,
      ...(draft.setupWizardMode !== 'DISABLED' && {setupwizard_mode: draft.setupWizardMode}),
      ...(draft.uuid && {uuid: draft.uuid}),
      // `auto` is upstream's own default, so sending it would only pin a value
      // that is already in force. Pinning it here would also outlive a change
      // to what the default means.
      ...((draft.crosvmSandbox || draft.vhostUserVsock !== 'auto') && {
        crosvm: {
          ...(draft.crosvmSandbox && {enable_sandbox: true}),
          ...(draft.vhostUserVsock !== 'auto' && {vhost_user_vsock: draft.vhostUserVsock}),
        },
      }),
    },
    graphics: {
      displays: draft.displays.map(display => ({
        width: display.width,
        height: display.height,
        dpi: display.dpi,
        refresh_rate_hertz: display.refreshRate,
      })),
      gpu_mode: draft.gpuMode,
      ...(draft.recordScreen && {record_screen: true}),
    },
    ...((draft.enableBootAnimation === false || draft.extraBootconfigArgs) && {
      boot: {
        enable_bootanimation: draft.enableBootAnimation,
        ...(draft.extraBootconfigArgs && {extra_bootconfig_args: draft.extraBootconfigArgs}),
      },
    }),
    ...((draft.serialNumber || draft.useRandomSerial || draft.guestEnforceSecurity) && {
      security: {
        ...(draft.serialNumber && {serial_number: draft.serialNumber}),
        ...(draft.useRandomSerial && {use_random_serial: true}),
        ...(draft.guestEnforceSecurity && {guest_enforce_security: true}),
      },
    }),
    ...(draft.vsockGuestGroup && {
      connectivity: {vsock: {guest_group: draft.vsockGuestGroup}},
    }),
    ...(draft.streamingDeviceId && {
      streaming: {device_id: draft.streamingDeviceId},
    }),
  };
}

function instanceBuildToSpec(build: string): Pick<Instance, '@build' | 'disk'> {
  const value = build.trim();
  if (value === '') {
    return {};
  }
  return isRegisteredBuildId(value) ? {'@build': value} : {disk: {default_build: value}};
}

/**
 * The config exactly as submitted, which is also what the preview shows and
 * the draft stores. A registered device build travels beside it as build_id
 * and is not merged in: the server fills the gaps it leaves. A device build
 * given as a URL has no build_id form, so it goes on every instance.
 */
export function toSpec(draft: AdvancedDraft): EnvironmentSpecification {
  const hostPackage = draft.hostPackage.trim();
  const shared = isRegisteredBuildId(draft.deviceBuild) ? '' : draft.deviceBuild;
  return {
    instances: draft.instances.map(inst =>
      instanceToSpec(inst, draft.sameBuild ? shared : inst.build)
    ),
    ...(hostPackage && {common: {host_package: hostPackage}}),
    ...(draft.netsimBt && {netsim_bt: true}),
    ...(draft.netsimUwb && {netsim_uwb: true}),
  };
}

/**
 * What travels beside a spec: the build switch, the registered device build
 * and the capacity choice, none of which the spec can carry.
 */
interface DeviceBuildHint {
  sameBuild?: boolean;
  buildId?: string;
  capacity?: CapacityModel;
}

/**
 * Rebuilds the draft model from a spec (JSON apply, file import, draft
 * restore). Fields the editor does not expose (disk fields other than
 * default_build, common.group_name, fetch, …) are dropped; unnamed instances
 * get generated names unique within the spec.
 *
 * Instances that carry their own builds turn the build switch off, and those
 * that do not are left empty for the user to fill. With no instance builds the
 * switch is on and `hint.buildId` is the device build. One URL on every
 * instance reads back as a shared build only when the hint says the switch
 * was on, since that is what a shared URL emits.
 */
export function fromSpec(
  spec: EnvironmentSpecification,
  hint: DeviceBuildHint = {}
): AdvancedDraft {
  const used = new Set<string>();
  const instances = spec.instances.map(inst => {
    const name = inst.name || uniqueInstanceName('device', used);
    used.add(name);
    return instanceFromSpec(inst, name);
  });
  const builds = instances.map(inst => inst.build);
  const first = builds[0] ?? '';
  let sameBuild = false;
  let deviceBuild = '';
  if (builds.every(build => build === '')) {
    sameBuild = hint.sameBuild ?? true;
    deviceBuild = sameBuild ? (hint.buildId ?? '') : '';
  } else if (hint.sameBuild && isBuildUrl(first) && builds.every(build => build === first)) {
    sameBuild = true;
    deviceBuild = first;
  }
  return {
    instances: sameBuild ? instances.map(inst => ({...inst, build: ''})) : instances,
    sameBuild,
    deviceBuild,
    hostPackage: spec.common?.host_package ?? '',
    netsimBt: spec.netsim_bt ?? false,
    netsimUwb: spec.netsim_uwb ?? false,
    capacity: hint.capacity ?? 'auto',
  };
}

function instanceFromSpec(inst: Instance, name: string): InstanceDraft {
  return {
    id: crypto.randomUUID(),
    template: inst['@import'] || 'phone',
    name,
    // A pasted config may name both keys, which the server refuses. `@build`
    // wins, so the draft holds one build and resubmits a config it accepts.
    build: inst['@build'] || inst.disk?.default_build || '',
    cpus: inst.vm?.cpus ?? 4,
    memoryMb: inst.vm?.memory_mb ?? 4096,
    useSdcard: inst.vm?.use_sdcard ?? true,
    displays: displayDraftsFrom(inst.graphics?.displays),
    gpuMode: consoleGpuMode(inst.graphics?.gpu_mode),
    recordScreen: inst.graphics?.record_screen ?? false,
    enableBootAnimation: inst.boot?.enable_bootanimation ?? true,
    extraBootconfigArgs: inst.boot?.extra_bootconfig_args ?? '',
    serialNumber: inst.security?.serial_number ?? '',
    useRandomSerial: inst.security?.use_random_serial ?? false,
    guestEnforceSecurity: inst.security?.guest_enforce_security ?? true,
    vsockGuestGroup: inst.connectivity?.vsock?.guest_group ?? '',
    vhostUserVsock: inst.vm?.crosvm?.vhost_user_vsock ?? 'auto',
    streamingDeviceId: inst.streaming?.device_id ?? '',
    setupWizardMode: inst.vm?.setupwizard_mode ?? 'DISABLED',
    uuid: inst.vm?.uuid ?? '',
    crosvmSandbox: inst.vm?.crosvm?.enable_sandbox ?? false,
  };
}

function consoleGpuMode(mode: WireGpuMode | undefined): GpuMode {
  if (mode === 'guest_swiftshader') {
    return 'swiftshader';
  }
  return mode ?? 'gfxstream';
}

/** First `base-N` (N from 1) not present in `existing`. */
export function uniqueInstanceName(base: string, existing: ReadonlySet<string>): string {
  let num = 1;
  let name = `${base}-${num}`;
  while (existing.has(name)) {
    num++;
    name = `${base}-${num}`;
  }
  return name;
}
