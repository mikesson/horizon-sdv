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

import {ChangeDetectionStrategy, Component, computed, inject, input, output} from '@angular/core';
import type {FieldTree} from '@angular/forms/signals';
import {FormField} from '@angular/forms/signals';
import {MatButtonModule} from '@angular/material/button';
import {MatButtonToggleModule} from '@angular/material/button-toggle';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatInputModule} from '@angular/material/input';
import {MatSliderModule} from '@angular/material/slider';
import {StepHeaderComponent} from '../../../shared/components/step-header/step-header.component';
import {PlatformPolicyService} from '../../../shared/services/platform-policy.service';
import type {BatchPlacement} from '../../models/batch.model';
import type {AdmissionPolicy, BatchIntent} from './batch-intent.model';
import {effectiveMinReady, MAX_BATCH_COUNT} from './batch-intent.model';

/** The subset of BatchIntent this step binds. */
export interface ShapeDraftFields {
  count: number;
  minReady: number;
  readyTimeoutSeconds: number;
  ttlHours: number;
}

const COUNT_PRESETS = [5, 10, 25, 50] as const;

/**
 * Step 3 (Shape): how many devices, how strictly they must all arrive, where
 * they sit, and for how long.
 *
 * The fill policy governs which of the two tolerance controls exists at all.
 * The backend rejects min_ready below count under atomic, and a ready timeout
 * outside it, so instead of validating those combinations the step does not
 * offer them.
 */
@Component({
  selector: 'app-batch-shape-step',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    FormField,
    MatButtonModule,
    MatButtonToggleModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
    MatSliderModule,
    StepHeaderComponent,
  ],
  template: `
    <app-step-header icon="tune" title="Shape" description="How many devices, and how strictly" />

    <h3 class="section-title">Devices</h3>
    <div class="presets">
      @for (preset of countPresets; track preset) {
        <button
          mat-stroked-button
          type="button"
          [class.selected]="intent().count === preset"
          (click)="countPreset.emit(preset)">
          {{ preset }}
        </button>
      }
    </div>
    <mat-form-field appearance="outline" class="number-field" subscriptSizing="dynamic">
      <mat-label>Device count</mat-label>
      <input matInput type="number" [formField]="fields().count">
      <mat-hint>1 to {{ maxCount }}</mat-hint>
      @for (error of fields().count().errors(); track error.kind) {
        <mat-error>{{ error.message }}</mat-error>
      }
    </mat-form-field>

    <h3 class="section-title">How much of the fleet must arrive</h3>
    <mat-button-toggle-group
      class="policy-toggle"
      [value]="intent().admission"
      (change)="admissionChanged.emit($event.value)">
      <mat-button-toggle value="atomic">
        <mat-icon>all_inclusive</mat-icon>
        Every device, or none
      </mat-button-toggle>
      <mat-button-toggle value="all">
        <mat-icon>done_all</mat-icon>
        Every device to start
      </mat-button-toggle>
      <mat-button-toggle value="partial">
        <mat-icon>done</mat-icon>
        At least some
      </mat-button-toggle>
    </mat-button-toggle-group>

    @switch (intent().admission) {
      @case ('atomic') {
        <p class="hint">
          All {{ intent().count }} must be admitted, and the batch tears itself
          down if any member later fails for good.
        </p>
        <mat-form-field appearance="outline" class="number-field" subscriptSizing="dynamic">
          <mat-label>Ready deadline (seconds)</mat-label>
          <input matInput type="number" [formField]="fields().readyTimeoutSeconds">
          <mat-hint>0 = wait indefinitely</mat-hint>
          @for (error of fields().readyTimeoutSeconds().errors(); track error.kind) {
            <mat-error>{{ error.message }}</mat-error>
          }
        </mat-form-field>
        <p class="hint">
          With a deadline set, the batch also tears down unless every member is
          running within that window.
        </p>
      }
      @case ('all') {
        <p class="hint">
          All {{ intent().count }} must be admitted or nothing is provisioned.
          Once running, a member that dies later does not take the batch
          with it.
        </p>
      }
      @case ('partial') {
        <div class="slider-row">
          <span class="slider-bound">1</span>
          <mat-slider
            [min]="1"
            [max]="intent().count"
            [step]="1"
            [discrete]="true"
            class="floor-slider">
            <input matSliderThumb [formField]="fields().minReady">
          </mat-slider>
          <span class="slider-bound">{{ intent().count }}</span>
        </div>
        <p class="hint">{{ floorHint() }}</p>
      }
      @default {}
    }

    <h3 class="section-title">Placement</h3>
    <mat-button-toggle-group
      [value]="intent().placement"
      (change)="placementChanged.emit($event.value)">
      <mat-button-toggle value="isolated">
        <mat-icon>lock</mat-icon>
        Dedicated hosts
      </mat-button-toggle>
      <mat-button-toggle value="pack">
        <mat-icon>view_module</mat-icon>
        Pack existing capacity
      </mat-button-toggle>
    </mat-button-toggle-group>
    <p class="hint">
      @if (intent().placement === 'isolated') {
        New hosts, fenced from other placement. The batch never shares a host
        with anything else, and nothing else lands on it afterwards.
      } @else {
        Free slots on existing shareable hosts are filled first, and only the
        remainder provisions new hosts. Cheaper and faster; noisier neighbours.
      }
    </p>

    <h3 class="section-title">Session length</h3>
    <div class="presets">
      @for (preset of ttlPresets(); track preset.hours) {
        <button
          mat-stroked-button
          type="button"
          [class.selected]="intent().ttlHours === preset.hours"
          (click)="ttlPreset.emit(preset.hours)">
          {{ preset.label }}
        </button>
      }
    </div>
    <mat-form-field appearance="outline" class="number-field" subscriptSizing="dynamic">
      <mat-label>Hours</mat-label>
      <input matInput type="number" [formField]="fields().ttlHours">
      <mat-hint>Applies to every member; the batch is reaped with them</mat-hint>
      @for (error of fields().ttlHours().errors(); track error.kind) {
        <mat-error>{{ error.message }}</mat-error>
      }
    </mat-form-field>
  `,
  styles: `
    .policy-toggle {
      flex-wrap: wrap;
    }

    .section-title {
      margin: var(--cco-space-24) 0 var(--cco-space-12);
      font-size: var(--cco-text-body);
      font-weight: 500;
    }

    .section-title:first-of-type {
      margin-top: 0;
    }

    .presets {
      display: flex;
      flex-wrap: wrap;
      gap: var(--cco-space-8);
      margin-bottom: var(--cco-space-12);
    }

    .presets button.selected {
      border-color: var(--cco-accent);
      color: var(--cco-accent);
    }

    .number-field {
      width: 100%;
      max-width: 240px;
    }

    .hint {
      margin: var(--cco-space-8) 0 0;
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
      line-height: 1.4;
      max-width: 60ch;
    }

    .slider-row {
      display: flex;
      align-items: center;
      gap: var(--cco-space-12);
      margin-top: var(--cco-space-8);
      max-width: 420px;
    }

    .slider-bound {
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
    }

    .floor-slider {
      flex: 1;
    }
  `,
})
export class BatchShapeStepComponent {
  private readonly lifetimes = inject(PlatformPolicyService).lifetimes;

  readonly fields = input.required<FieldTree<ShapeDraftFields>>();
  readonly intent = input.required<BatchIntent>();

  readonly countPreset = output<number>();
  readonly ttlPreset = output<number>();
  readonly admissionChanged = output<AdmissionPolicy>();
  readonly placementChanged = output<BatchPlacement>();

  protected readonly countPresets = COUNT_PRESETS;
  protected readonly maxCount = MAX_BATCH_COUNT;

  /** Half, default, double (capped), with the platform default labelled. */
  protected readonly ttlPresets = computed<ReadonlyArray<{hours: number; label: string}>>(() => {
    const policy = this.lifetimes();
    const preset = policy.deviceTtlHours;
    const hours = [
      ...new Set([
        Math.max(1, Math.ceil(preset / 2)),
        preset,
        Math.min(policy.maxDeviceTtlHours, preset * 2),
      ]),
    ];
    return hours.map(h => ({
      hours: h,
      label: h === preset ? `${h} hours (default)` : `${h} hours`,
    }));
  });

  protected readonly floorHint = computed(() => {
    const intent = this.intent();
    const floor = effectiveMinReady(intent);
    return (
      `The batch starts at whatever host quota allows, down to ${floor} of ` +
      `${intent.count}. Below that the create is rejected and nothing is provisioned.`
    );
  });
}
