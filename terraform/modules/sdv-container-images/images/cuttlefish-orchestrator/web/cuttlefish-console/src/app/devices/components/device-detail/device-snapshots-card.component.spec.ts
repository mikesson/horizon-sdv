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
import {beforeEach, describe, expect, it, vi} from 'vitest';
import type {Device} from '../../models/device.model';
import type {Snapshot} from '../../models/snapshot.model';
import {DeviceActionsService} from '../../services/device-actions.service';
import {DeviceStore} from '../../store/device.signal-store';
import {DeviceSnapshotsCardComponent} from './device-snapshots-card.component';

function makeDevice(overrides: Partial<Device> = {}): Device {
  return {
    id: 'dev-1',
    name: 'test-device',
    hostId: 'host-1',
    hostName: 'cf-test-host',
    hostZone: 'europe-west4-a',
    status: 'stopped',
    gpuMode: 'swiftshader',
    adbPort: 6520,
    createdBy: 'user@example.com',
    createdAt: new Date(),
    updatedAt: new Date(),
    lifecycle: {mode: 'ephemeral', ttlSeconds: 14400, extendedCount: 0},
    ...overrides,
  };
}

function makeSnapshot(overrides: Partial<Snapshot> = {}): Snapshot {
  return {
    id: 'a1b2c3d4-0000-0000-0000-000000000000',
    deviceId: 'dev-1',
    hostId: 'host-1',
    state: 'ready',
    createdAt: new Date('2026-07-30T10:00:00Z'),
    updatedAt: new Date('2026-07-30T10:00:00Z'),
    ...overrides,
  };
}

describe('DeviceSnapshotsCardComponent', () => {
  const actions = {deleteSnapshot: vi.fn()};
  const store = {loadSnapshots: vi.fn()};

  beforeEach(() => {
    vi.clearAllMocks();
    TestBed.configureTestingModule({
      imports: [DeviceSnapshotsCardComponent],
      providers: [
        provideZonelessChangeDetection(),
        {provide: DeviceActionsService, useValue: actions},
        {provide: DeviceStore, useValue: store},
      ],
    });
  });

  function render(device: Device, snapshots: Snapshot[]): HTMLElement {
    const fixture = TestBed.createComponent(DeviceSnapshotsCardComponent);
    fixture.componentRef.setInput('device', device);
    fixture.componentRef.setInput('snapshots', snapshots);
    fixture.detectChanges();
    return fixture.nativeElement as HTMLElement;
  }

  function buttonTexts(root: HTMLElement): string[] {
    return Array.from(root.querySelectorAll('button')).map(
      b => `${b.getAttribute('aria-label') ?? ''} ${b.textContent ?? ''}`
    );
  }

  // A stopped device with a ready snapshot is the state the old card offered
  // restore in, so it is where a leftover button would show.
  it('offers neither take nor restore', () => {
    const root = render(makeDevice(), [makeSnapshot()]);
    const texts = buttonTexts(root).join('\n');
    expect(texts).not.toMatch(/take|restore/i);
  });

  it('points to cfctl for take and restore on every device', () => {
    const root = render(makeDevice({gpuMode: 'auto'}), []);
    const note = root.querySelector('.cli-note')?.textContent ?? '';
    expect(note).toContain('cfctl device snapshot');
    expect(note).toContain('never serves its screen again');
  });

  it('lists each snapshot with its state', () => {
    const root = render(makeDevice(), [makeSnapshot(), makeSnapshot({id: 'b2', state: 'lost'})]);
    const rows = root.querySelectorAll('.snap-row');
    expect(rows).toHaveLength(2);
    expect(rows[1].classList.contains('lost')).toBe(true);
  });

  it('dispatches delete through the confirming action service', () => {
    const device = makeDevice();
    const snap = makeSnapshot();
    const root = render(device, [snap]);
    root.querySelector<HTMLButtonElement>('button[aria-label="Delete this snapshot"]')?.click();
    expect(actions.deleteSnapshot).toHaveBeenCalledWith(device, snap);
  });
});
