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

import {provideZonelessChangeDetection} from '@angular/core';
import {TestBed} from '@angular/core/testing';
import {MatSnackBar} from '@angular/material/snack-bar';
import {provideRouter} from '@angular/router';
import {of, throwError} from 'rxjs';
import {beforeEach, describe, expect, it, vi} from 'vitest';

import type {Operation} from '../models/operation.model';
import type {Snapshot} from '../models/snapshot.model';
import {DeviceApiService} from '../services/device-api.service';
import {DeviceStore} from './device.signal-store';

function operation(overrides: Partial<Operation> = {}): Operation {
  return {
    id: 'op-1',
    name: 'operations/op-1',
    done: false,
    metadata: {
      verb: 'device.restart',
      resourceType: 'device',
      resourceId: 'device-1',
      createTime: new Date('2026-07-26T10:00:00Z'),
    },
    ...overrides,
  };
}

/**
 * A restart and a powerwash leave the device row byte-identical, so the operation
 * list is the ONLY place their outcome appears. These tests pin that the store
 * actually holds one. A store that silently drops the handle looks exactly like a
 * working one until a verb fails.
 */
describe('DeviceStore operation handles', () => {
  const target = {deviceId: 'device-1', instance: 'watch'};
  let api: {
    list: ReturnType<typeof vi.fn>;
    restart: ReturnType<typeof vi.fn>;
    powerwash: ReturnType<typeof vi.fn>;
    listOperations: ReturnType<typeof vi.fn>;
  };
  let snackBar: {open: ReturnType<typeof vi.fn>};

  beforeEach(() => {
    api = {
      list: vi.fn(() => of({items: [], total: 0, limit: 20, offset: 0})),
      restart: vi.fn(() => of(operation())),
      powerwash: vi.fn(() => of(operation({id: 'op-2', name: 'operations/op-2'}))),
      listOperations: vi.fn(() => of([])),
    };
    snackBar = {open: vi.fn()};

    TestBed.configureTestingModule({
      providers: [
        provideZonelessChangeDetection(),
        provideRouter([]),
        {provide: DeviceApiService, useValue: api},
        {provide: MatSnackBar, useValue: snackBar},
      ],
    });
  });

  it('holds the handle a restart returns so the card can show it in flight', () => {
    const store = TestBed.inject(DeviceStore);
    store.restart(target);

    expect(api.restart).toHaveBeenCalledWith(target);
    expect(store.operations().map(op => op.id)).toEqual(['op-1']);
    expect(store.activeOperations()).toHaveLength(1);
  });

  it('prepends a new handle so the newest verb is first', () => {
    const store = TestBed.inject(DeviceStore);
    store.restart(target);
    store.powerwash(target);

    expect(store.operations().map(op => op.id)).toEqual(['op-2', 'op-1']);
  });

  it('a rejected verb reports rather than fabricating a handle', () => {
    api.restart = vi.fn(() => throwError(() => new Error('device is busy')));
    const store = TestBed.inject(DeviceStore);
    store.restart(target);

    expect(store.operations()).toHaveLength(0);
    expect(snackBar.open).toHaveBeenCalled();
  });

  it('separates settled failures from work still in flight', () => {
    api.listOperations = vi.fn(() =>
      of([
        operation({id: 'running-op', done: false}),
        operation({id: 'ok-op', done: true}),
        operation({
          id: 'failed-op',
          done: true,
          error: {title: 'powerwash failed', status: 500, detail: 'host unreachable'},
        }),
      ])
    );
    const store = TestBed.inject(DeviceStore);
    store.loadOperations('device-1');

    expect(store.operations()).toHaveLength(3);
    expect(store.activeOperations().map(op => op.id)).toEqual(['running-op']);
    // A settled success must NOT show up as a failure: `done` alone cannot tell
    // the two apart, which is why the error field exists.
    expect(store.failedOperations().map(op => op.id)).toEqual(['failed-op']);
  });

  it('a failed operations read leaves the page intact rather than surfacing an error', () => {
    api.listOperations = vi.fn(() => throwError(() => new Error('boom')));
    const store = TestBed.inject(DeviceStore);
    store.loadOperations('device-1');

    expect(store.operations()).toHaveLength(0);
    expect(snackBar.open).not.toHaveBeenCalled();
  });
});

function snapshot(overrides: Partial<Snapshot> = {}): Snapshot {
  return {
    id: 'snap-1',
    deviceId: 'device-1',
    hostId: 'host-1',
    state: 'ready',
    createdAt: new Date('2026-07-30T10:00:00Z'),
    updatedAt: new Date('2026-07-30T10:00:00Z'),
    ...overrides,
  };
}

/**
 * The inventory is server state: a delete pushes its handle and re-reads the
 * list, so what the card renders is what the backend recorded.
 */
describe('DeviceStore snapshots', () => {
  let api: {
    list: ReturnType<typeof vi.fn>;
    listSnapshots: ReturnType<typeof vi.fn>;
    deleteSnapshot: ReturnType<typeof vi.fn>;
  };
  let snackBar: {open: ReturnType<typeof vi.fn>};

  beforeEach(() => {
    api = {
      list: vi.fn(() => of({items: [], total: 0, limit: 20, offset: 0})),
      listSnapshots: vi.fn(() => of([snapshot()])),
      deleteSnapshot: vi.fn(() => of(operation({id: 'op-2', name: 'operations/op-2'}))),
    };
    snackBar = {open: vi.fn()};

    TestBed.configureTestingModule({
      providers: [
        provideZonelessChangeDetection(),
        provideRouter([]),
        {provide: DeviceApiService, useValue: api},
        {provide: MatSnackBar, useValue: snackBar},
      ],
    });
  });

  it('load replaces the inventory with what the server lists', () => {
    const store = TestBed.inject(DeviceStore);
    store.loadSnapshots('device-1');

    expect(api.listSnapshots).toHaveBeenCalledWith('device-1');
    expect(store.snapshots().map(s => s.id)).toEqual(['snap-1']);
  });

  it('delete holds the handle and re-reads the inventory after admitting', () => {
    const store = TestBed.inject(DeviceStore);
    api.listSnapshots = vi.fn(() => of([]));
    store.deleteSnapshot({deviceId: 'device-1', snapshotId: 'snap-1'});

    expect(api.deleteSnapshot).toHaveBeenCalledWith('device-1', 'snap-1');
    expect(store.operations().map(op => op.id)).toEqual(['op-2']);
    expect(store.snapshots()).toHaveLength(0);
  });

  it('a refused delete reports without fabricating a handle', () => {
    api.deleteSnapshot = vi.fn(() => throwError(() => new Error('snapshot is creating')));
    const store = TestBed.inject(DeviceStore);
    store.deleteSnapshot({deviceId: 'device-1', snapshotId: 'snap-1'});

    expect(store.operations()).toHaveLength(0);
    expect(snackBar.open).toHaveBeenCalled();
  });
});
