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

import {describe, expect, it} from 'vitest';
import type {FleetHost} from '../../models/autoscaler.model';
import {
  bandTotals,
  buildFleetRuler,
  fleetHeadline,
  fleetOrder,
  groupByClass,
  ordinal,
  refusalPhrase,
} from './fleet-band.model';

function host(over: Partial<FleetHost> = {}): FleetHost {
  return {
    id: 'h1',
    name: 'cf-pool-1',
    zone: 'europe-west4-a',
    machineType: 'n1-standard-8',
    gpuType: '',
    gpuCount: 0,
    status: 'running',
    hostClass: 'fleet',
    origin: 'pool',
    pool: 'default',
    maxDevices: 3,
    backedSlots: 3,
    liveInstances: 0,
    errorInstances: 0,
    occupants: [],
    standby: true,
    failingTerm: '',
    shortenOrder: 0,
    expiresAt: null,
    createdAt: new Date('2026-08-14T09:00:00Z'),
    ...over,
  };
}

describe('the fleet ruler', () => {
  // A device costs its host as many slots as it has guests. Drawn as one unit
  // it would show a full host as nearly empty, the same arithmetic that
  // oversubscribes a host silently.
  it('sizes a device by the instance slots it holds', () => {
    const notches = buildFleetRuler(
      host({
        maxDevices: 3,
        backedSlots: 3,
        liveInstances: 2,
        occupants: [{name: 'perf-1', instances: 2, errored: false}],
      })
    );

    expect(notches).toHaveLength(2);
    expect(notches[0]).toEqual({kind: 'occupied', span: 2, label: 'perf-1'});
    expect(notches[1].kind).toBe('free');
  });

  // An error row holds real slots with a dead thing in them. Conflating it with
  // an unbacked slot would blame the host image for a device failure.
  it('separates a slot held by an error row from an unbacked one', () => {
    const notches = buildFleetRuler(
      host({
        maxDevices: 5,
        backedSlots: 3,
        liveInstances: 1,
        errorInstances: 1,
        occupants: [
          {name: 'alive', instances: 1, errored: false},
          {name: 'dead', instances: 1, errored: true},
        ],
      })
    );

    expect(notches.map(n => n.kind)).toEqual([
      'occupied',
      'error-held',
      'free',
      'unbacked',
      'unbacked',
    ]);
  });

  // The whole tail, never a sample. Eliding it would understate the
  // over-admission by exactly the amount that makes it worth drawing.
  it('draws the entire unbacked tail', () => {
    const notches = buildFleetRuler(host({maxDevices: 9, backedSlots: 3}));

    expect(notches.filter(n => n.kind === 'unbacked')).toHaveLength(6);
  });

  // A host that over-admitted AND filled past what the image backs has no free
  // slots, not a negative number of them.
  it('never renders negative free space', () => {
    const notches = buildFleetRuler(
      host({
        maxDevices: 3,
        backedSlots: 3,
        liveInstances: 5,
        occupants: [{name: 'huge', instances: 5, errored: false}],
      })
    );

    expect(notches.filter(n => n.kind === 'free')).toHaveLength(0);
  });
});

describe('the host headline', () => {
  it('states backed slots, and promises nothing extra', () => {
    expect(fleetHeadline(host({maxDevices: 3, backedSlots: 3, liveInstances: 2}))).toBe(
      '2 of 3 backed'
    );
  });

  // The promise renders only where it exceeds what the image backs. A phrase on
  // every host stops being read on the host where it matters.
  it('names the promise only when the host over-admitted', () => {
    expect(fleetHeadline(host({maxDevices: 16, backedSlots: 10, liveInstances: 0}))).toBe(
      '0 of 10 backed · 16 promised'
    );
  });
});

describe('the band totals', () => {
  // THE rule. Summing max_devices is exactly the claim the ruler exists to
  // refute. On 16-slot hosts against a 10-slot image it overstates capacity by
  // 60%, and the number would look authoritative.
  it('sums backed slots and never the promise', () => {
    const totals = bandTotals([
      host({id: 'a', maxDevices: 16, backedSlots: 10, liveInstances: 2}),
      host({id: 'b', maxDevices: 16, backedSlots: 10, liveInstances: 1}),
    ]);

    expect(totals.backed).toBe(20);
    expect(totals.live).toBe(3);
  });

  it('breaks out the slots held by dead rows', () => {
    const totals = bandTotals([
      host({
        liveInstances: 1,
        errorInstances: 2,
        occupants: [
          {name: 'alive', instances: 1, errored: false},
          {name: 'dead', instances: 2, errored: true},
        ],
      }),
    ]);

    expect(totals.errored).toBe(2);
    expect(totals.devices).toBe(1);
  });
});

describe('the class grouping', () => {
  // origin=auto is FLEET. Excluding it, on the intuition that "the fleet" means
  // only what the keeper itself bought, would make this page's fleet disagree
  // with the keeper's own arithmetic.
  it('counts a declaratively provisioned host as the fleet’s', () => {
    const groups = groupByClass([
      host({id: 'a', origin: 'auto', hostClass: 'fleet'}),
      host({id: 'b', origin: 'pool', hostClass: 'fleet'}),
      host({id: 'c', origin: 'user', hostClass: 'reserved'}),
    ]);

    expect(groups[0].hostClass).toBe('fleet');
    expect(groups[0].hosts).toHaveLength(2);
  });

  it('puts the fleet first and drops the classes with no hosts', () => {
    const groups = groupByClass([
      host({id: 'a', hostClass: 'batch'}),
      host({id: 'b', hostClass: 'fleet'}),
    ]);

    expect(groups.map(g => g.hostClass)).toEqual(['fleet', 'batch']);
  });
});

describe('the band order', () => {
  // Under a surplus the reclamation order leads, because "which hosts does
  // lowering this take?" is the page's one opaque consequence.
  it('leads with the hosts a reduction reaches, in that order', () => {
    const ordered = fleetOrder([
      host({id: 'z', name: 'zeta', shortenOrder: 0}),
      host({id: 'b', name: 'beta', shortenOrder: 2}),
      host({id: 'a', name: 'alpha', shortenOrder: 1}),
    ]);

    expect(ordered.map(h => h.name)).toEqual(['alpha', 'beta', 'zeta']);
  });

  // A stable fallback. Sorting by a state would move rows under a reader every
  // time one flipped.
  it('falls back to name when nothing is being reclaimed', () => {
    const ordered = fleetOrder([host({id: 'b', name: 'beta'}), host({id: 'a', name: 'alpha'})]);

    expect(ordered.map(h => h.name)).toEqual(['alpha', 'beta']);
  });
});

describe('the refusal phrase', () => {
  it('names the lease against the lead it falls inside', () => {
    const now = new Date('2026-08-14T12:00:00Z');
    const phrase = refusalPhrase(
      host({failingTerm: 'lease', expiresAt: new Date('2026-08-14T12:04:00Z')}),
      600,
      now
    );

    expect(phrase).toContain('4m');
    expect(phrase).toContain('10m provisioning lead');
  });

  // A host that is counted has no exception to report, and a phrase rendered on
  // every row is a phrase nobody reads on the row that matters.
  it('is empty for a host placement will use', () => {
    expect(refusalPhrase(host(), 600, new Date())).toBe('');
  });

  // The other classes are drawn muted because their rules are not the keeper's.
  // Explaining their placement here would be answering a question nobody asked
  // on this page.
  it('is empty for a host that is not the fleet’s', () => {
    expect(
      refusalPhrase(host({hostClass: 'reserved', failingTerm: 'class'}), 600, new Date())
    ).toBe('');
  });
});

describe('ordinals', () => {
  it('reads naturally for the positions a cycle can reach', () => {
    expect([1, 2, 3, 4, 11, 12, 13, 21].map(ordinal)).toEqual([
      '1st',
      '2nd',
      '3rd',
      '4th',
      '11th',
      '12th',
      '13th',
      '21st',
    ]);
  });
});
