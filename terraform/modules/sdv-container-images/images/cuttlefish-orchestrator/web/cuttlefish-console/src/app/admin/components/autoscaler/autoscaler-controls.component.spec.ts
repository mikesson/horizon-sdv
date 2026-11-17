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
import {AutoscalerControlsComponent} from './autoscaler-controls.component';

function policyOf(mode: AutoscaleMode, standby: number, floor = 0): FleetSettings {
  return {
    mode,
    tiers: {default: {standby, floor, ladder: [], schedules: []}},
    floor,
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

function machineTypeOf(id: string): MachineType {
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
  };
}

describe('AutoscalerControlsComponent', () => {
  const policy = signal<FleetSettings | null>(policyOf('off', 0));
  const intents = signal<Record<string, FleetTier>>({
    default: {standby: 0, floor: 0, ladder: [], schedules: []},
  });
  const store = {
    policy,
    intents,
    snapshot: signal(null),
    policyError: signal<string | null>(null),
    ladderError: signal<{pool: string; message: string} | null>(null),
    saving: signal(false),
    standby: signal(0),
    floor: signal(0),
    servedLadders: signal({}),
    defaultShape: signal('n1-standard-8'),
    armImageReady: signal(false),
    density: signal(3),
    saveAutoscaler: vi.fn(),
    disablePool: vi.fn(),
    saveTuning: vi.fn(),
    saveSchedules: vi.fn(),
    saveLadder: vi.fn(),
  };
  const catalog = {
    entities: signal([machineTypeOf('n1-standard-8'), machineTypeOf('n2-standard-8')]),
    loadAll: vi.fn(),
  };
  let dialogResult: boolean;
  const dialog = {open: vi.fn(() => ({afterClosed: () => of(dialogResult)}))};

  let fixture: ComponentFixture<AutoscalerControlsComponent>;

  function render(mode: AutoscaleMode = 'off', standby = 0, floor = 0): HTMLElement {
    policy.set(policyOf(mode, standby, floor));
    store.standby.set(standby);
    store.floor.set(floor);
    intents.set({default: {standby, floor, ladder: [], schedules: []}});
    fixture = TestBed.createComponent(AutoscalerControlsComponent);
    fixture.detectChanges();
    return fixture.nativeElement as HTMLElement;
  }

  /**
   * Clicks a rung of the mode ladder, matching its label EXACTLY. A substring
   * match would find "provision only" when asked for "on", because the two
   * labels overlap, and the wrong rung would be clicked with every assertion
   * still reading plausibly.
   */
  function chooseMode(root: HTMLElement, label: string): void {
    const rung = Array.from(root.querySelectorAll<HTMLButtonElement>('.rung')).find(
      b => b.querySelector('.rung-label')?.textContent?.trim() === label
    );
    if (!rung) {
      throw new Error(`no ${label} rung`);
    }
    rung.click();
    fixture.detectChanges();
  }

  beforeEach(() => {
    vi.clearAllMocks();
    dialogResult = true;
    store.policyError.set(null);
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

  // The derivable set, offered whether or not one is switched on, because a card
  // for a pool nobody enabled is how it gets enabled.
  it('offers one card per pool and one for the mechanism', () => {
    const root = render('on', 2);

    const cards = Array.from(root.querySelectorAll('app-pool-controls-card')).map(card =>
      card.getAttribute('data-pool')
    );
    expect(cards).toEqual(['default', 'gpu', 'arm']);
    expect(root.querySelector('.mechanism')).not.toBeNull();
  });

  // Modes below `on` can add capacity but never take it away, so no direction of
  // them asks. Confirming here would train the operator to click through the one
  // direction that matters.
  it('climbs to a rung that only adds capacity without asking', () => {
    const root = render('off', 2);

    chooseMode(root, 'provision only');

    expect(dialog.open).not.toHaveBeenCalled();
    // The mode rides the default pool's stored bounds. That pool always exists,
    // and its values are the only ones that cannot be an unsaved edit elsewhere.
    expect(store.saveAutoscaler).toHaveBeenCalledWith('provision_only', 'default', 2, 0);
  });

  // `on` is the one rung that authorizes the keeper to shorten a lease, and it
  // does so unattended. Asserted on the WRITE as well as the dialog, because a
  // confirmation wired to nothing passes a dialog-only assertion.
  it('asks before letting the autoscaler reclaim hosts', () => {
    const root = render('provision_only', 2);

    chooseMode(root, 'on');

    expect(dialog.open).toHaveBeenCalled();
    expect(store.saveAutoscaler).toHaveBeenCalledWith('on', 'default', 2, 0);
  });

  it('changes nothing when the reclaim confirmation is dismissed', () => {
    dialogResult = false;
    const root = render('provision_only', 2);

    chooseMode(root, 'on');

    expect(store.saveAutoscaler).not.toHaveBeenCalled();
  });

  // THE reason the scopes are separate. A single Save would carry a stale copy
  // of the other scopes' fields and revert them, most destructively the lists,
  // which a horizon edit would silently clear.
  it('writes the tuning scalars alone', () => {
    const root = render('on', 2);
    const horizon = root.querySelector<HTMLInputElement>('input[aria-describedby="help-horizon"]');
    if (!horizon) {
      throw new Error('no demand-window input');
    }
    horizon.value = '1800';
    horizon.dispatchEvent(new Event('input'));
    fixture.detectChanges();

    const save = Array.from(root.querySelectorAll('button')).find(b =>
      b.textContent?.includes('Save measurement and drain')
    );
    save?.click();
    fixture.detectChanges();

    expect(store.saveTuning).toHaveBeenCalledWith(1800, 480);
    expect(store.saveAutoscaler).not.toHaveBeenCalled();
    expect(store.saveSchedules).not.toHaveBeenCalled();
    expect(store.saveLadder).not.toHaveBeenCalled();
  });

  // A Save offered when nothing changed writes an audited config change for an
  // edit nobody made.
  it('offers no tuning Save until something differs from the stored policy', () => {
    const root = render('on', 2);
    const labels = Array.from(root.querySelectorAll('button')).map(b => b.textContent ?? '');

    expect(labels.some(l => l.includes('Save measurement and drain'))).toBe(false);
  });

  // An explanation exists for every mechanism control, linked to the control
  // itself. Hover and focus reveal it in CSS, and aria-describedby is what makes
  // it reach a reader who does neither.
  it('gives every mechanism setting an explanation its control points at', () => {
    const root = render('on', 2);

    for (const id of ['help-mode', 'help-horizon', 'help-grace']) {
      const explanation = root.querySelector(`#${id}`);
      expect(explanation, `no explanation ${id}`).not.toBeNull();
      expect(explanation?.textContent?.length ?? 0).toBeGreaterThan(80);
      expect(
        root.querySelector(`button.ask[aria-describedby="${id}"]`),
        `nothing describes ${id}`
      ).not.toBeNull();
    }
  });

  // Half the reluctance to enable an autoscaler is not knowing whether it can be
  // undone, and this was written down nowhere a user could reach.
  it('states that every rung is reversible', () => {
    const root = render();

    expect(root.textContent).toContain('reversible by setting the mode back to off');
  });
});
