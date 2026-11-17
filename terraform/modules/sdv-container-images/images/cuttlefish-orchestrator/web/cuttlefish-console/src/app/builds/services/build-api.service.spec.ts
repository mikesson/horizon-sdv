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
import {BuildApiService} from './build-api.service';

const wireBuild = {
  id: 'build-1',
  name: 'aosp-main',
  source: 'android_build',
  build_id: 'P12345678',
  target: 'aosp_cf_x86_64_phone-userdebug',
  branch: 'aosp-main',
  created_at: '2026-06-15T10:00:00Z',
  updated_at: '2026-06-15T10:00:00Z',
};

describe('BuildApiService catalog reads', () => {
  let service: BuildApiService;
  let httpMock: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [BuildApiService, provideHttpClient(), provideHttpClientTesting()],
    });
    service = TestBed.inject(BuildApiService);
    httpMock = TestBed.inject(HttpTestingController);
  });

  afterEach(() => httpMock.verify());

  it('sends only the max page size by default', () => {
    service.list({}).subscribe();
    const req = httpMock.expectOne(r => r.url === '/v2/builds');
    expect(req.request.params.get('limit')).toBe('100');
    req.flush({items: [], total: 0, limit: 100, offset: 0});
  });

  // Every build picker in the console offers what the store holds, so a
  // catalog past one page hid launchable builds rather than shortening a list.
  it('follows the server pages until the catalog is whole', () => {
    let received: {items: Array<{id: string}>; complete: boolean} | undefined;
    service.listAll({source: 'android_build'}).subscribe(collection => (received = collection));

    const first = httpMock.expectOne(r => r.url === '/v2/builds' && !r.params.has('page_token'));
    first.flush({items: [wireBuild], total: 101, limit: 100, offset: 0, next_page_token: 'tok-1'});

    const second = httpMock.expectOne(
      r => r.url === '/v2/builds' && r.params.get('page_token') === 'tok-1'
    );
    expect(second.request.params.get('source')).toBe('android_build');
    expect(second.request.params.has('offset')).toBe(false);
    second.flush({items: [{...wireBuild, id: 'build-2'}], total: 101, limit: 100, offset: 0});

    expect(received?.items.map(b => b.id)).toEqual(['build-1', 'build-2']);
    expect(received?.complete).toBe(true);
  });
});
