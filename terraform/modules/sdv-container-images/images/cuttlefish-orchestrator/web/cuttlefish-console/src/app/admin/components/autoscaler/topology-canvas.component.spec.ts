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

import {TestBed} from '@angular/core/testing';
import {provideRouter} from '@angular/router';
import {beforeEach, describe, expect, it} from 'vitest';
import type {AutoscalerSnapshot, FleetHost, LadderRung} from '../../models/autoscaler.model';
import type {AutoscaleMode, FleetStatus} from '../../models/control-plane.model';
import {TopologyCanvasComponent} from './topology-canvas.component';

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

function snapshotOf(
  mode: AutoscaleMode,
  hosts: FleetHost[],
  ladder: LadderRung[] = []
): AutoscalerSnapshot {
  const status: FleetStatus = {
    mode,
    target: 3,
    standby: 1,
    draining: 0,
    misses: 3,
    demand: {servable: 3, unservable: 0, gpuRequired: 0, needsLargerHost: 0, oversizedGroup: 0},
    schedule: 0,
    floor: 0,
    total: 1,
    signal: 'demand',
    limitedBy: '',
    partial: false,
  };
  return {
    now: NOW,
    fleet: {
      status,
      policy: {
        mode,
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
      reason: mode === 'off' ? 'mode_off' : 'acting',
      deficit: 0,
      surplus: 0,
      quotaAvailable: 7,
      quotaSpendable: 6,
      leaderHeld: true,
      leaderHolder: 'instance-1',
    },
    // The default pool's entry is the fleet block split the way the keeper
    // acts on it, exactly as the server composes both from one derivation.
    pools: [
      {
        pool: 'default',
        status,
        deficit: 0,
        surplus: 0,
        reason: mode === 'off' ? 'mode_off' : 'acting',
        ladder,
      },
    ],
    hosts,
    shapes: [],
    ladder,
    activity: {provisions: 2, rescues: 1, shortens: 0, collapses: 0},
    activityWindowSec: 86_400,
    armImageReady: false,
    effectiveDensity: 3,
    densityMachineType: 'n1-standard-8',
    demandHorizonSec: 3600,
  };
}

function fleetOf(count: number): FleetHost[] {
  return Array.from({length: count}, (_, i) => host({id: `h${i}`, name: `cf-pool-${i}`}));
}

describe('TopologyCanvasComponent', () => {
  function render(mode: AutoscaleMode, hosts: FleetHost[], ladder: LadderRung[] = []): HTMLElement {
    const fixture = TestBed.createComponent(TopologyCanvasComponent);
    fixture.componentRef.setInput('snapshot', snapshotOf(mode, hosts, ladder));
    fixture.detectChanges();
    return fixture.nativeElement as HTMLElement;
  }

  beforeEach(() => {
    TestBed.configureTestingModule({
      imports: [TopologyCanvasComponent],
      providers: [provideRouter([])],
    });
  });

  it('states both permissions in words rather than as one switch', () => {
    const perms = Array.from(
      render('provision_only', fleetOf(1)).querySelectorAll('.perms li')
    ).map(item => item.textContent?.trim());

    expect(perms).toEqual(['provisioning permitted', 'lowering a lease not permitted']);
  });

  // Off is a configuration and not a fault, so the lines break rather than
  // reddening: a break reads as "not connected" before a word is read.
  it('breaks the lines from the autoscaler when the mode is off', () => {
    expect(render('off', fleetOf(1)).querySelector('.canvas.mode-off')).not.toBeNull();
    expect(render('on', fleetOf(1)).querySelector('.canvas.mode-off')).toBeNull();
  });

  // Card area is capacity, so a device that wants two slots is drawn twice the
  // width of one that wants one, and the span rides a custom property.
  it('sizes a slot chip by the instances it holds', () => {
    const root = render('on', [
      host({
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
    ]);
    const chips = Array.from(root.querySelectorAll<HTMLElement>('.slot'));

    expect(chips.map(chip => chip.style.getPropertyValue('--cco-canvas-span'))).toEqual([
      '2',
      '1',
      '1',
    ]);
    expect(chips[0].getAttribute('aria-label')).toBe('web-1, 2 instance slots');
  });

  // The tile states its rollup as its accessible name and opens from the
  // keyboard, because a cluster that only opens on click hides its members from
  // half the readers.
  it('expands a cluster into full cards from the keyboard', () => {
    const root = render('on', fleetOf(60));
    const tile = root.querySelector<HTMLButtonElement>('.cluster');

    expect(tile?.textContent).toContain('60');
    expect(tile?.getAttribute('aria-expanded')).toBe('false');
    expect(root.querySelectorAll('.members app-topology-machine')).toHaveLength(0);

    tile?.click();
    TestBed.tick();

    expect(root.querySelector('.cluster')?.getAttribute('aria-expanded')).toBe('true');
    expect(root.querySelectorAll('.members app-topology-machine')).toHaveLength(60);
    expect(root.querySelector('.members .slot')).not.toBeNull();
  });

  // Machines that are not the autoscaler's are present so the totals reconcile
  // against the hosts page, and outside the rail so nothing claims control of
  // them.
  it('draws the machines it does not own outside the rail', () => {
    const root = render('on', [
      host({id: 'a', name: 'cf-pool-a'}),
      host({id: 'b', name: 'cf-user-1', hostClass: 'reserved', standby: false}),
    ]);

    const owned = Array.from(root.querySelectorAll('.owned .name')).map(n => n.textContent?.trim());
    const strip = Array.from(root.querySelectorAll('.strip .name')).map(n => n.textContent?.trim());
    expect(owned).toEqual(['cf-pool-a']);
    expect(strip).toEqual(['cf-user-1']);
    expect(root.querySelector('.strip')?.textContent).toContain('User-made');
  });

  // The shapes the fleet builds, in order, beside the target that asks for
  // them. Never a strike alone: the words under the line are what say which
  // shape is being built and why.
  it('draws the shape order, with the words that carry it', () => {
    const root = render('on', fleetOf(1), [
      rung('c4-standard-16', {stockedOutZones: 3}),
      rung('n4-standard-16', {landing: true}),
    ]);

    const steps = Array.from(root.querySelectorAll('.owned .step'));
    expect(steps.map(step => step.textContent?.trim())).toEqual([
      'c4-standard-16',
      'n4-standard-16',
    ]);
    expect(steps[0].classList.contains('out')).toBe(true);
    expect(steps[1].classList.contains('building')).toBe(true);
    expect(root.querySelector('.shapes-note')?.textContent).toContain(
      'c4-standard-16 stocked out, fell back to n4-standard-16'
    );
  });

  it('draws no order when none is stored', () => {
    expect(render('on', fleetOf(1)).querySelector('.shapes')).toBeNull();
  });

  // No percentage, no part-to-whole mark: nothing samples CPU or memory here,
  // and a ratio over slots is the exact mark that overstates capacity when the
  // promise exceeds what the image backs.
  it('draws no percentage and speaks none of the borrowed vocabulary', () => {
    const text = render('on', fleetOf(2)).textContent ?? '';

    expect(text).not.toContain('%');
    for (const word of [/\btenant/i, /\bMIG\b/, /utilization/i, /\bnodes?\b/i, /\bpods?\b/i]) {
      expect(text).not.toMatch(word);
    }
  });
});
