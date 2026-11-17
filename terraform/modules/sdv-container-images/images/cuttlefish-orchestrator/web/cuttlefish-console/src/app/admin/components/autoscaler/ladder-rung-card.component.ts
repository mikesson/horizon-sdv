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
import {MatButtonModule} from '@angular/material/button';
import {MatIconModule} from '@angular/material/icon';
import {MatMenuModule} from '@angular/material/menu';
import {MatTooltipModule} from '@angular/material/tooltip';

/** One family's shapes, which is how the picker offers them. */
export interface ShapeGroup {
  readonly family: string;
  readonly shapes: readonly string[];
}

/**
 * What the server says about one shape of the order, in the four readings a
 * card can carry. `unsaved` and `dropped` are the two ways the server describes
 * no machine of this shape at all, and they need different remedies.
 */
export type ShapeState = 'building' | 'stocked-out' | 'available' | 'unsaved' | 'dropped';

/** One shape of the order, with everything its card renders from. */
export interface LadderRow {
  /** Where it sits in the array the editor emits. */
  readonly index: number;
  /** Where it sits on screen, counted from one. */
  readonly position: number;
  readonly machineType: string;
  readonly options: readonly ShapeGroup[];
  readonly first: boolean;
  readonly last: boolean;
  /** '' when the server describes no machine of this shape. */
  readonly facts: string;
  readonly state: ShapeState;
  readonly word: string;
  /** When the stockout behind the word was seen; '' when none stands. */
  readonly seen: string;
}

/**
 * One shape of the order: where it sits, what a machine built there holds, and
 * what provisioning is doing about it right now.
 *
 * Nothing here derives anything. The card arrives composed, so the rules that
 * decide a word (a stockout in every zone, the one shape being built) stay
 * assertable as arithmetic in the editor rather than as rendered text.
 */
@Component({
  selector: 'app-ladder-rung-card',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatButtonModule, MatIconModule, MatMenuModule, MatTooltipModule],
  host: {
    '[attr.data-state]': 'row().state',
  },
  template: `
    <div class="gutter">
      <span class="position" aria-hidden="true">{{ row().position }}</span>
      <button
        type="button"
        mat-icon-button
        matTooltip="Prefer this shape sooner"
        [disabled]="row().first"
        [attr.aria-label]="'Move ' + row().machineType + ' up'"
        (click)="moveUp.emit()">
        <mat-icon>arrow_upward</mat-icon>
      </button>
      <button
        type="button"
        mat-icon-button
        matTooltip="Prefer this shape later"
        [disabled]="row().last"
        [attr.aria-label]="'Move ' + row().machineType + ' down'"
        (click)="moveDown.emit()">
        <mat-icon>arrow_downward</mat-icon>
      </button>
      <button
        type="button"
        mat-icon-button
        matTooltip="Remove this shape"
        [attr.aria-label]="'Remove ' + row().machineType"
        (click)="remove.emit()">
        <mat-icon>delete</mat-icon>
      </button>
    </div>

    <div class="body">
      @if (row().first) {
        <span class="mark">preferred</span>
      }
      <!-- The caret is drawn rather than written, because an icon's ligature IS
           text, and a control whose accessible name does not contain its
           visible text fails the label rule. -->
      <button
        type="button"
        class="shape"
        [matMenuTriggerFor]="picker"
        [attr.aria-label]="row().machineType + ', change this shape'">
        {{ row().machineType }}
      </button>
      @if (row().facts) {
        <p class="facts">{{ row().facts }}</p>
      }
    </div>

    <div class="state">
      <span class="dot" aria-hidden="true"></span>
      <span class="word">{{ row().word }}</span>
      @if (row().seen) {
        <span class="seen">{{ row().seen }}</span>
      }
    </div>

    <mat-menu #picker="matMenu">
      <ng-template matMenuContent>
        @for (group of row().options; track group.family) {
          <div class="family" role="group" [attr.aria-label]="group.family + ' machines'">
            <span class="family-name" aria-hidden="true">{{ group.family }}</span>
            @for (shape of group.shapes; track shape) {
              <button type="button" mat-menu-item (click)="shapeChange.emit(shape)">
                {{ shape }}
              </button>
            }
          </div>
        }
      </ng-template>
    </mat-menu>
  `,
  styles: `
    :host {
      /* The gutter holds full-size targets, the state column sizes to its
         longest word, and the shape takes what is left. */
      display: grid;
      grid-template-columns: auto 1fr auto;
      align-items: center;
      gap: var(--cco-space-12);
      padding: var(--cco-space-8) var(--cco-space-16);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-sm);

      /* Dropped in light, because on a light ground a glow reads as a smear
         rather than a light source. */
      --cco-ladder-glow: light-dark(transparent, rgb(138 180 248 / 30%));
    }

    /* The one shape provisioning would build now, marked by an edge as well as
       by its word. The word is what carries it, and the edge is what finds it
       in a list of eight. */
    :host([data-state="building"]) {
      border-color: var(--cco-accent);
      box-shadow: 0 0 18px var(--cco-ladder-glow);
    }

    .gutter {
      display: flex;
      align-items: center;
      flex: none;
    }

    .position {
      width: 1.5rem;
      font-family: var(--cco-font-mono);
      font-variant-numeric: tabular-nums;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .body {
      display: flex;
      flex-direction: column;
      align-items: start;
      gap: 2px;
      min-width: 0;
    }

    .mark {
      font-size: var(--cco-text-label);
      letter-spacing: 0.08em;
      text-transform: uppercase;
      color: var(--cco-text-secondary);
    }

    .shape {
      padding: 0;
      border: none;
      background: none;
      font: inherit;
      font-family: var(--cco-font-mono);
      font-size: var(--cco-text-body);
      color: var(--cco-text);
      cursor: pointer;
    }

    .shape::after {
      content: "▾";
      margin-inline-start: var(--cco-space-4);
      color: var(--cco-text-secondary);
    }

    .shape:hover,
    .shape:focus-visible {
      text-decoration: underline;
    }

    .facts {
      margin: 0;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .state {
      display: grid;
      grid-template-columns: auto 1fr;
      align-items: center;
      gap: var(--cco-space-4) var(--cco-space-8);
      justify-items: start;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    /* A mark that agrees with the word, never one that replaces it. */
    .dot {
      width: 8px;
      height: 8px;
      border-radius: 50%;
      background: var(--cco-text-disabled);
    }

    .seen {
      grid-column: 2;
      font-variant-numeric: tabular-nums;
    }

    :host([data-state="building"]) .dot {
      background: var(--cco-accent);
    }

    :host([data-state="building"]) .word {
      color: var(--cco-text);
    }

    :host([data-state="stocked-out"]) .dot {
      background: var(--cco-warning);
    }

    :host([data-state="stocked-out"]) .word {
      color: var(--cco-warning);
    }

    :host([data-state="available"]) .dot {
      background: var(--cco-success);
    }

    .family {
      padding-bottom: var(--cco-space-4);
    }

    .family-name {
      display: block;
      padding: var(--cco-space-4) var(--cco-space-16) 0;
      font-family: var(--cco-font-mono);
      font-size: var(--cco-text-label);
      letter-spacing: 0.08em;
      text-transform: uppercase;
      color: var(--cco-text-secondary);
    }
  `,
})
export class LadderRungCardComponent {
  readonly row = input.required<LadderRow>();
  readonly shapeChange = output<string>();
  readonly moveUp = output<void>();
  readonly moveDown = output<void>();
  readonly remove = output<void>();
}
