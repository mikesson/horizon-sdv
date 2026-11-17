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
import {MatInputModule} from '@angular/material/input';
import {MatSelectModule} from '@angular/material/select';
import type {FamilyShape} from '../../models/machine-family.model';

/** One shape's capacity set or cleared by hand. */
export interface CapacityChange {
  machineType: string;
  /** Devices per host; 0 clears back to the computed fit. */
  maxDevices: number;
}

/** One shape as the picker offers it. */
interface CapacityChoice {
  machineType: string;
  label: string;
}

/**
 * The per-shape capacity overrides: the operator's word where the
 * cores-and-memory arithmetic is wrong for a workload.
 *
 * Beside the matrix rather than in it, the same split the exclusions follow:
 * the grid states what is, and the decisions an operator makes about a shape
 * each get their own control.
 *
 * Every shape the family holds is pickable, including the ones the catalog
 * withholds for holding no device. Those are what this control is most for,
 * since a stored capacity is what admits them at all.
 */
@Component({
  selector: 'app-family-capacity',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatButtonModule, MatFormFieldModule, MatIconModule, MatInputModule, MatSelectModule],
  template: `
    <h4>Capacity overrides</h4>
    @if (overridden().length === 0) {
      <p class="none">
        Every shape packs to its computed fit, from its cores and memory, whichever is tighter. Set
        a number here where the arithmetic is wrong for a workload; it is also what admits a shape
        the arithmetic holds no device on.
      </p>
    } @else {
      <ul class="rows">
        @for (shape of overridden(); track shape.machineType) {
          <li>
            <span class="mono">{{ shape.machineType }}</span>
            <span class="value">computed {{ shape.fit }} · set by hand {{ shape.maxDevicesOverride }}</span>
            <button
              type="button"
              mat-icon-button
              [attr.aria-label]="'Clear the capacity override on ' + shape.machineType"
              (click)="capacityChange.emit({machineType: shape.machineType, maxDevices: 0})">
              <mat-icon>close</mat-icon>
            </button>
          </li>
        }
      </ul>
    }
    <div class="editor">
      <mat-form-field appearance="outline" subscriptSizing="dynamic">
        <mat-label>Shape</mat-label>
        <mat-select [value]="picked()" (valueChange)="picked.set($event)">
          @for (choice of choices(); track choice.machineType) {
            <mat-option [value]="choice.machineType">{{ choice.label }}</mat-option>
          }
        </mat-select>
      </mat-form-field>
      <mat-form-field appearance="outline" subscriptSizing="dynamic" class="count">
        <mat-label>Devices</mat-label>
        <input
          matInput
          type="number"
          min="1"
          max="32"
          [value]="count()"
          (input)="count.set(number($event))" />
      </mat-form-field>
      <button
        type="button"
        mat-stroked-button
        [disabled]="!picked() || count() < 1"
        (click)="set()">
        Set
      </button>
    </div>
  `,
  styles: `
    :host {
      display: block;
    }

    h4 {
      margin: var(--cco-space-16) 0 var(--cco-space-4);
      font-size: var(--cco-text-body);
      font-weight: 600;
    }

    .none {
      margin: 0 0 var(--cco-space-8);
      max-width: 60ch;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .rows {
      margin: 0 0 var(--cco-space-8);
      padding: 0;
      list-style: none;
    }

    .rows li {
      display: flex;
      align-items: center;
      gap: var(--cco-space-8);
      font-size: var(--cco-text-label);
    }

    .mono {
      font-family: var(--cco-font-mono);
      color: var(--cco-text);
    }

    .value {
      color: var(--cco-text-secondary);
    }

    .editor {
      display: flex;
      align-items: center;
      gap: var(--cco-space-8);
    }

    mat-form-field {
      width: 16rem;
    }

    .count {
      width: 7rem;
    }
  `,
})
export class FamilyCapacityComponent {
  readonly shapes = input.required<FamilyShape[]>();
  readonly capacityChange = output<CapacityChange>();

  protected readonly picked = signal('');
  protected readonly count = signal(0);

  protected readonly overridden = computed(() =>
    this.shapes().filter(shape => shape.maxDevicesOverride > 0)
  );

  protected readonly choices = computed<CapacityChoice[]>(() =>
    this.shapes().map(shape => ({
      machineType: shape.machineType,
      label: shape.viable
        ? shape.machineType
        : `${shape.machineType} · holds none, set one to admit it`,
    }))
  );

  protected number(event: Event): number {
    return Number((event.target as HTMLInputElement).value);
  }

  protected set(): void {
    this.capacityChange.emit({machineType: this.picked(), maxDevices: this.count()});
    this.picked.set('');
    this.count.set(0);
  }
}
