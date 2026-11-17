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
 * The disclosure holds exactly the zones the board does not, and its rows are
 * the one place a zone outside the placement space can be brought into it. So
 * what is under test is which zones it lists, what it says about their shapes,
 * and that a row states the enablement it would be writing.
 */

import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {beforeEach, describe, expect, it} from 'vitest';
import type {CapacityZone} from '../../models/capacity.model';
import type {AdminZone} from '../../models/catalog-admin.model';
import {OfferedZonesComponent} from './offered-zones.component';

const CURATED = ['n1-standard-8', 'n2-standard-8', 'n2-standard-16'];

function catalogZone(overrides: Partial<AdminZone> = {}): AdminZone {
  return {
    zone: 'us-central1-f',
    region: 'us-central1',
    status: 'UP',
    isEnabled: false,
    offeredShapes: ['n1-standard-8', 'n2-standard-8'],
    shapesReported: true,
    ...overrides,
  };
}

function placedZone(zone: string): CapacityZone {
  return {
    zone,
    region: 'europe-west4',
    enabled: true,
    status: 'UP',
    machines: 1,
    arriving: 0,
    devices: 2,
    verdict: 'clear',
    verdictDetail: '',
    coolingUntil: null,
    spot: null,
    cells: [],
  };
}

describe('OfferedZonesComponent', () => {
  let fixture: ComponentFixture<OfferedZonesComponent>;

  beforeEach(async () => {
    await TestBed.configureTestingModule({imports: [OfferedZonesComponent]}).compileComponents();
    fixture = TestBed.createComponent(OfferedZonesComponent);
    fixture.componentRef.setInput('curatedShapes', CURATED);
    fixture.componentRef.setInput('placed', [placedZone('europe-west4-a')]);
  });

  function render(catalog: AdminZone[]): HTMLElement {
    fixture.componentRef.setInput('catalog', catalog);
    fixture.detectChanges();
    return fixture.nativeElement as HTMLElement;
  }

  it('lists only the zones the placement space does not already hold', () => {
    const el = render([
      catalogZone(),
      catalogZone({zone: 'europe-west4-a', region: 'europe-west4'}),
    ]);
    const rows = el.querySelectorAll('.offered-row');
    expect(rows).toHaveLength(1);
    expect(rows[0].querySelector('.mono')?.textContent?.trim()).toBe('us-central1-f');
  });

  it('counts one offered zone as one, not as "1 more zones"', () => {
    const el = render([catalogZone()]);
    expect(el.querySelector('summary')?.textContent?.trim()).toBe(
      '1 more zone Compute Engine offers this project'
    );
  });

  it('counts several as several', () => {
    const el = render([catalogZone(), catalogZone({zone: 'us-central1-c'})]);
    expect(el.querySelector('summary')?.textContent?.trim()).toBe(
      '2 more zones Compute Engine offers this project'
    );
  });

  it('counts the shapes against the curated catalog', () => {
    const el = render([catalogZone()]);
    expect(el.querySelector('.offered-shapes')?.textContent?.trim()).toBe('2 of 3 shapes');
  });

  it('says a zone the catalog is silent about is unreported, not offering none', () => {
    const el = render([catalogZone({offeredShapes: [], shapesReported: false, status: ''})]);
    expect(el.querySelector('.offered-shapes')?.textContent?.trim()).toBe('shapes unreported');
    expect(el.querySelector('.offered-status')?.textContent?.trim()).toBe('status unreported');
  });

  it('states the enablement the row would be writing rather than an unbound switch', () => {
    const el = render([catalogZone({isEnabled: true})]);
    expect(el.querySelector('[role="switch"]')?.getAttribute('aria-checked')).toBe('true');
  });

  it('renders nothing at all when the board already holds every zone', () => {
    const el = render([catalogZone({zone: 'europe-west4-a', region: 'europe-west4'})]);
    expect(el.querySelector('.offered')).toBeNull();
  });
});
