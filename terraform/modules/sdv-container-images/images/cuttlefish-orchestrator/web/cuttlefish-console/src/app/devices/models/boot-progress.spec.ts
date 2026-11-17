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

import {describe, expect, it} from 'vitest';
import {bootProgressLine} from './device.model';
import {BOOT_OBSERVATION_SCHEMA} from './device.schema';

describe('bootProgressLine', () => {
  it('renders the loop evidence on any status, with the panic line', () => {
    const line = bootProgressLine('error', {
      guestReboots: 435,
      kernelPanics: 11,
      verdict: 'looping',
      lastPanic: 'Kernel panic - not syncing: bad XCR0',
    });
    expect(line).toContain('rebooted 435×');
    expect(line).toContain('bad XCR0');
  });

  it('renders a guest-declared failure on any status', () => {
    expect(bootProgressLine('error', {verdict: 'boot_failed'})).toBe('Guest declared boot failure');
  });

  it('separates fetch-is-slow from guest-is-broken while creating', () => {
    expect(bootProgressLine('creating', {verdict: 'no_markers'})).toContain(
      'No guest boot activity yet'
    );
    expect(bootProgressLine('creating', {verdict: 'progress', kernelPanics: 2})).toContain(
      '2 failed attempts'
    );
  });

  it('never shows a stale progress line on a settled device', () => {
    expect(bootProgressLine('running', {verdict: 'progress', kernelPanics: 2})).toBeUndefined();
    expect(bootProgressLine('running', {verdict: 'booted'})).toBeUndefined();
    expect(bootProgressLine('stopped', {verdict: 'no_markers'})).toBeUndefined();
  });
});

describe('BOOT_OBSERVATION_SCHEMA', () => {
  it('transforms the wire shape to camelCase with parsed dates', () => {
    const result = BOOT_OBSERVATION_SCHEMA.safeParse({
      kernel_panics: 11,
      guest_reboots: 435,
      uboot_starts: 22,
      boot_completed: false,
      kernel_log_bytes: 10485760,
      last_panic: 'Kernel panic - not syncing: bad XCR0',
      verdict: 'looping',
      transient_since: '2026-08-02T10:00:00Z',
      observed_at: '2026-08-02T10:12:00Z',
    });
    expect(result.success).toBe(true);
    if (!result.success) {
      return;
    }
    expect(result.data.kernelPanics).toBe(11);
    expect(result.data.guestReboots).toBe(435);
    expect(result.data.verdict).toBe('looping');
    expect(result.data.observedAt).toEqual(new Date('2026-08-02T10:12:00Z'));
    expect(result.data.transientSince).toEqual(new Date('2026-08-02T10:00:00Z'));
  });

  it('accepts the sparse wire form (zero counters omitted)', () => {
    const result = BOOT_OBSERVATION_SCHEMA.safeParse({
      verdict: 'no_markers',
      observed_at: '2026-08-02T10:00:00Z',
    });
    expect(result.success).toBe(true);
    if (!result.success) {
      return;
    }
    expect(result.data.kernelPanics).toBeUndefined();
    expect(result.data.verdict).toBe('no_markers');
  });
});
