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
import type {FleetSchedule} from '../models/control-plane.model';
import {AutoscalerApiService} from './autoscaler-api.service';

const window: FleetSchedule = {
  name: 'CI morning',
  hosts: 3,
  start: '07:00',
  durationSec: 14400,
  days: [1, 2, 3, 4, 5],
  location: 'Europe/Stockholm',
};

/**
 * What every fleet write echoes back. Flushed in full rather than as an empty
 * object: the service parses the response, so a stub body would fail validation
 * and turn a passing write into an error nobody asked about.
 */
const policyBody = {
  mode: 'off',
  tiers: {
    default: {standby: 0, floor: 2, ladder: ['n2-standard-8', 'n1-standard-8'], schedules: []},
  },
  floor: 2,
  demand_horizon_sec: 3600,
  drain_grace_sec: 480,
  shape_ladder: ['n2-standard-8', 'n1-standard-8'],
  is_set: false,
  max_standby: 16,
  max_floor: 16,
  max_demand_horizon_sec: 86400,
  min_drain_grace_sec: 120,
  max_drain_grace_sec: 599,
  max_schedules: 8,
  max_ladder_rungs: 8,
};

describe('AutoscalerApiService', () => {
  let service: AutoscalerApiService;
  let httpMock: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [AutoscalerApiService, provideHttpClient(), provideHttpClientTesting()],
    });
    service = TestBed.inject(AutoscalerApiService);
    httpMock = TestBed.inject(HttpTestingController);
  });

  afterEach(() => httpMock.verify());

  // THE property this service exists to hold. The scopes are separate
  // endpoints because a whole-policy write from any one of them carries a stale
  // copy of the others and silently reverts them, so each write must touch its
  // own path and no other, whatever else the page happens to be showing.
  it('writes the mode and one pool’s machine counts without touching anything else', () => {
    service.saveAutoscaler('on', 'default', 3, 2).subscribe();

    const req = httpMock.expectOne('/v2/admin/system-config/fleet/autoscaler');
    expect(req.request.method).toBe('PUT');
    // All four every time. The endpoint replaces the mode and the named pool's
    // bounds together, so a body carrying only the edited value would set the
    // others to zero, and the pool would stop holding anything the moment
    // somebody changed the mode.
    expect(req.request.body).toEqual({mode: 'on', pool: 'default', standby: 3, floor: 2});
    httpMock.expectNone('/v2/admin/system-config/fleet/tuning');
    httpMock.expectNone('/v2/admin/system-config/fleet/schedules');
    httpMock.expectNone('/v2/admin/system-config/fleet/ladder');
    req.flush(policyBody);
  });

  // The bounds write names its pool, so one pool's card can never commit
  // another's numbers, which is the property the per-pool cards rest on.
  it('names the pool whose bounds it writes', () => {
    service.saveAutoscaler('on', 'gpu', 1, 0).subscribe();

    const req = httpMock.expectOne('/v2/admin/system-config/fleet/autoscaler');
    expect(req.request.body).toEqual({mode: 'on', pool: 'gpu', standby: 1, floor: 0});
    req.flush(policyBody);
  });

  it('writes the tuning scalars without touching the windows', () => {
    service.saveTuning(1800, 300).subscribe();

    const req = httpMock.expectOne('/v2/admin/system-config/fleet/tuning');
    expect(req.request.body).toEqual({demand_horizon_sec: 1800, drain_grace_sec: 300});
    httpMock.expectNone('/v2/admin/system-config/fleet/schedules');
    httpMock.expectNone('/v2/admin/system-config/fleet/autoscaler');
    httpMock.expectNone('/v2/admin/system-config/fleet/ladder');
    req.flush(policyBody);
  });

  it('writes one pool’s windows without touching the tuning scalars', () => {
    service.saveSchedules('default', [window]).subscribe();

    const req = httpMock.expectOne('/v2/admin/system-config/fleet/schedules');
    expect(req.request.body).toEqual({
      pool: 'default',
      schedules: [
        {
          name: 'CI morning',
          hosts: 3,
          start: '07:00',
          duration_sec: 14400,
          days: [1, 2, 3, 4, 5],
          location: 'Europe/Stockholm',
        },
      ],
    });
    httpMock.expectNone('/v2/admin/system-config/fleet/tuning');
    httpMock.expectNone('/v2/admin/system-config/fleet/ladder');
    req.flush(policyBody);
  });

  // The list scope a whole-policy write would empty without ever showing it:
  // the shape order is a list, so a body that carried it alongside a scalar
  // somebody else edited would restore the catalog default in silence.
  it('writes one pool’s shape order without touching the other scopes', () => {
    service.saveLadder('default', ['n2-standard-8', 'n1-standard-8']).subscribe();

    const req = httpMock.expectOne('/v2/admin/system-config/fleet/ladder');
    expect(req.request.method).toBe('PUT');
    expect(req.request.body).toEqual({
      pool: 'default',
      shape_ladder: ['n2-standard-8', 'n1-standard-8'],
    });
    httpMock.expectNone('/v2/admin/system-config/fleet/autoscaler');
    httpMock.expectNone('/v2/admin/system-config/fleet/tuning');
    httpMock.expectNone('/v2/admin/system-config/fleet/schedules');
    req.flush(policyBody);
  });

  // The order IS the setting, so the request must carry the operator's sequence
  // rather than a set: a body that sorted or deduplicated the rungs would choose
  // a different preferred shape from the one on the screen.
  it('sends the shapes in the order they were given', () => {
    service.saveLadder('default', ['n1-standard-8', 'n2-standard-8']).subscribe();

    const req = httpMock.expectOne('/v2/admin/system-config/fleet/ladder');
    expect(req.request.body).toEqual({
      pool: 'default',
      shape_ladder: ['n1-standard-8', 'n2-standard-8'],
    });
    req.flush(policyBody);
  });

  // Clearing the order is a real edit, exactly as clearing the windows is:
  // sending the list replaces it, so an empty array is the only way back to the
  // catalog's default shape.
  it('sends an empty list when the last shape is removed', () => {
    service.saveLadder('default', []).subscribe();

    const req = httpMock.expectOne('/v2/admin/system-config/fleet/ladder');
    expect(req.request.body).toEqual({pool: 'default', shape_ladder: []});
    req.flush(policyBody);
  });

  // Clearing every window is a real edit rather than "nothing to send". Sending
  // the list replaces it, so an empty array is the only verb there is for
  // removing the last one. Skipped as falsy, the windows stay stored and the
  // page shows none.
  it('sends an empty list when the last window is removed', () => {
    service.saveSchedules('default', []).subscribe();

    const req = httpMock.expectOne('/v2/admin/system-config/fleet/schedules');
    expect(req.request.body).toEqual({pool: 'default', schedules: []});
    req.flush(policyBody);
  });

  // Switching a pool off is its own verb, never a write of zeros: zero bounds
  // are a pool held empty on purpose, and this is a pool no longer routed to.
  it('switches a pool off with a DELETE naming it in the path', () => {
    service.disablePool('gpu').subscribe();

    const req = httpMock.expectOne('/v2/admin/system-config/fleet/pools/gpu');
    expect(req.request.method).toBe('DELETE');
    httpMock.expectNone('/v2/admin/system-config/fleet/autoscaler');
    req.flush(policyBody);
  });

  // The recommendation is its own request on purpose: a multi-day scan must not
  // ride the snapshot's five-second poll.
  it('reads the recommendation from its own endpoint', () => {
    service.recommendation().subscribe();

    const req = httpMock.expectOne('/v2/admin/autoscaler/recommendation');
    expect(req.request.method).toBe('GET');
    httpMock.expectNone('/v2/admin/autoscaler');
    req.flush({
      standby: 3,
      sufficient: true,
      servable: 9,
      unservable: 2,
      series: [],
      from: '2026-08-07T12:00:00Z',
      to: '2026-08-14T12:00:00Z',
    });
  });
});
