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

/** Pure mappers from the interview intent and route handoffs to the Advanced seed. */

import type {AdvancedDraft} from '../device-create-advanced/instance-draft.model';
import {instanceDraftFromTemplate} from '../device-create-advanced/instance-draft.model';
import {sdvPairDraft} from '../device-create-advanced/sdv-preset';
import type {LifecycleDraftFields} from '../device-create-wizard/wizard-lifecycle-step.component';
import type {BuildChoice, CreateIntent, HostChoice} from './create-intent.model';

/**
 * The Advanced editor's starting state. Each part is applied when present; an
 * absent part keeps the editor's own default.
 */
export interface AdvancedSeed {
  /** The device name; the editor generates one when absent. */
  name?: string;
  draft?: AdvancedDraft;
  hostChoice?: HostChoice;
  buildChoice?: BuildChoice;
  lifecycle?: LifecycleDraftFields;
}

/**
 * Simple→Advanced eject: the intent's device name, one phone instance named
 * after it with the usage-derived rendering (interactive→gfxstream,
 * headless→swiftshader), and the build/host/lifecycle answers carried over
 * losslessly.
 */
export function intentToAdvancedSeed(intent: CreateIntent): Required<AdvancedSeed> {
  const instance = instanceDraftFromTemplate('phone', intent.name);
  return {
    name: intent.name,
    draft: {
      instances: [
        {...instance, gpuMode: intent.usage === 'interactive' ? 'gfxstream' : 'swiftshader'},
      ],
      sameBuild: true,
      deviceBuild: '',
      hostPackage: '',
      netsimBt: false,
      netsimUwb: false,
      capacity: intent.capacity,
    },
    hostChoice: intent.host,
    // Advanced has no tracking picker (its authors builds via the picker or
    // raw env_config), so a tracked branch carries over as the default.
    buildChoice: intent.build.kind === 'track' ? {kind: 'default'} : intent.build,
    lifecycle: {
      ttlHours: intent.ttlHours,
      pinned: intent.pinned,
      pinnedReason: intent.pinnedReason,
      pinnedReasonDetail: intent.pinnedReasonDetail,
      pinnedUntil: intent.pinnedUntil,
      tags: intent.tags,
    },
  };
}

/**
 * Direct entry into Advanced: the host and build a handoff link names, and the
 * SDV pair when the chooser's SDV card asked for it. Null when there is
 * nothing to apply.
 */
export function handoffToAdvancedSeed(handoff: {
  hostId?: string;
  buildId?: string;
  sdvPair: boolean;
}): AdvancedSeed | null {
  const seed: AdvancedSeed = {};
  if (handoff.sdvPair) {
    seed.draft = sdvPairDraft();
  }
  if (handoff.hostId) {
    seed.hostChoice = {kind: 'host', id: handoff.hostId};
  }
  if (handoff.buildId) {
    seed.buildChoice = {kind: 'registered', id: handoff.buildId};
  }
  return Object.keys(seed).length > 0 ? seed : null;
}
