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

import type {GcsBrowserDialogData} from '../../../storage/models/storage.model';

const GS_URL_PATTERN = /^gs:\/\/([^/\s]+)\/(\S*)$/;

/**
 * Where the storage browser opens: the folder that lists the first gs:// value
 * given, so a picked object shows up again beside its siblings. With no gs://
 * value the browser starts at its bucket list.
 */
export function browseStart(
  values: readonly string[]
): Pick<GcsBrowserDialogData, 'initialBucket' | 'initialPath'> {
  for (const value of values) {
    const match = GS_URL_PATTERN.exec(value.trim());
    if (match) {
      const path = match[2].replace(/\/$/, '');
      return {initialBucket: match[1], initialPath: path.slice(0, path.lastIndexOf('/') + 1)};
    }
  }
  return {};
}
