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

import {ChangeDetectionStrategy, Component, computed, input, output, signal} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatSelectModule} from '@angular/material/select';
import type {FamilyShape} from '../../models/machine-family.model';

/** One shape withheld from, or returned to, an admitted family. */
export interface ExclusionChange {
  machineType: string;
  excluded: boolean;
}

/**
 * The exceptions to a family's admission: the shapes it offers that this fleet
 * does not.
 *
 * Collapsed to a count, because the shape of the decision is that there are
 * usually none. Enabling a family admits everything under it; an exclusion is
 * the rare correction, and giving each of a hundred shapes its own switch would
 * make the common case look like a hundred decisions.
 */
@Component({
  selector: 'app-family-exclusions',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatButtonModule, MatFormFieldModule, MatIconModule, MatSelectModule],
  template: `
    <button
      type="button"
      class="disclosure"
      [attr.aria-expanded]="open()"
      (click)="open.set(!open())">
      <mat-icon class="chevron" [class.open]="open()">expand_more</mat-icon>
      <span>{{ summary() }}</span>
    </button>

    @if (open()) {
      <div class="body">
        @if (excluded().length === 0) {
          <p class="hint">
            Every shape this family offers is in the catalog. Exclude one when the fleet should
            never provision it.
          </p>
        } @else {
          <div class="chips">
            @for (shape of excluded(); track shape.machineType) {
              <span class="chip">
                <span class="mono">{{ shape.machineType }}</span>
                <button
                  type="button"
                  class="chip-remove"
                  [attr.aria-label]="'Return ' + shape.machineType + ' to the catalog'"
                  (click)="exclusionChange.emit({machineType: shape.machineType, excluded: false})">
                  <mat-icon>close</mat-icon>
                </button>
              </span>
            }
          </div>
        }

        @if (excludable().length > 0) {
          <mat-form-field appearance="outline" subscriptSizing="dynamic" class="picker">
            <mat-label>Exclude a shape</mat-label>
            <mat-select [value]="''" (valueChange)="exclude($event)">
              @for (shape of excludable(); track shape.machineType) {
                <mat-option [value]="shape.machineType">{{ shape.machineType }}</mat-option>
              }
            </mat-select>
          </mat-form-field>
        }
      </div>
    }
  `,
  styles: `
    :host {
      display: block;
    }

    .disclosure {
      display: flex;
      align-items: center;
      gap: var(--cco-space-4);
      padding: var(--cco-space-4) 0;
      background: none;
      border: none;
      cursor: pointer;
      font: inherit;
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
    }

    .chevron {
      transform: rotate(-90deg);
      transition: transform var(--cco-duration-fast) ease;
      font-size: var(--cco-icon-sm);
      width: var(--cco-icon-sm);
      height: var(--cco-icon-sm);
    }

    .chevron.open {
      transform: rotate(0deg);
    }

    .body {
      display: flex;
      flex-direction: column;
      gap: var(--cco-space-8);
      padding: var(--cco-space-8) 0 0 var(--cco-space-16);
    }

    .hint {
      margin: 0;
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-label);
    }

    .chips {
      display: flex;
      flex-wrap: wrap;
      gap: var(--cco-space-4);
    }

    .chip {
      display: inline-flex;
      align-items: center;
      gap: var(--cco-space-4);
      padding: 2px 4px 2px 10px;
      border-radius: var(--cco-radius-pill);
      background: var(--cco-surface-variant);
      font-size: var(--cco-text-label);
    }

    .mono {
      font-family: var(--cco-font-mono);
    }

    .chip-remove {
      display: inline-flex;
      align-items: center;
      border: none;
      background: none;
      padding: 0;
      cursor: pointer;
      color: var(--cco-text-secondary);
    }

    .chip-remove mat-icon {
      font-size: var(--cco-icon-sm);
      width: var(--cco-icon-sm);
      height: var(--cco-icon-sm);
    }

    .picker {
      max-width: 280px;
    }
  `,
})
export class FamilyExclusionsComponent {
  readonly shapes = input.required<FamilyShape[]>();
  readonly exclusionChange = output<ExclusionChange>();

  protected readonly open = signal(false);

  protected readonly excluded = computed(() => this.shapes().filter(shape => shape.isExcluded));

  /**
   * A shape below the floor is already withheld by the admission rule, and the
   * catalog default cannot be withheld at all, because the server refuses to
   * leave the fleet without one.
   */
  protected readonly excludable = computed(() =>
    this.shapes().filter(shape => shape.viable && !shape.isExcluded && !shape.isDefault)
  );

  protected readonly summary = computed(() => {
    const count = this.excluded().length;
    if (count === 0) {
      return 'No shapes excluded';
    }
    return count === 1 ? '1 shape excluded' : `${count} shapes excluded`;
  });

  protected exclude(machineType: string): void {
    if (machineType) {
      this.exclusionChange.emit({machineType, excluded: true});
    }
  }
}
