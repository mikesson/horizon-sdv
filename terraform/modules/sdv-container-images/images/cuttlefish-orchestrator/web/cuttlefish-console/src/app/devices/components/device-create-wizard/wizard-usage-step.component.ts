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
import {MatButtonToggleModule} from '@angular/material/button-toggle';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatSelectModule} from '@angular/material/select';
import {MatSlideToggleModule} from '@angular/material/slide-toggle';
import {StepHeaderComponent} from '../../../shared/components/step-header/step-header.component';
import {poolFamily, poolPurpose} from '../../../shared/models/pool-copy';
import type {PoolPin} from '../../../shared/models/pool-placement';
import {POOL_NAMES, poolConsequence} from '../../../shared/models/pool-placement';
import type {
  CapacityModel,
  GuestArchitecture,
  Interaction,
} from '../../../shared/models/requirements.model';
import {CapacityFieldComponent} from '../capacity-field/capacity-field.component';

/**
 * Step 2 (Usage): how the device will be used. This single question replaces
 * the old gpu_mode radio. Interactive intent steers placement toward GPU
 * capacity, headless toward plain compute; rendering always matches the host.
 * With automatic placement it also takes the guest architecture and an optional
 * pool pin, an optional region preference (the geo term of zone ranking:
 * dominant for interactive, tie-break for headless) and a capacity choice (Spot
 * vs on-demand; Auto lets operator policy decide).
 *
 * The step never asks for the pool. It is derived from the answers above it
 * and shown, and the pin is there for the case where the derivation is not
 * what was wanted.
 */
@Component({
  selector: 'app-wizard-usage-step',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    CapacityFieldComponent,
    MatButtonToggleModule,
    MatFormFieldModule,
    MatIconModule,
    MatSelectModule,
    MatSlideToggleModule,
    StepHeaderComponent,
  ],
  template: `
    <app-step-header icon="touch_app" title="Usage" description="How will you use this device?" />

    <div class="usage-cards" role="radiogroup" aria-label="Device usage">
      <button
        type="button"
        class="usage-card"
        role="radio"
        [attr.aria-checked]="usage() === 'interactive'"
        [class.selected]="usage() === 'interactive'"
        (click)="usageChanged.emit('interactive')">
        <mat-icon>play_circle</mat-icon>
        <span class="usage-card__title">Interactive</span>
        <span class="usage-card__detail">
          View and control the device in the browser. Prefers a GPU host for
          smooth streaming; falls back to software rendering when none is
          available.
        </span>
      </button>

      <button
        type="button"
        class="usage-card"
        role="radio"
        [attr.aria-checked]="usage() === 'headless'"
        [class.selected]="usage() === 'headless'"
        (click)="usageChanged.emit('headless')">
        <mat-icon>terminal</mat-icon>
        <span class="usage-card__title">Headless</span>
        <span class="usage-card__detail">
          ADB, tests, and automation, with no streaming needed. Runs on plain
          compute capacity.
        </span>
      </button>
    </div>

    @if (usage() === 'interactive') {
      <mat-slide-toggle
        class="gpu-toggle"
        color="primary"
        [checked]="gpuRequired()"
        (change)="gpuRequiredChanged.emit($event.checked)">
        Require a GPU host
      </mat-slide-toggle>
      <p class="region-hint">
        Interactive placement prefers a GPU and silently falls back to software
        rendering when none is free. This turns that fallback off, so the create
        fails instead and you never get a device that streams badly.
      </p>
    }

    @if (showPlacement()) {
      <div class="placement">
        <h3 class="placement__title">Architecture</h3>
        <mat-button-toggle-group
          class="architecture-toggle"
          aria-label="Guest architecture"
          [value]="architecture()"
          (change)="architectureChanged.emit($event.value)">
          <mat-button-toggle value="x86">
            <mat-icon>memory</mat-icon>
            x86
          </mat-button-toggle>
          <mat-button-toggle value="arm" [disabled]="!armImageReady()">
            <mat-icon>developer_board</mat-icon>
            ARM
          </mat-button-toggle>
        </mat-button-toggle-group>
        <p class="region-hint">{{ architectureHint() }}</p>

        <mat-form-field appearance="outline" class="region-field">
          <mat-label>Fleet pool</mat-label>
          <mat-select [value]="pool()" (selectionChange)="poolChanged.emit($event.value)">
            <mat-option value="auto">Automatic (follows the answers above)</mat-option>
            @for (option of poolOptions; track option.pool) {
              <mat-option [value]="option.pool">{{ option.pool }} ({{ option.family }})</mat-option>
            }
          </mat-select>
        </mat-form-field>
        <p class="region-hint">
          Pinning forces the placement. The device lands in that pool or the
          create is refused. Automatic follows the answers above.
        </p>

        <p class="consequence" [class.consequence--refused]="consequence().refused">
          {{ consequence().line }}
        </p>
      </div>

      <div class="region-preference">
        <mat-form-field appearance="outline" class="region-field">
          <mat-label>Region preference (optional)</mat-label>
          <mat-select
            [value]="preferredRegion()"
            (selectionChange)="preferredRegionChanged.emit($event.value)">
            <mat-option value="">No preference (best capacity)</mat-option>
            @for (region of regions(); track region) {
              <mat-option [value]="region">{{ region }}</mat-option>
            }
          </mat-select>
        </mat-form-field>
        <p class="region-hint">
          A preference, never a constraint. The device still lands wherever
          capacity exists.
          @if (usage() === 'interactive') {
            Proximity matters most for interactive streaming latency.
          }
        </p>

        <app-capacity-field
          [capacity]="capacity()"
          (capacityChanged)="capacityChanged.emit($event)" />
      </div>
    }
  `,
  styles: `




    .usage-cards {
      display: grid;
      grid-template-columns: repeat(2, 1fr);
      gap: 16px;
    }

    .usage-card {
      display: flex;
      flex-direction: column;
      align-items: flex-start;
      gap: 8px;
      padding: 24px;
      background: var(--cco-surface);
      border: 2px solid var(--cco-border);
      border-radius: 8px;
      cursor: pointer;
      text-align: left;
      font: inherit;
      color: var(--cco-text);
      transition: border-color var(--cco-duration-fast) ease, background-color var(--cco-duration-fast) ease;
    }

    .usage-card:hover {
      border-color: var(--cco-accent);
    }

    .usage-card.selected {
      border-color: var(--cco-accent);
      background: var(--cco-accent-surface);
    }

    .usage-card mat-icon {
      color: var(--cco-accent);
      font-size: 32px;
      width: 28px;
      height: 28px;
    }

    .usage-card__title {
      font-size: 16px;
      font-weight: 500;
    }

    .usage-card__detail {
      font-size: 12px;
      color: var(--cco-text-secondary);
      line-height: 1.4;
    }

    .gpu-toggle {
      margin-top: var(--cco-space-24);
    }

    .placement,
    .region-preference {
      margin-top: 24px;
      padding-top: 16px;
      border-top: 1px solid var(--cco-border);
    }

    .placement__title {
      margin: 0 0 8px;
      font-size: 14px;
      font-weight: 500;
    }

    .architecture-toggle {
      margin-bottom: 4px;
    }

    .consequence {
      margin: 16px 0 0;
      padding: 12px;
      border-radius: 8px;
      background: var(--cco-surface-variant);
      font-size: 12px;
      line-height: 1.4;
      color: var(--cco-text-secondary);
      max-width: 60ch;
    }

    .consequence--refused {
      background: var(--cco-warning-surface);
      color: var(--cco-warning);
    }

    .region-field {
      width: 100%;
      max-width: 360px;
    }

    .region-hint {
      margin: 4px 0 0;
      font-size: 12px;
      color: var(--cco-text-secondary);
      line-height: 1.4;
    }

    @container page (max-width: 600px) {
      .usage-cards {
        grid-template-columns: 1fr;
      }
    }
  `,
})
export class WizardUsageStepComponent {
  readonly usage = input.required<Interaction>();
  /** '' = no preference (capacity-ranked). */
  readonly preferredRegion = input.required<string>();
  /** Regions with enabled catalog zones, the only meaningful preferences. */
  readonly regions = input.required<readonly string[]>();
  /** The placement answers are only sent for an automatically placed device. */
  readonly showPlacement = input.required<boolean>();
  readonly capacity = input.required<CapacityModel>();
  /** Hard GPU constraint; only meaningful for interactive intent. */
  readonly gpuRequired = input.required<boolean>();
  readonly architecture = input.required<GuestArchitecture>();
  readonly pool = input.required<PoolPin>();
  /** Pools this deployment offers; a pin outside them is refused server-side. */
  readonly enabledPools = input.required<readonly string[]>();
  readonly armImageReady = input.required<boolean>();
  readonly usageChanged = output<Interaction>();
  readonly gpuRequiredChanged = output<boolean>();
  readonly preferredRegionChanged = output<string>();
  readonly capacityChanged = output<CapacityModel>();
  readonly architectureChanged = output<GuestArchitecture>();
  readonly poolChanged = output<PoolPin>();

  protected readonly poolOptions = POOL_NAMES.map(pool => ({pool, family: poolFamily(pool)}));

  /** With no arm64 host image the hint to give is why the toggle is off. */
  protected readonly architectureHint = computed(() =>
    this.armImageReady()
      ? 'An arm64 guest runs on bare-metal arm64 hosts.'
      : poolPurpose('arm', false)
  );

  protected readonly consequence = computed(() =>
    poolConsequence(
      {pool: this.pool(), architecture: this.architecture(), gpuRequired: this.gpuRequired()},
      {pools: this.enabledPools(), armImageReady: this.armImageReady()}
    )
  );
}
