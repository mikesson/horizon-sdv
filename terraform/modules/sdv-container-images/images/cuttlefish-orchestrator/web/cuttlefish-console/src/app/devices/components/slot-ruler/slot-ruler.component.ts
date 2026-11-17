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

import {ChangeDetectionStrategy, Component, input, output} from '@angular/core';

import type {Notch} from '../../models/slot-space';
import {NOTCH_TINTS} from '../../models/slot-space';

/**
 * What a notch says when nothing about the viewer's scope changes it.
 *
 * Keyed by the kind union rather than switched on, so a new kind is a compile
 * error here instead of falling through a default arm into whichever label
 * happened to be last.
 */
const NOTCH_LABEL: Record<Notch['kind'], string> = {
  own: '',
  foreign: 'occupied',
  free: 'free',
  unbacked: 'unbacked',
};

const NOTCH_DESCRIPTION: Record<Notch['kind'], string> = {
  own: '',
  foreign: 'occupied',
  free: 'free',
  unbacked: 'promised by the host but not backed by its image',
};

/**
 * The host's instance-slot scale: one notch per slot, in true slot positions.
 *
 * Position is the whole content here. cvd allocates slots lowest-free-first and
 * one at a time, so a host that has been churning hands out holes, and a scale
 * that closed them would hide the fragmentation it exists to show. Nothing in
 * this component may reorder or elide a notch.
 */
@Component({
  selector: 'app-slot-ruler',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {class: 'slot-ruler', '[class.pinned]': "scope() === 'host'"},
  template: `
    <div class="ruler-head">
      <span class="ruler-label">slot</span>
      <span class="capacity">{{ headline() }}</span>
    </div>

    <ol class="notches" [attr.aria-label]="'Instance slots on ' + hostName()">
      @for (notch of notches(); track notch.slot) {
        <li>
          <button
            type="button"
            [class]="'notch ' + notch.kind"
            [class.highlighted]="highlighted().includes(notch.slot)"
            [style.--notch-tint]="tintOf(notch)"
            [attr.aria-disabled]="notch.kind === 'own' || notch.kind === 'foreign' ? null : true"
            [attr.aria-pressed]="notch.slot === selected() ? true : null"
            [attr.aria-label]="accessibleName(notch)"
            [title]="accessibleName(notch)"
            (click)="activate(notch)"
            (mouseenter)="highlight.emit(notch.slot)"
            (mouseleave)="highlight.emit(null)"
            (focus)="highlight.emit(notch.slot)"
            (blur)="highlight.emit(null)"
          >
            <span class="slot-number">{{ pad(notch.slot) }}</span>
            <span class="occupant">{{ occupantLabel(notch) }}</span>
          </button>
        </li>
      }
    </ol>
  `,
  styles: `
    .slot-ruler {
      display: block;
      background: var(--cco-surface);
      padding-block: var(--cco-space-8);
    }

    /* Pinned only where there is a lane stack to scroll past. The
       correspondence highlight is the sole ruler-to-lane link, so a ruler that
       scrolled away there would not degrade the mechanism, it would remove it.
       On a device page there are no lanes below it, so pinning would buy
       nothing and leave a bar floating over the cards that follow. */
    .slot-ruler.pinned {
      position: sticky;
      top: 0;
      z-index: 2;
    }

    .ruler-head {
      display: flex;
      justify-content: space-between;
      align-items: baseline;
      gap: var(--cco-space-16);
      margin-block-end: var(--cco-space-4);
    }

    .ruler-label {
      font-size: var(--cco-text-label);
      letter-spacing: 0.06em;
      text-transform: uppercase;
      color: var(--cco-text-secondary);
    }

    .capacity {
      font-size: var(--cco-text-sm);
      font-family: var(--cco-font-mono);
      font-variant-numeric: tabular-nums;
      color: var(--cco-text-secondary);
    }

    .notches {
      display: flex;
      gap: 2px;
      margin: 0;
      padding: 0;
      list-style: none;
      overflow-x: auto;
    }

    .notches li {
      flex: 1 1 0;
      min-width: 3.5rem;
    }

    .notch {
      display: flex;
      flex-direction: column;
      gap: 2px;
      width: 100%;
      padding: var(--cco-space-4);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-xs);
      background: var(--cco-surface-variant);
      color: var(--cco-text-secondary);
      font-family: var(--cco-font-mono);
      font-variant-numeric: tabular-nums;
      font-size: var(--cco-text-sm);
      text-align: start;
      cursor: pointer;
    }

    .slot-number {
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .occupant {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      color: var(--cco-text);
    }

    /* The tint identifies, it never signals. Status colour stays on the
       instance marks, which is the only vocabulary allowed to mean liveness.
       Low alpha for the same reason: enough to band a contiguous run, never
       enough to compete. A notch with no tint set keeps the plain surface. */
    .notch[style*='--notch-tint'] {
      background: color-mix(
        in oklab,
        hsl(var(--notch-tint) 70% 50% / 12%),
        var(--cco-surface-variant)
      );
    }

    .notch.own {
      background: color-mix(in srgb, var(--cco-accent) 12%, var(--cco-surface-variant));
      border-color: var(--cco-accent);
    }

    .notch.foreign .occupant {
      color: var(--cco-text-secondary);
    }

    .notch.free,
    .notch.unbacked {
      cursor: default;
    }

    .notch.free {
      border-style: dashed;
      border-color: var(--cco-text-disabled);
      background: transparent;
    }

    /* Hatched, plus a border, plus the word, and no status mark. The mark
       vocabulary means instance liveness, so a triangle here would read at a
       glance as failed instances on a host that has none. */
    .notch.unbacked {
      border-color: var(--cco-error);
      color: var(--cco-error);
      background: repeating-linear-gradient(
        45deg,
        transparent,
        transparent 3px,
        color-mix(in srgb, var(--cco-error) 18%, transparent) 3px,
        color-mix(in srgb, var(--cco-error) 18%, transparent) 6px
      );
    }

    .notch.unbacked .occupant {
      color: var(--cco-error);
    }

    .notch.highlighted {
      outline: 2px solid var(--cco-accent);
      outline-offset: 1px;
    }

    .notch:focus-visible {
      outline: 2px solid var(--cco-accent);
      outline-offset: 1px;
    }
  `,
})
export class SlotRulerComponent {
  readonly notches = input.required<readonly Notch[]>();
  readonly headline = input.required<string>();
  readonly hostName = input<string>('this host');

  /**
   * Whose page this is drawn on.
   *
   * The only difference is whether a slot someone else holds is named. A device
   * page has no business naming its neighbours; a host page is exactly where
   * they belong. The truncated name is an index either way. Five characters
   * collide, and what resolves one is the correspondence highlight and the full
   * name in the accessible name.
   */
  readonly scope = input<'device' | 'host'>('device');

  /** The slot the page's instance selection currently names, if any. */
  readonly selected = input<number | null>(null);

  /**
   * The slots lit by the correspondence, from hover or focus.
   *
   * A set rather than one slot: a lane owns several, and lighting only the
   * first would answer "which slots are this group's" with a lie on exactly the
   * fragmented host the ruler exists for.
   */
  readonly highlighted = input<readonly number[]>([]);

  readonly slotSelect = output<number>();

  /**
   * Hover and focus emit the same event, always. A keyboard user arrowing along
   * the ruler has to get the feedback a pointer gives, and highlighting must
   * never move focus. Focus is the keyboard's alone.
   */
  readonly highlight = output<number | null>();

  protected pad(slot: number): string {
    return String(slot).padStart(2, '0');
  }

  /**
   * The group tint, as the hue channel of the notch's fill.
   *
   * Withheld from a foreign notch at device scope for the same reason its name
   * is: which group is sitting in slot 06 is not that page's business, and a
   * tint would be an identity cue for something the page then refuses to name.
   * This device's own slots keep theirs, which is what bands its run.
   */
  protected tintOf(notch: Notch): string | null {
    if (notch.tint === undefined) {
      return null;
    }
    if (notch.kind === 'foreign' && this.scope() === 'device') {
      return null;
    }
    return `${(notch.tint * 360) / NOTCH_TINTS}`;
  }

  protected occupantLabel(notch: Notch): string {
    if (notch.kind === 'own') {
      return notch.instanceName ?? '';
    }
    // On a device's own page, which device is sitting in a foreign slot is not
    // that page's business. That the slot is taken, and where, is. On the
    // host's page every group is in scope, so the notch names its owner.
    if (notch.kind === 'foreign' && this.scope() === 'host') {
      return notch.deviceName ?? NOTCH_LABEL.foreign;
    }
    return NOTCH_LABEL[notch.kind];
  }

  /**
   * Five characters do not identify a group and are not asked to. The notch is
   * an index, and the correspondence highlight is what resolves it. The full
   * name lives here and in the title so neither reader has to guess.
   */
  protected accessibleName(notch: Notch): string {
    const slot = `slot ${this.pad(notch.slot)}`;
    if (notch.kind === 'own') {
      return `${slot}, ${notch.instanceName}`;
    }
    if (notch.kind === 'foreign' && this.scope() === 'host' && notch.deviceName) {
      return `${slot}, ${notch.deviceName}`;
    }
    return `${slot}, ${NOTCH_DESCRIPTION[notch.kind]}`;
  }

  protected activate(notch: Notch): void {
    if (notch.kind === 'own' || notch.kind === 'foreign') {
      this.slotSelect.emit(notch.slot);
    }
  }
}
