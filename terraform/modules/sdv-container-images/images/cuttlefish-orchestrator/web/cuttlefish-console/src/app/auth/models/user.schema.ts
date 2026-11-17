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

import {z} from 'zod';
import type {User} from './user.model';

/**
 * Zod schema for parsing the /v2/me API response.
 *
 * VALIDATION: Performs runtime validation of the user profile response.
 * TRANSFORMATION: Converts backend snake_case fields to frontend camelCase
 * to maintain idiomatic TypeScript conventions.
 */
export const UserSchema = z
  .object({
    email: z.string().email(),
    display_name: z.string().optional(),
  })
  .transform(
    (data): User => ({
      email: data.email,
      displayName: data.display_name,
    })
  );
