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

import type {User} from '../../auth/models/user.model';

/**
 * Warning line for destructive dialogs on a resource someone else created;
 * undefined for own resources or when the creator is unknown ('unknown' is
 * the device schema's fallback for a missing created_by).
 */
export function foreignCreatorWarning(
  createdBy: string | undefined,
  user: User | null
): string | undefined {
  if (!createdBy || createdBy === 'unknown' || !user || createdBy === user.email) {
    return undefined;
  }
  return `Created by ${createdBy}, another user.`;
}
