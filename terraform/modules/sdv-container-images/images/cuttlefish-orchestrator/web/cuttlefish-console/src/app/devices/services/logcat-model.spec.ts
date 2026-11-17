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

import type {AndroidLogEntry} from '@yume-chan/android-bin';
import {describe, expect, it} from 'vitest';
import {toLogEntry} from './adb/adb-logcat';
import type {LogEntry, LogLevel} from './logcat-model';
import {applyLogFilter} from './logcat-model';

describe('toLogEntry', () => {
  it('maps a binary logcat entry to the viewer model', () => {
    const timestampNs = 1_751_754_225_678_000_000n;
    const binaryEntry = {
      pid: 1234,
      tid: 5678,
      seconds: 1_751_754_225,
      nanoseconds: 678_000_000,
      logId: 0,
      uid: 1000,
      timestamp: timestampNs,
      priority: 5, // Warn
      tag: 'ActivityManager',
      message: 'Slow operation',
      toString: () => '06-05 22:23:45.678  1234  5678 W ActivityManager: Slow operation',
    } as unknown as AndroidLogEntry;

    const entry = toLogEntry(binaryEntry, 7);

    expect(entry).toMatchObject({
      id: 7,
      level: 'W',
      tag: 'ActivityManager',
      message: 'Slow operation',
      pid: 1234,
      tid: 5678,
    });
    expect(entry.timestamp.getTime()).toBe(1_751_754_225_678);
    // Local-time label; derive the expected hour/minute from the same Date.
    const expectedTime = new Date(1_751_754_225_678);
    expect(entry.timeLabel).toBe(
      `${expectedTime.getHours().toString().padStart(2, '0')}:` +
        `${expectedTime.getMinutes().toString().padStart(2, '0')}:45.678`
    );
    expect(entry.raw).toContain('W ActivityManager: Slow operation');
  });

  it('maps every standard priority and defaults odd ones to Info', () => {
    const level = (priority: number): LogLevel =>
      toLogEntry(
        {
          pid: 1,
          tid: 1,
          timestamp: 0n,
          priority,
          tag: 't',
          message: 'm',
          toString: () => '',
        } as unknown as AndroidLogEntry,
        1
      ).level;

    expect([2, 3, 4, 5, 6, 7].map(level)).toEqual(['V', 'D', 'I', 'W', 'E', 'F']);
    expect(level(0)).toBe('I');
    expect(level(8)).toBe('I');
  });
});

describe('applyLogFilter', () => {
  const entry = (id: number, level: LogEntry['level'], tag: string, message: string): LogEntry => ({
    id,
    timestamp: new Date(2026, 5, 15),
    timeLabel: '00:00:00.000',
    level,
    tag,
    message,
    raw: `${level} ${tag}: ${message}`,
  });

  const entries = [
    entry(1, 'V', 'Chatty', 'verbose noise'),
    entry(2, 'D', 'MyApp', 'debug detail'),
    entry(3, 'I', 'MyApp', 'started activity'),
    entry(4, 'W', 'System', 'low memory'),
    entry(5, 'E', 'MyApp', 'crash in onCreate'),
    entry(6, 'F', 'Kernel', 'panic'),
  ];

  it('passes everything through with an empty filter', () => {
    expect(applyLogFilter(entries, {})).toHaveLength(6);
  });

  it('filters by minimum level', () => {
    expect(applyLogFilter(entries, {minLevel: 'W'}).map(e => e.id)).toEqual([4, 5, 6]);
  });

  it('keeps only errors and fatals with showOnlyErrors', () => {
    expect(applyLogFilter(entries, {showOnlyErrors: true}).map(e => e.id)).toEqual([5, 6]);
  });

  it('includes by tag substring, case-insensitively', () => {
    expect(applyLogFilter(entries, {tags: ['myapp']}).map(e => e.id)).toEqual([2, 3, 5]);
  });

  it('excludes by tag substring', () => {
    expect(applyLogFilter(entries, {excludeTags: ['MyApp', 'Chatty']}).map(e => e.id)).toEqual([
      4, 6,
    ]);
  });

  it('searches message and tag text', () => {
    expect(applyLogFilter(entries, {searchText: 'crash'}).map(e => e.id)).toEqual([5]);
    expect(applyLogFilter(entries, {searchText: 'kernel'}).map(e => e.id)).toEqual([6]);
  });

  it('combines filters conjunctively', () => {
    expect(
      applyLogFilter(entries, {minLevel: 'D', tags: ['MyApp'], searchText: 'debug'}).map(e => e.id)
    ).toEqual([2]);
  });
});
