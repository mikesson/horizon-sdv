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

import type {OnInit} from '@angular/core';
import {ChangeDetectionStrategy, Component, computed, inject, linkedSignal} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatDialog} from '@angular/material/dialog';
import {MachineTypeStore} from '../../../machine-types';
import type {ConfirmDialogData} from '../../../shared/components';
import {ConfirmDialogComponent} from '../../../shared/components';
import {reclaims} from '../../models/autoscaler.model';
import type {AutoscaleMode} from '../../models/control-plane.model';
import {DEFAULT_FLEET_TIER, FLEET_POOL_NAMES} from '../../models/control-plane.model';
import {AutoscalerStore} from '../../store/autoscaler.signal-store';
import {ModeLadderComponent} from './mode-ladder.component';
import {PoolControlsCardComponent} from './pool-controls-card.component';

/**
 * Every autoscaler setting, as one card per pool and one card for the
 * mechanism the loop runs by.
 *
 * The split is what the numbers are ABOUT. A pool's card carries how big that
 * pool is; the mechanism card carries how the loop behaves, meaning what it is
 * permitted to do, how far back it measures, and how much lease a machine it
 * releases keeps. None of the three makes any pool larger, which is why they are
 * not per-pool settings.
 *
 * Save scopes stay separate, per pool and per group. A whole-policy write from
 * any one surface carries a stale copy of the others' fields and silently
 * reverts them. A client that read, overlaid and wrote back would clear every
 * window the moment somebody edited a horizon. One page may present them all; it
 * must not merge their commits.
 *
 * Every control carries an explanation written for somebody who has never seen
 * this system, revealed by hovering OR focusing its setting. Revealed rather
 * than always shown because six paragraphs standing open is a page nobody reads.
 * Done in CSS rather than in a script, so the reveal happens in the same frame
 * as the focus and the description is there when a screen reader asks for it.
 * The whole vocabulary is pool, machine, slot, device, free and running; a word
 * that has to be learned before the control can be used is a control that does
 * not work.
 */
@Component({
  selector: 'app-autoscaler-controls',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatButtonModule, ModeLadderComponent, PoolControlsCardComponent],
  template: `
    @if (store.policyError(); as message) {
      <p class="failure">{{ message }}</p>
    } @else if (store.policy(); as policy) {
      <div class="cards">
        @for (pool of pools; track pool) {
          <app-pool-controls-card [pool]="pool" />
        }

        <section class="card mechanism">
          <h4>Mechanism: how the loop behaves</h4>
          <p class="note">
            These three shape how the autoscaler works, not how big any pool is, which is why they
            are not set per pool.
          </p>

          <div class="setting">
            <app-mode-ladder
              [mode]="mode()"
              [busy]="store.saving()"
              (modeChange)="chooseMode($event)" />
            <button
              type="button"
              class="ask"
              aria-label="What the three modes mean"
              aria-describedby="help-mode">?</button>
            <p class="explain" id="help-mode">
              The only control here that changes what the system is <em>permitted</em> to do; every
              other one is a number it pursues. Off works out how many machines there should be and
              does nothing about it. Provision only may start machines, and may keep a machine that
              is shutting down alive instead, and it can never take one away. On may also give
              machines back, by cutting a machine's remaining time short so it is cleaned up sooner.
            </p>
          </div>

          <div class="fields">
            <div class="setting">
              <label class="field">
                <span class="name">Demand window (seconds)</span>
                <input
                  type="number"
                  min="0"
                  [max]="policy.maxDemandHorizonSec"
                  [value]="horizonDraft()"
                  [disabled]="store.saving()"
                  aria-describedby="help-horizon"
                  (input)="horizonDraft.set(number($event))" />
              </label>
              <button
                type="button"
                class="ask"
                aria-label="What the demand window measures"
                aria-describedby="help-horizon">?</button>
              <p class="explain" id="help-horizon">
                The autoscaler watches creates that found no machine free and had to wait for one to
                be built. This is how far back it looks. It raises each pool's free-machine count to
                the largest number of those waits that overlapped there, so a burst that already
                happened is answered the next time it comes. 0 stops that entirely, and only the
                numbers set on the pools decide their size.
              </p>
            </div>
            <div class="setting">
              <label class="field">
                <span class="name">Drain grace (seconds)</span>
                <input
                  type="number"
                  [min]="policy.minDrainGraceSec"
                  [max]="policy.maxDrainGraceSec"
                  [value]="graceDraft()"
                  [disabled]="store.saving()"
                  aria-describedby="help-grace"
                  (input)="graceDraft.set(number($event))" />
              </label>
              <button
                type="button"
                class="ask"
                aria-label="What the drain grace does"
                aria-describedby="help-grace">?</button>
              <p class="explain" id="help-grace">
                A machine that is given back is not deleted at once: its remaining time is cut to
                this many seconds, and it is cleaned up when that runs out. A device starting inside
                that window keeps the machine. The {{ policy.maxDrainGraceSec }}s ceiling sits just
                under how long building a machine takes, which is what makes a released machine stop
                counting as free straight away.
              </p>
            </div>
          </div>
          @if (tuningDirty()) {
            <button
              type="button"
              mat-stroked-button
              [disabled]="store.saving()"
              (click)="applyTuning()">
              Save measurement and drain
            </button>
          }
        </section>
      </div>
    } @else {
      <p class="note">Loading the stored policy…</p>
    }
  `,
  styles: `
    :host {
      display: block;
    }

    .cards {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(24rem, 1fr));
      gap: var(--cco-space-16);
      align-items: start;
    }

    .card {
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

    .note,
    .explain,
    .failure {
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .note {
      margin: var(--cco-space-4) 0 var(--cco-space-12);
      max-width: 60ch;
    }

    .failure {
      color: var(--cco-error);
    }

    .fields {
      display: flex;
      flex-wrap: wrap;
      gap: var(--cco-space-24);
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
       field opened a paragraph that pushed the section's Save button down, and
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

    app-mode-ladder {
      display: block;
    }
  `,
})
export class AutoscalerControlsComponent implements OnInit {
  protected readonly store = inject(AutoscalerStore);
  private readonly dialog = inject(MatDialog);
  private readonly catalog = inject(MachineTypeStore);

  /**
   * The pools this platform can partition its machines into, offered whether or
   * not one is switched on, because a card for a pool nobody enabled is how it
   * gets enabled. The server still decides, and a write naming a pool it does
   * not have is refused by name.
   */
  protected readonly pools = FLEET_POOL_NAMES;

  protected readonly horizonDraft = linkedSignal(() => this.store.policy()?.demandHorizonSec ?? 0);
  protected readonly graceDraft = linkedSignal(() => this.store.policy()?.drainGraceSec ?? 0);

  protected readonly mode = computed<AutoscaleMode>(() => this.store.policy()?.mode ?? 'off');

  ngOnInit(): void {
    this.catalog.loadAll(undefined);
  }

  protected readonly tuningDirty = computed(() => {
    const policy = this.store.policy();
    return (
      !!policy &&
      (this.horizonDraft() !== policy.demandHorizonSec ||
        this.graceDraft() !== policy.drainGraceSec)
    );
  });

  protected number(event: Event): number {
    return Number((event.target as HTMLInputElement).value);
  }

  /**
   * Only the rung that grants the authority to shorten a lease confirms.
   * Everything below `on` can add capacity and never take it away, so there is
   * nothing unattended to warn about.
   *
   * The mode rides the same write as a pool's two counts, so this one carries
   * the DEFAULT pool's stored numbers. That pool always exists, and its stored
   * values are the only ones here that cannot be somebody's unsaved edit on
   * another card.
   */
  protected chooseMode(mode: AutoscaleMode): void {
    const apply = (): void =>
      this.store.saveAutoscaler(mode, DEFAULT_FLEET_TIER, this.store.standby(), this.store.floor());
    if (!reclaims(mode)) {
      apply();
      return;
    }
    this.confirm(
      {
        title: 'Let the autoscaler give machines back?',
        message:
          'On, the autoscaler also releases machines it judges spare, in every pool. Their remaining time is cut to the drain grace, after which they are cleaned up. It never releases one that would take a pool below its machines-to-keep-running number.',
        warning:
          'Releasing is unattended. A device starting inside the drain grace keeps the machine; after that a create waits the full few minutes for a new one.',
        confirmText: 'Turn it on',
        confirmColor: 'warn',
        icon: 'warning',
        iconColor: 'warn',
      },
      apply
    );
  }

  protected applyTuning(): void {
    this.store.saveTuning(this.horizonDraft(), this.graceDraft());
  }

  private confirm(data: ConfirmDialogData, onConfirm: () => void): void {
    this.dialog
      .open(ConfirmDialogComponent, {data})
      .afterClosed()
      .subscribe((confirmed: boolean) => {
        if (confirmed) {
          onConfirm();
        }
      });
  }
}
