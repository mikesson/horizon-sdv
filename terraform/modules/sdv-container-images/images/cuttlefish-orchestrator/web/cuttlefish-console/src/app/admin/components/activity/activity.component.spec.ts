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
 * The filters are the URL. A link can arrive pre-filtered, a reload keeps the
 * filter, and applying one in the page leaves a link worth sending to someone.
 */

import {provideHttpClient} from '@angular/common/http';
import type {TestRequest} from '@angular/common/http/testing';
import {HttpTestingController, provideHttpClientTesting} from '@angular/common/http/testing';
import {TestBed} from '@angular/core/testing';
import {provideRouter, Router} from '@angular/router';
import {RouterTestingHarness} from '@angular/router/testing';
import {afterEach, beforeEach, describe, expect, it} from 'vitest';
import {ActivityComponent} from './activity.component';

const emptyPage = {items: [], total: 0, limit: 50, offset: 0};

describe('ActivityComponent URL-synced filters', () => {
  let harness: RouterTestingHarness;
  let httpMock: HttpTestingController;

  /** The audit request the page issues; flushed so the next one is unambiguous. */
  function takeAuditRequest(): TestRequest {
    const req = httpMock.expectOne(r => r.url === '/v2/admin/audit');
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

  function button(label: string): HTMLButtonElement {
    const match = Array.from(routeRoot().querySelectorAll('button')).find(
      el => el.textContent?.trim() === label
    );
    if (!match) {
      throw new Error(`no button labelled ${label}`);
    }
    return match;
  }

  beforeEach(async () => {
    TestBed.configureTestingModule({
      providers: [
        provideRouter([{path: 'admin/activity', component: ActivityComponent}]),
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

  it('applies a filter that arrives in the URL', async () => {
    await harness.navigateByUrl('/admin/activity?resource=device-1&type=device', ActivityComponent);

    const params = takeAuditRequest().request.params;
    expect(params.get('resource_id')).toBe('device-1');
    expect(params.get('resource_type')).toBe('device');
  });

  it('sends a custom range as both start_time and end_time', async () => {
    await harness.navigateByUrl(
      '/admin/activity?range=custom&from=2026-07-01T09:00&to=2026-07-02T17:30',
      ActivityComponent
    );

    const params = takeAuditRequest().request.params;
    expect(params.get('start_time')).toBe(new Date('2026-07-01T09:00').toISOString());
    expect(params.get('end_time')).toBe(new Date('2026-07-02T17:30').toISOString());
  });

  it('writes an applied filter back to the URL, and Clear removes it', async () => {
    await harness.navigateByUrl('/admin/activity', ActivityComponent);
    takeAuditRequest();

    const resourceInput = routeRoot().querySelector<HTMLInputElement>(
      'input[placeholder="host-a1b2c3"]'
    );
    if (!resourceInput) {
      throw new Error('the resource id filter did not render');
    }
    resourceInput.value = 'host-abc';
    resourceInput.dispatchEvent(new Event('input'));
    harness.detectChanges();

    button('Apply').click();
    await harness.fixture.whenStable();
    expect(takeAuditRequest().request.params.get('resource_id')).toBe('host-abc');
    expect(TestBed.inject(Router).url).toBe('/admin/activity?resource=host-abc');

    button('Clear').click();
    await harness.fixture.whenStable();
    expect(takeAuditRequest().request.params.has('resource_id')).toBe(false);
    expect(TestBed.inject(Router).url).toBe('/admin/activity');
  });
});
