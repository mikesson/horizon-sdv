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

import {HttpErrorResponse} from '@angular/common/http';
import {provideZonelessChangeDetection} from '@angular/core';
import {TestBed} from '@angular/core/testing';
import {MatSnackBar} from '@angular/material/snack-bar';
import {provideRouter} from '@angular/router';
import {of} from 'rxjs';
import {beforeEach, describe, expect, it, vi} from 'vitest';
import {createApiErrorHandler} from '../../shared/utils';
import type {Host} from '../models/host.model';
import {HostApiService} from '../services/host-api.service';
import {HostStore} from './host.signal-store';

function makeHost(overrides: Partial<Host> = {}): Host {
  const base: Partial<Host> = {
    id: 'host-1',
    name: 'test-host',
    status: 'running',
    hostClass: 'reserved',
    lifecycle: {
      mode: 'ephemeral',
      ttlSeconds: 36000,
      expiresAt: new Date('2099-01-01T12:00:00Z'),
      protectedDeviceCount: 0,
    },
    ...overrides,
  };
  return base as Host;
}

describe('HostStore.extend', () => {
  let api: {extend: ReturnType<typeof vi.fn>; get: ReturnType<typeof vi.fn>};
  let snackBar: {open: ReturnType<typeof vi.fn>};

  beforeEach(() => {
    api = {extend: vi.fn(), get: vi.fn(() => of(makeHost()))};
    snackBar = {open: vi.fn()};
    TestBed.configureTestingModule({
      providers: [
        provideZonelessChangeDetection(),
        provideRouter([]),
        {provide: HostApiService, useValue: api},
        {provide: MatSnackBar, useValue: snackBar},
      ],
    });
  });

  function seededStore(): InstanceType<typeof HostStore> {
    const store = TestBed.inject(HostStore);
    store.loadOne('host-1');
    return store;
  }

  it('replaces the host with the one the server returns', () => {
    const raised = new Date('2099-01-03T12:00:00Z');
    api.extend = vi.fn(() =>
      of(
        makeHost({
          lifecycle: {
            mode: 'ephemeral',
            ttlSeconds: 36000,
            expiresAt: raised,
            protectedDeviceCount: 0,
          },
        })
      )
    );
    const store = seededStore();
    store.extend({id: 'host-1', hours: 48});

    expect(api.extend).toHaveBeenCalledWith('host-1', 48);
    expect(store.entityMap()['host-1'].lifecycle?.expiresAt).toEqual(raised);
  });

  // Run through the real error handler so the test covers the path a 409
  // takes in the app, from problem+json to the snackbar text.
  it('shows the server refusal verbatim and leaves the host unchanged', () => {
    const detail =
      'host host-1 is a fleet host: the orchestrator manages its lease and would overwrite an extend.';
    const response = new HttpErrorResponse({
      status: 409,
      error: {title: 'Conflict', status: 409, detail},
    });
    api.extend = vi.fn(() => createApiErrorHandler('Host', 'HostApiService')(response));
    const store = seededStore();
    store.extend({id: 'host-1', hours: 24});

    expect(snackBar.open).toHaveBeenCalledWith(detail, expect.anything(), expect.anything());
    expect(store.error()).toBe(detail);
    expect(store.entityMap()['host-1'].lifecycle?.expiresAt).toEqual(
      new Date('2099-01-01T12:00:00Z')
    );
  });
});
