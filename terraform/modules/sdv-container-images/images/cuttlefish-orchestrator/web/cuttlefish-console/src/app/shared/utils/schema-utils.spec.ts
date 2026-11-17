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

import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest';
import {z} from 'zod';
import {
  createListResponseSchema,
  parseDate,
  parseDateOrDefault,
  pick,
  safeParseList,
} from './schema-utils';

describe('pick', () => {
  it('should return the first defined value', () => {
    expect(pick('first', 'second')).toBe('first');
    expect(pick(undefined, 'second')).toBe('second');
    expect(pick(null, 'second')).toBe('second');
    expect(pick(undefined, null, 'third')).toBe('third');
  });

  it('should return undefined if all values are undefined or null', () => {
    expect(pick(undefined, undefined)).toBeUndefined();
    expect(pick(null, null)).toBeUndefined();
    expect(pick(undefined, null, undefined)).toBeUndefined();
  });

  it('should handle falsy values correctly', () => {
    expect(pick(0, 1)).toBe(0);
    expect(pick('', 'fallback')).toBe('');
    expect(pick(false, true)).toBe(false);
  });

  it('should work with snake_case and camelCase pattern', () => {
    const data = {host_id: 'abc-123', hostId: undefined};
    expect(pick(data.host_id, data.hostId)).toBe('abc-123');

    const data2 = {host_id: undefined, hostId: 'xyz-789'};
    expect(pick(data2.host_id, data2.hostId)).toBe('xyz-789');
  });
});

describe('parseDate', () => {
  it('should parse valid ISO date string', () => {
    const result = parseDate('2025-12-18T10:30:00Z');
    expect(result).toBeInstanceOf(Date);
    expect(result?.toISOString()).toBe('2025-12-18T10:30:00.000Z');
  });

  it('should return undefined for empty string', () => {
    expect(parseDate('')).toBeUndefined();
  });

  it('should return undefined for null', () => {
    expect(parseDate(null)).toBeUndefined();
  });

  it('should return undefined for undefined', () => {
    expect(parseDate(undefined)).toBeUndefined();
  });

  it('should return undefined for invalid date string', () => {
    expect(parseDate('not-a-date')).toBeUndefined();
    expect(parseDate('invalid')).toBeUndefined();
  });

  it('should handle various date formats', () => {
    // ISO format
    expect(parseDate('2025-01-15T00:00:00.000Z')).toBeInstanceOf(Date);
    // Date only
    expect(parseDate('2025-01-15')).toBeInstanceOf(Date);
  });
});

describe('parseDateOrDefault', () => {
  const defaultDate = new Date('2000-01-01T00:00:00Z');

  it('should parse valid date string', () => {
    const result = parseDateOrDefault('2025-12-18T10:30:00Z', defaultDate);
    expect(result.toISOString()).toBe('2025-12-18T10:30:00.000Z');
  });

  it('should return default for undefined', () => {
    expect(parseDateOrDefault(undefined, defaultDate)).toBe(defaultDate);
  });

  it('should return default for null', () => {
    expect(parseDateOrDefault(null, defaultDate)).toBe(defaultDate);
  });

  it('should return default for empty string', () => {
    expect(parseDateOrDefault('', defaultDate)).toBe(defaultDate);
  });

  it('should return default for invalid date', () => {
    expect(parseDateOrDefault('invalid', defaultDate)).toBe(defaultDate);
  });
});

describe('createListResponseSchema', () => {
  const ITEM_SCHEMA = z.object({
    id: z.string(),
    name: z.string(),
  });

  const LIST_SCHEMA = createListResponseSchema(ITEM_SCHEMA);

  it('should parse valid list response', () => {
    const response = {
      items: [
        {id: '1', name: 'Item 1'},
        {id: '2', name: 'Item 2'},
      ],
      total: 2,
      limit: 20,
      offset: 0,
    };

    const result = LIST_SCHEMA.parse(response);

    expect(result.items).toHaveLength(2);
    expect(result.items[0]).toEqual({id: '1', name: 'Item 1'});
    expect(result.total).toBe(2);
    expect(result.limit).toBe(20);
    expect(result.offset).toBe(0);
  });

  it('should default total to items length if not provided', () => {
    const response = {
      items: [{id: '1', name: 'Item 1'}],
      limit: 20,
      offset: 0,
    };

    const result = LIST_SCHEMA.parse(response);

    expect(result.total).toBe(1);
  });

  it('should drop invalid items instead of throwing (one bad row must not blank the list)', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined);
    const response = {
      items: [
        {id: '1', name: 'Good'},
        {id: '2'}, // Missing 'name'
        {id: '3', name: 'Also good'},
      ],
      limit: 20,
      offset: 0,
    };

    const result = LIST_SCHEMA.parse(response);

    expect(result.items.map(i => i.id)).toEqual(['1', '3']);
    warn.mockRestore();
  });

  it('should handle empty items array', () => {
    const response = {
      items: [],
      total: 0,
      limit: 20,
      offset: 0,
    };

    const result = LIST_SCHEMA.parse(response);

    expect(result.items).toHaveLength(0);
    expect(result.total).toBe(0);
  });
});

describe('safeParseList', () => {
  const ITEM = z.object({id: z.string(), name: z.string()});
  let warn: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined);
  });
  afterEach(() => {
    warn.mockRestore();
  });

  it('keeps valid items and drops invalid ones without throwing', () => {
    const items = [{id: '1', name: 'a'}, {id: '2'}, 'garbage', {id: '3', name: 'c'}];
    expect(safeParseList(items, ITEM).map(i => i.id)).toEqual(['1', '3']);
  });

  it('warns for each dropped item (never silently drops)', () => {
    safeParseList([{id: '1', name: 'a'}, {id: '2'}, 'garbage'], ITEM, 'thing');
    expect(warn).toHaveBeenCalledTimes(2);
  });

  it('applies the item schema transform to the kept items', () => {
    const doubled = z.object({n: z.number()}).transform(d => d.n * 2);
    expect(safeParseList([{n: 3}, {n: 'bad'}, {n: 5}], doubled)).toEqual([6, 10]);
  });

  it('returns an empty array for empty input', () => {
    expect(safeParseList([], ITEM)).toEqual([]);
  });
});
