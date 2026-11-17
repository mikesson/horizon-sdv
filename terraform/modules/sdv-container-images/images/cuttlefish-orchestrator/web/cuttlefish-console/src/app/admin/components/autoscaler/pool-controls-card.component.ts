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

import {
  ChangeDetectionStrategy,
  Component,
  computed,
  inject,
  input,
  linkedSignal,
} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatDialog} from '@angular/material/dialog';
import {MachineTypeStore} from '../../../machine-types';
import type {ConfirmDialogData} from '../../../shared/components';
import {ConfirmDialogComponent} from '../../../shared/components';
import {poolDisabling, poolFamily, poolPurpose} from '../../../shared/models/pool-copy';
import {reclaims} from '../../models/autoscaler.model';
import type {AutoscaleMode, FleetSchedule} from '../../models/control-plane.model';
import {DEFAULT_FLEET_TIER} from '../../models/control-plane.model';
import {AutoscalerStore, UNSET_POOL_INTENT} from '../../store/autoscaler.signal-store';
import {ScheduleEditorComponent} from './schedule-editor.component';
import {ScheduleWeekComponent} from './schedule-week.component';
import {ShapeLadderEditorComponent} from './shape-ladder-editor.component';
import {StandbyTrackComponent} from './standby-track.component';

/**
 * One pool's settings: the two machine counts it is held to, the windows that
 * raise one of them, and the shapes its machines are built as.
 *
 * Three save scopes, and they stay three. That is the same rule that split them
 * before pools, now applied across pools too. Every write names the pool it
 * edits, so one card can never commit another's numbers, and a card holding a
 * stale read of a list cannot clear it.
 *
 * A pool nobody switched on renders as an offer rather than a form. Switching
 * one on IS storing its bounds, which is why there is no separate verb. The
 * bounds are what make a pool a pool creates are routed into.
 */
@Component({
  selector: 'app-pool-controls-card',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    MatButtonModule,
    ScheduleEditorComponent,
    ScheduleWeekComponent,
    ShapeLadderEditorComponent,
    StandbyTrackComponent,
  ],
  host: {
    '[attr.data-pool]': 'pool()',
  },
  template: `
    <h4>Pool · <span class="mono">{{ pool() }}</span></h4>
    <p class="family">{{ family() }}</p>
    <p class="purpose">{{ purpose() }}</p>

    @if (!enabled()) {
      <button
        type="button"
        mat-stroked-button
        [disabled]="store.saving()"
        [attr.aria-label]="'Switch the ' + pool() + ' pool on'"
        (click)="switchOn()">
        Switch this pool on
      </button>
      <p class="note">
        Switched on, it holds nothing until you give it a machine count. Creates that belong here
        are routed to it, and it buys only what the two counts below ask for.
      </p>
    } @else {
      <div class="setting">
        <label class="field">
          <span class="name">Machines to keep running</span>
          <input
            type="number"
            min="0"
            [max]="maxFloor()"
            [value]="floorDraft()"
            [disabled]="store.saving()"
            [attr.aria-describedby]="helpFloor()"
            (input)="floorDraft.set(number($event))" />
          <span class="hint">{{ runningNow() }} running now</span>
        </label>
        <button
          type="button"
          class="ask"
          [attr.aria-label]="'What machines to keep running means in the ' + pool() + ' pool'"
          [attr.aria-describedby]="helpFloor()">?</button>
        <p class="explain" [id]="helpFloor()">
          Keep this many machines running in this pool, even while devices fill them. A device
          starting on one changes nothing here, because this counts machines and not free machines.
          It is also where shrinking stops. The autoscaler never gives back a machine that would
          take this pool below the number. 0 lets the pool empty completely once nothing is running
          on it.
        </p>
      </div>

      <div class="setting">
        <app-standby-track
          [(value)]="standbyDraft"
          [max]="maxStandby()"
          [held]="freeNow()"
          [density]="store.density()"
          [quotaSpendable]="quotaSpendable()"
          [quotaReserve]="quotaReserve()"
          [busy]="store.saving()"
          [describedBy]="helpFree()" />
        <button
          type="button"
          class="ask"
          [attr.aria-label]="'What machines to keep free means in the ' + pool() + ' pool'"
          [attr.aria-describedby]="helpFree()">?</button>
        <p class="explain" [id]="helpFree()">
          Keep this many machines in this pool free (with no device on them) so a new device that
          belongs here starts in seconds instead of waiting several minutes for a machine to be
          built. A create that takes one buys a replacement, so the pool ends up this many machines
          larger than what is being used. 0 means a create that finds no room waits.
        </p>
      </div>
      @if (boundsDirty()) {
        <button
          type="button"
          mat-stroked-button
          [disabled]="store.saving()"
          [attr.aria-label]="'Apply machine counts for the ' + pool() + ' pool'"
          (click)="applyBounds()">
          Apply machine counts
        </button>
      }

      <div class="setting">
        <app-schedule-week [schedules]="scheduleDraft()" [baseline]="standbyDraft()" />
        <app-schedule-editor
          [schedules]="scheduleDraft()"
          [maxSchedules]="maxSchedules()"
          [maxHosts]="maxStandby()"
          (schedulesChange)="scheduleDraft.set($event)" />
        <button
          type="button"
          class="ask"
          [attr.aria-label]="'What a recurring window does in the ' + pool() + ' pool'"
          [attr.aria-describedby]="helpWindows()">?</button>
        <p class="explain" [id]="helpWindows()">
          A window is you telling the autoscaler about demand you already know is coming, such as a
          workday morning or a nightly test run. While a window is open it raises the machines this
          pool keeps free to the figure you give it. Windows only ever raise that number, never lower
          it, and where two overlap the larger one wins.
        </p>
      </div>
      @if (schedulesDirty()) {
        <button
          type="button"
          mat-stroked-button
          [disabled]="store.saving()"
          [attr.aria-label]="'Save windows for the ' + pool() + ' pool'"
          (click)="store.saveSchedules(pool(), scheduleDraft())">
          Save windows
        </button>
      }

      <div class="setting">
        <app-shape-ladder-editor
          [pool]="pool()"
          [ladder]="ladderDraft()"
          [maxRungs]="maxRungs()"
          [machineTypes]="machineTypes()"
          [rungs]="servedRungs()"
          [defaultShape]="defaultShape()"
          [defaultSlots]="store.density()"
          (ladderChange)="ladderDraft.set($event)" />
        <button
          type="button"
          class="ask"
          [attr.aria-label]="'What the shape order does in the ' + pool() + ' pool'"
          [attr.aria-describedby]="helpLadder()">?</button>
        <p class="explain" [id]="helpLadder()">
          Google Cloud can run out of a machine shape in every location at once, and a pool that only
          knows one shape then stops growing. This is the order to try instead. The preferred shape
          comes first, then the next one down whenever the shape above it is stocked out. Only this
          pool can actually run are offered. A machine already built keeps the shape it was built
          with. Leave the list empty to let the catalog pick.
        </p>
      </div>
      @if (refusal(); as message) {
        <p class="failure">{{ message }}</p>
      }
      @if (ladderDirty()) {
        <button
          type="button"
          mat-stroked-button
          [disabled]="store.saving()"
          [attr.aria-label]="'Save shape order for the ' + pool() + ' pool'"
          (click)="store.saveLadder(pool(), ladderDraft())">
          Save shape order
        </button>
      }

      @if (!isDefault()) {
        <button
          type="button"
          class="switch-off"
          mat-stroked-button
          [disabled]="store.saving()"
          [attr.aria-label]="'Switch the ' + pool() + ' pool off'"
          (click)="switchOff()">
          Switch this pool off
        </button>
      }
    }
  `,
  styles: `
    :host {
      display: block;
      padding: var(--cco-space-16);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-lg);
      background: var(--cco-surface);
    }

    h4 {
      margin: 0;
      font-size: var(--cco-text-body);
      font-weight: 600;
    }

    .mono {
      font-family: var(--cco-font-mono);
    }

    .family,
    .purpose,
    .note,
    .hint,
    .explain,
    .failure {
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .family {
      margin: 2px 0 0;
    }

    .purpose,
    .note {
      margin: var(--cco-space-4) 0 var(--cco-space-12);
      max-width: 60ch;
    }

    .failure {
      color: var(--cco-error);
    }

    /* Field, then its question mark. The panel below is out of flow, so this
       row is the whole of what the reveal can move. */
    .setting {
      position: relative;
      display: flex;
      align-items: flex-start;
      gap: var(--cco-space-8);
      margin-bottom: var(--cco-space-16);
    }

    .field {
      display: flex;
      flex-direction: column;
      gap: 2px;
      max-width: 32ch;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .field input {
      width: 8rem;
      padding: 2px 6px;
      font: inherit;
      font-family: var(--cco-font-mono);
      color: var(--cco-text);
      background: var(--cco-surface);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-sm);
    }

    /* A real button rather than a glyph, so it can be reached by Tab and the
       explanation is available to somebody who never touches a pointer. Small
       on purpose, because the reveal must be asked for exactly, or moving
       across a section opens paragraphs nobody wanted. */
    .ask {
      flex: none;
      width: 1.4em;
      height: 1.4em;
      padding: 0;
      font: inherit;
      font-size: var(--cco-text-label);
      line-height: 1;
      color: var(--cco-text-secondary);
      background: var(--cco-surface);
      border: 1px solid var(--cco-border);
      border-radius: 50%;
      cursor: help;
    }

    .ask:focus-visible {
      outline: 2px solid var(--cco-accent);
      outline-offset: 1px;
    }

    /* Revealed by hover OR focus, in CSS rather than in a signal, and OUT OF
       FLOW.
       Out of flow because the in-flow version had a real defect. Focusing a
       field opened a paragraph that pushed the section's Apply button down, and
       pressing that button blurred the field, closing the paragraph and moving
       the button up between the press and the release, so the click landed on
       nothing. Absolute positioning removes the reflow entirely, and
       pointer-events keeps the panel from ever standing between a pointer and a
       control.
       display, never opacity. A paragraph nobody can see must not be a
       paragraph the contrast scan judges. Screen readers still reach it while
       it is hidden, because a directly referenced aria-describedby target is
       read even when it is display:none, which is what makes this affordance
       visual only. */
    .explain {
      display: none;
      position: absolute;
      z-index: 2;
      top: 100%;
      left: 0;
      width: max-content;
      max-width: min(62ch, 90vw);
      margin: var(--cco-space-4) 0 0;
      padding: var(--cco-space-8) var(--cco-space-12);
      pointer-events: none;
      background: var(--cco-surface);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-sm);
    }

    .ask:hover + .explain,
    .ask:focus + .explain {
      display: block;
    }

    .switch-off {
      margin-top: var(--cco-space-8);
    }

    app-standby-track,
    app-schedule-week {
      display: block;
    }
  `,
})
export class PoolControlsCardComponent {
  protected readonly store = inject(AutoscalerStore);
  private readonly dialog = inject(MatDialog);
  /**
   * The catalog behind the shape picker. The cached root store is used rather
   * than a read of its own, because it already holds the enabled machine types
   * the create forms offer, and those are the shapes a pool may be built from.
   */
  private readonly catalog = inject(MachineTypeStore);

  readonly pool = input.required<string>();

  protected readonly machineTypes = this.catalog.entities;

  /** What this pool's operator stored, or the zero intent while it is off. */
  private readonly intent = computed(() => this.store.intents()[this.pool()] ?? UNSET_POOL_INTENT);

  protected readonly enabled = computed(() => this.store.intents()[this.pool()] !== undefined);
  protected readonly isDefault = computed(() => this.pool() === DEFAULT_FLEET_TIER);
  protected readonly family = computed(() => poolFamily(this.pool()));
  protected readonly purpose = computed(() => poolPurpose(this.pool(), this.store.armImageReady()));

  /**
   * Drafts derived from the stored intent and writable over it. linkedSignal
   * rather than a signal synced in an effect, because the server's copy is the
   * source, and a reload after a save has to reset the draft with no second
   * writer.
   */
  protected readonly standbyDraft = linkedSignal(() => this.intent().standby);
  protected readonly floorDraft = linkedSignal(() => this.intent().floor);
  protected readonly scheduleDraft = linkedSignal<FleetSchedule[]>(() =>
    structuredClone(this.intent().schedules)
  );
  protected readonly ladderDraft = linkedSignal<string[]>(() => [...this.intent().ladder]);

  /** The compiled bounds, served beside the policy so nothing here retypes one. */
  protected readonly maxFloor = computed(() => this.store.policy()?.maxFloor ?? 0);
  protected readonly maxStandby = computed(() => this.store.policy()?.maxStandby ?? 0);
  protected readonly maxSchedules = computed(() => this.store.policy()?.maxSchedules ?? 0);
  protected readonly maxRungs = computed(() => this.store.policy()?.maxLadderRungs ?? 0);

  /** This pool's own picture, or nothing before the snapshot lands. */
  private readonly region = computed(() =>
    this.store.snapshot()?.pools.find(entry => entry.pool === this.pool())
  );

  /** Machines this pool is running and holding free, server-derived, never a draft. */
  protected readonly runningNow = computed(() => this.region()?.status.total ?? 0);
  protected readonly freeNow = computed(() => this.region()?.status.standby ?? 0);

  /**
   * The quota facts belong to the deployment and not to this pool. Host quota
   * is one number every pool spends from, and the reserve is held out of it.
   */
  protected readonly quotaSpendable = computed(
    () => this.store.snapshot()?.gate.quotaSpendable ?? this.maxStandby()
  );
  protected readonly quotaReserve = computed(
    () => this.store.snapshot()?.limits.quotaReserveHosts ?? 1
  );

  /** This pool's order as the server judges it now, matched by shape. */
  protected readonly servedRungs = computed(() => this.store.servedLadders()[this.pool()] ?? []);

  /**
   * The shape a machine with no order is built as. Known for the pool every
   * create still lands in and for no other, so a pool that would not be built
   * that way is offered no shape rather than the wrong one.
   */
  protected readonly defaultShape = computed(() =>
    this.isDefault() ? this.store.defaultShape() : ''
  );

  /** The last refusal, drawn only under the order it was about. */
  protected readonly refusal = computed(() => {
    const error = this.store.ladderError();
    return error?.pool === this.pool() ? error.message : '';
  });

  protected readonly helpFloor = computed(() => `help-floor-${this.pool()}`);
  protected readonly helpFree = computed(() => `help-free-${this.pool()}`);
  protected readonly helpWindows = computed(() => `help-windows-${this.pool()}`);
  protected readonly helpLadder = computed(() => `help-ladder-${this.pool()}`);

  /**
   * One dirty flag for both counts, because they are one write. Offering a
   * separate Apply per count would build two requests out of one form, and the
   * second would carry whatever the first had already stored.
   */
  protected readonly boundsDirty = computed(
    () => this.standbyDraft() !== this.intent().standby || this.floorDraft() !== this.intent().floor
  );

  protected readonly schedulesDirty = computed(
    () => JSON.stringify(this.scheduleDraft()) !== JSON.stringify(this.intent().schedules)
  );

  /**
   * Structural, and by position. The order IS the setting, so two orders
   * holding the same shapes in a different sequence are a real edit.
   */
  protected readonly ladderDirty = computed(
    () => JSON.stringify(this.ladderDraft()) !== JSON.stringify(this.intent().ladder)
  );

  protected number(event: Event): number {
    return Number((event.target as HTMLInputElement).value);
  }

  /** Switching a pool on is storing its bounds; there is no other verb. */
  protected switchOn(): void {
    this.store.saveAutoscaler(this.mode(), this.pool(), this.standbyDraft(), this.floorDraft());
  }

  protected switchOff(): void {
    this.confirm(
      {
        title: `Switch the ${this.pool()} pool off?`,
        message: poolDisabling(this.pool()),
        warning:
          'Its machine counts, its windows and its shape order are all deleted. Switching it on again starts from nothing.',
        confirmText: 'Switch it off',
        confirmColor: 'warn',
        icon: 'warning',
        iconColor: 'warn',
      },
      () => this.store.disablePool(this.pool())
    );
  }

  /**
   * Both counts in one write, with a confirmation for whichever of them was
   * lowered, and only under `on`, because below it the machines already
   * standing simply run out their own time.
   *
   * The floor's confirmation is not the buffer's said twice. Lowering the free
   * count releases machines nothing is using; lowering the floor lifts the stop
   * that reclamation answers to, so it can release machines down to a pool the
   * operator had said would always be there.
   */
  protected applyBounds(): void {
    const apply = (): void =>
      this.store.saveAutoscaler(this.mode(), this.pool(), this.standbyDraft(), this.floorDraft());
    if (!reclaims(this.mode())) {
      apply();
      return;
    }
    const floorDrop = this.intent().floor - this.floorDraft();
    if (floorDrop > 0) {
      this.confirm(
        {
          title: `Let the ${this.pool()} pool shrink to ${this.floorDraft()} machine${this.floorDraft() === 1 ? '' : 's'}?`,
          message:
            'The machines-to-keep-running number is where shrinking stops. Lowering it lets the autoscaler release machines down to the new number, by cutting their remaining time to the drain grace.',
          warning:
            'Released machines are gone within minutes. A device starting on one first keeps it; after that, starting a device there waits for a new machine.',
          confirmText: 'Lower it',
          confirmColor: 'warn',
          icon: 'warning',
          iconColor: 'warn',
        },
        apply,
        // Dismissal has to put the number back by hand. The draft is derived
        // from the stored intent, and the stored intent did not change, so
        // nothing would repaint an input still showing the lower figure.
        () => this.floorDraft.set(this.intent().floor)
      );
      return;
    }
    const given = this.intent().standby - this.standbyDraft();
    if (given <= 0) {
      apply();
      return;
    }
    this.confirm(
      {
        title: `Stop keeping ${given} machine${given === 1 ? '' : 's'} free in the ${this.pool()} pool?`,
        message:
          'The autoscaler is on, so machines it no longer needs free are released by cutting their remaining time rather than being left to run out. The drawing above lists them in the order they go.',
        warning: 'Released machines are gone within minutes; a create arriving first keeps one.',
        confirmText: 'Give them back',
        confirmColor: 'warn',
        icon: 'warning',
        iconColor: 'warn',
      },
      apply,
      () => this.standbyDraft.set(this.intent().standby)
    );
  }

  /** The mode is the whole loop's; every bounds write carries the stored one. */
  private mode(): AutoscaleMode {
    return this.store.policy()?.mode ?? 'off';
  }

  private confirm(data: ConfirmDialogData, onConfirm: () => void, onDismiss?: () => void): void {
    this.dialog
      .open(ConfirmDialogComponent, {data})
      .afterClosed()
      .subscribe((confirmed: boolean) => {
        if (confirmed) {
          onConfirm();
          return;
        }
        onDismiss?.();
      });
  }
}
