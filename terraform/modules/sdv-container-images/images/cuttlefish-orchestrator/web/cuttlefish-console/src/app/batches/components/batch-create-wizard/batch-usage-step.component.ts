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

/**
 * Step 2 (Usage): what the fleet is for, and the placement preferences that
 * follow from it.
 *
 * The region choice reads differently here than it does for a single device.
 * A batch's hosts are one equivalence class, so the zone is ranked once and
 * every member lands in it. A preference applies to the whole fleet, and so
 * does the pool. One derivation answers for the batch, and every member packs
 * onto that pool's machines.
 */
@Component({
  selector: 'app-batch-usage-step',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    MatButtonToggleModule,
    MatFormFieldModule,
    MatIconModule,
    MatSelectModule,
    MatSlideToggleModule,
    StepHeaderComponent,
  ],
  template: `
    <app-step-header icon="touch_app" title="Usage" description="What will this fleet be used for?" />

    <div class="usage-cards" role="radiogroup" aria-label="Batch usage">
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
          ADB, test sharding, automation. Runs on plain compute, which is the
          capacity a fleet this size can actually get.
        </span>
      </button>

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
          Every member is viewable and controllable in the browser. Prefers GPU
          hosts, so a large batch is likelier to be admitted short.
        </span>
      </button>
    </div>

    <mat-slide-toggle
      class="gpu-toggle"
      color="primary"
      [checked]="gpuRequired()"
      (change)="gpuRequiredChanged.emit($event.checked)">
      Require GPU hosts
    </mat-slide-toggle>
    <p class="hint">
      Makes the GPU a hard constraint. Admission fails outright rather than
      placing the batch on software rendering.
    </p>

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
      <p class="hint">{{ architectureHint() }}</p>

      <mat-form-field appearance="outline" class="preference-field">
        <mat-label>Fleet pool</mat-label>
        <mat-select [value]="pool()" (selectionChange)="poolChanged.emit($event.value)">
          <mat-option value="auto">Automatic (follows the answers above)</mat-option>
          @for (option of poolOptions; track option.pool) {
            <mat-option [value]="option.pool">{{ option.pool }} ({{ option.family }})</mat-option>
          }
        </mat-select>
      </mat-form-field>
      <p class="hint">
        Pinning forces the placement. The whole fleet lands in that pool or the
        batch is refused. Automatic follows the answers above.
      </p>

      <p class="consequence" [class.consequence--refused]="consequence().refused">
        {{ consequence().line }}
      </p>
    </div>

    <div class="placement-preferences">
      <mat-form-field appearance="outline" class="preference-field">
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
      <p class="hint">
        The whole batch is placed in one zone, ranked once. A preference steers
        that single choice; it never splits the fleet across regions.
      </p>

      <mat-form-field appearance="outline" class="preference-field">
        <mat-label>Capacity</mat-label>
        <mat-select [value]="capacity()" (selectionChange)="capacityChanged.emit($event.value)">
          <mat-option value="auto">Auto (platform policy decides)</mat-option>
          <mat-option value="spot">Spot (discounted, may be preempted)</mat-option>
          <mat-option value="standard">Standard (on-demand)</mat-option>
        </mat-select>
      </mat-form-field>
      <p class="hint">
        Spot costs a fraction of on-demand, and Google Cloud can reclaim any of
        the batch's hosts at any time; the platform re-provisions them and
        recreates their devices.
      </p>
    </div>
  `,
  styles: `
    .usage-cards {
      display: grid;
      grid-template-columns: repeat(2, 1fr);
      gap: var(--cco-space-16);
    }

    .usage-card {
      display: flex;
      flex-direction: column;
      align-items: flex-start;
      gap: var(--cco-space-8);
      padding: var(--cco-space-24);
      background: var(--cco-surface);
      border: 2px solid var(--cco-border);
      border-radius: var(--cco-radius-sm);
      cursor: pointer;
      text-align: left;
      font: inherit;
      color: var(--cco-text);
      transition: border-color var(--cco-duration-fast) ease,
        background-color var(--cco-duration-fast) ease;
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
      font-size: var(--cco-icon-2xl);
      width: var(--cco-icon-2xl);
      height: var(--cco-icon-2xl);
    }

    .usage-card__title {
      font-size: var(--cco-text-title);
      font-weight: 500;
    }

    .usage-card__detail {
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
      line-height: 1.4;
    }

    .gpu-toggle {
      margin-top: var(--cco-space-24);
    }

    .hint {
      margin: var(--cco-space-4) 0 0;
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
      line-height: 1.4;
      max-width: 60ch;
    }

    .placement,
    .placement-preferences {
      margin-top: var(--cco-space-24);
      padding-top: var(--cco-space-16);
      border-top: 1px solid var(--cco-border);
    }

    .placement__title {
      margin: 0 0 var(--cco-space-8);
      font-size: var(--cco-text-body);
      font-weight: 500;
    }

    .architecture-toggle {
      margin-bottom: var(--cco-space-4);
    }

    .consequence {
      margin: var(--cco-space-16) 0 0;
      padding: var(--cco-space-12);
      border-radius: var(--cco-radius-sm);
      background: var(--cco-surface-variant);
      font-size: var(--cco-text-sm);
      line-height: 1.4;
      color: var(--cco-text-secondary);
      max-width: 60ch;
    }

    .consequence--refused {
      background: var(--cco-warning-surface);
      color: var(--cco-warning);
    }

    .preference-field {
      width: 100%;
      max-width: 360px;
    }

    @container page (max-width: 600px) {
      .usage-cards {
        grid-template-columns: 1fr;
      }
    }
  `,
})
export class BatchUsageStepComponent {
  readonly usage = input.required<Interaction>();
  readonly gpuRequired = input.required<boolean>();
  /** '' = no preference (capacity-ranked). */
  readonly preferredRegion = input.required<string>();
  /** Regions with enabled catalog zones, the only meaningful preferences. */
  readonly regions = input.required<readonly string[]>();
  readonly capacity = input.required<CapacityModel>();
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
