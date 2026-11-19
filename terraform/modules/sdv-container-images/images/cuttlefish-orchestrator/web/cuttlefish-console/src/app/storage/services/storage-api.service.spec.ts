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

import {provideHttpClient} from '@angular/common/http';
import {HttpTestingController, provideHttpClientTesting} from '@angular/common/http/testing';
import {provideZonelessChangeDetection} from '@angular/core';
import {TestBed} from '@angular/core/testing';
import {afterEach, beforeEach, describe, expect, it} from 'vitest';
import type {Bucket, ListObjectsResponse, VerifyAccessResponse} from '../models/storage.model';
import {StorageApiService} from './storage-api.service';

describe('StorageApiService', () => {
  let service: StorageApiService;
  let httpMock: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [
        provideZonelessChangeDetection(),
        StorageApiService,
        provideHttpClient(),
        provideHttpClientTesting(),
      ],
    });
    service = TestBed.inject(StorageApiService);
    httpMock = TestBed.inject(HttpTestingController);
  });

  afterEach(() => {
    httpMock.verify();
  });

  it('lists buckets and handles null buckets payloads cleanly', () => {
    let buckets: Bucket[] = [];
    service.listBuckets().subscribe(res => {
      buckets = res;
    });

    const req = httpMock.expectOne('/v2/storage/buckets');
    expect(req.request.method).toBe('GET');
    req.flush({
      buckets: [
        {
          name: 'artifacts-bkt',
          location: 'EU',
        },
      ],
    });
    expect(buckets).toEqual([
      {
        name: 'artifacts-bkt',
        location: 'EU',
      },
    ]);

    let empty: Bucket[] = [{name: 'x', location: ''}];
    service.listBuckets().subscribe(res => {
      empty = res;
    });
    httpMock.expectOne('/v2/storage/buckets').flush({buckets: null});
    expect(empty).toEqual([]);
  });

  it('lists objects with encoded prefix and maps fallback gs_uri/display_name', () => {
    let response: ListObjectsResponse | undefined;
    service.listObjects('bkt-1', 'builds/2026/').subscribe(res => {
      response = res;
    });

    const req = httpMock.expectOne(
      r =>
        r.url === '/v2/storage/buckets/bkt-1/objects' && r.params.get('prefix') === 'builds/2026/'
    );
    expect(req.request.method).toBe('GET');
    req.flush({
      objects: [
        {
          bucket: 'bkt-1',
          name: 'builds/2026/cvd.zip',
          size: 2048,
          updated: '2026-08-01T12:00:00Z',
          content_type: 'application/zip',
        },
        {
          bucket: 'bkt-1',
          name: 'builds/2026/app.apk',
          display_name: 'app.apk',
          gs_uri: 'gs://bkt-1/builds/2026/app.apk',
        },
      ],
    });

    const objects = response?.objects ?? [];
    expect(objects.length).toBe(2);
    expect(objects[0].gsUri).toBe('gs://bkt-1/builds/2026/cvd.zip');
    expect(objects[0].displayName).toBe('builds/2026/cvd.zip');
    expect(objects[0].size).toBe(2048);
    expect(objects[1].displayName).toBe('app.apk');
  });

  it('URL-encodes bucket and object name when deleting an object', () => {
    let completed = false;
    service.deleteObject('bkt-1', 'dir/my file.apk').subscribe(() => {
      completed = true;
    });

    const req = httpMock.expectOne('/v2/storage/buckets/bkt-1/objects/dir%2Fmy%20file.apk');
    expect(req.request.method).toBe('DELETE');
    req.flush(null);
    expect(completed).toBe(true);
  });

  it('posts snake_case verifyAccess payload and parses response', () => {
    let result: VerifyAccessResponse | undefined;
    service.verifyAccess('bkt-1', 'dir/img.zip', 'host-1', true).subscribe(res => {
      result = res;
    });

    const req = httpMock.expectOne('/v2/storage/verify-access');
    expect(req.request.method).toBe('POST');
    expect(req.request.body).toEqual({
      bucket: 'bkt-1',
      object_path: 'dir/img.zip',
      host_id: 'host-1',
      check_write: true,
    });
    req.flush({
      gs_uri: 'gs://bkt-1/dir/img.zip',
      orchestrator_access: {
        service_account: 'cco@proj.iam.gserviceaccount.com',
        has_access: true,
        permissions: ['storage.objects.get'],
      },
      can_proceed: true,
      message: 'ok',
    });
    expect(result).toEqual({
      gsUri: 'gs://bkt-1/dir/img.zip',
      orchestratorAccess: {
        serviceAccount: 'cco@proj.iam.gserviceaccount.com',
        hasAccess: true,
        permissions: ['storage.objects.get'],
        missingPerms: undefined,
        error: undefined,
        checkedAt: '',
      },
      hostAccess: undefined,
      canProceed: true,
      message: 'ok',
    });
  });
});
