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
import {MatCardModule} from '@angular/material/card';

/**
 * A settings section, in exactly one of two interaction models.
 *
 * `form` batches text/number/select edits behind an explicit Save that stays
 * disabled until dirty. `immediate` holds switches that apply on flip and has
 * no Save button in its template at all.
 *
 * The split matters because a toggle inside a form leaves the user unable to
 * tell whether flipping it did anything. Because the Save markup exists only
 * in the `form` branch, a card cannot mix the two without hand-rolling one
 * outside this component.
 */
@Component({
  selector: 'app-settings-card',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatButtonModule, MatCardModule],
  // See app-empty-state: a static `title` attribute reaches the DOM as well as
  // the input, and the browser then draws a native tooltip over the card.
  host: {'[attr.title]': 'null'},
  template: `
    <div class="settings-section">
      <div class="section-intro">
        <h2>{{ title() }}</h2>
        @if (subtitle()) {
          <p>{{ subtitle() }}</p>
        }
        <ng-content select="[subtitle]" />
      </div>

      <mat-card appearance="outlined" class="settings-card">
        <mat-card-content>
          <ng-content />
        </mat-card-content>

        @if (variant() === 'form') {
          <mat-card-actions align="end">
            <button type="button"
              mat-flat-button
              [disabled]="!dirty() || saving()"
              (click)="save.emit()"
            >
              {{ saving() ? 'Saving…' : 'Save' }}
            </button>
          </mat-card-actions>
        }
      </mat-card>
    </div>
  `,
  styles: `
    /* The section label reads down the left and the controls sit beside it, so
       the page uses its width without ever putting two fields on one line,
       which is the layout the form research rules out. Groups still
       stack vertically, one reading order.

       Ratio from Polaris's settings pattern: a label column wide enough for a
       sentence, and the rest to the card. */
    .settings-section {
      display: grid;
      grid-template-columns: minmax(200px, 2fr) minmax(0, 5fr);
      gap: var(--cco-space-32);
      align-items: start;
      max-width: 1080px;
    }

    .section-intro {
      padding-top: var(--cco-space-16);
    }

    .section-intro h2 {
      margin: 0;
      font-size: var(--cco-text-title);
      font-weight: 500;
    }

    .section-intro p {
      margin: var(--cco-space-4) 0 0;
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
    }

    .settings-card mat-card-content {
      padding: var(--cco-space-24) var(--cco-space-24) var(--cco-space-8);
    }

    /* Without an actions row the content owes the whole bottom edge; with one,
       the 52px row already carries 8px above its 36px button. */
    .settings-card mat-card-content:last-child {
      padding-bottom: var(--cco-space-24);
    }

    .settings-card mat-card-actions {
      padding: 0 var(--cco-space-24) var(--cco-space-16);
    }

    /* Under this the label has nowhere useful to go, so it returns to sitting
       above its own card. */
    @container page (max-width: 800px) {
      .settings-section {
        grid-template-columns: 1fr;
        gap: var(--cco-space-8);
      }

      .section-intro {
        padding-top: 0;
      }
    }
  `,
})
export class SettingsCardComponent {
  readonly title = input.required<string>();
  readonly subtitle = input('');
  readonly variant = input<'form' | 'immediate'>('form');
  readonly dirty = input(false);
  readonly saving = input(false);

  readonly save = output<void>();
}
