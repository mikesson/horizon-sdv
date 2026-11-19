/**
 * Copyright 2026 Google LLC
 * SPDX-License-Identifier: Apache-2.0
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

import {provideZonelessChangeDetection} from '@angular/core';
import {TestBed} from '@angular/core/testing';
import {MatSnackBar} from '@angular/material/snack-bar';
import {of, throwError} from 'rxjs';
import {beforeEach, describe, expect, it, vi} from 'vitest';
import type {AutoscalerSnapshot} from '../models/autoscaler.model';
import type {FleetSchedule, FleetSettings} from '../models/control-plane.model';
import {AutoscalerApiService} from '../services/autoscaler-api.service';
import {AutoscalerStore} from './autoscaler.signal-store';

function makePolicy(overrides: Partial<FleetSettings> = {}): FleetSettings {
  return {
    mode: 'on',
    tiers: {
      default: {
        standby: 2,
        floor: 1,
        ladder: ['n2-standard-8', 'n1-standard-8'],
        schedules: [],
      },
      gpu: {
        standby: 1,
        floor: 0,
        ladder: ['g2-standard-8'],
        schedules: [],
      },
    },
    floor: 1,
    demandHorizonSec: 3600,
    drainGraceSec: 480,
    shapeLadder: ['n2-standard-8', 'n1-standard-8'],
    isSet: true,
    maxStandby: 16,
    maxFloor: 16,
    maxDemandHorizonSec: 86400,
    minDrainGraceSec: 120,
    maxDrainGraceSec: 599,
    maxSchedules: 8,
    maxLadderRungs: 8,
    ...overrides,
  };
}

function makeSnapshot(overrides: Partial<AutoscalerSnapshot> = {}): AutoscalerSnapshot {
  const status = {
    mode: 'on' as const,
    target: 2,
    standby: 2,
    draining: 0,
    misses: 0,
    demand: {
      servable: 1,
      unservable: 0,
      gpuRequired: 0,
      needsLargerHost: 0,
      oversizedGroup: 0,
    },
    schedule: 0,
    floor: 1,
    total: 2,
    signal: 'standby' as const,
    limitedBy: '',
    partial: false,
  };
  return {
    fleet: {
      status,
      policy: makePolicy(),
      deviceSlots: 8,
      gpuDegradation: {degraded: 0, wanted: 0},
    },
    pools: [{pool: 'default', status, deficit: 0, surplus: 0, reason: 'acting', ladder: []}],
    limits: {maxStandbyHosts: 16},
    intents: {
      default: {standby: 2, floor: 1, ladder: ['n2-standard-8'], schedules: []},
    },
    standby: 2,
    servedLadders: {default: ['n2-standard-8', 'n1-standard-8'], gpu: ['g2-standard-8']},
    defaultShape: 'n1-standard-8',
    armImageReady: true,
    density: 4,
    ...overrides,
  } as unknown as AutoscalerSnapshot;
}

function makeRecommendation(): {
  standby: number;
  sufficient: boolean;
  servable: number;
  unservable: number;
  series: never[];
  from: Date;
  to: Date;
} {
  return {
    standby: 3,
    sufficient: true,
    servable: 10,
    unservable: 1,
    series: [],
    from: new Date('2026-07-25T12:00:00Z'),
    to: new Date('2026-08-01T12:00:00Z'),
  };
}

describe('AutoscalerStore', () => {
  const api = {
    snapshot: vi.fn(),
    policy: vi.fn(),
    recommendation: vi.fn(),
    saveAutoscaler: vi.fn(),
    disablePool: vi.fn(),
    saveTuning: vi.fn(),
    saveSchedules: vi.fn(),
    saveLadder: vi.fn(),
  };
  const snackBar = {open: vi.fn()};

  let store: InstanceType<typeof AutoscalerStore>;

  beforeEach(() => {
    vi.clearAllMocks();
    api.snapshot.mockReturnValue(of(makeSnapshot()));
    api.policy.mockReturnValue(of(makePolicy()));
    api.recommendation.mockReturnValue(of(makeRecommendation()));

    TestBed.configureTestingModule({
      providers: [
        provideZonelessChangeDetection(),
        AutoscalerStore,
        {provide: AutoscalerApiService, useValue: api},
        {provide: MatSnackBar, useValue: snackBar},
      ],
    });
    store = TestBed.inject(AutoscalerStore);
  });

  it('loads snapshot and policy and exposes derived floor cleanly', () => {
    store.refresh();
    store.loadPolicy();

    expect(store.loading()).toBe(false);
    expect(store.error()).toBeNull();
    expect(store.snapshot()?.pools.length).toBe(1);
    expect(store.floor()).toBe(1);
  });

  it('loads policy and recommendation independently and handles failures', () => {
    store.loadPolicy();
    expect(store.policy()?.mode).toBe('on');

    store.loadRecommendation();
    expect(store.recommendation()?.standby).toBe(3);

    api.recommendation.mockReturnValue(throwError(() => new Error('bq timeout')));
    store.loadRecommendation();
    expect(store.recommendationError()).toContain('bq timeout');
  });

  it('writes autoscaler, pool disable, tuning, and schedules while syncing policy', () => {
    const saved = makePolicy({mode: 'off'});
    api.saveAutoscaler.mockReturnValue(of(saved));
    api.policy.mockReturnValue(of(saved));
    store.saveAutoscaler('off', 'default', 0, 0);
    expect(api.saveAutoscaler).toHaveBeenCalledWith('off', 'default', 0, 0);
    expect(store.policy()?.mode).toBe('off');

    const schedule: FleetSchedule = {
      name: 'Workday',
      hosts: 2,
      start: '08:00',
      durationSec: 3600,
      days: [1, 2],
      location: 'UTC',
    };
    api.saveSchedules.mockReturnValue(of(saved));
    store.saveSchedules('default', [schedule]);
    expect(api.saveSchedules).toHaveBeenCalledWith('default', [schedule]);

    api.saveTuning.mockReturnValue(of(saved));
    store.saveTuning(7200, 600);
    expect(api.saveTuning).toHaveBeenCalledWith(7200, 600);

    api.disablePool.mockReturnValue(of(saved));
    store.disablePool('gpu');
    expect(api.disablePool).toHaveBeenCalledWith('gpu');
    expect(snackBar.open).not.toHaveBeenCalled();

    api.disablePool.mockReturnValueOnce(throwError(() => new Error('pool locked')));
    store.disablePool('gpu');
    expect(snackBar.open).toHaveBeenCalled();
  });

  it('tracks ladderError per pool and clears it before saving', () => {
    api.saveLadder.mockReturnValueOnce(throwError(() => new Error('invalid rung')));
    store.saveLadder('gpu', ['bad-shape']);
    expect(store.ladderError()).toEqual({pool: 'gpu', message: 'invalid rung'});

    api.saveLadder.mockReturnValueOnce(of(makePolicy()));
    store.saveLadder('gpu', ['g2-standard-8']);
    expect(store.ladderError()).toBeNull();
  });

  it('toggles live polling flag cleanly', () => {
    expect(store.live()).toBe(true);
    store.toggleLive();
    expect(store.live()).toBe(false);
    store.toggleLive();
    expect(store.live()).toBe(true);
  });
});
