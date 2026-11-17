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

import {ChangeDetectionStrategy, Component, computed, input, output} from '@angular/core';
import type {FieldTree} from '@angular/forms/signals';
import {FormField} from '@angular/forms/signals';
import {MatButtonModule} from '@angular/material/button';
import {MatCheckboxModule} from '@angular/material/checkbox';
import type {MatChipInputEvent} from '@angular/material/chips';
import {MatChipsModule} from '@angular/material/chips';
import {MatDividerModule} from '@angular/material/divider';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatInputModule} from '@angular/material/input';
import {MatSelectModule} from '@angular/material/select';
import {MatSliderModule} from '@angular/material/slider';
import {StepHeaderComponent} from '../../../shared/components/step-header/step-header.component';
import type {PlatformLifetimes} from '../../../shared/models/platform-lifetimes.schema';
import type {PinReason} from '../../models/device.model';

interface PinReasonOption {
  readonly value: string;
  readonly label: string;
}

/**
 * The lifecycle slice of a create draft. The step binds only these fields,
 * so any model carrying them (interview intent, advanced draft) can host it.
 */
export interface LifecycleDraftFields {
  ttlHours: number;
  pinned: boolean;
  pinnedReason: PinReason | '';
  pinnedReasonDetail: string;
  /** ISO local datetime; '' pins indefinitely. */
  pinnedUntil: string;
  tags: string[];
}

/** Step 3 body: session TTL, optional pinning with a reason, and tags. */
@Component({
  selector: 'app-wizard-lifecycle-step',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    StepHeaderComponent,
    FormField,
    MatButtonModule,
    MatSliderModule,
    MatFormFieldModule,
    MatInputModule,
    MatCheckboxModule,
    MatSelectModule,
    MatChipsModule,
    MatIconModule,
    MatDividerModule,
  ],
  template: `
    <app-step-header icon="schedule" title="Set Lifecycle" description="Configure session duration and persistence" />

    <div class="lifecycle-section">
      <h3>Session Duration</h3>
      <p class="section-hint">How long should this device stay running?</p>

      <div class="ttl-presets-chips">
        @for (preset of presets(); track preset.hours) {
          <button
            mat-stroked-button
            type="button"
            [class.selected]="draft().ttlHours === preset.hours"
            (click)="ttlPreset.emit(preset.hours)">
            {{ preset.label }}
          </button>
        }
      </div>

      <div class="ttl-control">
        <div class="ttl-slider-wrapper">
          <span class="slider-min">1h</span>
          <mat-slider
            [min]="1"
            [max]="maxTtlHours()"
            [step]="1"
            [discrete]="true"
            class="ttl-slider">
            <input matSliderThumb [formField]="fields().ttlHours">
          </mat-slider>
          <span class="slider-max">{{ maxTtlHours() }}h</span>
        </div>
        <mat-form-field appearance="outline" class="ttl-input" subscriptSizing="dynamic">
          <input matInput type="number" [formField]="fields().ttlHours">
          <span matSuffix>hrs</span>
        </mat-form-field>
      </div>

      <p class="extend-note">{{ extendNote() }}</p>
    </div>

    <mat-divider />

    <div class="lifecycle-section">
      <mat-checkbox [formField]="fields().pinned" class="pin-checkbox">
        <div class="pin-option">
          <mat-icon class="pin-icon">push_pin</mat-icon>
          <div class="pin-info">
            <span class="pin-title">Pin this device</span>
            <span class="pin-detail">Prevent auto-termination</span>
          </div>
        </div>
      </mat-checkbox>

      @if (draft().pinned) {
        <div class="pin-config">
          <mat-form-field appearance="outline" class="full-width" subscriptSizing="dynamic">
            <mat-label>Reason for Pinning</mat-label>
            <mat-select [formField]="fields().pinnedReason">
              @for (reason of pinReasons(); track reason.value) {
                <mat-option [value]="reason.value">
                  {{ reason.label }}
                </mat-option>
              }
            </mat-select>
          </mat-form-field>
          @if (draft().pinnedReason === 'other') {
            <mat-form-field appearance="outline" class="full-width" subscriptSizing="dynamic">
              <mat-label>Please specify</mat-label>
              <textarea matInput [formField]="fields().pinnedReasonDetail" rows="2"></textarea>
            </mat-form-field>
          }
          <mat-form-field appearance="outline" class="full-width" subscriptSizing="dynamic">
            <mat-label>Unpin automatically at (optional)</mat-label>
            <input matInput type="datetime-local" [formField]="fields().pinnedUntil">
            <mat-hint>
              Leave empty to pin indefinitely. At this time the device returns to
              normal TTL reaping rather than being deleted.
            </mat-hint>
            @for (error of fields().pinnedUntil().errors(); track error.kind) {
              <mat-error>{{ error.message }}</mat-error>
            }
          </mat-form-field>
        </div>
      }
    </div>

    <mat-divider />

    <div class="lifecycle-section">
      <h3>Tags (Optional)</h3>
      <mat-form-field appearance="outline" class="full-width" subscriptSizing="dynamic">
        <mat-label>Add tags</mat-label>
        <mat-chip-grid #chipGrid>
          @for (tag of draft().tags; track tag) {
            <mat-chip-row (removed)="tagRemoved.emit(tag)">
              {{ tag }}
              <mat-icon matChipRemove>cancel</mat-icon>
            </mat-chip-row>
          }
        </mat-chip-grid>
        <input
          matInput
          [matChipInputFor]="chipGrid"
          (matChipInputTokenEnd)="tagAdded.emit($event)"
          placeholder="Type and press Enter..."
          >
      </mat-form-field>
    </div>
  `,
  styles: `




    .lifecycle-section {
      margin: 24px 0;
    }

    .lifecycle-section h3 {
      margin: 0 0 16px;
      font-size: 16px;
      font-weight: 500;
      color: var(--cco-text);
    }

    .section-hint {
      color: var(--cco-text-secondary);
      font-size: 12px;
      margin: -8px 0 16px;
    }

    .ttl-presets-chips {
      display: flex;
      gap: 8px;
      margin-bottom: 16px;
    }

    .ttl-presets-chips button {
      border-radius: 20px;
    }

    .ttl-presets-chips button.selected {
      background: var(--cco-accent-surface);
      border-color: var(--cco-accent);
      color: var(--cco-accent);
    }

    .ttl-control {
      display: flex;
      align-items: center;
      gap: 16px;
    }

    .ttl-slider-wrapper {
      flex: 1;
      display: flex;
      align-items: center;
      gap: 8px;
    }

    .ttl-slider-wrapper .slider-min,
    .ttl-slider-wrapper .slider-max {
      font-size: 12px;
      color: var(--cco-text-secondary);
      min-width: 24px;
    }

    .ttl-slider {
      flex: 1;
    }

    .ttl-input {
      width: 90px;
    }

    .ttl-input input {
      text-align: center;
    }

    .extend-note {
      margin: 12px 0 0;
      font-size: 12px;
      color: var(--cco-text-secondary);
    }

    .pin-checkbox {
      margin-top: 16px;
    }

    .pin-option {
      display: flex;
      align-items: center;
      gap: 12px;
    }

    .pin-icon {
      color: var(--cco-accent);
      transform: rotate(45deg);
    }

    .pin-info {
      display: flex;
      flex-direction: column;
    }

    .pin-title {
      font-weight: 500;
      color: var(--cco-text);
    }

    .pin-detail {
      font-size: 12px;
      color: var(--cco-text-secondary);
    }

    .pin-config {
      margin-top: 16px;
      padding: 16px;
      background: var(--cco-surface-variant);
      border-radius: 8px;
    }

    .full-width {
      width: 100%;
    }
  `,
})
export class WizardLifecycleStepComponent {
  readonly fields = input.required<FieldTree<LifecycleDraftFields>>();
  readonly draft = input.required<LifecycleDraftFields>();
  readonly pinReasons = input.required<readonly PinReasonOption[]>();

  /** Platform lifetime policy: the preset ladder, the cap, the extend note. */
  readonly lifetimes = input.required<PlatformLifetimes>();

  readonly ttlPreset = output<number>();
  readonly tagAdded = output<MatChipInputEvent>();
  readonly tagRemoved = output<string>();

  protected readonly maxTtlHours = computed(() => this.lifetimes().maxDeviceTtlHours);

  /**
   * Half, default, double (capped), with the platform default labelled, so
   * the operator's policy is visible exactly where the choice is made.
   */
  protected readonly presets = computed<ReadonlyArray<{hours: number; label: string}>>(() => {
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

  /** What extensions this session can earn later. Policy, stated up front. */
  protected readonly extendNote = computed(() => {
    const policy = this.lifetimes();
    if (policy.extendCount <= 0) {
      return 'No extensions once created. Pin the device if you need it longer.';
    }
    const total = this.draft().ttlHours + policy.extendCount * policy.extendHours;
    return (
      `Extendable ${policy.extendCount} × ${policy.extendHours} h while running, ` +
      `so this session can reach ${total} h.`
    );
  });
}
