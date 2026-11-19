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

/**
 * Pins the Host Orchestrator affordance per host visibility. A public host
 * gets the direct :2080 link; a private host gets IAP-tunnel instructions,
 * never a link into /v2/hosts/{id}/ho/, which is not a route the backend
 * serves and which this card used to render as a dead link.
 */

import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {provideRouter} from '@angular/router';
import {beforeEach, describe, expect, it} from 'vitest';
import type {Device} from '../../models/device.model';
import {DeviceConnectionCardComponent} from './device-connection-card.component';

function makeDevice(overrides: Partial<Device> = {}): Device {
  return {
    id: 'dev-1',
    name: 'test-device',
    hostId: 'host-1',
    hostName: 'cf-test-host',
    hostZone: 'europe-west4-a',
    status: 'running',
    gpuMode: 'auto',
    adbPort: 6520,
    createdBy: 'user@example.com',
    createdAt: new Date(),
    updatedAt: new Date(),
    lifecycle: {mode: 'ephemeral', ttlSeconds: 14400, extendedCount: 0},
    ...overrides,
  };
}

describe('DeviceConnectionCardComponent Host Orchestrator access', () => {
  let fixture: ComponentFixture<DeviceConnectionCardComponent>;

  function render(device: Device): HTMLElement {
    fixture = TestBed.createComponent(DeviceConnectionCardComponent);
    fixture.componentRef.setInput('device', device);
    // The card is scoped to one Android VM: its ADB block and its player link
    // both address the instance, so rendering without one is not a state the
    // detail page can produce.
    fixture.componentRef.setInput('instance', device.name);
    fixture.detectChanges();
    return fixture.nativeElement as HTMLElement;
  }

  beforeEach(() => {
    TestBed.configureTestingModule({
      imports: [DeviceConnectionCardComponent],
      providers: [provideRouter([])],
    });
  });

  it('links the public host directly on port 2080', () => {
    const root = render(makeDevice({hostIp: '34.90.1.2', hostExternalIp: '34.90.1.2'}));
    const hrefs = Array.from(root.querySelectorAll('a')).map(a => a.getAttribute('href'));
    expect(hrefs).toContain('http://34.90.1.2:2080/');
  });

  it('never links /v2/hosts/{id}/ho/ for a private host, since the route does not exist', () => {
    const root = render(makeDevice({hostIp: '10.164.0.7', hostExternalIp: ''}));
    const hrefs = Array.from(root.querySelectorAll('a')).map(a => a.getAttribute('href') ?? '');
    expect(hrefs.some(h => h.includes('/ho/'))).toBe(false);
  });

  it('shows the IAP tunnel command for a private host instead', () => {
    const root = render(makeDevice({hostIp: '10.164.0.7', hostExternalIp: ''}));
    expect(root.textContent).toContain(
      'gcloud compute start-iap-tunnel cf-test-host 2080 --zone=europe-west4-a'
    );
  });

  it('classifies RFC1918 172.16.0.0/12 boundaries accurately without matching 172.15 or 172.32', () => {
    for (const privIp of ['172.16.0.1', '172.31.255.1', '192.168.1.5']) {
      const root = render(makeDevice({hostIp: privIp, hostExternalIp: ''}));
      expect(root.textContent).toContain(
        'gcloud compute start-iap-tunnel cf-test-host 2080 --zone=europe-west4-a'
      );
    }

    for (const pubIp of ['172.15.255.1', '172.32.0.1']) {
      const root = render(makeDevice({hostIp: pubIp, hostExternalIp: pubIp}));
      expect(root.textContent).not.toContain('start-iap-tunnel');
      expect(root.innerHTML).toContain(pubIp);
    }
  });
});
