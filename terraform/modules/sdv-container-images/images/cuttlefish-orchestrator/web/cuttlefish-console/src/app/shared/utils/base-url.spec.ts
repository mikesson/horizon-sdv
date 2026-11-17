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

import {afterEach, beforeEach, describe, expect, it} from 'vitest';
import {resolveBasePath, resolveWebSocketUrl} from './base-url';

describe('base-url utilities', () => {
  let baseEl: HTMLBaseElement | null = null;
  const originalPath = `${window.location.pathname}${window.location.search}${window.location.hash}`;

  const mountBase = (href: string): void => {
    baseEl?.remove();
    baseEl = document.createElement('base');
    baseEl.setAttribute('href', href);
    document.head.appendChild(baseEl);
  };

  beforeEach(() => {
    baseEl?.remove();
    baseEl = null;
  });

  afterEach(() => {
    baseEl?.remove();
    baseEl = null;
    window.history.replaceState(null, '', originalPath || '/');
  });

  it('resolves root-relative paths unchanged when no base tag is present', () => {
    window.history.pushState(null, '', '/devices/123');
    expect(resolveBasePath('/v2/devices')).toBe('/v2/devices');
    expect(resolveBasePath('/docs')).toBe('/docs');
    expect(resolveBasePath('/oauth2/start')).toBe('/oauth2/start');
  });

  it('prefixes paths under subpath base href even on deeply nested SPA routes', () => {
    mountBase('/cuttlefish-orchestrator/');
    window.history.pushState(null, '', '/cuttlefish-orchestrator/devices/d1/player?instance=ins-1');

    expect(resolveBasePath('/v2/devices')).toBe('/cuttlefish-orchestrator/v2/devices');
    expect(resolveBasePath('/docs')).toBe('/cuttlefish-orchestrator/docs');
    expect(resolveBasePath('/devices/d1/player?instance=ins-1')).toBe(
      '/cuttlefish-orchestrator/devices/d1/player?instance=ins-1'
    );
    expect(resolveBasePath('/oauth2/sign_out')).toBe('/cuttlefish-orchestrator/oauth2/sign_out');
    expect(resolveBasePath('/')).toBe('/cuttlefish-orchestrator/');
  });

  it('is idempotent when given an already-prefixed path and ignores http(s) URLs', () => {
    mountBase('/cuttlefish-orchestrator/');
    expect(resolveBasePath('/cuttlefish-orchestrator/v2/devices')).toBe(
      '/cuttlefish-orchestrator/v2/devices'
    );
    expect(resolveBasePath('/cuttlefish-orchestrator')).toBe('/cuttlefish-orchestrator');
    expect(resolveBasePath('https://example.com/v2/devices')).toBe(
      'https://example.com/v2/devices'
    );
  });

  it('normalizes base href missing a trailing slash', () => {
    mountBase('/cuttlefish-orchestrator');
    expect(resolveBasePath('/v2/hosts')).toBe('/cuttlefish-orchestrator/v2/hosts');
  });

  it('builds ws/wss URLs matching window protocol under subpath base href', () => {
    mountBase('/cuttlefish-orchestrator/');
    const expectedProto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const expectedHost = window.location.host;

    expect(resolveWebSocketUrl('/v2/devices/d1/instances/1/adb')).toBe(
      `${expectedProto}//${expectedHost}/cuttlefish-orchestrator/v2/devices/d1/instances/1/adb`
    );
    expect(
      resolveWebSocketUrl('/cuttlefish-orchestrator/v2/devices/d1/instances/1/signaling/ws')
    ).toBe(
      `${expectedProto}//${expectedHost}/cuttlefish-orchestrator/v2/devices/d1/instances/1/signaling/ws`
    );
  });
});
