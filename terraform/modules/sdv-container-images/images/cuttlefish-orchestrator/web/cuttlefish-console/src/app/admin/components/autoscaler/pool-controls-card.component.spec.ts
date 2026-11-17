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

import {signal} from '@angular/core';
import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {MatDialog} from '@angular/material/dialog';
import {of} from 'rxjs';
import {beforeEach, describe, expect, it, vi} from 'vitest';
import type {MachineType} from '../../../machine-types';
import {MachineTypeStore} from '../../../machine-types';
import type {AutoscaleMode, FleetSettings, FleetTier} from '../../models/control-plane.model';
import {AutoscalerStore} from '../../store/autoscaler.signal-store';
import {PoolControlsCardComponent} from './pool-controls-card.component';

function policyOf(mode: AutoscaleMode): FleetSettings {
  return {
    mode,
    tiers: {},
    floor: 0,
    demandHorizonSec: 3600,
    drainGraceSec: 480,
    shapeLadder: [],
    isSet: true,
    maxStandby: 16,
    maxFloor: 16,
    maxDemandHorizonSec: 86400,
    minDrainGraceSec: 120,
    maxDrainGraceSec: 599,
    maxSchedules: 8,
    maxLadderRungs: 8,
  };
}

function machineTypeOf(id: string, over: Partial<MachineType> = {}): MachineType {
  return {
    id,
    family: id.split('-')[0],
    name: id,
    category: 'general_purpose',
    architecture: 'x86',
    vcpus: 8,
    memoryGb: 30,
    fit: 3,
    gpuSupport: false,
    maxGpus: 0,
    defaultGpuCount: 0,
    gpuMode: 'none',
    integratedGpuCount: 0,
    isDefault: id === 'n1-standard-8',
    isEnabled: true,
    sortOrder: 1,
    ...over,
  };
}

/** A catalog wide enough that every pool's admission rule has something to refuse. */
const CATALOG = [
  machineTypeOf('n1-standard-8'),
  machineTypeOf('n2-standard-8'),
  machineTypeOf('g2-standard-4', {gpuMode: 'integrated', integratedGpuCount: 1, gpuSupport: true}),
  machineTypeOf('c4a-standard-4-metal', {architecture: 'arm'}),
];

describe('PoolControlsCardComponent', () => {
  const policy = signal<FleetSettings | null>(policyOf('on'));
  const intents = signal<Record<string, FleetTier>>({});
  const store = {
    policy,
    intents,
    snapshot: signal(null),
    ladderError: signal<{pool: string; message: string} | null>(null),
    saving: signal(false),
    servedLadders: signal<Record<string, never[]>>({}),
    defaultShape: signal('n1-standard-8'),
    armImageReady: signal(false),
    density: signal(3),
    saveAutoscaler: vi.fn(),
    disablePool: vi.fn(),
    saveSchedules: vi.fn(),
    saveLadder: vi.fn(),
  };
  const catalog = {entities: signal(CATALOG), loadAll: vi.fn()};
  let dialogResult: boolean;
  const dialog = {open: vi.fn(() => ({afterClosed: () => of(dialogResult)}))};

  let fixture: ComponentFixture<PoolControlsCardComponent>;

  function render(pool: string, tier?: FleetTier, mode: AutoscaleMode = 'on'): HTMLElement {
    policy.set(policyOf(mode));
    intents.set(tier ? {[pool]: tier} : {});
    fixture = TestBed.createComponent(PoolControlsCardComponent);
    fixture.componentRef.setInput('pool', pool);
    fixture.detectChanges();
    return fixture.nativeElement as HTMLElement;
  }

  function tierOf(over: Partial<FleetTier> = {}): FleetTier {
    return {standby: 0, floor: 0, ladder: [], schedules: [], ...over};
  }

  function inputFor(root: HTMLElement, pool: string, which: 'free' | 'running'): HTMLInputElement {
    const selector =
      which === 'free' ? 'app-standby-track input' : `input[aria-describedby="help-floor-${pool}"]`;
    const input = root.querySelector<HTMLInputElement>(selector);
    if (!input) {
      throw new Error(`no machines-to-keep-${which} input`);
    }
    return input;
  }

  function applyBound(
    root: HTMLElement,
    pool: string,
    which: 'free' | 'running',
    next: number
  ): void {
    const input = inputFor(root, pool, which);
    input.value = String(next);
    input.dispatchEvent(new Event('input'));
    fixture.detectChanges();
    clickButton(root, 'Apply machine counts');
  }

  function clickButton(root: HTMLElement, label: string): void {
    const button = Array.from(root.querySelectorAll('button')).find(b =>
      b.textContent?.includes(label)
    );
    if (!button) {
      throw new Error(`no ${label} button`);
    }
    button.click();
    fixture.detectChanges();
  }

  beforeEach(() => {
    vi.clearAllMocks();
    dialogResult = true;
    store.ladderError.set(null);
    store.saving.set(false);
    TestBed.configureTestingModule({
      providers: [
        {provide: AutoscalerStore, useValue: store},
        {provide: MachineTypeStore, useValue: catalog},
        {provide: MatDialog, useValue: dialog},
      ],
    });
  });

  // A pool nobody switched on renders as an offer, and taking the offer IS
  // storing its bounds, the write that makes it a pool creates are routed into.
  it('offers a pool nobody enabled, and enables it by storing its bounds', () => {
    const root = render('gpu');

    expect(root.textContent).toContain('Switch this pool on');
    expect(root.querySelector('app-standby-track')).toBeNull();

    clickButton(root, 'Switch this pool on');

    expect(store.saveAutoscaler).toHaveBeenCalledWith('on', 'gpu', 0, 0);
  });

  // Raising is bounded, gradual and reversible; the keeper converges over cycles
  // and every host it buys can be given back. Nothing unattended to warn about.
  it('raises the free-machine count without asking, naming its pool', () => {
    const root = render('default', tierOf({standby: 1}));

    applyBound(root, 'default', 'free', 3);

    expect(dialog.open).not.toHaveBeenCalled();
    expect(store.saveAutoscaler).toHaveBeenCalledWith('on', 'default', 3, 0);
  });

  it('asks before letting machines stop being kept free while the autoscaler is on', () => {
    const root = render('default', tierOf({standby: 2}));

    applyBound(root, 'default', 'free', 0);

    expect(dialog.open).toHaveBeenCalled();
    expect(store.saveAutoscaler).toHaveBeenCalledWith('on', 'default', 0, 0);
  });

  it('gives nothing back when that confirmation is dismissed', () => {
    dialogResult = false;
    const root = render('default', tierOf({standby: 2}));

    applyBound(root, 'default', 'free', 0);

    expect(store.saveAutoscaler).not.toHaveBeenCalled();
  });

  // Lowering is free while the mode may only add hosts, because the hosts
  // already standing run out their own terms. Same edit, different consequence,
  // which is why the confirmation turns on the MODE and not on the direction.
  it('lowers a bound without asking while the mode cannot reclaim', () => {
    const root = render('default', tierOf({standby: 2}), 'provision_only');

    applyBound(root, 'default', 'free', 0);

    expect(dialog.open).not.toHaveBeenCalled();
    expect(store.saveAutoscaler).toHaveBeenCalledWith('provision_only', 'default', 0, 0);
  });

  it('asks before lowering the floor while the autoscaler is on', () => {
    const root = render('default', tierOf({floor: 4}));

    applyBound(root, 'default', 'running', 1);

    expect(dialog.open).toHaveBeenCalled();
    expect(store.saveAutoscaler).toHaveBeenCalledWith('on', 'default', 0, 1);
  });

  // Dismissal has to put the typed number back by hand. The draft is derived
  // from the stored intent and the stored intent did not change, so nothing
  // repaints the input, leaving a field reading 1 beside a pool still held at
  // 4, and an Apply button offering to make it true.
  it('restores the typed floor when its confirmation is dismissed', () => {
    dialogResult = false;
    const root = render('default', tierOf({floor: 4}));

    applyBound(root, 'default', 'running', 1);

    expect(store.saveAutoscaler).not.toHaveBeenCalled();
    expect(inputFor(root, 'default', 'running').value).toBe('4');
    const labels = Array.from(root.querySelectorAll('button')).map(b => b.textContent ?? '');
    expect(labels.some(l => l.includes('Apply machine counts'))).toBe(false);
  });

  // One write for both, because the endpoint replaces the named pool's group.
  // Two Applies would build the second request out of what the first had
  // already stored.
  it('writes both bounds together from one edit', () => {
    const root = render('default', tierOf({standby: 1, floor: 2}));
    const free = inputFor(root, 'default', 'free');
    free.value = '3';
    free.dispatchEvent(new Event('input'));
    fixture.detectChanges();

    applyBound(root, 'default', 'running', 5);

    expect(store.saveAutoscaler).toHaveBeenCalledTimes(1);
    expect(store.saveAutoscaler).toHaveBeenCalledWith('on', 'default', 3, 5);
  });

  // The scopes stay apart per pool. A window edit commits windows for THIS pool
  // and touches nothing else.
  it('writes this pool’s windows alone', () => {
    const root = render('gpu', tierOf());

    clickButton(root, 'Add a window');
    clickButton(root, 'Save windows');

    expect(store.saveSchedules).toHaveBeenCalledTimes(1);
    expect(store.saveSchedules.mock.calls[0][0]).toBe('gpu');
    expect(store.saveAutoscaler).not.toHaveBeenCalled();
    expect(store.saveLadder).not.toHaveBeenCalled();
  });

  it('writes this pool’s shape order alone when a shape is moved', () => {
    const root = render('default', tierOf({ladder: ['n2-standard-8', 'n1-standard-8']}));

    const arrow = root.querySelector<HTMLButtonElement>(
      'button[aria-label="Move n1-standard-8 up"]'
    );
    arrow?.click();
    fixture.detectChanges();
    clickButton(root, 'Save shape order');

    expect(store.saveLadder).toHaveBeenCalledWith('default', ['n1-standard-8', 'n2-standard-8']);
    expect(store.saveAutoscaler).not.toHaveBeenCalled();
    expect(store.saveSchedules).not.toHaveBeenCalled();
  });

  // A Save offered when nothing changed writes an audited config change for an
  // edit nobody made.
  it('offers no Save until something differs from the stored intent', () => {
    const root = render('default', tierOf({standby: 2, floor: 1}));
    const labels = Array.from(root.querySelectorAll('button')).map(b => b.textContent ?? '');

    expect(labels.some(l => l.includes('Apply machine counts'))).toBe(false);
    expect(labels.some(l => l.includes('Save windows'))).toBe(false);
    expect(labels.some(l => l.includes('Save shape order'))).toBe(false);
  });

  // The refusal names a shape in one pool's order. Drawn under every pool it
  // would blame orders nobody wrote.
  it('states a refused shape only on the pool whose order it was', () => {
    store.ladderError.set({pool: 'gpu', message: '"n1-standard-8" carries no accelerator'});

    expect(render('gpu', tierOf()).textContent).toContain('carries no accelerator');
    expect(render('default', tierOf()).textContent).not.toContain('carries no accelerator');
  });

  // Which shapes are admissible depends on the pool that owns the order, since
  // a list valid in one pool and refused in another cannot be one list. Adding to
  // an empty order takes the first shape that pool may run, so the shape the
  // new card shows IS the filter's verdict.
  it('offers each pool only the shapes it can run', () => {
    const firstAdmissible = (pool: string): string => {
      const root = render(pool, tierOf({ladder: []}));
      clickButton(root, 'Add a shape');
      return root.querySelector('app-ladder-rung-card .shape')?.textContent?.trim() ?? '';
    };

    expect(firstAdmissible('default')).toBe('n1-standard-8');
    expect(firstAdmissible('gpu')).toBe('g2-standard-4');
    expect(firstAdmissible('arm')).toBe('c4a-standard-4-metal');
  });

  // Switching off is destructive of stored settings and asks; the default pool
  // is the one every unrouted create lands in and offers no such verb.
  it('asks before switching a pool off, then deletes it as one commit', () => {
    const root = render('gpu', tierOf({standby: 1}));

    clickButton(root, 'Switch this pool off');

    expect(dialog.open).toHaveBeenCalled();
    expect(store.disablePool).toHaveBeenCalledWith('gpu');
  });

  it('offers the default pool no switch-off', () => {
    const root = render('default', tierOf({standby: 1}));

    const labels = Array.from(root.querySelectorAll('button')).map(b => b.textContent ?? '');
    expect(labels.some(l => l.includes('Switch this pool off'))).toBe(false);
  });

  // The arm card's first sentence is a served fact rather than copy. With no
  // arm64 host image the true statement is that nothing can be built, and the
  // moment an operator gives a family one, the card describes routing instead.
  it('states the arm pool’s buildability from the served fact', () => {
    store.armImageReady.set(false);
    expect(render('arm').textContent).toContain('Nothing can be built for this pool yet');

    store.armImageReady.set(true);
    expect(render('arm').textContent).toContain('routed here, onto bare-metal machines');
  });

  // The relabel is the finding that produced the second bound. "Standing
  // intent" sold a buffer under a name that reads as a floor.
  it('states each bound as the contract it holds', () => {
    const root = render('default', tierOf());

    expect(root.textContent).toContain('Machines to keep free');
    expect(root.textContent).toContain('Machines to keep running');
    expect(root.textContent).not.toContain('Standing intent');
  });

  // Help ids are suffixed per pool so three cards on one page keep every
  // aria-describedby pointing at its own paragraph.
  it('gives every setting an explanation scoped to this pool', () => {
    const root = render('gpu', tierOf());

    for (const id of ['help-floor-gpu', 'help-free-gpu', 'help-windows-gpu', 'help-ladder-gpu']) {
      expect(root.querySelector(`#${id}`), `no explanation ${id}`).not.toBeNull();
      expect(
        root.querySelector(`button.ask[aria-describedby="${id}"]`),
        `nothing describes ${id}`
      ).not.toBeNull();
    }
  });
});
