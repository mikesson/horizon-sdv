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
import {ArtifactsAdminApiService} from './artifacts-admin-api.service';

const wireBuckets = {
  bugreport_bucket: 'triage',
  recording_bucket: '',
  default_bucket: 'cco-artifacts',
  capture_on_error: true,
};

describe('ArtifactsAdminApiService', () => {
  let service: ArtifactsAdminApiService;
  let httpMock: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [ArtifactsAdminApiService, provideHttpClient(), provideHttpClientTesting()],
    });
    service = TestBed.inject(ArtifactsAdminApiService);
    httpMock = TestBed.inject(HttpTestingController);
  });

  afterEach(() => httpMock.verify());

  it('transforms the capture-on-error toggle from the wire response', () => {
    let result: {captureOnError: boolean; bugreportBucket: string} | undefined;
    service.get().subscribe(b => (result = b));
    const req = httpMock.expectOne('/v2/admin/system-config/artifact-buckets');
    expect(req.request.method).toBe('GET');
    req.flush(wireBuckets);
    expect(result?.captureOnError).toBe(true);
    expect(result?.bugreportBucket).toBe('triage');
  });
});
