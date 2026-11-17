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
import type {
  AutoscalerSnapshot,
  FleetHost,
  FleetPool,
  GateReason,
  LadderRung,
} from '../../models/autoscaler.model';
import type {AutoscaleMode, FleetStatus} from '../../models/control-plane.model';
import type {MachineCard} from './topology-canvas.model';
import {buildCanvas, tierFor} from './topology-canvas.model';

const NOW = new Date('2026-08-14T09:00:00Z');

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
    createdAt: NOW,
    ...over,
  };
}

/** One shape of the order as the server judges it; nothing observed by default. */
function rung(machineType: string, over: Partial<LadderRung> = {}): LadderRung {
  return {
    machineType,
    machineFamily: machineType.split('-')[0],
    slots: 3,
    zones: 3,
    stockedOutZones: 0,
    newestStockoutAt: null,
    gpuType: '',
    gpuCount: 0,
    landing: false,
    refused: '',
    ...over,
  };
}

/** A second pool's entry, with its own composed status. */
function poolOf(name: string, over: Partial<FleetPool> = {}): FleetPool {
  return {
    pool: name,
    status: {
      mode: 'on',
      target: 1,
      standby: 0,
      draining: 0,
      misses: 0,
      demand: {servable: 0, unservable: 0, gpuRequired: 0, needsLargerHost: 0, oversizedGroup: 0},
      schedule: 0,
      floor: 0,
      total: 0,
      signal: 'standby',
      limitedBy: '',
      partial: false,
    },
    deficit: 0,
    surplus: 0,
    reason: 'acting',
    ladder: [],
    ...over,
  };
}

interface Shape {
  mode: AutoscaleMode;
  target: number;
  standby: number;
  deficit: number;
  surplus: number;
  reason: GateReason;
  hosts: FleetHost[];
  ladder: LadderRung[];
  /** Appended after the default pool's own entry. */
  morePools: FleetPool[];
}

function snapshotOf(over: Partial<Shape> = {}): AutoscalerSnapshot {
  const s: Shape = {
    mode: 'on',
    target: 3,
    standby: 1,
    deficit: 0,
    surplus: 0,
    reason: 'acting',
    hosts: [],
    ladder: [],
    morePools: [],
    ...over,
  };
  const status: FleetStatus = {
    mode: s.mode,
    target: s.target,
    standby: s.standby,
    draining: 0,
    misses: 3,
    demand: {servable: 3, unservable: 0, gpuRequired: 0, needsLargerHost: 0, oversizedGroup: 0},
    schedule: 0,
    floor: 0,
    total: s.standby,
    signal: 'demand',
    limitedBy: '',
    partial: false,
  };
  return {
    now: NOW,
    fleet: {
      status,
      policy: {
        mode: s.mode,
        tiers: {default: {standby: 2, floor: 0, ladder: [], schedules: []}},
        floor: 0,
        demandHorizonSec: 3600,
        drainGraceSec: 480,
        shapeLadder: [],
        isSet: true,
      },
      deviceSlots: 9,
      gpuDegradation: {degraded: 0, wanted: 0},
    },
    limits: {
      maxStandbyHosts: 16,
      maxTopUpsPerCycle: 2,
      maxShortensPerCycle: 2,
      maxCollapsesPerCycle: 2,
      quotaReserveHosts: 1,
      cycleSeconds: 30,
      provisionLeadSeconds: 600,
      hostImageBackedSlots: 10,
      minShortenAgeSeconds: 600,
    },
    gate: {
      reason: s.reason,
      deficit: s.deficit,
      surplus: s.surplus,
      quotaAvailable: 7,
      quotaSpendable: 6,
      leaderHeld: true,
      leaderHolder: 'instance-1',
    },
    // The default pool's entry is the fleet block split the way the keeper acts
    // on it, exactly as the server composes both from one derivation.
    pools: [
      {
        pool: 'default',
        status,
        deficit: s.deficit,
        surplus: s.surplus,
        reason: s.reason,
        ladder: s.ladder,
      },
      ...s.morePools,
    ],
    hosts: s.hosts,
    shapes: [],
    ladder: s.ladder,
    activity: {provisions: 2, rescues: 1, shortens: 0, collapses: 0},
    activityWindowSec: 86_400,
    armImageReady: false,
    effectiveDensity: 3,
    densityMachineType: 'n1-standard-8',
    demandHorizonSec: 3600,
  };
}

/** A fleet of `count` interchangeable machines, for the count breakpoints. */
function fleetOf(count: number, over: Partial<FleetHost> = {}): FleetHost[] {
  return Array.from({length: count}, (_, i) => host({id: `h${i}`, name: `cf-pool-${i}`, ...over}));
}

function cards(snapshot: AutoscalerSnapshot): MachineCard[] {
  return buildCanvas(snapshot)
    .regions.flatMap(region => region.banks)
    .flatMap(bank => [...bank.cards]);
}

function cardNamed(snapshot: AutoscalerSnapshot, name: string): MachineCard {
  const found = cards(snapshot).find(card => card.name === name);
  if (!found) {
    throw new Error(`no card named ${name}`);
  }
  return found;
}

describe('the tier', () => {
  // Count arithmetic, never measurement: the same snapshot has to draw
  // identically on every refresh, and a layout that reads its own rendered size
  // cannot promise that.
  it('picks fidelity by the machine count alone', () => {
    expect([1, 16, 17, 48, 49, 400].map(tierFor)).toEqual([
      'cards',
      'cards',
      'compact',
      'compact',
      'clusters',
      'clusters',
    ]);
  });

  it('drops the slot grid past the full-card count', () => {
    const full = cards(snapshotOf({hosts: fleetOf(16)}));
    const compact = cards(snapshotOf({hosts: fleetOf(17)}));

    expect(full[0].slots.length).toBe(3);
    expect(compact[0].slots).toEqual([]);
    // Still one card per machine, still focusable.
    expect(compact).toHaveLength(17);
  });

  it('rolls machines into one tile per state past the compact count', () => {
    const canvas = buildCanvas(snapshotOf({hosts: fleetOf(49)}));
    const region = canvas.regions[0];
    const clusters = region.banks.flatMap(bank => [...bank.clusters]);

    expect(region.tier).toBe('clusters');
    expect(region.banks.flatMap(bank => bank.cards)).toEqual([]);
    expect(clusters).toHaveLength(1);
    expect(clusters[0].count).toBe(49);
    expect(clusters[0].word).toContain('free');
    // Expansion is a filtered detail at full fidelity, not a second table.
    expect(clusters[0].members[0].slots.length).toBe(3);
  });

  // Fidelity answers "can this many cards be read", which is a question about
  // one region: a crowded default pool must not cost a two-machine pool its
  // slot grids.
  it('picks fidelity per pool, from that pool’s own machine count', () => {
    const canvas = buildCanvas(
      snapshotOf({
        hosts: [...fleetOf(20), ...fleetOf(2, {pool: 'gpu'})],
        morePools: [poolOf('gpu')],
      })
    );

    expect(canvas.regions.map(region => [region.pool, region.tier])).toEqual([
      ['default', 'compact'],
      ['gpu', 'cards'],
    ]);
  });

  // Machines outside the autoscaler are background, so they lose fidelity one
  // tier before the fleet does.
  it('collapses the machines that are not the autoscaler’s one tier early', () => {
    const others = [host({id: 'u', name: 'cf-user-1', hostClass: 'reserved', standby: false})];

    expect(
      buildCanvas(snapshotOf({hosts: [...fleetOf(8), ...others]})).strip[0].cards
    ).toHaveLength(1);
    expect(buildCanvas(snapshotOf({hosts: [...fleetOf(20), ...others]})).strip[0].cards).toEqual(
      []
    );
  });
});

describe('a machine card', () => {
  // A device can want eight slots. Drawn as one unit square it is the
  // arithmetic that oversubscribes a machine silently.
  it('sizes a device by its instance count', () => {
    const card = cardNamed(
      snapshotOf({
        hosts: [
          host({
            name: 'cf-pool-2b1',
            standby: false,
            failingTerm: 'idle',
            maxDevices: 4,
            backedSlots: 4,
            liveInstances: 3,
            occupants: [
              {name: 'web-1', instances: 2, errored: false},
              {name: 'web-2', instances: 1, errored: false},
            ],
          }),
        ],
      }),
      'cf-pool-2b1'
    );

    expect(card.slots.map(slot => [slot.label, slot.span])).toEqual([
      ['web-1', 2],
      ['web-2', 1],
      ['free', 1],
    ]);
  });

  // The instances of one device share the machine's radio medium, so placement
  // fences the machine to them: a stranger's device would join their radio.
  it('draws a group as one block over the whole machine', () => {
    const card = cardNamed(
      snapshotOf({
        hosts: [
          host({
            name: 'cf-auto-9c4',
            standby: false,
            failingTerm: 'idle',
            maxDevices: 4,
            backedSlots: 4,
            liveInstances: 4,
            occupants: [{name: 'perf-1', instances: 4, errored: false}],
          }),
        ],
      }),
      'cf-auto-9c4'
    );

    expect(card.slots).toEqual([]);
    expect(card.group).toEqual({
      name: 'perf-1',
      instances: 4,
      note: '4 instances · takes the whole machine, exclusive',
    });
  });

  // A dead row sits in a real slot the image backs; unbacked is a slot the
  // image cannot launch at all. Conflating them blames the machine's image for
  // a device failure.
  it('separates a slot held by a dead row from an unbacked one', () => {
    const card = cardNamed(
      snapshotOf({
        hosts: [
          host({
            name: 'cf-pool-8d0',
            standby: false,
            failingTerm: 'idle',
            maxDevices: 5,
            backedSlots: 3,
            errorInstances: 1,
            occupants: [{name: 'stranded-1', instances: 1, errored: true}],
          }),
        ],
      }),
      'cf-pool-8d0'
    );

    const held = card.slots.find(slot => slot.kind === 'error-held');
    const unbacked = card.slots.filter(slot => slot.kind === 'unbacked');
    expect(held?.title).toContain('in error');
    expect(unbacked).toHaveLength(2);
    expect(unbacked[0].title).toContain('not backed by its image');
  });

  it('states a free machine as free, in words', () => {
    const card = cardNamed(snapshotOf({hosts: [host({name: 'cf-auto-7a3'})]}), 'cf-auto-7a3');

    expect(card.state).toBe('free');
    expect(card.word).toContain('ready for a device');
    expect(card.lines.join(' ')).toContain('warm');
  });

  // A machine coming up is counted by placement, and the minutes it takes are
  // the whole argument for keeping free machines warm.
  it('draws a machine that is coming up as arriving', () => {
    const card = cardNamed(
      snapshotOf({
        hosts: [
          host({
            name: 'cf-auto-new',
            status: 'provisioning',
            standby: false,
            createdAt: new Date(NOW.getTime() - 3 * 60_000),
          }),
        ],
      }),
      'cf-auto-new'
    );

    expect(card.state).toBe('arriving');
    expect(card.lines.join(' ')).toContain('started 3m ago');
  });
});

describe('what the autoscaler owns', () => {
  // Dedicated, batch and user-made machines are not its inventory. Drawing them
  // connected claims control it does not have.
  it('banks the fleet into its pool’s region and strips everything else', () => {
    const canvas = buildCanvas(
      snapshotOf({
        hosts: [
          host({id: 'a', name: 'cf-pool-a'}),
          host({id: 'b', name: 'cvd-perf-3', hostClass: 'dedicated', standby: false}),
          host({id: 'c', name: 'cf-batch-1', hostClass: 'batch', standby: false}),
          host({id: 'd', name: 'cf-user-1', hostClass: 'reserved', standby: false}),
        ],
      })
    );

    expect(cards(snapshotOf({hosts: [host({id: 'a', name: 'cf-pool-a'})]}))).toHaveLength(1);
    expect(canvas.strip.map(group => group.hostClass)).toEqual(['dedicated', 'batch', 'reserved']);
    expect(canvas.strip[0].label).toBe('Exclusive');
  });

  // A machine is counted in the pool stamped on it, never inferred from its
  // shape or its name: two pools may share a shape, and the stamp is what the
  // keeper's own inventory groups by.
  it('groups machines into regions by the stamped pool', () => {
    const canvas = buildCanvas(
      snapshotOf({
        hosts: [
          host({id: 'a', name: 'cf-pool-a'}),
          host({id: 'b', name: 'cf-gpu-b', pool: 'gpu', gpuType: 'nvidia-l4', gpuCount: 1}),
        ],
        morePools: [poolOf('gpu')],
      })
    );

    const byRegion = canvas.regions.map(region => [
      region.pool,
      region.banks.flatMap(bank => bank.cards).map(card => card.name),
    ]);
    expect(byRegion).toEqual([
      ['default', ['cf-pool-a']],
      ['gpu', ['cf-gpu-b']],
    ]);
  });

  // origin=auto IS fleet: a machine a declarative create provisioned because
  // placement found nothing is counted, rescuable and shortenable. Excluding it
  // would make this page disagree with the keeper's arithmetic.
  it('counts a declaratively provisioned machine as the autoscaler’s', () => {
    const canvas = buildCanvas(
      snapshotOf({hosts: [host({name: 'cf-auto-7a3', origin: 'auto', hostClass: 'fleet'})]})
    );

    expect(
      canvas.regions.flatMap(r => r.banks.flatMap(bank => bank.cards)).map(card => card.name)
    ).toEqual(['cf-auto-7a3']);
    expect(canvas.strip).toEqual([]);
  });

  // Σ backed, never Σ max_devices: on 16-slot machines against a 10-slot image
  // the promise overstates capacity by 60%, and the figure would look
  // authoritative.
  it('totals backed slots per pool and never the promise', () => {
    const totals = buildCanvas(
      snapshotOf({
        target: 4,
        hosts: [
          host({id: 'a', maxDevices: 16, backedSlots: 10, liveInstances: 2, standby: false}),
          host({id: 'b', maxDevices: 16, backedSlots: 10, liveInstances: 1, standby: false}),
        ],
      })
    ).totals;

    expect(totals).toHaveLength(1);
    expect(totals[0]).toContain('default · 3 of 20 backed slots in use');
    expect(totals[0]).toContain('12 slots planned (target 4 × 3)');
    expect(totals[0]).not.toContain('32');
  });
});

describe('the engine', () => {
  it('states both permissions and one gap line per pool', () => {
    const engine = buildCanvas(
      snapshotOf({target: 3, deficit: 2, morePools: [poolOf('gpu', {surplus: 1})]})
    ).engine;

    expect(engine.provisions).toBe(true);
    expect(engine.lowers).toBe(true);
    // The same fact each region states about itself, on the loop that acts on
    // it: the pools' gaps are never added into one number.
    expect(engine.gaps).toEqual([
      {pool: 'default', line: '2 short', short: true},
      {pool: 'gpu', line: '1 spare', short: false},
    ]);
  });

  // THE correctness property. provision_only rescues and provisions while
  // lowering no lease, so nothing that lowers one may be drawn as permitted:
  // the keeper gates shorten and collapse on the reclaim permission.
  it('permits provisioning but no lease-lowering under provision_only', () => {
    const snapshot = snapshotOf({
      mode: 'provision_only',
      surplus: 1,
      reason: 'provision_only',
      hosts: [host({name: 'cf-pool-spare', shortenOrder: 1, expiresAt: NOW})],
    });
    const canvas = buildCanvas(snapshot);

    expect(canvas.engine.provisions).toBe(true);
    expect(canvas.engine.lowers).toBe(false);
    const card = cardNamed(snapshot, 'cf-pool-spare');
    expect(card.state).not.toBe('leaving');
    expect(card.lines.join(' ')).toContain('not in this mode');
    expect(cards(snapshot).some(c => c.state === 'leaving')).toBe(false);
  });

  it('draws the machines a reduction would take, in that order, when it may', () => {
    const snapshot = snapshotOf({
      surplus: 2,
      hosts: [
        host({id: 'a', name: 'cf-pool-a', shortenOrder: 2, expiresAt: NOW}),
        host({id: 'b', name: 'cf-pool-b', shortenOrder: 1, expiresAt: NOW}),
      ],
    });

    expect(cardNamed(snapshot, 'cf-pool-b').lines.join(' ')).toContain('1st to give back');
    // A shortened lease deletes nothing; it brings the reaper forward.
    expect(cardNamed(snapshot, 'cf-pool-b').lines.join(' ')).toContain('deletes nothing');
    expect(cards(snapshot).map(card => card.name)).toEqual(['cf-pool-b', 'cf-pool-a']);
  });

  // Six causes are indistinguishable in a still drawing without a sentence
  // naming which holds. The server decides which one; the wording is the
  // console's.
  it('names the cause the server chose, for every cause', () => {
    const causes: Array<[GateReason, string]> = [
      ['acting', 'acting on this target'],
      ['no_leader', 'no cycle is running anywhere'],
      ['mode_off', 'nothing is permitted to act on it'],
      ['provision_only', 'run their leases out'],
      ['quota_reserved', 'silent and permanent'],
      ['capped', 'over several cycles'],
      ['target_met', 'holds what the target asks for'],
    ];

    for (const [reason, phrase] of causes) {
      expect(buildCanvas(snapshotOf({reason})).engine.cause).toContain(phrase);
    }
  });

  // No leader and a mode that permits nothing gate every pool at once, so a
  // region repeating them would state the same fault once per pool.
  it('keeps the deployment-wide causes off the pool regions', () => {
    const held = buildCanvas(snapshotOf({reason: 'mode_off'})).regions[0];
    const own = buildCanvas(snapshotOf({reason: 'target_met'})).regions[0];

    expect(held.cause).toBe('');
    expect(own.cause).toContain('holds what the target asks for');
  });

  it('carries what the audit log recorded rather than a moving line', () => {
    expect(buildCanvas(snapshotOf()).engine.activity).toBe(
      'over the last 24h · 2 bought · 1 renewed · 0 given back · 0 collapsed'
    );
  });
});

describe('requests that found no machine', () => {
  // With one pool every miss belongs to it, and a line would restate the
  // totals beside it.
  it('names no pool while there is only one', () => {
    expect(buildCanvas(snapshotOf()).unplaced).toEqual([]);
  });

  it('attributes the window to each pool once a second one exists', () => {
    const unplaced = buildCanvas(
      snapshotOf({
        morePools: [
          poolOf('gpu', {
            status: {
              ...poolOf('gpu').status,
              demand: {
                servable: 2,
                unservable: 1,
                gpuRequired: 1,
                needsLargerHost: 0,
                oversizedGroup: 0,
              },
            },
          }),
        ],
      })
    ).unplaced;

    expect(unplaced).toEqual([
      {pool: 'default', line: '3 requests · 3 a free machine in that pool would have taken'},
      {pool: 'gpu', line: '3 requests · 2 a free machine in that pool would have taken'},
    ]);
  });
});

describe('the gap drawn as machines', () => {
  // The deficit rather than the target: with three machines standing against a
  // target of three, drawing three of these would say the fleet is empty.
  it('draws one machine-shaped hole per machine short, at the buying density', () => {
    const ghosts = cards(snapshotOf({deficit: 2})).filter(card => card.state === 'ghost');

    expect(ghosts).toHaveLength(2);
    expect(ghosts[0].slots).toHaveLength(3);
    expect(ghosts[0].slots[0].kind).toBe('would');
    expect(ghosts[0].hostId).toBe('');
  });

  it('says a hole cannot be filled while the mode permits no buying', () => {
    const ghost = cards(snapshotOf({mode: 'off', deficit: 1, reason: 'mode_off'}))[0];

    expect(ghost.lines.join(' ')).toContain('permits no buying');
  });

  // A hole drawn as the shape the pool prefers, while the walk has already
  // fallen past it, says the pool is buying something it is not.
  it('draws the shape the order fell back to, and why', () => {
    const snapshot = snapshotOf({
      deficit: 1,
      ladder: [
        rung('c4-standard-16', {slots: 7, stockedOutZones: 3}),
        rung('n4-standard-16', {slots: 7, landing: true}),
      ],
    });

    const ghost = cards(snapshot).find(card => card.state === 'ghost');
    expect(ghost?.name).toBe('would buy n4-standard-16');
    expect(ghost?.shape).toBe('c4-standard-16 stocked out');
    // The shape it fell back to is what sizes the hole, not the catalog default.
    expect(ghost?.slotLine).toBe('7 slots once it is bought');
    expect(ghost?.slots).toHaveLength(7);
  });

  it('draws the preferred shape while it still stands', () => {
    const snapshot = snapshotOf({
      deficit: 1,
      ladder: [rung('c4-standard-16', {slots: 7, landing: true})],
    });

    const ghost = cards(snapshot).find(card => card.state === 'ghost');
    expect(ghost?.name).toBe('would buy this one');
    expect(ghost?.shape).toBe('c4-standard-16');
    expect(ghost?.slotLine).toBe('7 slots once it is bought');
  });

  it('draws the catalog’s default shape for the default pool with no order', () => {
    const ghost = cards(snapshotOf({deficit: 1})).find(card => card.state === 'ghost');

    expect(ghost?.name).toBe('would buy this one');
    expect(ghost?.shape).toBe('n1-standard-8');
    expect(ghost?.slotLine).toBe('3 slots once it is bought');
  });

  // The snapshot names the default shape for the pool every create still lands
  // in, and for no other. A gpu hole sized by the CPU default would state a
  // capacity nobody is buying.
  it('says another pool’s shape is decided at buying time when it has no order', () => {
    const ghost = buildCanvas(snapshotOf({morePools: [poolOf('gpu', {deficit: 1})]}))
      .regions.find(region => region.pool === 'gpu')
      ?.banks.flatMap(bank => bank.cards)
      .find(card => card.state === 'ghost');

    expect(ghost?.name).toBe('would buy a machine');
    expect(ghost?.shape).toContain('decided when it is bought');
    expect(ghost?.slots).toEqual([]);
  });
});

describe('the shape order', () => {
  // No order is the catalog's default shape, and a line drawn for it would put
  // a preference on screen that nobody set.
  it('draws nothing when no order is stored', () => {
    expect(buildCanvas(snapshotOf()).regions[0].ladder).toBeNull();
  });

  // Struck through AND named in the sentence: a line through a name is a mark,
  // and the words under it are what state which shape is being built.
  it('marks the shape with no zone left and the one being built', () => {
    const ladder = buildCanvas(
      snapshotOf({
        ladder: [
          rung('c4-standard-16', {stockedOutZones: 3}),
          rung('n4-standard-16', {stockedOutZones: 1, landing: true}),
          rung('n4-standard-8'),
        ],
      })
    ).regions[0].ladder;

    expect(ladder?.steps).toEqual([
      {shape: 'c4-standard-16', out: true, building: false},
      {shape: 'n4-standard-16', out: false, building: true},
      {shape: 'n4-standard-8', out: false, building: false},
    ]);
    expect(ladder?.sentence).toBe('c4-standard-16 stocked out, fell back to n4-standard-16');
  });

  it('says the preferred shape is the one being built when it is', () => {
    const ladder = buildCanvas(
      snapshotOf({ladder: [rung('c4-standard-16', {landing: true}), rung('n4-standard-16')]})
    ).regions[0].ladder;

    expect(ladder?.sentence).toBe('building c4-standard-16, the preferred shape');
  });

  // The state the server marks no shape in, which is where provisioning has
  // nothing left to try.
  it('says so when every shape in the order is stocked out', () => {
    const out = {stockedOutZones: 3};
    const ladder = buildCanvas(
      snapshotOf({ladder: [rung('c4-standard-16', out), rung('n4-standard-16', out)]})
    ).regions[0].ladder;

    expect(ladder?.steps.every(step => step.out)).toBe(true);
    expect(ladder?.sentence).toBe(
      'every shape in the order is stocked out, so nothing can be built'
    );
  });

  // A shape no zone offers has nothing left to probe for the same reason as one
  // every zone is out of, the walk's own rule, and the line draws it the same.
  it('counts a shape no zone offers as stocked out', () => {
    const ladder = buildCanvas(
      snapshotOf({
        ladder: [rung('c4-standard-16', {zones: 0}), rung('n4-standard-16', {landing: true})],
      })
    ).regions[0].ladder;

    expect(ladder?.steps[0].out).toBe(true);
  });

  // Each pool's line is its own order: the default's fall-through must not be
  // written under a pool whose preferred shape still stands.
  it('keeps each pool’s order on its own region', () => {
    const canvas = buildCanvas(
      snapshotOf({
        ladder: [
          rung('n2-standard-8', {stockedOutZones: 3}),
          rung('n1-standard-8', {landing: true}),
        ],
        morePools: [poolOf('gpu', {ladder: [rung('g2-standard-4', {landing: true})]})],
      })
    );

    expect(canvas.regions[0].ladder?.sentence).toBe(
      'n2-standard-8 stocked out, fell back to n1-standard-8'
    );
    expect(canvas.regions[1].ladder?.sentence).toBe('building g2-standard-4, the preferred shape');
  });
});
