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
import type {Batch, BatchMember, CreateBatchRequest} from '../models/batch.model';
import {BatchApiService} from '../services/batch-api.service';
import {BatchStore} from './batch.signal-store';

function makeBatch(overrides: Partial<Batch> = {}): Batch {
  return {
    id: 'batch-1',
    namePrefix: 'ci',
    status: 'ready',
    total: 2,
    running: 2,
    errored: 0,
    reconciling: false,
    ...overrides,
  };
}

describe('BatchStore', () => {
  let api: {
    listAll: ReturnType<typeof vi.fn>;
    create: ReturnType<typeof vi.fn>;
    members: ReturnType<typeof vi.fn>;
    delete: ReturnType<typeof vi.fn>;
  };
  let snackBar: {open: ReturnType<typeof vi.fn>};

  beforeEach(() => {
    api = {
      listAll: vi.fn(() =>
        of({
          items: [
            makeBatch({id: 'b-prov', status: 'provisioning', running: 0}),
            makeBatch({id: 'b-ready', status: 'ready'}),
            makeBatch({id: 'b-deg', status: 'degraded', running: 1, errored: 1}),
            makeBatch({id: 'b-fail', status: 'failed', running: 0, errored: 2}),
          ],
          total: 4,
          complete: true,
        })
      ),
      create: vi.fn(() => of(makeBatch({id: 'b-new', status: 'provisioning'}))),
      members: vi.fn(() =>
        of<BatchMember[]>([
          {
            deviceId: 'd-1',
            name: 'ci-000',
            status: 'running',
            hostId: 'h-1',
            adbSerial: '10.0.0.1:6520',
          },
        ])
      ),
      delete: vi.fn(() => of(void 0)),
    };
    snackBar = {open: vi.fn()};

    TestBed.configureTestingModule({
      providers: [
        provideZonelessChangeDetection(),
        provideRouter([
          {path: 'batches', children: []},
          {path: 'batches/:id', children: []},
        ]),
        {provide: BatchApiService, useValue: api},
        {provide: MatSnackBar, useValue: snackBar},
      ],
    });
  });

  it('loads batches and computes liveCount and failedCount accurately', () => {
    const store = TestBed.inject(BatchStore);
    store.loadAll({mine: true});

    expect(api.listAll).toHaveBeenCalledWith({mine: true});
    expect(store.entities()).toHaveLength(4);
    expect(store.liveCount()).toBe(3); // provisioning + ready + degraded
    expect(store.failedCount()).toBe(1);
  });

  it('creates a batch with an idempotency key and appends it to entities', () => {
    const store = TestBed.inject(BatchStore);
    const req: CreateBatchRequest = {
      namePrefix: 'nightly',
      count: 2,
      minReady: 2,
      fillPolicy: 'atomic',
      placement: 'isolated',
      requirements: {interaction: 'headless'},
    };

    store.create({request: req, idempotencyKey: 'idem-123'});

    expect(api.create).toHaveBeenCalledWith(req, 'idem-123');
    expect(store.entities().some(b => b.id === 'b-new')).toBe(true);
    expect(snackBar.open).toHaveBeenCalled();
  });

  it('loads batch members and handles delete', () => {
    const store = TestBed.inject(BatchStore);

    store.loadAll({});
    store.loadMembers('b-ready');
    expect(api.members).toHaveBeenCalledWith('b-ready');
    expect(store.members()).toHaveLength(1);
    expect(store.members()[0].deviceId).toBe('d-1');

    store.delete('b-ready');
    expect(api.delete).toHaveBeenCalledWith('b-ready');
    expect(store.entities().some(b => b.id === 'b-ready')).toBe(false);
  });

  it('records error state when batch creation fails', () => {
    api.create.mockReturnValue(throwError(() => new Error('quota exceeded')));
    const store = TestBed.inject(BatchStore);

    store.create({
      request: {
        namePrefix: 'fail',
        count: 1,
        minReady: 1,
        fillPolicy: 'best_effort',
        placement: 'pack',
        requirements: {interaction: 'headless'},
      },
      idempotencyKey: 'idem-fail',
    });

    expect(store.error()).toContain('quota exceeded');
    expect(store.entities()).toHaveLength(0);
  });
});
