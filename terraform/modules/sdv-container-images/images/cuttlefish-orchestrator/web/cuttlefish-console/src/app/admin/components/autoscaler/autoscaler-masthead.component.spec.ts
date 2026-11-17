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

import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {describe, expect, it} from 'vitest';
import type {AutoscalerSnapshot} from '../../models/autoscaler.model';
import {AutoscalerMastheadComponent} from './autoscaler-masthead.component';

/** The four numbers the binding comparison reads, and nothing else. */
interface Bounds {
  target: number;
  standby: number;
  floor: number;
  total: number;
}

function snapshotOf(bounds: Bounds): AutoscalerSnapshot {
  const status = {
    mode: 'on',
    target: bounds.target,
    standby: bounds.standby,
    floor: bounds.floor,
    total: bounds.total,
    draining: 0,
    misses: 0,
    demand: {servable: 0, unservable: 0, gpuRequired: 0, needsLargerHost: 0, oversizedGroup: 0},
    schedule: 0,
    signal: 'standby',
    limitedBy: '',
    partial: false,
  };
  return {
    fleet: {
      status,
      policy: {
        mode: 'on',
        tiers: {default: {standby: bounds.target, floor: bounds.floor, ladder: [], schedules: []}},
        floor: bounds.floor,
        demandHorizonSec: 3600,
        drainGraceSec: 480,
        isSet: true,
      },
      deviceSlots: 0,
      gpuDegradation: {degraded: 0, wanted: 0},
    },
    // The row under test is the default pool's, composed from the same status.
    pools: [{pool: 'default', status, deficit: 0, surplus: 0, reason: 'acting', ladder: []}],
    limits: {maxStandbyHosts: 16},
  } as unknown as AutoscalerSnapshot;
}

describe('AutoscalerMastheadComponent', () => {
  let fixture: ComponentFixture<AutoscalerMastheadComponent>;

  function render(bounds: Bounds): HTMLElement {
    fixture = TestBed.createComponent(AutoscalerMastheadComponent);
    fixture.componentRef.setInput('snapshot', snapshotOf(bounds));
    fixture.detectChanges();
    return fixture.nativeElement as HTMLElement;
  }

  // Both promises are drawn whatever the fleet is doing. Drawing only the one
  // currently deciding leaves an operator who set the other with nowhere on the
  // page to find it, which is the confusion the second bound came out of.
  it('states both promises at once', () => {
    const root = render({target: 1, standby: 1, floor: 3, total: 4});

    expect(root.textContent).toContain('machines kept free');
    expect(root.textContent).toContain('machines kept running');
    expect(root.textContent).toContain('floor 3 → 4 now');
  });

  // The binding promise is the one the fleet has less room above, the same
  // comparison behind the server's own buy and release arithmetic. Each row is
  // a fleet where the two answers differ, so a derivation reading one bound
  // alone cannot pass the pair.
  it.each([
    {
      name: 'a fleet sitting on its floor with room to spare free',
      bounds: {target: 0, standby: 2, floor: 2, total: 2},
      binding: 'machines kept running',
      copy: 'the fleet stops shrinking here',
    },
    {
      name: 'a fleet well above its floor with nothing free',
      bounds: {target: 2, standby: 2, floor: 1, total: 6},
      binding: 'machines kept free',
      copy: 'a machine a device takes is replaced',
    },
    {
      name: 'equal slack goes to the cheaper explanation',
      bounds: {target: 1, standby: 2, floor: 3, total: 4},
      binding: 'machines kept free',
      copy: 'a machine a device takes is replaced',
    },
  ])('names $name as bound by the $binding', ({bounds, binding, copy}) => {
    const root = render(bounds);

    const which = root.querySelector('.which');
    expect(which?.textContent).toContain(binding);
    expect(which?.textContent).toContain(copy);
    // Never colour alone, and never on the wrong line: the marked equation and
    // the named promise have to agree, or the page says two things.
    const marked = root.querySelector('.equation.binding');
    expect(marked?.textContent).toContain(binding === 'machines kept running' ? 'floor' : 'max(');
  });
});
