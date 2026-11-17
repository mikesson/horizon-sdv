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

import {ChangeDetectionStrategy, Component, computed, inject, input, signal} from '@angular/core';
import {MatDialog} from '@angular/material/dialog';
import {MatIconModule} from '@angular/material/icon';
import {MatSlideToggleModule} from '@angular/material/slide-toggle';
import {MatSnackBar} from '@angular/material/snack-bar';
import {MatTooltipModule} from '@angular/material/tooltip';
import {RouterLink} from '@angular/router';
import type {ConfirmDialogData} from '../../../shared/components';
import {ConfirmDialogComponent} from '../../../shared/components';
import {getErrorMessage} from '../../../shared/utils';
import type {
  AutoscaleMode,
  FleetPanel,
  LeaderLease,
  SweepPolicy,
  SweepPolicyPatch,
} from '../../models/control-plane.model';
import {BACKGROUND_PLANE_LEASE} from '../../models/control-plane.model';
import {TaskAdminApiService} from '../../services/task-admin-api.service';
import {ControlPlaneStore} from '../../store/control-plane.signal-store';

/** One card's fixed description plus the switches that loop owns. */
interface ReconcilerMeta {
  label: string;
  icon: string;
  interval: string;
  description: string;
  controls?: ReadonlyArray<{
    key: ControlKey;
    label: string;
    detail: string;
    confirmOn: 'enable' | 'disable';
  }>;
  /** The fleet keeper's card, the one that carries the autoscaler summary. */
  fleet?: boolean;
  note?: string;
}

/** The leader-gated periodic jobs, in display order. */
const RECONCILERS: readonly ReconcilerMeta[] = [
  {
    label: 'Host reconciler',
    icon: 'sync_alt',
    interval: 'every minute',
    description:
      'Reconciles GCE reality with host rows: preemption recovery, drift sync, orphan-VM collection, quota recount.',
    controls: [
      {
        key: 'deleteOrphanVms',
        label: 'Delete orphan VMs',
        detail:
          'Managed instances with no host row, at most 2 per cycle. Off, they are only counted.',
        confirmOn: 'enable',
      },
    ],
  },
  {
    label: 'Device controller',
    icon: 'monitor_heart',
    interval: 'every minute',
    description:
      'Polls device health through each host’s orchestrator; catches orphan CVD groups, stale teardowns, and stranded creates.',
    controls: [
      {
        key: 'orphanCvds',
        label: 'Delete orphan CVD groups',
        detail:
          'CVD groups with no device row, at most 2 per host per cycle. Off, they are only counted.',
        confirmOn: 'enable',
      },
      {
        key: 'zombieRecreate',
        label: 'Recreate dead guests',
        detail:
          'A running device whose CVD died is rebuilt from its stored spec, on a bounded budget.',
        confirmOn: 'enable',
      },
    ],
  },
  {
    label: 'Reaper',
    icon: 'auto_delete',
    interval: 'every minute',
    description: 'Enforces lifecycle time: lapsed pins, expired devices, expired hosts.',
    controls: [
      {
        key: 'reaperDeletes',
        label: 'Deletes on expiry',
        detail:
          'Off suspends every deletion by the reaper and batch settlement; both keep logging what they would have done.',
        confirmOn: 'disable',
      },
    ],
  },
  {
    label: 'Batch settle',
    icon: 'fact_check',
    interval: 'every minute',
    description:
      'Settles atomic batches (ready or failed), collects emptied batch rows, reclaims drained batch hosts.',
    note: 'Deletes only while the reaper does.',
  },
  {
    label: 'Catalog sync',
    icon: 'category',
    interval: 'every 5m-1h',
    description: 'Refreshes the zone, machine-type, quota, and image catalog.',
  },
  {
    label: 'Task reclaimer',
    icon: 'cleaning_services',
    interval: 'every minute',
    description:
      'Crash recovery for the queue: returns expired task leases to pending, dead-letters the exhausted.',
  },
  {
    label: 'Fleet keeper',
    icon: 'stacks',
    interval: 'every 30s',
    description:
      'Composes a standby target from standing intent and observed demand, and holds idle hosts to it so a create can skip the multi-minute provisioning leg.',
    fleet: true,
  },
];

type LoopStatus = 'leading' | 'stalled' | 'unheld' | 'unknown';

/**
 * The switch key as the card names it. reaperDeletes is the one control
 * stated in the opposite polarity from the setting behind it, because an
 * operator reasons about whether the reaper deletes and not about a dry-run
 * flag.
 */
type ControlKey = 'deleteOrphanVms' | 'orphanCvds' | 'zombieRecreate' | 'reaperDeletes';

interface LoopControl {
  key: ControlKey;
  label: string;
  detail: string;
  /** Which direction changes what runs unattended, and so must be confirmed. */
  confirmOn: 'enable' | 'disable';
  checked: boolean;
  saved: boolean;
  busy: boolean;
}

/**
 * The fleet keeper card's summary. It is read-only. Every autoscaler setting
 * lives on the Autoscaler page, so that no value has two editors.
 *
 * Every number is server-derived. The target is composed by the backend from
 * the same policy and observations the keeper acts on, so the card cannot show a
 * target the keeper is not pursuing.
 */
interface FleetSummary {
  mode: AutoscaleMode;
  target: number;
  held: number;
  draining: number;
  /** Target × auto-host density, server-derived, the capacity the target buys. */
  deviceSlots: number;
  signal: string;
}

interface LoopCard {
  label: string;
  icon: string;
  interval: string;
  description: string;
  controls: LoopControl[];
  fleet: FleetSummary | null;
  note: string;
}

/** Who is running the background plane, and how fresh its renewal is. */
interface PlaneLeadership {
  status: LoopStatus;
  statusText: string;
}

/**
 * The loops that run on whichever instance leads the background plane, and one
 * row saying which instance that is. Leadership is stated once because there is
 * one election. The holder renews every few seconds while alive, so an expired
 * lease means none of these loops is running anywhere.
 */
@Component({
  selector: 'app-reconciler-panel',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatIconModule, MatSlideToggleModule, MatTooltipModule, RouterLink],
  template: `
    <div class="plane" [class.stalled]="plane().status === 'stalled'">
      <span [class]="'dot ' + plane().status"></span>
      <span class="plane-label">Background plane</span>
      <span class="plane-status">{{ plane().statusText }}</span>
      <span class="plane-note">every loop below runs on the instance holding this lease</span>
    </div>
    <div class="cards">
      @for (card of cards(); track card.label) {
        <div class="card">
          <div class="card-head">
            <mat-icon class="loop-icon">{{ card.icon }}</mat-icon>
            <span class="label">{{ card.label }}</span>
            <span class="interval">{{ card.interval }}</span>
          </div>
          <p class="description">{{ card.description }}</p>
          <!-- Every card carries the slot, empty where the loop has no switch,
               because a half-interactive grid reads as a bug. -->
          <div class="controls">
            @for (control of card.controls; track control.key) {
              <div class="control">
                <mat-slide-toggle
                  [checked]="control.checked"
                  [disabled]="control.busy"
                  [matTooltip]="control.detail"
                  (change)="onToggle(control, $event.checked)">
                  {{ control.label }}
                </mat-slide-toggle>
                @if (control.saved) {
                  <span class="saved">Saved</span>
                }
              </div>
            }
            @if (card.fleet; as fleet) {
              <!-- Target first, because it is the number the loop pursues, and
                   what the fleet holds only explains it. Showing held alone
                   hides a target demand raised above the operator's intent. -->
              <span class="note">
                {{ fleet.held }} held of target {{ fleet.target }} ({{ fleet.signal }})
                @if (fleet.draining > 0) {
                  · {{ fleet.draining }} draining
                }
                @if (fleet.target > 0) {
                  · {{ fleet.deviceSlots }} device slots
                }
              </span>
              <span class="note">mode {{ fleet.mode }}</span>
              <!-- The settings and the whole picture live on their own page. This
                   card states what the loop is doing and points at where it is
                   steered; a second editor here is the defect that page removed. -->
              <a class="link" routerLink="/admin/autoscaler">Autoscaler →</a>
            }
            @if (card.note) {
              <span class="note">{{ card.note }}</span>
            }
          </div>
        </div>
      }
    </div>
  `,
  styles: `
    .plane {
      display: flex;
      align-items: center;
      flex-wrap: wrap;
      gap: var(--cco-space-8);
      margin-bottom: 12px;
      padding: var(--cco-space-8) 16px;
      background: var(--cco-surface);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-lg);
      font-size: 12px;
    }

    .plane.stalled {
      border-color: var(--cco-error);
    }

    .plane-label {
      font-weight: 500;
    }

    .plane-status {
      color: var(--cco-text-secondary);
    }

    .plane.stalled .plane-status {
      color: var(--cco-error);
    }

    .plane-note {
      margin-left: auto;
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-label);
    }

    .cards {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(210px, 1fr));
      gap: 12px;
    }

    .card {
      padding: 16px 16px;
      background: var(--cco-surface);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-lg);
      display: flex;
      flex-direction: column;
      gap: 8px;
    }

    .card-head {
      display: flex;
      align-items: center;
      gap: 8px;
    }

    .loop-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
      color: var(--cco-accent);
    }

    .label {
      font-weight: 500;
      font-size: 12px;
      flex: 1;
    }

    .dot {
      width: 8px;
      height: 8px;
      border-radius: 50%;
      background: var(--cco-text-disabled);
    }

    .dot.leading {
      background: var(--cco-success);
    }

    .dot.stalled {
      background: var(--cco-error);
    }

    .description {
      margin: 0;
      font-size: 11px;
      line-height: 1.5;
      color: var(--cco-text-secondary);
      flex: 1;
    }

    .interval {
      color: var(--cco-text-secondary);
      font-size: 11px;
      white-space: nowrap;
    }

    .controls {
      display: flex;
      flex-direction: column;
      gap: var(--cco-space-4);
      min-height: var(--cco-space-24);
      padding-top: var(--cco-space-8);
      border-top: 1px solid var(--cco-border);
      font-size: var(--cco-text-label);
    }

    .control {
      display: flex;
      align-items: center;
      gap: var(--cco-space-8);
    }

    .saved {
      color: var(--cco-success);
      animation: saved-in var(--cco-duration-fast) ease-out;
    }

    .link {
      color: var(--cco-accent);
      text-decoration: none;
    }

    .link:hover,
    .link:focus-visible {
      text-decoration: underline;
    }

    .note {
      color: var(--cco-text-secondary);
    }

    @keyframes saved-in {
      from {
        opacity: 0;
      }
      to {
        opacity: 1;
      }
    }
  `,
})
export class ReconcilerPanelComponent {
  private readonly api = inject(TaskAdminApiService);
  private readonly controlPlane = inject(ControlPlaneStore);
  private readonly dialog = inject(MatDialog);
  private readonly snackBar = inject(MatSnackBar);

  readonly leases = input.required<LeaderLease[]>();
  /** Server time of the snapshot the leases came from. */
  readonly serverNow = input.required<Date>();

  /** What the loops may currently do on their own. */
  readonly sweeps = input.required<SweepPolicy>();

  /** The autoscaler picture: the composed target and what the fleet holds. */
  readonly fleet = input.required<FleetPanel>();

  /** In-flight optimistic values, keyed by control; cleared on the answer. */
  private readonly pending = signal<Partial<Record<ControlKey, boolean>>>({});
  /** The control showing its "Saved" affordance, if any. */
  private readonly justSaved = signal<ControlKey | null>(null);

  /**
   * Leadership, once. A lease row the plane does not elect on is one no loop
   * gates on, so it is not shown at all. Leaving a retired row to read as a
   * stopped loop would put a fault on the page that nobody can fix.
   */
  protected readonly plane = computed<PlaneLeadership>(() =>
    describeLease(
      this.leases().find(l => l.name === BACKGROUND_PLANE_LEASE),
      this.serverNow().getTime()
    )
  );

  protected readonly cards = computed<LoopCard[]>(() => {
    const policy = this.sweeps();
    const pending = this.pending();
    const saved = this.justSaved();
    const fleet = this.fleet();
    return RECONCILERS.map(meta => ({
      ...meta,
      controls: (meta.controls ?? []).map(control => ({
        ...control,
        checked: pending[control.key] ?? storedValue(policy, control.key),
        saved: saved === control.key,
        busy: pending[control.key] !== undefined,
      })),
      fleet: meta.fleet
        ? {
            mode: fleet.status.mode,
            target: fleet.status.target,
            held: fleet.status.standby,
            draining: fleet.status.draining,
            deviceSlots: fleet.deviceSlots,
            signal: fleet.status.signal,
          }
        : null,
      note: meta.note ?? '',
    }));
  });

  /**
   * Applies a switch. The confirmation guards the direction that changes what
   * happens unattended (enabling a sweep, or suspending the reaper) and the
   * safe direction applies straight away.
   */
  protected onToggle(control: LoopControl, checked: boolean): void {
    const needsConfirm = control.confirmOn === (checked ? 'enable' : 'disable');
    if (!needsConfirm) {
      this.apply(control, checked);
      return;
    }
    const data: ConfirmDialogData = {
      title: checked ? `Turn on “${control.label}”?` : `Turn off “${control.label}”?`,
      message: control.detail,
      warning:
        control.key === 'reaperDeletes'
          ? 'Expired devices and hosts stay allocated until deletions resume, and keep consuming host quota.'
          : 'The loop starts deleting on its own, with nobody watching each decision.',
      confirmText: checked ? 'Turn on' : 'Turn off',
      confirmColor: 'warn',
      icon: 'warning',
      iconColor: 'warn',
    };
    this.dialog
      .open(ConfirmDialogComponent, {data})
      .afterClosed()
      .subscribe((confirmed: boolean) => {
        if (confirmed) {
          this.apply(control, checked);
        }
      });
  }

  private apply(control: LoopControl, checked: boolean): void {
    const previous = control.checked;
    this.pending.update(p => ({...p, [control.key]: checked}));
    this.api.updateSweeps(patchFor(control.key, checked)).subscribe({
      next: () => {
        this.pending.update(p => ({...p, [control.key]: undefined}));
        this.justSaved.set(control.key);
        setTimeout(() => {
          if (this.justSaved() === control.key) {
            this.justSaved.set(null);
          }
        }, SAVED_VISIBLE_MS);
        // The snapshot is the truth; the optimistic value only covers the gap.
        this.controlPlane.refresh();
      },
      error: (err: unknown) => {
        // Put the switch back where the operator left it, visibly, and say why.
        this.pending.update(p => ({...p, [control.key]: previous}));
        setTimeout(
          () => this.pending.update(p => ({...p, [control.key]: undefined})),
          REVERT_VISIBLE_MS
        );
        this.snackBar.open(getErrorMessage(err, `Failed to change ${control.label}`), 'Close', {
          duration: 6000,
        });
      },
    });
  }
}

/** How long the "Saved" affordance stays up, and the visible revert. */
const SAVED_VISIBLE_MS = 2000;
const REVERT_VISIBLE_MS = 400;

/** The stored value behind a control, un-inverting the reaper's dry run. */
function storedValue(policy: SweepPolicy, key: ControlKey): boolean {
  // biome-ignore lint/style/useDefaultSwitchClause: exhaustive over ControlKey, and a default would hide a missing case that the compiler otherwise catches.
  switch (key) {
    case 'deleteOrphanVms':
      return policy.deleteOrphanVms;
    case 'orphanCvds':
      return policy.orphanCvds;
    case 'zombieRecreate':
      return policy.zombieRecreate;
    case 'reaperDeletes':
      return !policy.reaperDryRun;
  }
}

/** The wire patch for one control; the reaper's switch inverts here. */
function patchFor(key: ControlKey, checked: boolean): SweepPolicyPatch {
  // biome-ignore lint/style/useDefaultSwitchClause: exhaustive over ControlKey, and a default would hide a missing case that the compiler otherwise catches.
  switch (key) {
    case 'deleteOrphanVms':
      return {deleteOrphanVms: checked};
    case 'orphanCvds':
      return {orphanCvds: checked};
    case 'zombieRecreate':
      return {zombieRecreate: checked};
    case 'reaperDeletes':
      return {reaperDryRun: !checked};
  }
}

function describeLease(lease: LeaderLease | undefined, nowMs: number): PlaneLeadership {
  if (!lease) {
    return {status: 'unknown', statusText: 'never led yet'};
  }
  if (!lease.holder) {
    return {status: 'unheld', statusText: 'no leader (released)'};
  }
  if (lease.expiry && lease.expiry.getTime() < nowMs) {
    return {
      status: 'stalled',
      statusText: `lease expired ${formatAgo(nowMs - lease.expiry.getTime())} · every loop stopped`,
    };
  }
  return {
    status: 'leading',
    statusText: `led by ${shortInstance(lease.holder)} · renewed ${formatAgo(nowMs - lease.renewedAt.getTime())}`,
  };
}

/** "3s ago" / "2m ago" / "1h ago". */
function formatAgo(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) {
    return `${s}s ago`;
  }
  if (s < 3600) {
    return `${Math.floor(s / 60)}m ago`;
  }
  return `${Math.floor(s / 3600)}h ago`;
}

/** Instance IDs are long and opaque; the tail is the distinctive part. */
function shortInstance(id: string): string {
  return id.length <= 10 ? id : `…${id.slice(-8)}`;
}
