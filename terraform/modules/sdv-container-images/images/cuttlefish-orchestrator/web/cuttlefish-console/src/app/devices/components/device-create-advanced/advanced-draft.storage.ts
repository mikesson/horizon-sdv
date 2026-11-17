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

/** localStorage persistence for the advanced editor's unsaved-draft recovery. */

import {z} from 'zod';
import type {CapacityModel} from '../../../shared/models/requirements.model';
import {EnvironmentSpecificationSchema} from '../../models/cvd-config.schema';

const ConfigDraftSchema = z.object({
  config: EnvironmentSpecificationSchema,
  hostId: z.string(),
  buildId: z.string().optional(),
  /** Absent in drafts saved before the build switch; the restore then infers it. */
  sameBuild: z.boolean().optional(),
  /** Absent in drafts saved before the capacity choice; the restore then uses auto. */
  capacity: z.enum(['auto', 'spot', 'standard']).optional(),
  savedAt: z.string(),
});

export type ConfigDraft = z.infer<typeof ConfigDraftSchema>;

const DRAFT_STORAGE_KEY = 'cvd-advanced-draft';

/**
 * Persists the draft, silently dropping configs that are not a valid
 * EnvironmentSpecification and swallowing storage errors. Autosave is
 * best-effort and must never surface failures mid-edit.
 */
export function saveDraft(
  configJson: string,
  hostId: string,
  buildId: string,
  sameBuild: boolean,
  capacity: CapacityModel
): void {
  try {
    const parsed: unknown = JSON.parse(configJson);
    const result = EnvironmentSpecificationSchema.safeParse(parsed);
    if (!result.success) {
      return;
    }
    const draft: ConfigDraft = {
      config: result.data,
      hostId,
      buildId,
      sameBuild,
      capacity,
      savedAt: new Date().toISOString(),
    };
    localStorage.setItem(DRAFT_STORAGE_KEY, JSON.stringify(draft));
  } catch {
    // Ignore save errors
  }
}

export function loadDraft(): ConfigDraft | null {
  try {
    const stored = localStorage.getItem(DRAFT_STORAGE_KEY);
    if (!stored) {
      return null;
    }
    const parsed: unknown = JSON.parse(stored);
    const result = ConfigDraftSchema.safeParse(parsed);
    return result.success ? result.data : null;
  } catch {
    return null;
  }
}

export function clearDraft(): void {
  localStorage.removeItem(DRAFT_STORAGE_KEY);
}
