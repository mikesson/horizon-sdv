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

import {TestBed} from '@angular/core/testing';
import {MatDialog} from '@angular/material/dialog';
import {Router} from '@angular/router';
import {of} from 'rxjs';
import {beforeEach, describe, expect, it, vi} from 'vitest';
import {AuthStore} from '../../auth/store/auth.signal-store';
import type {Host} from '../models/host.model';
import {HostStore} from '../store/host.signal-store';
import {HostActionsService} from './host-actions.service';

function makeHost(overrides: Partial<Host> = {}): Host {
  const base: Partial<Host> = {id: 'host-1', name: 'test-host', status: 'running', ...overrides};
  return base as Host;
}

describe('HostActionsService', () => {
  const store = {
    start: vi.fn(),
    stop: vi.fn(),
    sync: vi.fn(),
    retry: vi.fn(),
    delete: vi.fn(),
    deleteMany: vi.fn(),
    updateMaxDevices: vi.fn(),
    extend: vi.fn(),
  };
  const router = {navigate: vi.fn()};
  const authStore = {user: () => ({email: 'tester@example.com', displayName: 'Tester'})};

  // afterClosed() emits this; flip per test to simulate confirm/cancel.
  let dialogResult: unknown = true;
  const dialog = {open: vi.fn(() => ({afterClosed: () => of(dialogResult)}))};

  let service: HostActionsService;

  beforeEach(() => {
    vi.clearAllMocks();
    dialogResult = true;
    TestBed.configureTestingModule({
      providers: [
        HostActionsService,
        {provide: HostStore, useValue: store},
        {provide: AuthStore, useValue: authStore},
        {provide: MatDialog, useValue: dialog},
        {provide: Router, useValue: router},
      ],
    });
    service = TestBed.inject(HostActionsService);
  });

  it('start and sync dispatch immediately with no dialog', () => {
    service.start(makeHost());
    service.sync(makeHost());
    expect(store.start).toHaveBeenCalledWith('host-1');
    expect(store.sync).toHaveBeenCalledWith('host-1');
    expect(dialog.open).not.toHaveBeenCalled();
  });

  it('stop dispatches only after the confirm dialog resolves true', () => {
    service.stop(makeHost());
    expect(store.stop).toHaveBeenCalledWith('host-1');
  });

  it('stop does nothing when the dialog is cancelled', () => {
    dialogResult = false;
    service.stop(makeHost());
    expect(store.stop).not.toHaveBeenCalled();
  });

  it('retry defaults fallbackToStandard to false and forwards an override', () => {
    service.retry(makeHost());
    expect(store.retry).toHaveBeenCalledWith({id: 'host-1', request: {fallbackToStandard: false}});

    service.retry(makeHost(), true);
    expect(store.retry).toHaveBeenLastCalledWith({
      id: 'host-1',
      request: {fallbackToStandard: true},
    });
  });

  it('delete navigates to the list only when navigateToListAfter is set', () => {
    service.delete(makeHost());
    expect(store.delete).toHaveBeenCalledWith('host-1');
    expect(router.navigate).not.toHaveBeenCalled();

    service.delete(makeHost(), {navigateToListAfter: true});
    expect(router.navigate).toHaveBeenCalledWith(['/hosts']);
  });

  it('delete warns when the host was created by someone else', () => {
    service.delete(makeHost({createdBy: 'other@example.com'}));
    expect(dialog.open).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({
        data: expect.objectContaining({warning: 'Created by other@example.com, another user.'}),
      })
    );
  });

  it('delete carries no warning for own hosts', () => {
    service.delete(makeHost({createdBy: 'tester@example.com'}));
    expect(dialog.open).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({data: expect.objectContaining({warning: undefined})})
    );
  });

  it('deleteMany dispatches every id after the confirm dialog resolves true', () => {
    service.deleteMany([makeHost({id: 'host-1'}), makeHost({id: 'host-2'})]);
    expect(store.deleteMany).toHaveBeenCalledWith(['host-1', 'host-2']);
  });

  it('deleteMany does nothing for an empty selection and opens no dialog', () => {
    service.deleteMany([]);
    expect(dialog.open).not.toHaveBeenCalled();
    expect(store.deleteMany).not.toHaveBeenCalled();
  });

  it('deleteMany does nothing when the dialog is cancelled', () => {
    dialogResult = false;
    service.deleteMany([makeHost()]);
    expect(store.deleteMany).not.toHaveBeenCalled();
  });

  it('deleteMany warns with a count of foreign-created hosts', () => {
    service.deleteMany([
      makeHost({id: 'host-1', createdBy: 'other@example.com'}),
      makeHost({id: 'host-2', createdBy: 'another@example.com'}),
    ]);
    expect(dialog.open).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({
        data: expect.objectContaining({warning: '2 of these were created by someone else.'}),
      })
    );
  });

  describe('editCapacity', () => {
    it('dispatches the entered limit', () => {
      dialogResult = '8';
      service.editCapacity(makeHost({maxDevices: 4, currentDevices: 2}));
      expect(store.updateMaxDevices).toHaveBeenCalledWith({id: 'host-1', maxDevices: 8});
    });

    it('dispatches nothing when the dialog is dismissed', () => {
      dialogResult = null;
      service.editCapacity(makeHost({maxDevices: 4, currentDevices: 2}));
      expect(store.updateMaxDevices).not.toHaveBeenCalled();
    });

    it('tells the operator what the floor is', () => {
      dialogResult = null;
      service.editCapacity(makeHost({maxDevices: 4, currentDevices: 3}));
      const call = dialog.open.mock.calls.at(-1) as unknown as [unknown, {data: {message: string}}];
      expect(call[1].data.message).toContain('3');
    });
  });

  describe('extend', () => {
    const expiresAt = new Date('2099-01-01T12:00:00Z');
    const leased = (): Host =>
      makeHost({
        lifecycle: {mode: 'ephemeral', ttlSeconds: 36000, expiresAt, protectedDeviceCount: 0},
        vmTerminatesAt: new Date('2099-01-20T12:00:00Z'),
      });

    it('dispatches the hours the dialog returns', () => {
      dialogResult = 48;
      service.extend(leased());
      expect(store.extend).toHaveBeenCalledWith({id: 'host-1', hours: 48});
    });

    it('dispatches nothing when the dialog is dismissed', () => {
      dialogResult = undefined;
      service.extend(leased());
      expect(store.extend).not.toHaveBeenCalled();
    });

    it('hands the dialog the expiry and the VM stop time to preview against', () => {
      dialogResult = undefined;
      service.extend(leased());
      expect(dialog.open).toHaveBeenCalledWith(
        expect.anything(),
        expect.objectContaining({
          data: {
            hostName: 'test-host',
            expiresAt,
            vmTerminatesAt: new Date('2099-01-20T12:00:00Z'),
          },
        })
      );
    });
  });
});
