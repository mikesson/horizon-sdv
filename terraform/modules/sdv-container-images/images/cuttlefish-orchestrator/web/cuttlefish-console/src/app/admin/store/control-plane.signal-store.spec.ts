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
import {of, throwError} from 'rxjs';
import {beforeEach, describe, expect, it, vi} from 'vitest';
import type {Operation} from '../../devices/models/operation.model';
import {OperationsApiService} from '../../shared/services/operations-api.service';
import type {ControlPlaneSnapshot} from '../models/control-plane.model';
import {TaskAdminApiService} from '../services/task-admin-api.service';
import {ControlPlaneStore} from './control-plane.signal-store';

function makeSnapshot(overrides: Partial<ControlPlaneSnapshot> = {}): ControlPlaneSnapshot {
  return {
    now: new Date(),
    workersPerInstance: 4,
    leaders: [
      {
        name: 'background-plane',
        holder: 'orch-pod-a',
        acquiredAt: new Date('2026-08-01T11:00:00Z'),
        expiry: new Date('2026-08-01T12:00:30Z'),
        renewedAt: new Date('2026-08-01T11:59:58Z'),
      },
    ],
    sweeps: {
      deleteOrphanVms: false,
      orphanCvds: false,
      zombieRecreate: false,
      reaperDryRun: false,
      isSet: false,
    },
    queue: {
      pending: 1,
      leased: 1,
      failed: 0,
      oldestPendingAgeSeconds: 5,
    },
    typeCounts: [{type: 'create_device', state: 'leased', count: 1}],
    liveTasks: [
      {
        id: 't-leased',
        type: 'create_device',
        state: 'leased',
        resourceId: 'dev-1',
        attempts: 1,
        maxAttempts: 3,
        priority: 0,
        availableAt: new Date('2026-08-01T11:59:00Z'),
        leaseExpiry: new Date('2026-08-01T12:05:00Z'),
        leaseHolder: 'orch-pod-b',
        lastError: '',
        updatedAt: new Date('2026-08-01T11:59:00Z'),
      },
      {
        id: 't-pending-retry',
        type: 'delete_host',
        state: 'pending',
        resourceId: 'host-1',
        attempts: 2,
        maxAttempts: 3,
        priority: -100,
        availableAt: new Date('2026-08-01T12:00:00Z'),
        leaseExpiry: null,
        leaseHolder: '',
        lastError: 'quota wait',
        updatedAt: new Date('2026-08-01T11:58:00Z'),
      },
    ],
    connectivity: {
      ingressMode: 'public',
      transportPolicy: 'all',
      stunServers: ['stun:stun.l.google.com:19302'],
      turnClientServers: [],
      turnDeviceServers: [],
      turnRealm: '',
      credentialsMintable: false,
      credentialTtlSeconds: 3600,
      problems: [],
    },
    fleet: {
      status: {
        mode: 'off',
        target: 0,
        standby: 0,
        draining: 0,
        misses: 0,
        demand: {
          servable: 0,
          unservable: 0,
          gpuRequired: 0,
          needsLargerHost: 0,
          oversizedGroup: 0,
        },
        schedule: 0,
        floor: 0,
        total: 0,
        signal: 'standby',
        limitedBy: '',
        partial: false,
      },
      policy: {
        mode: 'off',
        tiers: {},
        floor: 0,
        demandHorizonSec: 3600,
        drainGraceSec: 480,
        shapeLadder: [],
        isSet: false,
      },
      deviceSlots: 0,
      gpuDegradation: {degraded: 0, wanted: 0},
    },
    ...overrides,
  };
}

function makeOperation(id = 'op-1'): Operation {
  return {
    id,
    name: `operations/${id}`,
    done: false,
    metadata: {
      verb: 'create',
      resourceType: 'device',
      resourceId: 'dev-1',
      createTime: new Date('2026-08-01T11:59:00Z'),
    },
  };
}

describe('ControlPlaneStore', () => {
  const taskApi = {controlPlane: vi.fn()};
  const operationsApi = {listOpen: vi.fn()};

  let store: InstanceType<typeof ControlPlaneStore>;

  beforeEach(() => {
    vi.clearAllMocks();
    taskApi.controlPlane.mockReturnValue(of(makeSnapshot()));
    operationsApi.listOpen.mockReturnValue(of([makeOperation('op-1')]));

    TestBed.configureTestingModule({
      providers: [
        provideZonelessChangeDetection(),
        ControlPlaneStore,
        {provide: TaskAdminApiService, useValue: taskApi},
        {provide: OperationsApiService, useValue: operationsApi},
      ],
    });
    store = TestBed.inject(ControlPlaneStore);
  });

  it('computes inFlight, waiting, retryingCount, knownInstances, and diff edgeActivity across polls', () => {
    store.refresh();

    expect(store.loading()).toBe(false);
    expect(store.error()).toBeNull();
    expect(store.operationsError()).toBeNull();
    expect(store.openOperations().map(o => o.name)).toEqual(['operations/op-1']);
    expect(store.inFlight().map(t => t.id)).toEqual(['t-leased']);
    expect(store.waiting().map(t => t.id)).toEqual(['t-pending-retry']);
    expect(store.retryingCount()).toBe(1);
    expect(store.knownInstances()).toBe(2);
    expect(store.edgeActivity().intent).toBe(false);
    expect(store.history().length).toBe(1);

    // Second snapshot: t-leased vanished (complete), t-pending-retry leased (claim),
    // plus one new user intent (attempts=0, priority=0) and one new reconciler task (priority<0).
    taskApi.controlPlane.mockReturnValueOnce(
      of(
        makeSnapshot({
          liveTasks: [
            {
              id: 't-pending-retry',
              type: 'delete_host',
              state: 'leased',
              resourceId: 'host-1',
              attempts: 2,
              maxAttempts: 3,
              priority: -100,
              availableAt: new Date('2026-08-01T12:00:00Z'),
              leaseExpiry: new Date('2026-08-01T12:05:00Z'),
              leaseHolder: 'orch-pod-b',
              lastError: '',
              updatedAt: new Date('2026-08-01T12:00:05Z'),
            },
            {
              id: 't-new-intent',
              type: 'create_device',
              state: 'pending',
              resourceId: 'dev-2',
              attempts: 0,
              maxAttempts: 3,
              priority: 0,
              availableAt: new Date('2026-08-01T12:00:05Z'),
              leaseExpiry: null,
              leaseHolder: '',
              lastError: '',
              updatedAt: new Date('2026-08-01T12:00:05Z'),
            },
            {
              id: 't-new-reconcile',
              type: 'reconcile_host',
              state: 'pending',
              resourceId: 'host-2',
              attempts: 0,
              maxAttempts: 3,
              priority: -100,
              availableAt: new Date('2026-08-01T12:00:05Z'),
              leaseExpiry: null,
              leaseHolder: '',
              lastError: '',
              updatedAt: new Date('2026-08-01T12:00:05Z'),
            },
          ],
        })
      )
    );
    store.refresh();

    expect(store.edgeActivity().intent).toBe(true);
    expect(store.edgeActivity().reconcile).toBe(true);
    expect(store.edgeActivity().claim).toBe(true);
    expect(store.edgeActivity().complete).toBe(true);
    expect(store.history().length).toBe(2);
  });

  it('preserves previous openOperations and sets operationsError when listOpen fails', () => {
    store.refresh();
    expect(store.openOperations().length).toBe(1);

    operationsApi.listOpen.mockReturnValue(throwError(() => new Error('ops unavailable')));
    store.refresh();

    expect(store.operationsError()).toBe('Could not refresh in-flight operations');
    expect(store.openOperations().length).toBe(1);
    expect(store.history().length).toBe(2);
  });

  it('sets top-level error when controlPlane() fails', () => {
    taskApi.controlPlane.mockReturnValue(throwError(() => new Error('spanner unavailable')));

    store.refresh();

    expect(store.loading()).toBe(false);
    expect(store.error()).toContain('spanner unavailable');
  });

  it('toggles live flag cleanly', () => {
    expect(store.live()).toBe(true);
    store.toggleLive();
    expect(store.live()).toBe(false);
    store.toggleLive();
    expect(store.live()).toBe(true);
  });
});
