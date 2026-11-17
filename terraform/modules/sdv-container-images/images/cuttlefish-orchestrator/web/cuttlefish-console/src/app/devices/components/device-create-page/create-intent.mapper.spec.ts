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
import {DEFAULT_TRACK_BRANCH, DEFAULT_TRACK_TARGET} from '../../../shared/constants';
import {handoffToAdvancedSeed, intentToAdvancedSeed} from './create-intent.mapper';
import type {CreateIntent} from './create-intent.model';
import {toCreateDeviceRequest} from './create-intent.model';

function intent(overrides: Partial<CreateIntent> = {}): CreateIntent {
  return {
    name: 'my-device',
    build: {kind: 'default'},
    trackBranch: DEFAULT_TRACK_BRANCH,
    trackTarget: DEFAULT_TRACK_TARGET,
    usage: 'interactive',
    gpuRequired: false,
    architecture: 'x86',
    pool: 'auto',
    host: {kind: 'auto'},
    preferredRegion: '',
    capacity: 'auto',
    ttlHours: 4,
    pinned: false,
    pinnedReason: '',
    pinnedReasonDetail: '',
    pinnedUntil: '',
    tags: [],
    ...overrides,
  };
}

describe('toCreateDeviceRequest', () => {
  it('maps an automatic host to a declarative request', () => {
    const req = toCreateDeviceRequest(intent({usage: 'headless'}), null);

    expect(req.hostId).toBeUndefined();
    expect(req.requirements).toEqual({interaction: 'headless', build: undefined});
    expect(req.gpuMode).toBeUndefined(); // backend default (auto)
  });

  it('carries a registered build choice into requirements', () => {
    const req = toCreateDeviceRequest(intent({build: {kind: 'registered', id: 'b1'}}), null);

    expect(req.requirements?.build).toEqual({kind: 'registered', id: 'b1'});
  });

  it('maps a tracked branch to a declarative track requirement (automatic host)', () => {
    const req = toCreateDeviceRequest(
      intent({
        build: {kind: 'track'},
        trackBranch: 'aosp-main',
        trackTarget: 'aosp_cf_x86_64_auto-userdebug',
      }),
      null
    );

    expect(req.requirements?.build).toEqual({
      kind: 'track',
      branch: 'aosp-main',
      target: 'aosp_cf_x86_64_auto-userdebug',
    });
  });

  it('maps a tracked branch on a chosen host to buildId=branch (imperative @ab)', () => {
    const req = toCreateDeviceRequest(
      intent({
        host: {kind: 'host', id: 'h1'},
        build: {kind: 'track'},
        trackBranch: 'aosp-main',
        trackTarget: 'aosp_cf_x86_64_auto-userdebug',
      }),
      null
    );

    expect(req.hostId).toBe('h1');
    expect(req.buildId).toBe('aosp-main');
    expect(req.buildTarget).toBe('aosp_cf_x86_64_auto-userdebug');
    expect(req.requirements).toBeUndefined();
  });

  it('maps a chosen host to an imperative request with the effective build', () => {
    const build = {id: 'b1', target: 'aosp_cf_x86_64_phone-userdebug'};
    const req = toCreateDeviceRequest(
      intent({host: {kind: 'host', id: 'h1'}, build: {kind: 'registered', id: 'b1'}}),
      build as never
    );

    expect(req.hostId).toBe('h1');
    expect(req.buildId).toBe('b1');
    expect(req.buildTarget).toBe('aosp_cf_x86_64_phone-userdebug');
    expect(req.requirements).toBeUndefined();
  });

  it('uses the platform default as the effective build on a chosen host', () => {
    // Intent still says 'default'; the caller resolves it to the default
    // registered build. Imperative creates cannot express 'default'.
    const defaultBuild = {id: 'b-default', target: 't'};
    const req = toCreateDeviceRequest(
      intent({host: {kind: 'host', id: 'h1'}, build: {kind: 'default'}}),
      defaultBuild as never
    );

    expect(req.buildId).toBe('b-default');
  });

  it('carries a region preference into the declarative requirements', () => {
    const req = toCreateDeviceRequest(intent({preferredRegion: 'us-central1'}), null);

    expect(req.requirements?.preferredRegion).toBe('us-central1');
  });

  it('omits the region preference when none is set', () => {
    const req = toCreateDeviceRequest(intent(), null);

    expect(req.requirements?.preferredRegion).toBeUndefined();
  });

  it('carries an arm64 guest and a pinned pool into the declarative requirements', () => {
    const req = toCreateDeviceRequest(intent({architecture: 'arm', pool: 'arm'}), null);

    expect(req.requirements?.architecture).toBe('arm');
    expect(req.requirements?.pool).toBe('arm');
  });

  it('pins a pool without stating an architecture', () => {
    const req = toCreateDeviceRequest(intent({pool: 'gpu'}), null);

    expect(req.requirements?.pool).toBe('gpu');
    expect(req.requirements?.architecture).toBeUndefined();
  });

  it('omits the architecture and the pool at their defaults', () => {
    const req = toCreateDeviceRequest(intent(), null);

    expect(req.requirements?.architecture).toBeUndefined();
    expect(req.requirements?.pool).toBeUndefined();
  });

  it('collapses empty lifecycle fields to undefined', () => {
    const req = toCreateDeviceRequest(intent(), null);

    expect(req.pinnedReason).toBeUndefined();
    expect(req.pinnedReasonDetail).toBeUndefined();
    expect(req.tags).toBeUndefined();
  });
});

describe('intentToAdvancedSeed', () => {
  it('derives gpu mode from usage on eject', () => {
    const interactive = intentToAdvancedSeed(intent({usage: 'interactive'}));
    const headless = intentToAdvancedSeed(intent({usage: 'headless'}));

    expect(interactive.draft.instances[0].gpuMode).toBe('gfxstream');
    expect(headless.draft.instances[0].gpuMode).toBe('swiftshader');
  });

  it('carries the name, host, build and lifecycle answers', () => {
    const seed = intentToAdvancedSeed(
      intent({
        name: 'repro-device',
        build: {kind: 'registered', id: 'b1'},
        host: {kind: 'host', id: 'h1'},
        ttlHours: 8,
        pinned: true,
        pinnedReason: 'memory_leak_debug',
        tags: ['repro'],
      })
    );

    expect(seed.name).toBe('repro-device');
    expect(seed.draft.instances.map(i => i.name)).toEqual(['repro-device']);
    expect(seed.hostChoice).toEqual({kind: 'host', id: 'h1'});
    expect(seed.buildChoice).toEqual({kind: 'registered', id: 'b1'});
    expect(seed.lifecycle).toMatchObject({
      ttlHours: 8,
      pinned: true,
      pinnedReason: 'memory_leak_debug',
      tags: ['repro'],
    });
  });

  it('carries the capacity answer', () => {
    expect(intentToAdvancedSeed(intent({capacity: 'spot'})).draft.capacity).toBe('spot');
    expect(intentToAdvancedSeed(intent()).draft.capacity).toBe('auto');
  });

  it('normalizes a tracked build to the platform default on eject to Advanced', () => {
    const seed = intentToAdvancedSeed(intent({build: {kind: 'track'}, trackBranch: 'aosp-main'}));

    expect(seed.buildChoice).toEqual({kind: 'default'});
  });
});

describe('handoffToAdvancedSeed', () => {
  it('has nothing to apply without a handoff', () => {
    expect(handoffToAdvancedSeed({sdvPair: false})).toBeNull();
    expect(handoffToAdvancedSeed({hostId: '', buildId: '', sdvPair: false})).toBeNull();
  });

  it('pre-selects the named host and build and leaves the rest to the editor', () => {
    const seed = handoffToAdvancedSeed({hostId: 'h1', buildId: 'b1', sdvPair: false});

    expect(seed).toEqual({
      hostChoice: {kind: 'host', id: 'h1'},
      buildChoice: {kind: 'registered', id: 'b1'},
    });
  });

  it('starts from the SDV pair when asked', () => {
    const seed = handoffToAdvancedSeed({sdvPair: true});

    expect(seed?.draft?.instances.map(i => i.name)).toEqual(['core', 'ivi']);
    expect(seed?.draft?.capacity).toBe('standard');
    expect(seed?.hostChoice).toBeUndefined();
    // Direct entry names no device, so the editor generates a name.
    expect(seed?.name).toBeUndefined();
  });
});
