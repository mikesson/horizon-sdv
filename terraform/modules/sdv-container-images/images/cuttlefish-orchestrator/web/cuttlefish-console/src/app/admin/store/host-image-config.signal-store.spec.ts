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
import type {HostImageConfig} from '../models/host-image-config.model';
import {HostImageConfigApiService} from '../services/host-image-config-api.service';
import {HostImageConfigStore} from './host-image-config.signal-store';

function makeConfig(machineFamily: string, enabled = true): HostImageConfig {
  return {
    id: machineFamily,
    machineFamily,
    imageFamily: `cco-${machineFamily}`,
    enabled,
    createdAt: new Date('2026-05-01T12:00:00Z'),
    updatedAt: new Date('2026-05-01T12:00:00Z'),
  };
}

describe('HostImageConfigStore', () => {
  let api: {
    list: ReturnType<typeof vi.fn>;
    create: ReturnType<typeof vi.fn>;
    update: ReturnType<typeof vi.fn>;
    delete: ReturnType<typeof vi.fn>;
  };
  let store: InstanceType<typeof HostImageConfigStore>;

  beforeEach(() => {
    api = {
      list: vi.fn(() =>
        of({
          items: [makeConfig('n2', true), makeConfig('g2', false)],
          limit: 50,
          offset: 0,
          total: 2,
        })
      ),
      create: vi.fn(),
      update: vi.fn(),
      delete: vi.fn(),
    };
    TestBed.configureTestingModule({
      providers: [
        provideZonelessChangeDetection(),
        {provide: HostImageConfigApiService, useValue: api},
      ],
    });
    store = TestBed.inject(HostImageConfigStore);
  });

  it('loads configs once unless forceReload is set and computes stats and categories', () => {
    store.loadAll(undefined);
    expect(store.isLoaded()).toBe(true);
    expect(store.stats()).toEqual({total: 2, enabled: 1, disabled: 1});
    expect(store.enabledConfigs().map(c => c.machineFamily)).toEqual(['n2']);
    expect(store.byCategory().some(c => c.category === 'gpus')).toBe(true);
    expect(store.getByFamily('n2')?.imageFamily).toBe('cco-n2');

    // Cached load skips API until forceReload is true.
    store.loadAll(undefined);
    expect(api.list).toHaveBeenCalledTimes(1);

    store.loadAll({forceReload: true});
    expect(api.list).toHaveBeenCalledTimes(2);
  });

  it('creates, updates, deletes, and handles errors with clearError', () => {
    store.loadAll(undefined);
    const created = makeConfig('c4a', true);
    api.create.mockReturnValueOnce(of(created));
    store.create({machineFamily: 'c4a', imageFamily: 'cco-arm', enabled: true});
    expect(store.getByFamily('c4a')).toEqual(created);

    api.update.mockReturnValueOnce(of({...created, enabled: false}));
    store.update({machineFamily: 'c4a', changes: {imageFamily: 'cco-arm', enabled: false}});
    expect(store.getByFamily('c4a')?.enabled).toBe(false);

    api.delete.mockReturnValueOnce(of(undefined));
    store.delete('c4a');
    expect(store.getByFamily('c4a')).toBeUndefined();

    api.create.mockReturnValueOnce(throwError(() => new Error('duplicate family')));
    store.create({machineFamily: 'n2', imageFamily: 'dup', enabled: true});
    expect(store.error()).toContain('duplicate family');
    store.clearError();
    expect(store.error()).toBeNull();
  });
});
