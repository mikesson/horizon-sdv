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

import {pattern, required, schema} from '@angular/forms/signals';
import type {Build} from '../../../builds/models/build.model';
import {DEFAULT_TRACK_BRANCH, DEFAULT_TRACK_TARGET} from '../../../shared/constants';
import {COMPILED_LIFETIMES} from '../../../shared/models/platform-lifetimes.schema';
import type {PoolPin} from '../../../shared/models/pool-placement';
import type {
  BuildRequirement,
  CapacityModel,
  GuestArchitecture,
  Interaction,
} from '../../../shared/models/requirements.model';
import {generateResourceName, RESOURCE_NAME_PATTERN} from '../../../shared/utils/resource-name';
import type {CreateDeviceRequest, PinReason} from '../../models/device.model';

/**
 * Which Android build to run: the platform default (auto-registered as a
 * latest-tracking build when the registry is empty), a specific registered
 * build, or a tracked AOSP branch/target (the newest build resolved at every
 * launch). The `track` branch/target live as flat CreateIntent fields
 * (trackBranch/trackTarget) so signal forms can bind and validate them
 * directly. The union carries only the discriminator.
 */
export type BuildChoice = {kind: 'default'} | {kind: 'registered'; id: string} | {kind: 'track'};

/** Where the device runs: server-side placement or a user-chosen host. */
export type HostChoice = {kind: 'auto'} | {kind: 'host'; id: string};

/**
 * The create intent the Simple interview edits and the Advanced seed is built
 * from. The container owns one instance so moving into Advanced and detours
 * never lose state.
 */
export interface CreateIntent {
  name: string;
  build: BuildChoice;
  /** Branch/target for build.kind === 'track' (flat for signal-forms binding). */
  trackBranch: string;
  trackTarget: string;
  usage: Interaction;
  /** Hard constraint: fail the create rather than degrade to software rendering. */
  gpuRequired: boolean;
  /** Architecture of the guest; arm64 is served by the arm pool alone. */
  architecture: GuestArchitecture;
  /**
   * The pool to pin the host to; 'auto' lets the server derive one from the
   * needs above (architecture, then a hard GPU requirement, then the default).
   */
  pool: PoolPin;
  host: HostChoice;
  /**
   * Preferred GCE region for automatic placement ('' = capacity-ranked). A
   * preference, never a constraint; ignored for a chosen host.
   */
  preferredRegion: string;
  /**
   * Provisioning economics for an automatically provisioned host; 'auto'
   * (the default) lets operator policy decide by usage. Ignored for a chosen
   * host.
   */
  capacity: CapacityModel;
  ttlHours: number;
  pinned: boolean;
  pinnedReason: PinReason | '';
  pinnedReasonDetail: string;
  /** ISO local datetime ('' = pinned indefinitely); only read when pinned. */
  pinnedUntil: string;
  tags: string[];
}

/** The device name rules, shared by the Simple interview and the Advanced editor. */
export const deviceNameSchema = schema<string>(name => {
  required(name, {message: 'Name is required'});
  pattern(name, RESOURCE_NAME_PATTERN, {
    message: 'Must start with a lowercase letter; lowercase letters, numbers, and hyphens only',
  });
});

export function defaultCreateIntent(): CreateIntent {
  return {
    name: generateResourceName('device'),
    build: {kind: 'default'},
    trackBranch: DEFAULT_TRACK_BRANCH,
    trackTarget: DEFAULT_TRACK_TARGET,
    usage: 'interactive',
    gpuRequired: false,
    architecture: 'x86',
    pool: 'auto',
    host: {kind: 'auto'},
    preferredRegion: '',
    capacity: 'auto',
    ttlHours: COMPILED_LIFETIMES.deviceTtlHours,
    pinned: false,
    pinnedReason: '',
    pinnedReasonDetail: '',
    pinnedUntil: '',
    tags: [],
  };
}

/**
 * Maps the intent to the wire request. An automatic host produces a
 * declarative request (requirements; the platform resolves build and
 * placement); a chosen host produces an imperative one. gpu_mode is always
 * left to the backend default (auto: rendering matches the host).
 *
 * A tracked branch reaches an imperative create as buildId=branch +
 * buildTarget=target: an unregistered build string resolves to
 * @ab/{branch}/{target} (latest on branch), the same result the declarative
 * track requirement produces.
 */
export function toCreateDeviceRequest(
  intent: CreateIntent,
  build: Build | null
): CreateDeviceRequest {
  const lifecycle = {
    ttlHours: intent.ttlHours,
    pinned: intent.pinned,
    pinnedReason: intent.pinnedReason || undefined,
    pinnedReasonDetail: intent.pinnedReasonDetail || undefined,
    // The API rejects an expiry without a pin, so an expiry left behind by a
    // since-unpinned draft must not travel.
    pinnedUntil: intent.pinned && intent.pinnedUntil ? new Date(intent.pinnedUntil) : undefined,
    tags: intent.tags.length > 0 ? intent.tags : undefined,
  };

  if (intent.host.kind === 'host') {
    // The effective build is a tracked branch, the registered pick, or the
    // platform default the caller resolved. An imperative create cannot
    // express 'default' directly.
    const imperativeBuild =
      intent.build.kind === 'track'
        ? {buildId: intent.trackBranch, buildTarget: intent.trackTarget}
        : {buildId: build?.id, buildTarget: build?.target};
    return {
      hostId: intent.host.id,
      name: intent.name,
      ...imperativeBuild,
      ...lifecycle,
    };
  }

  return {
    name: intent.name,
    requirements: {
      interaction: intent.usage,
      gpuRequired: intent.gpuRequired || undefined,
      build: toBuildRequirement(intent),
      preferredRegion: intent.preferredRegion || undefined,
      capacity: intent.capacity !== 'auto' ? intent.capacity : undefined,
      // x86 and an unpinned pool are what the server assumes, so neither
      // travels. An omitted field and its default mean the same thing there.
      architecture: intent.architecture === 'arm' ? 'arm' : undefined,
      pool: intent.pool !== 'auto' ? intent.pool : undefined,
    },
    ...lifecycle,
  };
}

/** The declarative build requirement for an automatic-host create; undefined
 * (the platform default) unless the interview named a registered or tracked
 * build. */
function toBuildRequirement(intent: CreateIntent): BuildRequirement | undefined {
  const build = intent.build;
  if (build.kind === 'registered') {
    return {kind: 'registered', id: build.id};
  }
  if (build.kind === 'track') {
    return {kind: 'track', branch: intent.trackBranch, target: intent.trackTarget};
  }
  return undefined;
}
