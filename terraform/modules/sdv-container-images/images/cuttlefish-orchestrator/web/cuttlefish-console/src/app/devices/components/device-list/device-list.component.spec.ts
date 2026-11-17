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
 * The host deep link is a filter only if it reaches the request. The table
 * renders whatever the store holds either way, so the evidence is `host_id` on
 * the wire. A row count would pass against an unfiltered fleet.
 */

import {provideHttpClient} from '@angular/common/http';
import type {TestRequest} from '@angular/common/http/testing';
import {HttpTestingController, provideHttpClientTesting} from '@angular/common/http/testing';
import {TestBed} from '@angular/core/testing';
import {provideRouter, Router} from '@angular/router';
import {RouterTestingHarness} from '@angular/router/testing';
import {afterEach, beforeEach, describe, expect, it} from 'vitest';
import {DeviceListComponent} from './device-list.component';

const emptyPage = {items: [], total: 0, limit: 100, offset: 0};

describe('DeviceListComponent host scope', () => {
  let harness: RouterTestingHarness;
  let httpMock: HttpTestingController;

  /** The list request the page issues; flushed so the next one is unambiguous. */
  function takeDeviceRequest(): TestRequest {
    const req = httpMock.expectOne(r => r.url === '/v2/devices');
    req.flush(emptyPage);
    return req;
  }

  function routeRoot(): HTMLElement {
    const root = harness.routeNativeElement;
    if (!root) {
      throw new Error('the route rendered no element');
    }
    return root;
  }

  function click(selector: string): void {
    const target = routeRoot().querySelector<HTMLElement>(selector);
    if (!target) {
      throw new Error(`nothing matched ${selector}`);
    }
    target.click();
  }

  beforeEach(async () => {
    TestBed.configureTestingModule({
      providers: [
        provideRouter([{path: 'devices', component: DeviceListComponent}]),
        provideHttpClient(),
        provideHttpClientTesting(),
      ],
    });
    harness = await RouterTestingHarness.create();
    httpMock = TestBed.inject(HttpTestingController);
  });

  afterEach(() => {
    httpMock.verify();
  });

  it('scopes the list to the host the URL names', async () => {
    await harness.navigateByUrl('/devices?hostId=host-abc', DeviceListComponent);

    const params = takeDeviceRequest().request.params;
    expect(params.get('host_id')).toBe('host-abc');
    // A host-scoped entry answers "what is on this host", not "what of mine is".
    expect(params.has('mine')).toBe(false);
  });

  it('scopes to Mine when no host is named', async () => {
    await harness.navigateByUrl('/devices', DeviceListComponent);

    const params = takeDeviceRequest().request.params;
    expect(params.has('host_id')).toBe(false);
    expect(params.get('mine')).toBe('true');
  });

  it('keeps the host scope when the Mine toggle changes', async () => {
    await harness.navigateByUrl('/devices?hostId=host-abc', DeviceListComponent);
    takeDeviceRequest();

    click('mat-slide-toggle button');
    await harness.fixture.whenStable();

    const params = takeDeviceRequest().request.params;
    expect(params.get('host_id')).toBe('host-abc');
    expect(params.get('mine')).toBe('true');
  });

  it('widens the list when the host chip is cleared', async () => {
    await harness.navigateByUrl('/devices?hostId=host-abc', DeviceListComponent);
    takeDeviceRequest();

    click('button[aria-label="Show devices on every host"]');
    await harness.fixture.whenStable();

    expect(takeDeviceRequest().request.params.has('host_id')).toBe(false);
    expect(TestBed.inject(Router).url).toBe('/devices');
  });
});
