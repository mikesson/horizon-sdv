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
 * The AAOS SDV Core + IVI pair: two instances of one CVD group, each booting
 * its own image.
 */

import type {Host} from '../../../hosts/models/host.model';
import type {AdvancedDraft, InstanceDraft} from './instance-draft.model';
import {instanceDraftFromTemplate, isGcsFolder, isRegisteredBuildId} from './instance-draft.model';

/** Every SDV guest reads its identity from bootconfig keys under this prefix. */
const SDV_BOOTCONFIG_PREFIX = 'androidboot.sdv.';

/**
 * Both instances state gpu_mode and displays outright. Each SDV host package
 * carries only its own cvd presets, and cvd broadcasts a preset value found for
 * one instance to every instance, so a field left unset on the Core can arrive
 * from the IVI's preset. A Core that inherited the IVI's two displays hung
 * before its kernel started.
 */
function sdvInstance(
  name: string,
  overrides: Pick<InstanceDraft, 'cpus' | 'memoryMb' | 'useSdcard' | 'displays'>,
  bootconfig: string
): InstanceDraft {
  return {
    ...instanceDraftFromTemplate('auto', name),
    ...overrides,
    gpuMode: 'swiftshader',
    extraBootconfigArgs: bootconfig,
  };
}

/**
 * The pair as proven on a live host. Each instance takes its own build, and
 * the builds and the host package are left for the user: the images are
 * per-branch artifacts no default can name. The host is on-demand, since a
 * Spot preemption kills both guests mid-session.
 */
export function sdvPairDraft(): AdvancedDraft {
  return {
    instances: [
      sdvInstance(
        'core',
        {
          cpus: 2,
          memoryMb: 2096,
          useSdcard: false,
          displays: [{width: 720, height: 480, dpi: 160, refreshRate: 60}],
        },
        'androidboot.sdv.instance_name=instance1 androidboot.virt.address=3 androidboot.sdv.boot_mode=unlocked'
      ),
      sdvInstance(
        'ivi',
        {
          cpus: 4,
          memoryMb: 4096,
          useSdcard: true,
          displays: [
            {width: 1080, height: 600, dpi: 120, refreshRate: 60},
            {width: 400, height: 600, dpi: 120, refreshRate: 60},
          ],
        },
        'androidboot.sdv.instance_name=instance2 androidboot.virt.address=4 androidboot.sdv.boot_mode=unlocked'
      ),
    ],
    sameBuild: false,
    deviceBuild: '',
    hostPackage: '',
    netsimBt: false,
    netsimUwb: false,
    capacity: 'standard',
  };
}

/** Which SDV image an instance boots, read from the instance name its bootconfig sets. */
export function sdvBuildHint(inst: InstanceDraft): string {
  if (inst.extraBootconfigArgs.includes(`${SDV_BOOTCONFIG_PREFIX}instance_name=instance1`)) {
    return 'Core image zip or build folder, for example sdv_core_cf-img-user.zip';
  }
  if (inst.extraBootconfigArgs.includes(`${SDV_BOOTCONFIG_PREFIX}instance_name=instance2`)) {
    return 'IVI image zip or build folder, for example sdv_ivi_cf-img-user.zip';
  }
  return '';
}

/** Read from the bootconfig, so a pasted or restored SDV config counts too. */
export function isSdvDraft(draft: AdvancedDraft): boolean {
  return draft.instances.some(inst => inst.extraBootconfigArgs.includes(SDV_BOOTCONFIG_PREFIX));
}

/**
 * Why a chosen host cannot take this SDV group, or '' when it can. SDV identity
 * and addresses come from the bootconfig, so a second pair or any other device
 * on the same bridge collides. Automatic placement needs no check: it
 * provisions a dedicated host for every multi-instance device.
 */
export function sdvHostWarning(
  draft: AdvancedDraft,
  host: Pick<Host, 'currentDevices'> | undefined
): string {
  if (!host || host.currentDevices === 0 || !isSdvDraft(draft)) {
    return '';
  }
  const running = host.currentDevices === 1 ? 'one device' : `${host.currentDevices} devices`;
  return (
    `This host already runs ${running}. An SDV pair takes its identity and addresses ` +
    'from the bootconfig, so it needs a host to itself. Choose an empty host or automatic placement.'
  );
}

/**
 * Whether this SDV draft must name its host package. The server takes the host
 * package from the user, else the device build, else a registered instance
 * build that has one. cvd also finds one inside a gs:// build folder, but only
 * the first instance's. An image zip URL carries no host tools, so a pair
 * with none of those fails at cvd load. The platform default does not count,
 * since the editor cannot see what it resolves to.
 */
export function sdvNeedsHostPackage(draft: AdvancedDraft): boolean {
  if (!isSdvDraft(draft)) {
    return false;
  }
  const registered = draft.sameBuild
    ? isRegisteredBuildId(draft.deviceBuild)
    : draft.instances.some(inst => isRegisteredBuildId(inst.build));
  return !(registered || isGcsFolder(firstInstanceBuild(draft)));
}

/** The build value instance 0 boots, '' when it has none. */
export function firstInstanceBuild(draft: AdvancedDraft): string {
  return (draft.sameBuild ? draft.deviceBuild : (draft.instances[0]?.build ?? '')).trim();
}
