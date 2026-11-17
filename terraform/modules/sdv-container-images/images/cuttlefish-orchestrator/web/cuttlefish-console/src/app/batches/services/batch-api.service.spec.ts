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
import type {CreateBatchRequest} from '../models/batch.model';
import {BatchApiService} from './batch-api.service';

const wireBatch = {
  batch_id: 'batch-1',
  name_prefix: 'ci-run',
  status: 'ready',
  total: 2,
  running: 2,
  errored: 0,
  reconciling: false,
  created_by: 'tester@example.com',
  created_at: '2026-06-15T10:00:00Z',
};

describe('BatchApiService list scoping', () => {
  let service: BatchApiService;
  let httpMock: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [BatchApiService, provideHttpClient(), provideHttpClientTesting()],
    });
    service = TestBed.inject(BatchApiService);
    httpMock = TestBed.inject(HttpTestingController);
  });

  afterEach(() => httpMock.verify());

  it('sends mine=true when the filter scopes to the caller', () => {
    service.list({mine: true}).subscribe();
    const req = httpMock.expectOne(r => r.url === '/v2/batches');
    expect(req.request.params.get('mine')).toBe('true');
    req.flush({items: [wireBatch], total: 1});
  });

  it('sends created_by for an explicit creator filter', () => {
    service.list({createdBy: 'other@example.com'}).subscribe();
    const req = httpMock.expectOne(r => r.url === '/v2/batches');
    expect(req.request.params.get('created_by')).toBe('other@example.com');
    req.flush({items: [], total: 0});
  });

  it('sends no creator params without a filter', () => {
    service.list().subscribe();
    const req = httpMock.expectOne(r => r.url === '/v2/batches');
    expect(req.request.params.keys()).toHaveLength(0);
    req.flush({items: [], total: 0});
  });
});

describe('BatchApiService placement serialization', () => {
  let service: BatchApiService;
  let httpMock: HttpTestingController;

  function createRequest(
    requirements: Partial<CreateBatchRequest['requirements']>
  ): CreateBatchRequest {
    return {
      namePrefix: 'ci-run',
      count: 2,
      minReady: 2,
      fillPolicy: 'best_effort',
      placement: 'isolated',
      requirements: {interaction: 'headless', ...requirements},
    };
  }

  /** The requirements block the service actually put on the wire. */
  function requirementsSent(payload: CreateBatchRequest): Record<string, unknown> {
    service.create(payload, 'key-1').subscribe();
    const req = httpMock.expectOne(r => r.url === '/v2/batches');
    const body = req.request.body as {requirements: Record<string, unknown>};
    req.flush(wireBatch);
    return body.requirements;
  }

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [BatchApiService, provideHttpClient(), provideHttpClientTesting()],
    });
    service = TestBed.inject(BatchApiService);
    httpMock = TestBed.inject(HttpTestingController);
  });

  afterEach(() => httpMock.verify());

  it('sends the architecture and the pool a batch pinned', () => {
    expect(requirementsSent(createRequest({architecture: 'arm', pool: 'arm'}))).toMatchObject({
      architecture: 'arm',
      pool: 'arm',
    });
  });

  it('sends a pinned pool on its own', () => {
    const sent = requirementsSent(createRequest({pool: 'gpu'}));

    expect(sent['pool']).toBe('gpu');
    expect(sent).not.toHaveProperty('architecture');
  });

  it('sends neither at their defaults, which the backend assumes anyway', () => {
    const sent = requirementsSent(createRequest({architecture: 'x86'}));

    expect(sent).not.toHaveProperty('architecture');
    expect(sent).not.toHaveProperty('pool');
  });
});
