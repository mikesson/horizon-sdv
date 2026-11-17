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
 * The switches decide what the platform deletes with nobody watching, so the
 * asymmetry is the thing under test: granting a loop autonomy asks first,
 * taking it back does not.
 */

import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {MatDialog} from '@angular/material/dialog';
import {MatSnackBar} from '@angular/material/snack-bar';
import {provideRouter} from '@angular/router';
import {of, switchMap, throwError, timer} from 'rxjs';
import {beforeEach, describe, expect, it, vi} from 'vitest';
import type {FleetPanel, LeaderLease, SweepPolicy} from '../../models/control-plane.model';
import {TaskAdminApiService} from '../../services/task-admin-api.service';
import {ControlPlaneStore} from '../../store/control-plane.signal-store';
import {ReconcilerPanelComponent} from './reconciler-panel.component';

const DETECT_ONLY: SweepPolicy = {
  deleteOrphanVms: false,
  orphanCvds: false,
  zombieRecreate: false,
  reaperDryRun: false,
  isSet: false,
};

/** The autoscaler picture, off and holding nothing. */
function fleetPanel(
  mode: FleetPanel['status']['mode'],
  standby: number,
  target = standby
): FleetPanel {
  return {
    status: {
      mode,
      target,
      standby: 0,
      draining: 0,
      misses: 0,
      demand: {
        servable: 0,
        unservable: 0,
        gpuRequired: 0,
        needsLargerHost: 0,
        oversizedGroup: 0,
      },
      schedule: 0,
      floor: 0,
      total: 0,
      signal: 'standby',
      limitedBy: '',
      partial: false,
    },
    policy: {
      mode,
      tiers: {default: {standby, floor: 0, ladder: [], schedules: []}},
      floor: 0,
      demandHorizonSec: 3600,
      drainGraceSec: 480,
      shapeLadder: [],
      isSet: standby > 0,
    },
    deviceSlots: 0,
    gpuDegradation: {degraded: 0, wanted: 0},
  };
}

/**
 * The card's text with template whitespace collapsed, the way a reader sees it.
 * Asserting on the raw textContent would tie these tests to where the formatter
 * happens to wrap a template line.
 */
function textOf(root: HTMLElement): string {
  return (root.textContent ?? '').replace(/\s+/g, ' ');
}

describe('ReconcilerPanelComponent sweep switches', () => {
  const api = {updateSweeps: vi.fn()};
  const controlPlane = {refresh: vi.fn()};
  const snackBar = {open: vi.fn()};
  let dialogResult: boolean;
  const dialog = {open: vi.fn(() => ({afterClosed: () => of(dialogResult)}))};

  let fixture: ComponentFixture<ReconcilerPanelComponent>;

  function render(
    policy: SweepPolicy = DETECT_ONLY,
    fleet: FleetPanel = fleetPanel('off', 0),
    leases: LeaderLease[] = []
  ): HTMLElement {
    fixture = TestBed.createComponent(ReconcilerPanelComponent);
    fixture.componentRef.setInput('leases', leases);
    fixture.componentRef.setInput('serverNow', new Date('2026-08-01T12:00:00Z'));
    fixture.componentRef.setInput('sweeps', policy);
    fixture.componentRef.setInput('fleet', fleet);
    fixture.detectChanges();
    return fixture.nativeElement as HTMLElement;
  }

  /** The switch whose label starts with the given text. */
  function toggleFor(root: HTMLElement, label: string): HTMLInputElement {
    const match = Array.from(root.querySelectorAll('mat-slide-toggle')).find(el =>
      el.textContent?.includes(label)
    );
    const input = match?.querySelector<HTMLInputElement>('button, input');
    if (!input) {
      throw new Error(`no switch labelled ${label}`);
    }
    return input;
  }

  beforeEach(() => {
    vi.clearAllMocks();
    dialogResult = true;
    api.updateSweeps.mockReturnValue(of(DETECT_ONLY));
    TestBed.configureTestingModule({
      providers: [
        // The card links to the Autoscaler page, so RouterLink needs a route
        // tree even though nothing here navigates.
        provideRouter([]),
        {provide: TaskAdminApiService, useValue: api},
        {provide: ControlPlaneStore, useValue: controlPlane},
        {provide: MatDialog, useValue: dialog},
        {provide: MatSnackBar, useValue: snackBar},
      ],
    });
  });

  /** A held lease, renewed a moment before the panel's server clock. */
  function heldLease(name: string): LeaderLease {
    return {
      name,
      holder: 'orchestrator-00160-abc',
      acquiredAt: new Date('2026-08-01T11:00:00Z'),
      expiry: new Date('2026-08-01T12:00:30Z'),
      renewedAt: new Date('2026-08-01T11:59:57Z'),
    };
  }

  // One election gates every loop, so the panel states leadership once. Seven
  // copies of one row was the shape this replaced, and a card that carried its
  // own copy would be back to claiming loops can lead separately.
  it('states leadership once, from the lease the plane elects on', () => {
    const root = render(DETECT_ONLY, fleetPanel('off', 0), [heldLease('background-plane')]);

    expect(root.querySelectorAll('.plane').length).toBe(1);
    expect(textOf(root)).toContain('led by …0160-abc');
    expect(root.querySelectorAll('.dot.leading').length).toBe(1);
  });

  // A retired lease row outlives the code that renewed it, because nothing
  // deletes one, so reading whichever row happens to be there would report a
  // loop nobody runs as the plane's leadership.
  it('ignores a lease row the plane does not elect on', () => {
    const root = render(DETECT_ONLY, fleetPanel('off', 0), [heldLease('poolkeeper')]);

    expect(textOf(root)).toContain('never led yet');
    expect(root.querySelectorAll('.dot.leading').length).toBe(0);
  });

  // The audit's own warning: a count of rendered switches passes forever once
  // the component renders any. Assert the slot exists on every card instead,
  // which is what stops the grid becoming half-interactive.
  it('renders the control slot on every loop card, switch or not', () => {
    const root = render();
    const cards = root.querySelectorAll('.card');

    expect(cards.length).toBe(7);
    for (const card of Array.from(cards)) {
      expect(card.querySelector('.controls')).not.toBeNull();
    }
  });

  it('asks before granting a loop the power to delete', () => {
    const root = render();

    toggleFor(root, 'Delete orphan VMs').click();
    fixture.detectChanges();

    expect(dialog.open).toHaveBeenCalled();
    expect(api.updateSweeps).toHaveBeenCalledWith({deleteOrphanVms: true});
  });

  it('writes nothing when the confirmation is dismissed', () => {
    dialogResult = false;
    const root = render();

    toggleFor(root, 'Delete orphan VMs').click();
    fixture.detectChanges();

    expect(api.updateSweeps).not.toHaveBeenCalled();
  });

  it('takes the power back without asking', () => {
    const root = render({...DETECT_ONLY, deleteOrphanVms: true});

    toggleFor(root, 'Delete orphan VMs').click();
    fixture.detectChanges();

    expect(dialog.open).not.toHaveBeenCalled();
    expect(api.updateSweeps).toHaveBeenCalledWith({deleteOrphanVms: false});
  });

  // The reaper's switch is stated in the opposite polarity from the setting:
  // an operator reasons about whether it deletes, not about a dry-run flag.
  it('suspends the reaper by sending dry run, and confirms that direction', () => {
    const root = render();

    toggleFor(root, 'Deletes on expiry').click();
    fixture.detectChanges();

    expect(dialog.open).toHaveBeenCalled();
    expect(api.updateSweeps).toHaveBeenCalledWith({reaperDryRun: true});
  });

  it('resumes the reaper without asking', () => {
    const root = render({...DETECT_ONLY, reaperDryRun: true});

    toggleFor(root, 'Deletes on expiry').click();
    fixture.detectChanges();

    expect(dialog.open).not.toHaveBeenCalled();
    expect(api.updateSweeps).toHaveBeenCalledWith({reaperDryRun: false});
  });

  it('refreshes the snapshot rather than trusting its own optimism', () => {
    const root = render();

    toggleFor(root, 'Recreate dead guests').click();
    fixture.detectChanges();

    expect(controlPlane.refresh).toHaveBeenCalled();
  });

  // The card is a SUMMARY now: it states what the loop is pursuing and points
  // at where it is steered. A control here would be the second editor for one
  // value that moving the settings to their own page removed.
  it('summarises the keeper and links to the page that steers it', () => {
    const panel = fleetPanel('provision_only', 2, 3);
    panel.status.standby = 1;
    panel.status.draining = 1;
    panel.deviceSlots = 9;

    const root = render(DETECT_ONLY, panel);
    const text = textOf(root);

    expect(text).toContain('1 held of target 3 (standby)');
    expect(text).toContain('1 draining');
    expect(text).toContain('9 device slots');
    expect(text).toContain('mode provision_only');
    expect(root.querySelector('a[href="/admin/autoscaler"]')).not.toBeNull();
  });

  // Named individually because "no control" is the property, and a count of
  // rendered inputs would pass forever once the card renders none of anything.
  it('offers no way to change the policy from this card', () => {
    const root = render(DETECT_ONLY, fleetPanel('on', 2));

    expect(root.querySelector('.mode-select')).toBeNull();
    expect(root.querySelector('.floor-input')).toBeNull();
    expect(root.querySelector('.apply')).toBeNull();
  });

  // The failure arrives after the optimistic render, as it does in
  // production: the switch goes on, then visibly comes back.
  it('puts the switch back and says why when the write fails', async () => {
    api.updateSweeps.mockReturnValue(
      timer(0).pipe(switchMap(() => throwError(() => new Error('boom'))))
    );
    const root = render();

    toggleFor(root, 'Delete orphan VMs').click();
    fixture.detectChanges();
    expect(toggleFor(root, 'Delete orphan VMs').getAttribute('aria-checked')).toBe('true');

    await new Promise(resolve => setTimeout(resolve, 5));
    fixture.detectChanges();

    expect(snackBar.open).toHaveBeenCalled();
    expect(toggleFor(root, 'Delete orphan VMs').getAttribute('aria-checked')).toBe('false');
    expect(controlPlane.refresh).not.toHaveBeenCalled();
  });
});
