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
import {MatSnackBar} from '@angular/material/snack-bar';
import {Router} from '@angular/router';
import {of, throwError} from 'rxjs';
import {beforeEach, describe, expect, it, vi} from 'vitest';
import {AuthStore} from '../../auth/store/auth.signal-store';
import type {Device} from '../models/device.model';
import {DeviceStore} from '../store/device.signal-store';
import {DeviceActionsService} from './device-actions.service';
import {DeviceApiService} from './device-api.service';

// The instance the interactive verbs address. The caller names it; these
// actions no longer pick one.
const TARGET = {deviceId: 'dev-1', instance: 'my-device'};

function makeDevice(overrides: Partial<Device> = {}): Device {
  const base: Partial<Device> = {
    id: 'dev-1',
    name: 'my-device',
    hostId: 'host-1',
    status: 'running',
    instances: [{name: 'my-device'}],
    ...overrides,
  };
  return base as Device;
}

describe('DeviceActionsService', () => {
  const store = {
    start: vi.fn(),
    stop: vi.fn(),
    restart: vi.fn(),
    powerwash: vi.fn(),
    powerbtn: vi.fn(),
    recordStart: vi.fn(),
    recordStop: vi.fn(),
    delete: vi.fn(),
    deleteMany: vi.fn(),
    pin: vi.fn(),
    unpin: vi.fn(),
    extend: vi.fn(),
  };
  const deviceApi = {
    getScreenshot: vi.fn(() =>
      of({screenshot_bytes: btoa('img'), screenshot_mime_type: 'image/png'})
    ),
  };
  const router = {navigate: vi.fn()};
  const snackBar = {open: vi.fn(() => ({onAction: () => of(undefined)}))};
  const authStore = {user: () => ({email: 'tester@example.com', displayName: 'Tester'})};

  // afterClosed() emits this; flip per test to simulate confirm/cancel.
  let dialogResult: unknown = true;
  const dialog = {open: vi.fn(() => ({afterClosed: () => of(dialogResult)}))};

  let service: DeviceActionsService;

  beforeEach(() => {
    vi.clearAllMocks();
    dialogResult = true;
    TestBed.configureTestingModule({
      providers: [
        DeviceActionsService,
        {provide: DeviceStore, useValue: store},
        {provide: DeviceApiService, useValue: deviceApi},
        {provide: AuthStore, useValue: authStore},
        {provide: MatDialog, useValue: dialog},
        {provide: MatSnackBar, useValue: snackBar},
        {provide: Router, useValue: router},
      ],
    });
    service = TestBed.inject(DeviceActionsService);
  });

  it('start dispatches immediately with no dialog', () => {
    service.start(makeDevice());
    expect(store.start).toHaveBeenCalledWith('dev-1');
    expect(dialog.open).not.toHaveBeenCalled();
  });

  it('stop dispatches only after the confirm dialog resolves true', () => {
    service.stop(makeDevice());
    expect(store.stop).toHaveBeenCalledWith('dev-1');
  });

  it('stop does nothing when the dialog is cancelled', () => {
    dialogResult = false;
    service.stop(makeDevice());
    expect(store.stop).not.toHaveBeenCalled();
  });

  it('restart dispatches only after the confirm dialog resolves true', () => {
    service.restart(makeDevice(), 'my-device');
    expect(store.restart).toHaveBeenCalledWith(TARGET);

    dialogResult = false;
    store.restart.mockClear();
    service.restart(makeDevice(), 'my-device');
    expect(store.restart).not.toHaveBeenCalled();
  });

  it('powerwash dispatches only after the confirm dialog resolves true', () => {
    service.powerwash(makeDevice(), 'my-device');
    expect(store.powerwash).toHaveBeenCalledWith(TARGET);

    dialogResult = false;
    store.powerwash.mockClear();
    service.powerwash(makeDevice(), 'my-device');
    expect(store.powerwash).not.toHaveBeenCalled();
  });

  // The regression this whole step exists to prevent: a verb reaching the
  // group's first Android VM when the user asked for another one.
  it('targets the named instance, not the group first member', () => {
    const group = makeDevice({instances: [{name: 'phone'}, {name: 'watch'}]});

    service.restart(group, 'watch');
    expect(store.restart).toHaveBeenCalledWith({deviceId: 'dev-1', instance: 'watch'});

    service.screenshot(group, 'watch');
    expect(deviceApi.getScreenshot).toHaveBeenCalledWith({deviceId: 'dev-1', instance: 'watch'}, 0);
  });

  it('names the instance in a group confirmation, and only the device at N=1', () => {
    service.powerwash(makeDevice({instances: [{name: 'phone'}, {name: 'watch'}]}), 'watch');
    expect(dialog.open).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({
        data: expect.objectContaining({message: expect.stringContaining('my-device / watch')}),
      })
    );

    dialog.open.mockClear();
    service.powerwash(makeDevice(), 'my-device');
    expect(dialog.open).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({
        data: expect.objectContaining({message: expect.stringContaining('"my-device"?')}),
      })
    );
  });

  it('powerbtn dispatches immediately with no dialog', () => {
    service.powerbtn(makeDevice(), 'my-device');
    expect(store.powerbtn).toHaveBeenCalledWith(TARGET);
    expect(dialog.open).not.toHaveBeenCalled();
  });

  it('recordStart and recordStop dispatch immediately with no dialog', () => {
    service.recordStart(makeDevice(), 'my-device');
    service.recordStop(makeDevice(), 'my-device');
    expect(store.recordStart).toHaveBeenCalledWith(TARGET);
    expect(store.recordStop).toHaveBeenCalledWith(TARGET);
    expect(dialog.open).not.toHaveBeenCalled();
  });

  it('screenshot downloads the captured image', () => {
    const anchor = {href: '', download: '', click: vi.fn()};
    const createElement = vi
      .spyOn(document, 'createElement')
      .mockReturnValue(anchor as unknown as HTMLAnchorElement);
    const createObjectURL = vi.fn(() => 'blob:url');
    const revokeObjectURL = vi.fn();
    vi.stubGlobal('URL', {createObjectURL, revokeObjectURL});

    service.screenshot(makeDevice(), 'my-device');

    expect(deviceApi.getScreenshot).toHaveBeenCalledWith(TARGET, 0);
    expect(anchor.click).toHaveBeenCalled();
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:url');

    createElement.mockRestore();
    vi.unstubAllGlobals();
  });

  it('screenshot failure surfaces a toast', () => {
    deviceApi.getScreenshot.mockReturnValueOnce(throwError(() => new Error('boom')));
    service.screenshot(makeDevice(), 'my-device');
    expect(snackBar.open).toHaveBeenCalledWith('Failed to capture screenshot', 'Dismiss', {
      duration: 4000,
    });
  });

  it('delete navigates to the list only when navigateToListAfter is set', () => {
    service.delete(makeDevice());
    expect(store.delete).toHaveBeenCalledWith('dev-1');
    expect(router.navigate).not.toHaveBeenCalled();

    service.delete(makeDevice(), {navigateToListAfter: true});
    expect(router.navigate).toHaveBeenCalledWith(['/devices']);
  });

  it('delete warns when the device was created by someone else', () => {
    service.delete(makeDevice({createdBy: 'other@example.com'}));
    expect(dialog.open).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({
        data: expect.objectContaining({warning: 'Created by other@example.com, another user.'}),
      })
    );
    expect(store.delete).toHaveBeenCalledWith('dev-1');
  });

  it('delete carries no warning for own devices', () => {
    service.delete(makeDevice({createdBy: 'tester@example.com'}));
    expect(dialog.open).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({data: expect.objectContaining({warning: undefined})})
    );
  });

  it('deleteMany dispatches every id after the confirm dialog resolves true', () => {
    service.deleteMany([makeDevice({id: 'dev-1'}), makeDevice({id: 'dev-2'})]);
    expect(store.deleteMany).toHaveBeenCalledWith(['dev-1', 'dev-2']);
  });

  it('deleteMany does nothing for an empty selection and opens no dialog', () => {
    service.deleteMany([]);
    expect(dialog.open).not.toHaveBeenCalled();
    expect(store.deleteMany).not.toHaveBeenCalled();
  });

  it('deleteMany does nothing when the dialog is cancelled', () => {
    dialogResult = false;
    service.deleteMany([makeDevice()]);
    expect(store.deleteMany).not.toHaveBeenCalled();
  });

  it('deleteMany warns with a count of foreign-created devices', () => {
    service.deleteMany([
      makeDevice({id: 'dev-1', createdBy: 'other@example.com'}),
      makeDevice({id: 'dev-2', createdBy: 'tester@example.com'}),
    ]);
    expect(dialog.open).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({
        data: expect.objectContaining({warning: '1 of these was created by someone else.'}),
      })
    );
  });

  it('connectAdb copies a localhost command and warns for private-IP hosts', () => {
    const writeText = vi.fn();
    vi.stubGlobal('navigator', {clipboard: {writeText}});

    service.connectAdb(makeDevice({hostIp: '10.0.0.4', adbPort: 6520}));
    expect(writeText).toHaveBeenCalledWith('adb connect localhost:6520');
    expect(snackBar.open).toHaveBeenCalledWith(
      expect.stringContaining('IAP Tunnel'),
      'Dismiss',
      expect.anything()
    );

    service.connectAdb(makeDevice({hostIp: '34.1.2.3', adbPort: 6520}));
    expect(writeText).toHaveBeenLastCalledWith('adb connect 34.1.2.3:6520');

    service.connectAdb(makeDevice({hostIp: '172.32.10.5', adbPort: 6520}));
    expect(writeText).toHaveBeenLastCalledWith('adb connect 172.32.10.5:6520');

    service.connectAdb(makeDevice({hostIp: '172.20.1.5', adbPort: 6520}));
    expect(writeText).toHaveBeenLastCalledWith('adb connect localhost:6520');

    vi.unstubAllGlobals();
  });

  // The name is what the row shows; the ID is what the clipboard must hold,
  // because a shared name is what the CLI cannot resolve.
  it('copyId puts the device ID on the clipboard', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal('navigator', {clipboard: {writeText}});

    service.copyId(makeDevice({id: 'f1e2d3c4-0000-4000-8000-000000000001', name: 'ci-001'}));
    expect(writeText).toHaveBeenCalledWith('f1e2d3c4-0000-4000-8000-000000000001');
    await vi.waitFor(() =>
      expect(snackBar.open).toHaveBeenCalledWith(
        'Copied the ID of "ci-001"',
        'Dismiss',
        expect.anything()
      )
    );

    vi.unstubAllGlobals();
  });

  it('copyId reports a failed clipboard write', async () => {
    const writeText = vi.fn().mockRejectedValue(new Error('not allowed'));
    vi.stubGlobal('navigator', {clipboard: {writeText}});

    service.copyId(makeDevice({name: 'ci-001'}));
    await vi.waitFor(() =>
      expect(snackBar.open).toHaveBeenCalledWith(
        'Could not copy the ID of "ci-001"',
        'Dismiss',
        expect.anything()
      )
    );
    expect(snackBar.open).not.toHaveBeenCalledWith(
      expect.stringContaining('Copied'),
      'Dismiss',
      expect.anything()
    );

    vi.unstubAllGlobals();
  });
});
