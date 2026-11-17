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
 * One bar idiom for every pool, and one absence that must not be drawn as a
 * zero-length fill: Google publishes nothing for a per-family metric until the
 * family is first used, and placement treats what it cannot read as open.
 */

import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {beforeEach, describe, expect, it} from 'vitest';
import type {CapacityPool} from '../../models/capacity.model';
import {HeadroomPanelComponent} from './headroom-panel.component';

function pool(overrides: Partial<CapacityPool> = {}): CapacityPool {
  return {
    region: 'europe-west4',
    pool: 'CPUS',
    kind: 'cpu',
    families: ['n1'],
    spot: false,
    state: 'open',
    usage: 1600,
    limit: 40960,
    reserve: 4096,
    syncedAt: null,
    ...overrides,
  };
}

describe('HeadroomPanelComponent', () => {
  let fixture: ComponentFixture<HeadroomPanelComponent>;

  beforeEach(async () => {
    await TestBed.configureTestingModule({imports: [HeadroomPanelComponent]}).compileComponents();
    fixture = TestBed.createComponent(HeadroomPanelComponent);
  });

  function render(pools: CapacityPool[]): HTMLElement {
    fixture.componentRef.setInput('pools', pools);
    fixture.detectChanges();
    return fixture.nativeElement as HTMLElement;
  }

  it('states the numbers in full, grouped, rather than as a share', () => {
    const el = render([pool()]);
    expect(el.querySelector('.pval')?.textContent?.trim()).toBe('1,600 of 40,960');
  });

  it('draws a pool with no series as a dashed track, never as a zero', () => {
    const el = render([pool({state: 'unknown', usage: 0, limit: 0, reserve: 0})]);
    const bar = el.querySelector('.pool');
    expect(bar?.classList.contains('unknown')).toBe(true);
    expect(bar?.querySelector('.fill')).toBeNull();
    expect(bar?.querySelector('.pval')?.textContent?.trim()).toBe('no data until first use');
  });

  it('warms the fill past the reserve and reddens it at the wall', () => {
    const el = render([
      pool({pool: 'CPUS', state: 'near_reserve'}),
      pool({pool: 'SSD_TOTAL_GB', kind: 'disk', state: 'at_wall'}),
    ]);
    const fills = el.querySelectorAll('.fill');
    expect(fills[0].classList.contains('warm')).toBe(true);
    expect(fills[1].classList.contains('hot')).toBe(true);
  });

  it('names each pool by what draws it, with the metric beside it', () => {
    const el = render([
      pool({pool: 'PREEMPTIBLE_CPUS', spot: true, families: ['n1', 'n2']}),
      pool({pool: 'SSD_TOTAL_GB', kind: 'disk'}),
      pool({pool: 'CPUS_PER_VM_FAMILY:C4', families: []}),
    ]);
    const names = [...el.querySelectorAll('.pname')].map(name => name.textContent?.trim());
    expect(names[0]).toContain('Spot vCPUs');
    expect(names[0]).toContain('PREEMPTIBLE_CPUS · n1, n2');
    expect(names[1]).toContain('Boot disk GB');
    // No families left to name it by, so the pool says it is shared rather than
    // rendering an empty label.
    expect(names[2]).toContain('Shared vCPUs');
  });

  it('groups the bars under the region that accounts them', () => {
    const el = render([pool(), pool({region: 'us-east5', pool: 'CPUS'})]);
    const regions = [...el.querySelectorAll('.prname')].map(name => name.textContent?.trim());
    expect(regions).toEqual(['europe-west4', 'us-east5']);
  });
});
