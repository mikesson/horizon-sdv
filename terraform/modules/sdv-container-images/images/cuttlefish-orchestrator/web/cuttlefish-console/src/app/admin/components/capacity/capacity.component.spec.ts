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

/**
 * The page states numbers it did not derive, so what is under test is the four
 * KPIs against the bodies that fed them, including the two failures that must
 * not render as measurements. A reclaimable read that errored does not mean
 * "0 reclaimable"; a board with nothing holding it back does.
 */

import {provideHttpClient} from '@angular/common/http';
import {HttpTestingController, provideHttpClientTesting} from '@angular/common/http/testing';
import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {provideRouter} from '@angular/router';
import {afterEach, beforeEach, describe, expect, it} from 'vitest';
import {CapacityComponent} from './capacity.component';

const NOW = '2026-06-15T10:00:00Z';

/** One clear zone; the KPI counts under test ride the fleet block. */
function snapshot(fleet: Record<string, number>): Record<string, unknown> {
  return {
    now: NOW,
    fleet: {
      machines: 4,
      devices: 11,
      regions: 1,
      zones_enabled: 3,
      zones_clear: 1,
      zones_cooling: 0,
      zones_blocked: 0,
      zones_constrained: 0,
      ...fleet,
    },
    regions: [
      {
        region: 'europe-west4',
        machines: 4,
        devices: 11,
        zones_enabled: 3,
        zones: 3,
        quota_state: 'comfortable',
      },
    ],
    zones: [
      {
        zone: 'europe-west4-a',
        region: 'europe-west4',
        enabled: true,
        status: 'UP',
        machines: 4,
        arriving: 0,
        devices: 11,
        verdict: 'clear',
        cells: [],
      },
    ],
    pools: [],
    spot: {
      fleet: [],
      spot_machines: 0,
      standard_machines: 0,
      preempted_7d: 0,
      recovered_7d: 0,
      unrecovered_7d: 0,
      typical_recovery_seconds: 0,
      advice: {enabled: false, cells: 0, stale: true},
    },
    reserve_margin_percent: 10,
  };
}

const QUOTA = {
  scope: 'global',
  scope_id: 'global',
  max_hosts: 10,
  used_hosts: 1,
  available: 9,
  percentage: 10,
  high_water_used_hosts: 3,
  high_water_at: NOW,
};

function reclaimable(total: number): Record<string, unknown> {
  return {
    total,
    pinned_devices: [],
    stopped_devices: [],
    stopped_hosts: [],
    expiring_hosts: [],
  };
}

describe('CapacityComponent', () => {
  let fixture: ComponentFixture<CapacityComponent>;
  let httpMock: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideRouter([]), provideHttpClient(), provideHttpClientTesting()],
    });
    fixture = TestBed.createComponent(CapacityComponent);
    httpMock = TestBed.inject(HttpTestingController);
    fixture.detectChanges();
  });

  afterEach(() => httpMock.verify());

  /** Answers the page's initial fan-out; `reads` overrides one body per test. */
  async function serveLoad(
    reads: {fleet?: Record<string, number>; reclaimable?: number} = {}
  ): Promise<void> {
    httpMock.expectOne('/v2/admin/capacity').flush(snapshot(reads.fleet ?? {}));
    httpMock.expectOne('/v2/quota').flush(QUOTA);
    httpMock.expectOne('/v2/admin/reclaimable').flush(reclaimable(reads.reclaimable ?? 0));
    serveCatalog();
    await fixture.whenStable();
    fixture.detectChanges();
  }

  /** The catalog store's own fan-out, which the board's inputs come from. */
  function serveCatalog(): void {
    httpMock.expectOne('/v2/admin/machine-families').flush({families: []});
    httpMock.expectOne('/v2/machine-types').flush({items: [], total: 0});
    httpMock.expectOne('/v2/admin/zones').flush({zones: [], curated_shapes: []});
    httpMock.expectOne('/v2/admin/catalog/freshness').flush({built_at: NOW, partial: []});
  }

  function kpi(label: string): HTMLElement | undefined {
    return [...(fixture.nativeElement as HTMLElement).querySelectorAll<HTMLElement>('.kpi')].find(
      card => card.querySelector('.l')?.textContent?.includes(label)
    );
  }

  it('states what is holding placement back from the counts the server sent', async () => {
    await serveLoad({fleet: {zones_cooling: 1, zones_blocked: 2, zones_constrained: 1}});
    expect(kpi('enabled zones clear')?.querySelector('.note')?.textContent?.trim()).toBe(
      '1 cooling · 2 blocked · 1 Spot-constrained'
    );
  });

  it('says nothing is holding placement back when no zone is short of clear', async () => {
    await serveLoad();
    expect(kpi('enabled zones clear')?.querySelector('.note')?.textContent?.trim()).toBe(
      'nothing is holding placement back'
    );
  });

  it('states the reclaimable rows the server totalled', async () => {
    await serveLoad({reclaimable: 7});
    expect(kpi('reclaimable now')?.querySelector('.v')?.textContent?.trim()).toBe('7');
  });

  it('renders a failed reclaimable read as an error, never as nothing to reclaim', async () => {
    httpMock.expectOne('/v2/admin/capacity').flush(snapshot({}));
    httpMock.expectOne('/v2/quota').flush(QUOTA);
    httpMock
      .expectOne('/v2/admin/reclaimable')
      .flush('boom', {status: 500, statusText: 'Server Error'});
    serveCatalog();
    await fixture.whenStable();
    fixture.detectChanges();

    const el = fixture.nativeElement as HTMLElement;
    expect(kpi('reclaimable now')?.querySelector('.v')?.textContent?.trim()).toBe('-');
    expect(el.querySelector('.split .error-banner')?.textContent).toBeTruthy();
  });
});
