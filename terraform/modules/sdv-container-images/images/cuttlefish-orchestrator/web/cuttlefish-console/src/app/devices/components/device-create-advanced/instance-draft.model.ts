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

import type {Build} from '../../../builds/models/build.model';
import type {CapacityModel} from '../../../shared/models/requirements.model';
import type {
  GpuMode,
  Instance,
  SetupWizardMode,
  TemplateName,
  VhostUserVsock,
} from '../../models/cvd-config.schema';
import {TEMPLATE_DEFAULTS} from '../../models/cvd-config.schema';

/** The wire display, whose every field is optional. */
type WireDisplay = NonNullable<NonNullable<Instance['graphics']>['displays']>[number];

/** Per-instance ceiling the backend enforces; a fifth display is a 400. */
export const MAX_DISPLAYS = 4;

/** One screen of an instance, with every wire-optional field resolved. */
export interface DisplayDraft {
  width: number;
  height: number;
  dpi: number;
  refreshRate: number;
}

/**
 * Flat editable state for one CVD instance in the advanced editor.
 * The nested EnvironmentSpecification wire shape is derived from this via
 * the mapper; only fields the editor exposes are represented.
 */
export interface InstanceDraft {
  /** Client-side identity for list rendering and selection; never serialized. */
  id: string;
  template: TemplateName;
  name: string;
  /**
   * This instance's own build picker value: a registered build id, an image
   * URL or `@ab/` reference, or '' while nothing is picked. Read only while
   * `AdvancedDraft.sameBuild` is off.
   */
  build: string;
  cpus: number;
  memoryMb: number;
  useSdcard: boolean;
  displays: DisplayDraft[];
  gpuMode: GpuMode;
  recordScreen: boolean;
  enableBootAnimation: boolean;
  extraBootconfigArgs: string;
  serialNumber: string;
  useRandomSerial: boolean;
  guestEnforceSecurity: boolean;
  vsockGuestGroup: string;
  /**
   * Whether the guest-to-guest vsock layer exists. `auto` is the upstream
   * default and resolves off on x86_64, which makes it the switch that decides
   * whether `vsockGuestGroup` above means anything at all.
   */
  vhostUserVsock: VhostUserVsock;
  streamingDeviceId: string;
  setupWizardMode: SetupWizardMode;
  uuid: string;
  crosvmSandbox: boolean;
}

/** Whole advanced-editor state: the instance list plus group-wide settings. */
export interface AdvancedDraft {
  instances: InstanceDraft[];
  /** One build for the whole device, or a build per instance. */
  sameBuild: boolean;
  /**
   * The whole device's build picker value while `sameBuild` is on. A
   * registered id travels as the request's build_id, a URL as every
   * instance's disk.default_build, and '' leaves it to the platform default.
   */
  deviceBuild: string;
  /** `common.host_package`; empty leaves it to the builds. */
  hostPackage: string;
  netsimBt: boolean;
  netsimUwb: boolean;
  /**
   * `requirements.capacity` for a host that automatic placement provisions;
   * a chosen host ignores it.
   */
  capacity: CapacityModel;
}

/** cvd refuses cleartext http://, and the server refuses local paths. */
const REMOTE_URL_PATTERN = /^(gs|https):\/\/\S+$/;

/** An Android Build reference such as `@ab/12345678/aosp_cf_x86_64-userdebug`. */
const ANDROID_BUILD_PATTERN = /^@ab\/\S+$/;

/**
 * An instance's own image source: an image zip, a gs:// directory, or an
 * Android Build reference, all of which cvd fetches from disk.default_build.
 */
export function isBuildUrl(value: string): boolean {
  const trimmed = value.trim();
  return REMOTE_URL_PATTERN.test(trimmed) || ANDROID_BUILD_PATTERN.test(trimmed);
}

/**
 * A gs:// build folder, which ends in a slash. cvd finds the image zip and the
 * host package inside it.
 */
export function isGcsFolder(value: string): boolean {
  return /^gs:\/\/\S+\/$/.test(value.trim());
}

/** A host package archive, which the group field takes only as a gs:// or https:// URL. */
export function isHostPackageUrl(value: string): boolean {
  return REMOTE_URL_PATTERN.test(value.trim());
}

/**
 * Whether a picker value names a registered build by id. URLs and `@ab/`
 * references always carry one of these characters, and build ids never do.
 */
export function isRegisteredBuildId(value: string): boolean {
  const trimmed = value.trim();
  return trimmed !== '' && !/[:/@]/.test(trimmed);
}

/** Why a picker value cannot be sent, or null. An empty value is the caller's rule. */
export function buildValueError(
  value: string,
  catalog: readonly Build[] | null
): {kind: string; message: string} | null {
  const trimmed = value.trim();
  if (trimmed === '') {
    return null;
  }
  if (!isRegisteredBuildId(trimmed)) {
    return isBuildUrl(trimmed)
      ? null
      : {kind: 'url', message: 'Enter a gs:// or https:// URL, or an @ab/ build'};
  }
  // Only a fully loaded catalog can say a build is missing. The server
  // refuses an unregistered id, so a restored draft naming a deleted build
  // must not reach it.
  if (catalog === null || catalog.some(build => build.id === trimmed)) {
    return null;
  }
  return {
    kind: 'unregistered',
    message: 'Pick a build from the list, or paste a gs://, https:// or @ab/ build',
  };
}

/** How a picker value reads: a registered build's name, else the value itself. */
export function buildValueLabel(value: string, builds: readonly Build[]): string {
  const trimmed = value.trim();
  if (isRegisteredBuildId(trimmed)) {
    return builds.find(build => build.id === trimmed)?.name ?? trimmed;
  }
  return trimmed;
}

/**
 * The build line on an instance card. `automatic` says whether an empty
 * device build means the platform default, which only automatic placement
 * resolves.
 */
export function instanceBuildLabel(
  draft: AdvancedDraft,
  inst: InstanceDraft,
  builds: readonly Build[],
  automatic: boolean
): string {
  const value = (draft.sameBuild ? draft.deviceBuild : inst.build).trim();
  if (value === '') {
    return draft.sameBuild && automatic ? 'Platform default' : 'No build picked';
  }
  if (!isRegisteredBuildId(value)) {
    return value.split('/').filter(Boolean).at(-1) ?? value;
  }
  const name = buildValueLabel(value, builds);
  return draft.sameBuild ? `Device build: ${name}` : name;
}

/** The screen an instance gets when neither its template nor its spec names one. */
export function defaultDisplay(): DisplayDraft {
  return {width: 720, height: 1280, dpi: 320, refreshRate: 60};
}

/** Wire displays as editable screens; an absent or empty list yields one default screen. */
export function displayDraftsFrom(displays: readonly WireDisplay[] | undefined): DisplayDraft[] {
  if (displays === undefined || displays.length === 0) {
    return [defaultDisplay()];
  }
  const fallback = defaultDisplay();
  return displays.map(display => ({
    width: display.width ?? fallback.width,
    height: display.height ?? fallback.height,
    dpi: display.dpi ?? fallback.dpi,
    refreshRate: display.refresh_rate_hertz ?? fallback.refreshRate,
  }));
}

function templateDisplays(template: TemplateName): DisplayDraft[] {
  return displayDraftsFrom(TEMPLATE_DEFAULTS[template]?.graphics?.displays);
}

/** A fresh instance draft seeded with the template's VM/display defaults. */
export function instanceDraftFromTemplate(template: TemplateName, name: string): InstanceDraft {
  const defaults = TEMPLATE_DEFAULTS[template];
  return {
    id: crypto.randomUUID(),
    template,
    name,
    build: '',
    cpus: 4,
    memoryMb: defaults?.vm?.memory_mb ?? 4096,
    useSdcard: defaults?.vm?.use_sdcard ?? true,
    displays: templateDisplays(template),
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
  };
}

/**
 * Switches an instance's template, re-applying only the template-scoped
 * defaults (memory, SD card, the whole screen list) and leaving user edits to
 * everything else intact. The screen list is replaced wholesale, so callers
 * confirm first whenever `templateSwitchDiscardsDisplayEdits` says so.
 */
export function applyTemplateDefaults(inst: InstanceDraft, template: TemplateName): InstanceDraft {
  const defaults = TEMPLATE_DEFAULTS[template];
  const displays = defaults?.graphics?.displays;
  return {
    ...inst,
    template,
    ...(defaults?.vm && {
      memoryMb: defaults.vm.memory_mb ?? 4096,
      useSdcard: defaults.vm.use_sdcard ?? true,
    }),
    ...(displays &&
      displays.length > 0 && {
        displays: displayDraftsFrom(displays),
      }),
  };
}

/**
 * Whether switching to `template` would throw away screens the user edited.
 * True once the instance's screens no longer match the ones its current
 * template seeds. Gaining a screen nobody touched asks nothing.
 */
export function templateSwitchDiscardsDisplayEdits(
  inst: InstanceDraft,
  template: TemplateName
): boolean {
  const incoming = TEMPLATE_DEFAULTS[template]?.graphics?.displays;
  if (incoming === undefined || incoming.length === 0) {
    return false;
  }
  return !sameDisplays(inst.displays, templateDisplays(inst.template));
}

function sameDisplays(a: readonly DisplayDraft[], b: readonly DisplayDraft[]): boolean {
  return (
    a.length === b.length &&
    a.every(
      (display, i) =>
        display.width === b[i].width &&
        display.height === b[i].height &&
        display.dpi === b[i].dpi &&
        display.refreshRate === b[i].refreshRate
    )
  );
}
