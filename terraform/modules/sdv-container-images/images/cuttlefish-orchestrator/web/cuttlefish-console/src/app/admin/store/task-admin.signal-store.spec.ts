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
import type {FailedTask} from '../models/task-admin.model';
import {TaskAdminApiService} from '../services/task-admin-api.service';
import {TaskAdminStore} from './task-admin.signal-store';

function makeTask(id: string, type = 'device.create_cvd'): FailedTask {
  return {
    id,
    type,
    resourceId: `res-${id}`,
    attempts: 3,
    maxAttempts: 3,
    lastError: 'timeout',
    updatedAt: new Date('2026-05-01T12:00:00Z'),
  };
}

describe('TaskAdminStore', () => {
  let api: {
    listFailed: ReturnType<typeof vi.fn>;
    retry: ReturnType<typeof vi.fn>;
    delete: ReturnType<typeof vi.fn>;
    refreshStats: ReturnType<typeof vi.fn>;
  };
  let snackBar: {open: ReturnType<typeof vi.fn>};
  let store: InstanceType<typeof TaskAdminStore>;

  beforeEach(() => {
    api = {
      listFailed: vi.fn(() => of([makeTask('t1'), makeTask('t2', 'host.provision')])),
      retry: vi.fn(() => of(undefined)),
      delete: vi.fn(() => of(undefined)),
      refreshStats: vi.fn(),
    };
    snackBar = {open: vi.fn()};
    TestBed.configureTestingModule({
      providers: [
        provideZonelessChangeDetection(),
        {provide: TaskAdminApiService, useValue: api},
        {provide: MatSnackBar, useValue: snackBar},
      ],
    });
    store = TestBed.inject(TaskAdminStore);
  });

  it('loads dead-letter tasks and retries/deletes entries with stats refresh', () => {
    store.loadAll();
    expect(store.tasks().map(t => t.id)).toEqual(['t1', 't2']);

    store.retry(makeTask('t1'));
    expect(api.retry).toHaveBeenCalledWith('t1');
    expect(api.refreshStats).toHaveBeenCalledTimes(1);
    expect(store.tasks().map(t => t.id)).toEqual(['t2']);

    store.delete(makeTask('t2', 'host.provision'));
    expect(api.delete).toHaveBeenCalledWith('t2');
    expect(api.refreshStats).toHaveBeenCalledTimes(2);
    expect(store.tasks()).toEqual([]);
  });

  it('keeps task list intact and reports errors on load/retry/delete failures', () => {
    store.loadAll();

    api.retry.mockReturnValueOnce(throwError(() => new Error('retry refused')));
    store.retry(makeTask('t1'));
    expect(store.tasks().map(t => t.id)).toEqual(['t1', 't2']);
    expect(snackBar.open).toHaveBeenCalledWith(
      expect.stringContaining('retry'),
      'Close',
      expect.anything()
    );

    api.listFailed.mockReturnValueOnce(throwError(() => new Error('list boom')));
    store.loadAll();
    expect(store.error()).toContain('list boom');
  });
});
