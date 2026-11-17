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
import {DeviceApiService} from './device-api.service';

const wireDevice = {
  id: 'device-1',
  name: 'my-device',
  host_id: 'host-1',
  status: 'running',
  gpu_mode: 'gfxstream',
  created_by: 'tester@example.com',
  created_at: '2026-06-15T10:00:00Z',
  updated_at: '2026-06-15T10:00:00Z',
};

describe('DeviceApiService list scoping', () => {
  let service: DeviceApiService;
  let httpMock: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [DeviceApiService, provideHttpClient(), provideHttpClientTesting()],
    });
    service = TestBed.inject(DeviceApiService);
    httpMock = TestBed.inject(HttpTestingController);
  });

  afterEach(() => httpMock.verify());

  it('sends mine=true when the filter scopes to the caller', () => {
    service.list({mine: true}).subscribe();
    const req = httpMock.expectOne(r => r.url === '/v2/devices');
    expect(req.request.params.get('mine')).toBe('true');
    req.flush({items: [wireDevice], total: 1, limit: 20, offset: 0});
  });

  it('sends created_by for an explicit creator filter', () => {
    service.list({createdBy: 'other@example.com'}).subscribe();
    const req = httpMock.expectOne(r => r.url === '/v2/devices');
    expect(req.request.params.get('created_by')).toBe('other@example.com');
    expect(req.request.params.has('mine')).toBe(false);
    req.flush({items: [], total: 0, limit: 20, offset: 0});
  });

  it('sends only the max page size by default', () => {
    service.list({}).subscribe();
    const req = httpMock.expectOne(r => r.url === '/v2/devices');
    expect(req.request.params.keys()).toEqual(['limit']);
    expect(req.request.params.get('limit')).toBe('100');
    req.flush({items: [], total: 0, limit: 100, offset: 0});
  });

  it('an explicit limit wins over the page-size default', () => {
    service.list({limit: 5}).subscribe();
    const req = httpMock.expectOne(r => r.url === '/v2/devices');
    expect(req.request.params.get('limit')).toBe('5');
    req.flush({items: [], total: 0, limit: 5, offset: 0});
  });

  // A fleet past one page used to arrive truncated with no way to ask for the
  // rest: the KPI strip, the ADB install picker and select-all all count what
  // the store holds, so the shortfall showed up as wrong numbers, not a short
  // table.
  it('follows the server pages until the collection is whole', () => {
    let received: {items: Array<{id: string}>; total: number; complete: boolean} | undefined;
    service.listAll({mine: true}).subscribe(collection => (received = collection));

    const first = httpMock.expectOne(r => r.url === '/v2/devices' && !r.params.has('page_token'));
    expect(first.request.params.get('limit')).toBe('100');
    expect(first.request.params.get('mine')).toBe('true');
    first.flush({items: [wireDevice], total: 101, limit: 100, offset: 0, next_page_token: 'tok-1'});

    // The filter rides every page, including the ones after the first.
    // Dropping it would page through a different collection than the one that
    // was asked for.
    const second = httpMock.expectOne(
      r => r.url === '/v2/devices' && r.params.get('page_token') === 'tok-1'
    );
    expect(second.request.params.get('mine')).toBe('true');
    expect(second.request.params.has('offset')).toBe(false);
    second.flush({items: [{...wireDevice, id: 'device-2'}], total: 101, limit: 100, offset: 0});

    expect(received?.items.map(d => d.id)).toEqual(['device-1', 'device-2']);
    expect(received?.complete).toBe(true);
  });

  it('makes a single request when the collection fits one page', () => {
    service.listAll({}).subscribe();
    const req = httpMock.expectOne(r => r.url === '/v2/devices');
    req.flush({items: [wireDevice], total: 1, limit: 100, offset: 0});
    httpMock.verify();
  });
});

const wireOperation = {
  name: 'operations/op-1',
  done: false,
  metadata: {
    verb: 'device.restart',
    resource_type: 'device',
    resource_id: 'device-1',
    create_time: '2026-07-26T10:00:00Z',
    attempt: 1,
  },
};

describe('DeviceApiService operation handles', () => {
  let service: DeviceApiService;
  let httpMock: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [DeviceApiService, provideHttpClient(), provideHttpClientTesting()],
    });
    service = TestBed.inject(DeviceApiService);
    httpMock = TestBed.inject(HttpTestingController);
  });

  afterEach(() => httpMock.verify());

  // Operations are a top-level collection served at /v2/operations. Asserted as a
  // whole URL rather than a suffix: this read was briefly the only call in the
  // console prefixed /api/, which every other service omits, and a prefix typo
  // fails as a 404 at runtime with nothing at build time to catch it.
  it('scopes a listing to one device and unwraps the page', () => {
    let received: ReadonlyArray<{id: string}> = [];
    service.listOperations('device-1', 5).subscribe(ops => (received = ops));

    const req = httpMock.expectOne(r => r.method === 'GET' && r.url.includes('operations'));
    expect(req.request.url).toBe('/v2/operations');
    expect(req.request.params.get('resource_id')).toBe('device-1');
    expect(req.request.params.get('limit')).toBe('5');
    req.flush({operations: [wireOperation], count: 1});

    // The page wrapper is dropped and the collection prefix stripped from the id.
    expect(received.map(op => op.id)).toEqual(['op-1']);
  });

  it('restart posts to the instance and returns the handle, not a status', () => {
    let received: {id: string; done: boolean} | undefined;
    service.restart({deviceId: 'device-1', instance: 'watch'}).subscribe(op => (received = op));

    const req = httpMock.expectOne('/v2/devices/device-1/instances/watch/restart');
    expect(req.request.method).toBe('POST');
    req.flush(wireOperation);

    expect(received?.id).toBe('op-1');
    expect(received?.done).toBe(false);
  });

  it('powerwash posts to the instance and returns the handle', () => {
    let received: {id: string} | undefined;
    service.powerwash({deviceId: 'device-1', instance: 'watch'}).subscribe(op => (received = op));

    const req = httpMock.expectOne('/v2/devices/device-1/instances/watch/powerwash');
    expect(req.request.method).toBe('POST');
    req.flush({...wireOperation, metadata: {...wireOperation.metadata, verb: 'device.powerwash'}});

    expect(received?.id).toBe('op-1');
  });

  // A settled failure carries its RFC 9457 problem. This is the only trace a failed
  // powerwash leaves, because the device row reads the same either way.
  it('surfaces a settled failure as an error on the handle', () => {
    let received: ReadonlyArray<{done: boolean; error?: {title: string; detail?: string}}> = [];
    service.listOperations('device-1').subscribe(ops => (received = ops));

    httpMock
      .expectOne(r => r.url === '/v2/operations')
      .flush({
        operations: [
          {
            ...wireOperation,
            done: true,
            error: {title: 'powerwash failed', status: 500, detail: 'host unreachable'},
          },
        ],
        count: 1,
      });

    expect(received[0]?.done).toBe(true);
    expect(received[0]?.error?.detail).toBe('host unreachable');
  });
});
