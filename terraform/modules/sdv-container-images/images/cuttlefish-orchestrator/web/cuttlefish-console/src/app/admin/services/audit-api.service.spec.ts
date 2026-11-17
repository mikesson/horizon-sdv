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

import {provideHttpClient} from '@angular/common/http';
import {HttpTestingController, provideHttpClientTesting} from '@angular/common/http/testing';
import {TestBed} from '@angular/core/testing';
import {afterEach, beforeEach, describe, expect, it} from 'vitest';
import {AuditApiService} from './audit-api.service';

const emptyPage = {items: [], total: 0, limit: 50, offset: 0};

describe('AuditApiService time window', () => {
  let service: AuditApiService;
  let httpMock: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [AuditApiService, provideHttpClient(), provideHttpClientTesting()],
    });
    service = TestBed.inject(AuditApiService);
    httpMock = TestBed.inject(HttpTestingController);
  });

  afterEach(() => {
    httpMock.verify();
  });

  it('sends both ends of a bounded window', () => {
    service
      .list(
        {
          startTime: new Date('2026-08-01T00:00:00Z'),
          endTime: new Date('2026-08-02T12:30:00Z'),
        },
        50,
        0
      )
      .subscribe();

    const req = httpMock.expectOne(r => r.url === '/v2/admin/audit');
    expect(req.request.params.get('start_time')).toBe('2026-08-01T00:00:00.000Z');
    expect(req.request.params.get('end_time')).toBe('2026-08-02T12:30:00.000Z');
    req.flush(emptyPage);
  });

  it('omits end_time when the window is open-ended', () => {
    service.list({startTime: new Date('2026-08-01T00:00:00Z')}, 50, 0).subscribe();

    const req = httpMock.expectOne(r => r.url === '/v2/admin/audit');
    expect(req.request.params.has('end_time')).toBe(false);
    req.flush(emptyPage);
  });
});
