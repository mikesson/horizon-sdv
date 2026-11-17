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
 * The card states four facts about the fleet's Spot capacity, and three of them
 * have an absence that must not read as a measurement. Advice that is switched
 * off, advice the server has judged stale, and a preemption window with nothing
 * to recover. Each of those is a separate branch here.
 */

import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {describe, expect, it} from 'vitest';
import type {SpotCapacity} from '../../models/capacity.model';
import {SpotCapacityCardComponent} from './spot-capacity-card.component';

const NOW = new Date('2026-06-15T10:00:00Z');

const HEALTHY: SpotCapacity = {
  fleet: [
    {model: 'SPOT', status: 'running', count: 3},
    {model: 'SPOT', status: 'provisioning', count: 1},
    {model: 'STANDARD', status: 'running', count: 1},
  ],
  spotMachines: 4,
  standardMachines: 1,
  preempted7d: 2,
  recovered7d: 2,
  unrecovered7d: 0,
  typicalRecoverySeconds: 34,
  advice: {
    enabled: true,
    cells: 36,
    newestSyncedAt: new Date(NOW.getTime() - 4 * 60_000),
    stale: false,
  },
};

async function render(spot: SpotCapacity): Promise<ComponentFixture<SpotCapacityCardComponent>> {
  await TestBed.configureTestingModule({
    imports: [SpotCapacityCardComponent],
  }).compileComponents();
  const fixture = TestBed.createComponent(SpotCapacityCardComponent);
  fixture.componentRef.setInput('spot', spot);
  fixture.componentRef.setInput('serverNow', NOW);
  fixture.detectChanges();
  return fixture;
}

async function text(spot: SpotCapacity): Promise<string> {
  const fixture = await render(spot);
  return (fixture.nativeElement as HTMLElement).textContent ?? '';
}

describe('SpotCapacityCardComponent', () => {
  it('states the model split, the churn and fresh advice', async () => {
    const rendered = await text(HEALTHY);
    expect(rendered).toContain('4 Spot · 1 on-demand');
    expect(rendered).toContain('34s');
    expect(rendered).toContain('fresh · 36 cells · synced 4m ago');
  });

  it('flags the preemptions Google has not given back', async () => {
    const rendered = await text({...HEALTHY, recovered7d: 1, unrecovered7d: 1});
    expect(rendered).toContain('2 · 1 not recovered');
  });

  it('reports a disabled advice sync accurately instead of counting zero cells', async () => {
    const rendered = await text({
      ...HEALTHY,
      advice: {enabled: false, cells: 0, newestSyncedAt: null, stale: true},
    });
    expect(rendered).toContain('off (CCO_SPOT_ADVICE)');
    expect(rendered).not.toContain('stale');
  });

  it('says advice the server judged stale has stopped steering placement', async () => {
    const fixture = await render({
      ...HEALTHY,
      advice: {...HEALTHY.advice, stale: true},
    });
    const el = fixture.nativeElement as HTMLElement;
    expect(el.textContent).toContain('stale, ranking on quota and health only');
    // Stated as a problem, never as one more grey fact.
    expect(el.querySelector('.alarming')?.textContent).toContain('stale');
  });

  it('says a window with no preemptions had nothing to recover, never a duration', async () => {
    const rendered = await text({
      ...HEALTHY,
      preempted7d: 0,
      recovered7d: 0,
      unrecovered7d: 0,
      typicalRecoverySeconds: 0,
    });
    expect(rendered).toContain('nothing to recover');
  });

  it('says recoveries with no measured duration were all recovered', async () => {
    const rendered = await text({...HEALTHY, typicalRecoverySeconds: 0});
    expect(rendered).toContain('all recovered');
  });

  it('reads an empty fleet as a real zero on both models', async () => {
    const rendered = await text({...HEALTHY, fleet: [], spotMachines: 0, standardMachines: 0});
    expect(rendered).toContain('0 Spot · 0 on-demand');
  });
});
