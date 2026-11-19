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
import type {AuditEntry} from '../models/audit.model';
import {AuditApiService} from '../services/audit-api.service';
import {ActivityStore} from './activity.signal-store';

function makeEntry(id: string): AuditEntry {
  return {
    id,
    timestamp: new Date('2026-05-01T12:00:00Z'),
    actorEmail: 'op@example.com',
    action: 'device.create',
    resourceType: 'device',
    resourceId: `dev-${id}`,
    changes: {},
    metadata: {},
  };
}

describe('ActivityStore', () => {
  let api: {list: ReturnType<typeof vi.fn>};
  let store: InstanceType<typeof ActivityStore>;

  beforeEach(() => {
    api = {list: vi.fn()};
    TestBed.configureTestingModule({
      providers: [provideZonelessChangeDetection(), {provide: AuditApiService, useValue: api}],
    });
    store = TestBed.inject(ActivityStore);
  });

  it('loads first page with filter, computes hasMore, and appends via loadMore', () => {
    api.list.mockReturnValueOnce(of({items: [makeEntry('1'), makeEntry('2')], total: 3}));
    store.load({action: 'device.create'});

    expect(api.list).toHaveBeenCalledWith({action: 'device.create'}, 50, 0);
    expect(store.entries().map(e => e.id)).toEqual(['1', '2']);
    expect(store.hasMore()).toBe(true);
    expect(store.loading()).toBe(false);

    api.list.mockReturnValueOnce(of({items: [makeEntry('3')], total: 3}));
    store.loadMore();

    expect(api.list).toHaveBeenLastCalledWith({action: 'device.create'}, 50, 2);
    expect(store.entries().map(e => e.id)).toEqual(['1', '2', '3']);
    expect(store.hasMore()).toBe(false);
  });

  it('captures error messages on load and loadMore failures', () => {
    api.list.mockReturnValueOnce(throwError(() => new Error('backend unavailable')));
    store.load({});
    expect(store.error()).toContain('backend unavailable');
    expect(store.loading()).toBe(false);

    api.list.mockReturnValueOnce(throwError(() => new Error('page timeout')));
    store.loadMore();
    expect(store.error()).toContain('page timeout');
  });
});
