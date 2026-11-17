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
import {CatalogAdminApiService} from './catalog-admin-api.service';

const wireMachineType = {
  id: 'n1-standard-8',
  family: 'n1',
  name: 'N1 Standard 8',
  category: 'general_purpose',
  architecture: 'x86',
  vcpus: 8,
  memory_gb: 30,
  gpu_support: true,
  gpu_mode: 'attachable',
  max_gpus: 2,
  default_gpu_type: 'nvidia-l4',
  default_gpu_count: 1,
  is_default: true,
  is_enabled: true,
  sort_order: 101,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
};

describe('CatalogAdminApiService family curation', () => {
  let service: CatalogAdminApiService;
  let httpMock: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [CatalogAdminApiService, provideHttpClient(), provideHttpClientTesting()],
    });
    service = TestBed.inject(CatalogAdminApiService);
    httpMock = TestBed.inject(HttpTestingController);
  });

  afterEach(() => {
    httpMock.verify();
  });

  // The GPU policy is family curation: it describes what a host of the series
  // gets, and writing it per shape would ask the same question once per width.
  it('sends the GPU policy to the family under the wire names', () => {
    service
      .patchFamily('n1', {maxGpus: 2, defaultGpuType: 'nvidia-l4', defaultGpuCount: 1})
      .subscribe();

    const req = httpMock.expectOne('/v2/admin/machine-families/n1');
    expect(req.request.method).toBe('PATCH');
    expect(req.request.body).toEqual({
      max_gpus: 2,
      default_gpu_type: 'nvidia-l4',
      default_gpu_count: 1,
    });
    req.flush({});
  });

  // An empty default_gpu_type clears the policy, so it must survive the
  // "omit falsy fields" reflex that would drop it from the body.
  it('sends an empty default GPU type rather than dropping it', () => {
    service.patchFamily('n1', {defaultGpuType: ''}).subscribe();

    const req = httpMock.expectOne('/v2/admin/machine-families/n1');
    expect(req.request.body).toEqual({default_gpu_type: ''});
    req.flush({});
  });

  // Withdrawing a family must not carry the rest of its curation along: a
  // body that also restated the GPU policy would rewrite fields the operator
  // never touched.
  it('sends only the enablement when that is the whole patch', () => {
    service.patchFamily('n1', {isEnabled: false}).subscribe();

    const req = httpMock.expectOne('/v2/admin/machine-families/n1');
    expect(req.request.body).toEqual({is_enabled: false});
    req.flush({});
  });

  // The per-shape PATCH now carries the two moves a family row cannot express,
  // and nothing else. Display order and GPU policy left it for the family.
  it('sends only exclusion and election on the per-shape patch', () => {
    service.patchMachineType('n1-standard-8', {isEnabled: false}).subscribe();

    const req = httpMock.expectOne('/v2/admin/machine-types/n1-standard-8');
    expect(req.request.body).toEqual({is_enabled: false});
    req.flush(wireMachineType);
  });
});

const wireAdminZone = {
  zone: 'europe-west4-a',
  region: 'europe-west4',
  status: 'UP',
  is_enabled: true,
  offered_shapes: ['n1-standard-8'],
  shapes_reported: true,
};

describe('CatalogAdminApiService zone curation', () => {
  let service: CatalogAdminApiService;
  let httpMock: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [CatalogAdminApiService, provideHttpClient(), provideHttpClientTesting()],
    });
    service = TestBed.inject(CatalogAdminApiService);
    httpMock = TestBed.inject(HttpTestingController);
  });

  afterEach(() => {
    httpMock.verify();
  });

  // The curation list carries the snapshot's per-zone shape availability and
  // the curated set the count is against, so the page needs no second copy of
  // what "the fleet's shapes" means.
  it('parses the zone list with its per-zone shape availability', () => {
    let zones: string[] = [];
    let curated: string[] = [];
    service.listZones().subscribe(list => {
      zones = list.zones.map(z => `${z.zone}:${z.offeredShapes.length}:${z.shapesReported}`);
      curated = list.curatedShapes;
    });

    const req = httpMock.expectOne('/v2/admin/zones');
    req.flush({
      zones: [
        wireAdminZone,
        {
          zone: 'us-central1-a',
          region: 'us-central1',
          status: 'DOWN',
          is_enabled: false,
          offered_shapes: [],
          shapes_reported: false,
        },
      ],
      curated_shapes: ['n1-standard-8', 'n2-standard-8'],
    });

    expect(zones).toEqual(['europe-west4-a:1:true', 'us-central1-a:0:false']);
    expect(curated).toEqual(['n1-standard-8', 'n2-standard-8']);
  });

  // Enablement is one stored set: the PUT replaces it whole, so the body is
  // the set the caller wants rather than a patch of one member.
  it('replaces the enabled set whole', () => {
    let stored: string[] = [];
    service.setEnabledZones(['europe-west4-a', 'europe-west4-b']).subscribe(zones => {
      stored = zones;
    });

    const req = httpMock.expectOne('/v2/admin/zones/enabled');
    expect(req.request.method).toBe('PUT');
    expect(req.request.body).toEqual({zones: ['europe-west4-a', 'europe-west4-b']});
    req.flush({zones: ['europe-west4-a', 'europe-west4-b']});
    expect(stored).toEqual(['europe-west4-a', 'europe-west4-b']);
  });

  it('sends an empty set rather than dropping the write', () => {
    service.setEnabledZones([]).subscribe();

    const req = httpMock.expectOne('/v2/admin/zones/enabled');
    expect(req.request.body).toEqual({zones: []});
    req.flush({zones: []});
  });
});
