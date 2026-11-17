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
import type {PlatformLifetimes} from '../../shared/models/platform-lifetimes.schema';
import type {
  ArtifactSettings,
  DefaultsSettings,
  PlacementSettings,
  SpotSettings,
} from '../models/settings.model';
import {SettingsAdminApiService} from './settings-admin-api.service';

const placement: PlacementSettings = {
  zonePin: 'europe-west4-a',
  zonePinIsSet: true,
  quotaMarginPercent: 10,
  quotaMarginIsSet: false,
  v1ZoneIsHint: true,
  v1ZoneHintIsSet: false,
};

const spot: SpotSettings = {
  autoDefault: false,
  autoDefaultIsSet: false,
  minUptimeSingleSeconds: 600,
  minUptimeBatchSeconds: 3600,
  minUptimeIsSet: false,
};

const defaults: DefaultsSettings = {
  gpuMachineType: '',
  gpuMachineTypeIsSet: false,
  upstreamBranch: 'aosp-main',
  upstreamTarget: 'phone',
  upstreamBuildIsSet: true,
  dockerMachineType: 'n1-standard-8',
  dockerTtlHours: 12,
  dockerMaxTtlHours: 168,
  dockerHostIsSet: false,
};

const lifetimes: PlatformLifetimes = {
  deviceTtlHours: 4,
  extendHours: 2,
  extendCount: 3,
  hostTtlHours: 10,
  isSet: false,
  maxDeviceTtlHours: 8,
  maxExtendHours: 4,
  maxExtendCount: 10,
  maxHostTtlHours: 168,
  maxDevicesPerHost: 16,
  hostImageBackedSlots: 10,
  gracePeriodSeconds: 900,
};

const artifacts: ArtifactSettings = {
  bugreportBucket: 'triage',
  recordingBucket: '',
  defaultBucket: 'fallback',
  captureOnError: false,
  artifactsIsSet: true,
};

describe('SettingsAdminApiService', () => {
  let service: SettingsAdminApiService;
  let httpMock: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [SettingsAdminApiService, provideHttpClient(), provideHttpClientTesting()],
    });
    service = TestBed.inject(SettingsAdminApiService);
    httpMock = TestBed.inject(HttpTestingController);
  });

  afterEach(() => httpMock.verify());

  // Every PUT lands a config_change audit entry, so an unchanged section must
  // issue none at all rather than re-writing what it read.
  it('issues no request when nothing changed', () => {
    service.savePlacement(placement, placement).subscribe();
    service.saveSpot(spot, spot).subscribe();
    service.saveDefaults(defaults, defaults).subscribe();
    service.saveArtifacts(artifacts, artifacts).subscribe();
    service.saveLifetimes(lifetimes, lifetimes).subscribe();

    httpMock.expectNone(() => true);
  });

  // The group endpoint writes the four keys as one conceptual change, so any
  // moved field sends the whole body in a single PUT.
  it('sends the whole lifetime group when any field moves', () => {
    service.saveLifetimes({...lifetimes, deviceTtlHours: 6}, lifetimes).subscribe();

    const req = httpMock.expectOne('/v2/admin/system-config/lifetimes');
    expect(req.request.method).toBe('PUT');
    expect(req.request.body).toEqual({
      device_ttl_hours: 6,
      extend_hours: 2,
      extend_count: 3,
      host_ttl_hours: 10,
    });
    req.flush({});
  });

  it('writes only the placement key that changed', () => {
    service.savePlacement({...placement, quotaMarginPercent: 25}, placement).subscribe();

    const req = httpMock.expectOne('/v2/admin/system-config/quota-margin');
    expect(req.request.method).toBe('PUT');
    expect(req.request.body).toEqual({value: 25});
    httpMock.expectNone('/v2/admin/system-config/zone-pin');
    req.flush({});
  });

  it('sends both min-uptime tiers when either moves', () => {
    service.saveSpot({...spot, minUptimeBatchSeconds: 60}, spot).subscribe();

    const req = httpMock.expectOne('/v2/admin/system-config/spot-min-uptime');
    expect(req.request.body).toEqual({single_seconds: 600, batch_seconds: 60});
    req.flush({});
  });

  it('sends the docker-host pair together when either half moves', () => {
    service.saveDefaults({...defaults, dockerTtlHours: 48}, defaults).subscribe();

    const req = httpMock.expectOne('/v2/admin/system-config/docker-host');
    expect(req.request.method).toBe('PUT');
    expect(req.request.body).toEqual({machine_type: 'n1-standard-8', ttl_hours: 48});
    req.flush({});
  });

  it('sends the upstream pair together when either half moves', () => {
    service.saveDefaults({...defaults, upstreamTarget: 'tablet'}, defaults).subscribe();

    const req = httpMock.expectOne('/v2/admin/system-config/default-upstream-build');
    expect(req.request.body).toEqual({branch: 'aosp-main', target: 'tablet'});
    req.flush({});
  });

  it('carries the buckets along when only the capture toggle flips', () => {
    service.saveArtifacts({...artifacts, captureOnError: true}, artifacts).subscribe();

    const req = httpMock.expectOne('/v2/admin/system-config/artifact-buckets');
    expect(req.request.body).toEqual({
      bugreport_bucket: 'triage',
      recording_bucket: '',
      capture_on_error: true,
    });
    req.flush({});
  });

  it('reads no image project setting, because the deployment owns that value', () => {
    let loaded: DefaultsSettings | undefined;
    service.getDefaults().subscribe(v => (loaded = v));

    httpMock
      .expectOne('/v2/admin/system-config/default-gpu-machine-type')
      .flush({value: '', is_set: false});
    httpMock
      .expectOne('/v2/admin/system-config/default-upstream-build')
      .flush({branch: 'aosp-main', target: 'phone', is_set: true});
    httpMock
      .expectOne('/v2/admin/system-config/docker-host')
      .flush({machine_type: 'n1-standard-8', ttl_hours: 12, max_ttl_hours: 168});
    httpMock.expectNone('/v2/admin/system-config/default-image-project');

    expect(loaded?.upstreamBranch).toBe('aosp-main');
    expect(loaded).not.toHaveProperty('imageProject');
  });

  it('loads a section from its own endpoints', () => {
    let loaded: PlacementSettings | undefined;
    service.getPlacement().subscribe(v => (loaded = v));

    httpMock
      .expectOne('/v2/admin/system-config/zone-pin')
      .flush({value: 'europe-west4-b', is_set: true});
    httpMock.expectOne('/v2/admin/system-config/quota-margin').flush({value: 15, is_set: true});
    httpMock.expectOne('/v2/admin/system-config/v1-zone-hint').flush({value: false, is_set: true});

    expect(loaded?.zonePin).toBe('europe-west4-b');
    expect(loaded?.quotaMarginPercent).toBe(15);
    expect(loaded?.v1ZoneIsHint).toBe(false);
  });
});
