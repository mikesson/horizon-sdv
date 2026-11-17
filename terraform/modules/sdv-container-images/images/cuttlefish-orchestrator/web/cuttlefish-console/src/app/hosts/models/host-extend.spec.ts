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
import type {Host} from './host.model';
import {canExtendHost, extendedExpiry, isValidExtendHours} from './host-extend';

const EXPIRY = new Date('2026-11-01T12:00:00Z');

function makeHost(overrides: Partial<Host> = {}): Host {
  const base: Partial<Host> = {
    id: 'host-1',
    status: 'running',
    hostClass: 'reserved',
    lifecycle: {mode: 'ephemeral', ttlSeconds: 36000, expiresAt: EXPIRY, protectedDeviceCount: 0},
    ...overrides,
  };
  return base as Host;
}

describe('canExtendHost', () => {
  it('allows a live reserved host with an expiry', () => {
    expect(canExtendHost(makeHost())).toBe(true);
    expect(canExtendHost(makeHost({status: 'stopped'}))).toBe(true);
  });

  it.each(['fleet', 'dedicated', 'batch'] as const)('refuses a %s host', hostClass => {
    expect(canExtendHost(makeHost({hostClass}))).toBe(false);
  });

  it('refuses a host with no class on the wire', () => {
    expect(canExtendHost(makeHost({hostClass: undefined}))).toBe(false);
  });

  it.each(['deleting', 'deleted'] as const)('refuses a %s host', status => {
    expect(canExtendHost(makeHost({status}))).toBe(false);
  });

  it('refuses a protected host, which has no expiry to raise', () => {
    const lifecycle = {mode: 'protected', ttlSeconds: 36000, protectedDeviceCount: 2} as const;
    expect(canExtendHost(makeHost({lifecycle}))).toBe(false);
  });

  it('refuses a host with no lifecycle or no expiry', () => {
    expect(canExtendHost(makeHost({lifecycle: undefined}))).toBe(false);
    const lifecycle = {mode: 'ephemeral', ttlSeconds: 36000, protectedDeviceCount: 0} as const;
    expect(canExtendHost(makeHost({lifecycle}))).toBe(false);
  });
});

describe('isValidExtendHours', () => {
  it('accepts whole hours from 1 to 168', () => {
    expect(isValidExtendHours(1)).toBe(true);
    expect(isValidExtendHours(168)).toBe(true);
  });

  it.each([0, 169, -1, 1.5, Number.NaN])('rejects %s', hours => {
    expect(isValidExtendHours(hours)).toBe(false);
  });
});

describe('extendedExpiry', () => {
  it('counts from the current expiry while it is in the future', () => {
    const now = new Date('2026-10-31T12:00:00Z');
    expect(extendedExpiry(EXPIRY, 2, now).toISOString()).toBe('2026-11-01T14:00:00.000Z');
  });

  it('counts from now once the expiry has passed', () => {
    const now = new Date('2026-11-02T00:00:00Z');
    expect(extendedExpiry(EXPIRY, 2, now).toISOString()).toBe('2026-11-02T02:00:00.000Z');
  });
});
