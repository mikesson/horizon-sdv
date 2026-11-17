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

/** localStorage persistence for the create intent (interview draft). */

import {z} from 'zod';
import {DEFAULT_TRACK_BRANCH, DEFAULT_TRACK_TARGET} from '../../../shared/constants';
import {POOL_NAMES} from '../../../shared/models/pool-placement';
import type {PinReason} from '../../models/device.model';
import type {CreateIntent} from './create-intent.model';

const IntentSchema = z.object({
  name: z.string(),
  build: z.union([
    z.object({kind: z.literal('default')}),
    z.object({kind: z.literal('registered'), id: z.string()}),
    z.object({kind: z.literal('track')}),
  ]),
  // Defaulted so a draft saved before branch tracking still loads.
  trackBranch: z.string().default(DEFAULT_TRACK_BRANCH),
  trackTarget: z.string().default(DEFAULT_TRACK_TARGET),
  usage: z.enum(['interactive', 'headless']),
  // Defaulted so a draft saved before the GPU constraint still loads.
  gpuRequired: z.boolean().default(false),
  // Defaulted so a draft saved before pool targeting still loads.
  architecture: z.enum(['x86', 'arm']).default('x86'),
  pool: z.enum(['auto', ...POOL_NAMES]).default('auto'),
  host: z.union([
    z.object({kind: z.literal('auto')}),
    z.object({kind: z.literal('host'), id: z.string()}),
  ]),
  // Defaulted so a draft saved before region preferences still loads.
  preferredRegion: z.string().default(''),
  // Defaulted so a draft saved before capacity intent still loads.
  capacity: z.enum(['auto', 'spot', 'standard']).default('auto'),
  ttlHours: z.number(),
  pinned: z.boolean(),
  pinnedReason: z.string(),
  pinnedReasonDetail: z.string(),
  // Defaulted so a draft saved before pin expiry still loads.
  pinnedUntil: z.string().default(''),
  tags: z.array(z.string()),
});

const IntentDraftSchema = z.object({
  intent: IntentSchema,
  savedAt: z.string(),
});

const INTENT_STORAGE_KEY = 'device-create-intent-draft';

/** Best-effort autosave; storage errors must never surface mid-edit. */
export function saveIntentDraft(intent: CreateIntent): void {
  try {
    localStorage.setItem(
      INTENT_STORAGE_KEY,
      JSON.stringify({intent, savedAt: new Date().toISOString()})
    );
  } catch {
    // Ignore save errors
  }
}

export function loadIntentDraft(): CreateIntent | null {
  try {
    const stored = localStorage.getItem(INTENT_STORAGE_KEY);
    if (!stored) {
      return null;
    }
    const result = IntentDraftSchema.safeParse(JSON.parse(stored));
    if (!result.success) {
      return null;
    }
    const intent = result.data.intent;
    return {...intent, pinnedReason: intent.pinnedReason as PinReason | ''};
  } catch {
    return null;
  }
}

export function clearIntentDraft(): void {
  localStorage.removeItem(INTENT_STORAGE_KEY);
}
