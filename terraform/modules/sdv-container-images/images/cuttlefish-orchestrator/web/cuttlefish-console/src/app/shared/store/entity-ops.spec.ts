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

import {of, throwError} from 'rxjs';
import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest';

import {pollUntilTerminal} from './entity-ops';

interface Entity {
  status: string;
}

describe('pollUntilTerminal', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('completes on the first stable status', () => {
    const responses = [{status: 'creating'}, {status: 'creating'}, {status: 'running'}];
    let call = 0;
    const seen: string[] = [];
    let completed = false;

    pollUntilTerminal<Entity>(
      () => of(responses[Math.min(call++, responses.length - 1)]),
      'id-1',
      ['running', 'stopped', 'error', 'deleted'],
      e => seen.push(e.status)
    ).subscribe({complete: () => (completed = true)});

    vi.advanceTimersByTime(10_000);
    expect(seen).toEqual(['creating', 'creating', 'running']);
    expect(completed).toBe(true);
    expect(call).toBe(3);
  });

  it('a stopped entity ends the poll immediately (no background polling leak)', () => {
    let calls = 0;
    let completed = false;

    pollUntilTerminal<Entity>(
      () => {
        calls++;
        return of({status: 'stopped'});
      },
      'id-1',
      ['running', 'stopped', 'error', 'deleted'],
      () => undefined
    ).subscribe({complete: () => (completed = true)});

    vi.advanceTimersByTime(60_000);
    expect(calls).toBe(1);
    expect(completed).toBe(true);
  });

  it('a failed tick is skipped, not fatal: the next tick refetches', () => {
    const responses = [
      () => of({status: 'creating'}),
      () => throwError(() => new Error('blip')),
      () => of({status: 'running'}),
    ];
    let call = 0;
    const seen: string[] = [];
    let errored = false;
    let completed = false;

    pollUntilTerminal<Entity>(
      () => responses[Math.min(call++, responses.length - 1)](),
      'id-1',
      ['running'],
      e => seen.push(e.status)
    ).subscribe({error: () => (errored = true), complete: () => (completed = true)});

    vi.advanceTimersByTime(10_000);
    expect(seen).toEqual(['creating', 'running']);
    expect(errored).toBe(false);
    expect(completed).toBe(true);
  });

  it('gives up after the tick budget even when the status never stabilizes', () => {
    let calls = 0;
    let completed = false;

    pollUntilTerminal<Entity>(
      () => {
        calls++;
        return of({status: 'creating'});
      },
      'id-1',
      ['running'],
      () => undefined
    ).subscribe({complete: () => (completed = true)});

    // Two hours >> the 30-minute budget (900 ticks at 2s).
    vi.advanceTimersByTime(2 * 60 * 60 * 1000);
    expect(calls).toBe(900);
    expect(completed).toBe(true);
  });
});
