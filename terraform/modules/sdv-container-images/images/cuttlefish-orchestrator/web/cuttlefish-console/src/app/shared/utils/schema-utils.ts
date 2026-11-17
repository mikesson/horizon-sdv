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

/**
 * Helpers for reading Huma API responses through Zod.
 *
 * The Go backend serves snake_case JSON field names, and the console's types
 * are camelCase. These helpers do the field reads, the date parsing and the
 * per-item validation that every schema in the app repeats.
 */

/**
 * Returns the first value that is neither undefined nor null.
 *
 * @example
 * pick(data.host_id, data.hostId) // Returns whichever is defined
 */
export function pick<T>(...values: Array<T | undefined | null>): T | undefined {
  for (const value of values) {
    if (value !== undefined && value !== null) {
      return value;
    }
  }
  return undefined;
}

/**
 * Parses an ISO date string to a Date object.
 * Returns undefined if the input is null/undefined/empty.
 *
 * @example
 * parseDate(data.created_at) // Returns Date or undefined
 */
export function parseDate(value: string | undefined | null): Date | undefined {
  if (!value) {
    return undefined;
  }
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? undefined : date;
}

/**
 * Parses an ISO date string to a Date object, with a fallback default value.
 *
 * @example
 * parseDateOrDefault(data.created_at, new Date()) // Returns Date
 */
export function parseDateOrDefault(value: string | undefined | null, defaultValue: Date): Date {
  return parseDate(value) ?? defaultValue;
}

/**
 * Validates each item of an API list independently, keeping the ones that pass
 * and dropping (with a warning) the ones that don't.
 *
 * A single malformed item must not throw and blank the entire list. The rest
 * still render, and the failure degrades to one missing row instead of a dead
 * page. Real contract drift is caught at build time by
 * `npm run check:schema-drift`; this is the runtime safety net.
 */
export function safeParseList<T>(
  items: readonly unknown[],
  itemSchema: z.ZodType<T>,
  label = 'list item'
): T[] {
  const parsed: T[] = [];
  for (const raw of items) {
    const result = itemSchema.safeParse(raw);
    if (result.success) {
      parsed.push(result.data);
    } else {
      console.warn(`Dropping malformed ${label} from API response`, result.error.issues);
    }
  }
  return parsed;
}

/**
 * Generates a Zod schema for standard Huma-compliant paginated lists.
 *
 * This factory function ensures consistent validation and typing for all
 * 'List' API endpoints, mapping the generic `items` array through the provided
 * item-specific Zod schema. A malformed item is dropped, not thrown.
 *
 * @param itemSchema - The Zod schema to validate individual items in the list.
 */
export function createListResponseSchema<T>(
  itemSchema: z.ZodType<T>
): z.ZodType<{items: T[]; total: number; limit: number; offset: number}> {
  return z
    .object({
      items: z.array(z.unknown()),
      total: z.number().optional(),
      limit: z.number(),
      offset: z.number(),
    })
    .transform(data => ({
      items: safeParseList(data.items, itemSchema),
      total: data.total ?? data.items.length,
      limit: data.limit,
      offset: data.offset,
    }));
}
