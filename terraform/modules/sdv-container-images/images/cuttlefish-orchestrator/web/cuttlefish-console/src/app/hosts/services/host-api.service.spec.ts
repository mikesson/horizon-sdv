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
import {HostApiService} from './host-api.service';

const wireHost = {
  id: 'host-1',
  name: 'test-host',
  zone: 'europe-west4-a',
  status: 'running',
  machine_type: 'n1-standard-8',
  max_devices: 4,
  current_device_count: 0,
  provisioning_model: 'STANDARD',
  gpu_type: 'nvidia-tesla-t4',
  gpu_count: 1,
  created_by: 'tester@example.com',
  created_at: '2026-06-15T10:00:00Z',
  updated_at: '2026-06-15T10:00:00Z',
};

describe('HostApiService list scoping', () => {
  let service: HostApiService;
  let httpMock: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [HostApiService, provideHttpClient(), provideHttpClientTesting()],
    });
    service = TestBed.inject(HostApiService);
    httpMock = TestBed.inject(HttpTestingController);
  });

  afterEach(() => httpMock.verify());

  it('sends mine=true when the filter scopes to the caller', () => {
    service.list({mine: true}).subscribe();
    const req = httpMock.expectOne(r => r.url === '/v2/hosts');
    expect(req.request.params.get('mine')).toBe('true');
    req.flush({items: [wireHost], total: 1, limit: 20, offset: 0});
  });

  it('sends created_by for an explicit creator filter', () => {
    service.list({createdBy: 'other@example.com'}).subscribe();
    const req = httpMock.expectOne(r => r.url === '/v2/hosts');
    expect(req.request.params.get('created_by')).toBe('other@example.com');
    expect(req.request.params.has('mine')).toBe(false);
    req.flush({items: [], total: 0, limit: 20, offset: 0});
  });

  it('sends only the max page size by default', () => {
    service.list({}).subscribe();
    const req = httpMock.expectOne(r => r.url === '/v2/hosts');
    expect(req.request.params.keys()).toEqual(['limit']);
    expect(req.request.params.get('limit')).toBe('100');
    req.flush({items: [], total: 0, limit: 100, offset: 0});
  });

  it('an explicit limit wins over the page-size default', () => {
    service.list({limit: 5}).subscribe();
    const req = httpMock.expectOne(r => r.url === '/v2/hosts');
    expect(req.request.params.get('limit')).toBe('5');
    req.flush({items: [], total: 0, limit: 5, offset: 0});
  });

  // A fleet past one page used to arrive truncated: the device-create picker
  // offers what the store holds, so hosts with free capacity went missing
  // rather than merely falling off the end of the table.
  it('follows the server pages until the collection is whole', () => {
    let received: {items: Array<{id: string}>; total: number; complete: boolean} | undefined;
    service.listAll({mine: true}).subscribe(collection => (received = collection));

    const first = httpMock.expectOne(r => r.url === '/v2/hosts' && !r.params.has('page_token'));
    expect(first.request.params.get('limit')).toBe('100');
    expect(first.request.params.has('offset')).toBe(false);
    first.flush({items: [wireHost], total: 101, limit: 100, offset: 0, next_page_token: 'tok-1'});

    const second = httpMock.expectOne(
      r => r.url === '/v2/hosts' && r.params.get('page_token') === 'tok-1'
    );
    expect(second.request.params.get('mine')).toBe('true');
    expect(second.request.params.has('offset')).toBe(false);
    second.flush({items: [{...wireHost, id: 'host-2'}], total: 101, limit: 100, offset: 0});

    expect(received?.items.map(h => h.id)).toEqual(['host-1', 'host-2']);
    expect(received?.complete).toBe(true);
  });

  // Only the token says whether more exist. A host deleted between pages
  // lowers the total, and a walk that compared against it would stop or skip.
  // No token means the walk is over, whatever total says.
  it('stops when a page carries no next_page_token', () => {
    let received: {items: Array<{id: string}>; complete: boolean} | undefined;
    service.listAll().subscribe(collection => (received = collection));

    httpMock
      .expectOne(r => r.url === '/v2/hosts')
      .flush({items: [wireHost], total: 101, limit: 100, offset: 0});

    httpMock.expectNone(r => r.url === '/v2/hosts');
    expect(received?.items.map(h => h.id)).toEqual(['host-1']);
    expect(received?.complete).toBe(true);
  });

  it('reshapes capacity with a PATCH carrying only max_devices', () => {
    service.updateMaxDevices('host-1', 8).subscribe();
    const req = httpMock.expectOne(r => r.url === '/v2/hosts/host-1');
    expect(req.request.method).toBe('PATCH');
    expect(req.request.body).toEqual({max_devices: 8});
    req.flush(wireHost);
  });
});
