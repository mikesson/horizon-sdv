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
 * The board renders verdicts it did not derive. What is under test is the
 * rendering the server cannot do for it. The stripe class the verdict picks,
 * the countdown against the snapshot's own clock, the shape count against the
 * curated catalog, and the two absences that must not read as zero: a zone the
 * catalog is silent about, and a zone with no advice cell.
 */

import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {beforeEach, describe, expect, it} from 'vitest';
import type {CapacityRegion, CapacityZone} from '../../models/capacity.model';
import type {AdminZone} from '../../models/catalog-admin.model';
import {ZoneBoardComponent} from './zone-board.component';

const NOW = new Date('2026-06-15T10:00:00Z');

/** The fleet's curated shapes, which no single zone below stocks in full. */
const CURATED = ['n1-standard-8', 'n2-standard-8', 'n2-standard-16'];

function zone(overrides: Partial<CapacityZone> = {}): CapacityZone {
  return {
    zone: 'europe-west4-a',
    region: 'europe-west4',
    enabled: true,
    status: 'UP',
    machines: 2,
    arriving: 0,
    devices: 6,
    verdict: 'clear',
    verdictDetail: '',
    coolingUntil: null,
    spot: null,
    cells: [],
    ...overrides,
  };
}

function region(overrides: Partial<CapacityRegion> = {}): CapacityRegion {
  return {
    region: 'europe-west4',
    machines: 2,
    devices: 6,
    zonesEnabled: 1,
    zones: 1,
    quotaState: 'comfortable',
    quotaPool: '',
    quotaUsage: 0,
    quotaLimit: 0,
    ...overrides,
  };
}

function catalogZone(overrides: Partial<AdminZone> = {}): AdminZone {
  return {
    zone: 'europe-west4-a',
    region: 'europe-west4',
    status: 'UP',
    isEnabled: true,
    offeredShapes: ['n1-standard-8', 'n2-standard-8'],
    shapesReported: true,
    ...overrides,
  };
}

describe('ZoneBoardComponent', () => {
  let fixture: ComponentFixture<ZoneBoardComponent>;

  beforeEach(async () => {
    await TestBed.configureTestingModule({imports: [ZoneBoardComponent]}).compileComponents();
    fixture = TestBed.createComponent(ZoneBoardComponent);
    fixture.componentRef.setInput('serverNow', NOW);
    fixture.componentRef.setInput('regions', [region()]);
    fixture.componentRef.setInput('catalog', [catalogZone()]);
    fixture.componentRef.setInput('curatedShapes', CURATED);
  });

  function render(zones: CapacityZone[]): HTMLElement {
    fixture.componentRef.setInput('zones', zones);
    fixture.detectChanges();
    return fixture.nativeElement as HTMLElement;
  }

  it('states the verdict as a word beside the stripe, never as colour alone', () => {
    const el = render([zone()]);
    const tile = el.querySelector('.tile');
    expect(tile?.classList.contains('clear')).toBe(true);
    expect(tile?.querySelector('.word')?.textContent?.trim()).toBe('Clear');
  });

  it('counts the cooldown down against the snapshot clock, not the browser one', () => {
    const el = render([
      zone({
        verdict: 'cooling',
        coolingUntil: new Date(NOW.getTime() + 41 * 60 * 1000),
        verdictDetail: 'capacity failed here 2 time(s) in a row on n1',
      }),
    ]);
    const tile = el.querySelector('.tile');
    expect(tile?.classList.contains('cooling')).toBe(true);
    expect(tile?.querySelector('.word')?.textContent?.trim()).toBe('Cooling · 41 min');
    expect(el.textContent).toContain('capacity failed here 2 time(s) in a row on n1');
  });

  it('renders both counts as terms, never as a share', () => {
    const el = render([zone({machines: 1, devices: 3, arriving: 1})]);
    expect(el.querySelector('.count')?.textContent?.trim()).toBe(
      '1 machine · 3 devices · 1 arriving'
    );
  });

  it('says a zone with no advice cell has no signal rather than none available', () => {
    const el = render([zone()]);
    expect(el.querySelector('.sig')?.textContent).toContain('Spot: no signal');
    // The dot stays the hollow ring: a filled one would read as a measured band.
    const dot = el.querySelector('.dot');
    expect(['high', 'medium', 'low'].some(band => dot?.classList.contains(band))).toBe(false);
  });

  it('says a zone the catalog is silent about is unreported, not offering none', () => {
    fixture.componentRef.setInput('catalog', [
      catalogZone({offeredShapes: [], shapesReported: false}),
    ]);
    const el = render([zone()]);
    expect(el.querySelector('.shapes')?.textContent?.trim()).toBe('shapes unreported');
  });

  it('offers a clear action per cooled cell and none for a quiet zone', () => {
    const quiet = render([zone()]);
    expect(quiet.querySelector('.tile-menu')).toBeNull();

    const cooled = render([
      zone({
        verdict: 'cooling',
        cells: [
          {
            machineFamily: 'n1',
            machines: 1,
            arriving: 0,
            devices: 0,
            verdict: 'cooling',
            verdictDetail: '',
            coolingUntil: null,
            quotaPool: 'CPUS',
            quotaState: 'open',
            spot: null,
            cooldown: {
              machineFamily: 'n1',
              bindingMetric: 'capacity',
              metricLabel: 'capacity',
              regional: false,
              consecutiveFailures: 2,
              inCooldown: true,
              cooldownUntil: new Date(NOW.getTime() + 60_000),
              lastFailureAt: NOW,
              lastReason: 'zone stockout',
            },
          },
        ],
      }),
    ]);
    expect(cooled.querySelector('.tile-menu')).not.toBeNull();
  });

  it('counts the shapes a zone stocks against the curated catalog, not the widest offer', () => {
    const el = render([zone()]);
    // The widest offer on the board is two shapes; the fleet curates three, and
    // the tile has to say which number it is short of.
    expect(el.querySelector('.shapes')?.textContent?.trim()).toBe('2 of 3 shapes');
  });

  it('renders the toggle from the catalog the write lands in, not from the snapshot', () => {
    fixture.componentRef.setInput('catalog', [catalogZone({isEnabled: false})]);
    const el = render([zone({enabled: true})]);
    const toggle = el.querySelector('.foot [role="switch"]');
    expect(toggle?.getAttribute('aria-checked')).toBe('false');
    expect(toggle?.hasAttribute('disabled')).toBe(false);
  });

  it('refuses the toggle for a zone the catalog never reported, instead of dropping the click', () => {
    fixture.componentRef.setInput('catalog', []);
    const el = render([zone()]);
    expect(el.querySelector('.foot [role="switch"]')?.hasAttribute('disabled')).toBe(true);
  });

  it('groups the digits in the rail chip, as the bars below it do', () => {
    fixture.componentRef.setInput('regions', [
      region({quotaState: 'blocked', quotaPool: 'CPUS', quotaUsage: 2912, quotaLimit: 3000}),
    ]);
    const el = render([zone()]);
    expect(el.querySelector('.chip')?.textContent?.trim()).toBe('quota spent, CPUS 2,912 of 3,000');
  });
});
