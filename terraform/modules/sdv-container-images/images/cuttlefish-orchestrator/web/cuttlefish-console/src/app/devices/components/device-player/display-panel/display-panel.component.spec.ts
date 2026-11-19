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

import {provideZonelessChangeDetection, signal} from '@angular/core';
import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {MatSnackBar} from '@angular/material/snack-bar';
import {of, throwError} from 'rxjs';
import {beforeEach, describe, expect, it, vi} from 'vitest';
import {LoggingService} from '../../../../shared/services';
import {DeviceApiService} from '../../../services/device-api.service';
import {DeviceInputService} from '../../../services/input/device-input.service';
import {DeviceStreamService} from '../../../services/streaming/device-stream.service';
import {DisplayPanelComponent} from './display-panel.component';

describe('DisplayPanelComponent', () => {
  const deviceApi = {listDisplays: vi.fn()};
  const inputService = {addDisplay: vi.fn(), removeDisplay: vi.fn()};
  const sessionMock = {expectSignalingSoon: vi.fn()};
  const sessionSignal = signal<typeof sessionMock | null>(sessionMock);
  const streamService = {session: vi.fn(() => sessionSignal)};
  const snackBar = {open: vi.fn()};
  const logger = {error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn()};

  let fixture: ComponentFixture<DisplayPanelComponent>;

  beforeEach(() => {
    vi.clearAllMocks();
    sessionSignal.set(sessionMock);
    inputService.addDisplay.mockReturnValue(true);
    inputService.removeDisplay.mockReturnValue(true);
    deviceApi.listDisplays.mockReturnValue(
      of({
        displays: [
          {displayId: 0, label: 'Primary', width: 1080, height: 1920, density: 420, rotation: 0},
          {displayId: 1, label: 'Cluster', width: 1920, height: 720, density: 240, rotation: 0},
        ],
        presets: [
          {name: 'Automotive', width: 1920, height: 1080, dpi: 240, refresh_rate_hz: 60},
          {name: 'Phone', width: 1080, height: 1920, dpi: 420, refresh_rate_hz: 60},
        ],
      })
    );

    TestBed.configureTestingModule({
      imports: [DisplayPanelComponent],
      providers: [
        provideZonelessChangeDetection(),
        {provide: DeviceApiService, useValue: deviceApi},
        {provide: DeviceInputService, useValue: inputService},
        {provide: DeviceStreamService, useValue: streamService},
        {provide: MatSnackBar, useValue: snackBar},
        {provide: LoggingService, useValue: logger},
      ],
    }).overrideProvider(MatSnackBar, {useValue: snackBar});
  });

  async function mount(): Promise<HTMLElement> {
    fixture = TestBed.createComponent(DisplayPanelComponent);
    fixture.componentRef.setInput('deviceId', 'dev-1');
    fixture.componentRef.setInput('instance', 'cvd-1');
    fixture.detectChanges();
    await fixture.whenStable();
    fixture.detectChanges();
    return fixture.nativeElement as HTMLElement;
  }

  it('loads and displays active displays and enables removal when multiple displays exist', async () => {
    const root = await mount();
    const text = (root.textContent ?? '').replace(/\s+/g, ' ');

    expect(deviceApi.listDisplays).toHaveBeenCalledWith({deviceId: 'dev-1', instance: 'cvd-1'});
    expect(text).toContain('1080');
    expect(text).toContain('1920');

    const buttons = Array.from(root.querySelectorAll('button'));
    const removeBtn = buttons.find(
      b => b.textContent?.includes('delete') || b.getAttribute('aria-label')?.includes('Remove')
    );
    if (removeBtn) {
      expect(removeBtn.disabled).toBe(false);
    }
  });

  it('adds a display via preset and notifies WebRTC session to expect signaling', async () => {
    const root = await mount();
    const cmp = fixture.componentInstance as unknown as {
      presets(): Array<{name: string; icon: string}>;
      onAddDisplay(presetName?: string): void;
      onRemoveDisplay(displayNumber: number): void;
    };

    expect(cmp.presets().find(p => p.name === 'Automotive')?.icon).toBe('directions_car');
    cmp.onAddDisplay('Phone');

    expect(sessionMock.expectSignalingSoon).toHaveBeenCalled();
    expect(inputService.addDisplay).toHaveBeenCalledWith('dev-1', 1080, 1920, 420, 60);

    cmp.onRemoveDisplay(1);
    expect(inputService.removeDisplay).toHaveBeenCalledWith('dev-1', 1);

    expect(root.textContent).toBeTruthy();
  });

  it('warns via snackbar when WebRTC control channel is not connected', async () => {
    await mount();
    inputService.addDisplay.mockReturnValue(false);
    const cmp = fixture.componentInstance as unknown as {
      onAddDisplay(presetName?: string): void;
    };

    cmp.onAddDisplay('Phone');
    expect(snackBar.open).toHaveBeenCalledWith(
      expect.stringContaining('WebRTC'),
      expect.anything(),
      expect.anything()
    );
  });

  it('handles listDisplays errors cleanly without crashing', async () => {
    deviceApi.listDisplays.mockReturnValue(throwError(() => new Error('displays fetch failed')));
    const root = await mount();
    expect(root.textContent).toBeTruthy();
    expect(logger.error).toHaveBeenCalled();
  });
});
