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

import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  inject,
  linkedSignal,
  resource,
  signal,
} from '@angular/core';
import {toSignal} from '@angular/core/rxjs-interop';
import type {FieldTree} from '@angular/forms/signals';
import {form, max, maxLength, min, pattern, required} from '@angular/forms/signals';
import {MatButtonModule} from '@angular/material/button';
import {MatIconModule} from '@angular/material/icon';
import {MatProgressBarModule} from '@angular/material/progress-bar';
import {MatStepperModule} from '@angular/material/stepper';
import {Router, RouterModule} from '@angular/router';
import {firstValueFrom} from 'rxjs';
import type {Build} from '../../../builds/models/build.model';
import {BuildStore} from '../../../builds/store/build.signal-store';
import {PageHeaderComponent, PageLayoutComponent} from '../../../shared/components';
import {COMPILED_LIFETIMES} from '../../../shared/models/platform-lifetimes.schema';
import {GceCatalogService} from '../../../shared/services/gce-catalog.service';
import {PlatformPolicyService} from '../../../shared/services/platform-policy.service';
import {PoolFactsService} from '../../../shared/services/pool-facts.service';
import {getErrorMessage} from '../../../shared/utils';
import {RESOURCE_NAME_PATTERN} from '../../../shared/utils/resource-name';
import {BatchApiService} from '../../services/batch-api.service';
import {BatchStore} from '../../store/batch.signal-store';
import type {BatchIntent} from './batch-intent.model';
import {
  defaultBatchIntent,
  MAX_BATCH_COUNT,
  MAX_NAME_PREFIX,
  toCreateBatchRequest,
} from './batch-intent.model';
import {BatchPlanStepComponent} from './batch-plan-step.component';
import type {ShapeDraftFields} from './batch-shape-step.component';
import {BatchShapeStepComponent} from './batch-shape-step.component';
import type {TemplateDraftFields} from './batch-template-step.component';
import {BatchTemplateStepComponent} from './batch-template-step.component';
import {BatchUsageStepComponent} from './batch-usage-step.component';

/**
 * The batch interview: Template → Usage → Shape → Plan. The shell owns the
 * draft, the shared form, and the dry run that fires when the Plan step
 * becomes active.
 *
 * Duration is not a step of its own the way it is for a device. A batch takes
 * no pin, no reason and no tags, so its one lifecycle field lives with the
 * rest of the fleet's shape.
 */
@Component({
  selector: 'app-batch-create-wizard',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    RouterModule,
    MatButtonModule,
    MatIconModule,
    MatProgressBarModule,
    MatStepperModule,
    PageLayoutComponent,
    PageHeaderComponent,
    BatchTemplateStepComponent,
    BatchUsageStepComponent,
    BatchShapeStepComponent,
    BatchPlanStepComponent,
  ],
  template: `
    <app-page-layout variant="list">
      <div page-header>
        <app-page-header
          title="Create Batch"
          subtitle="A fleet of devices created and torn down as one unit"
          [showBackButton]="true"
          (back)="goBack()" />
      </div>

      <div page-content>
        <div class="wizard-content">
          <div class="step-progress">
            <span class="step-indicator">Step {{ stepper.selectedIndex + 1 }} of 4</span>
            <mat-progress-bar aria-label="Wizard progress" mode="determinate" [value]="((stepper.selectedIndex + 1) / 4) * 100" />
          </div>

          <mat-stepper
            [linear]="true"
            #stepper
            (selectionChange)="onStepChange($event.selectedIndex)">
            <mat-step [completed]="templateValid()">
              <ng-template matStepLabel>
                <span class="step-label"><mat-icon>dataset</mat-icon> Template</span>
              </ng-template>
              <div class="step-form">
                <app-batch-template-step
                  [fields]="templateFields"
                  [intent]="intent()"
                  [builds]="builds()"
                  [buildsLoading]="buildsLoading()"
                  [defaultBuildName]="defaultBuildName()"
                  (buildKindChanged)="setBuildKind($event)"
                  (buildSelected)="selectBuild($event)" />
                <div class="step-actions">
                  <button type="button" mat-stroked-button routerLink="/batches">Cancel</button>
                  <button type="button"
                    mat-flat-button
                    matStepperNext
                    [disabled]="!templateValid()">
                    Next
                    <mat-icon>arrow_forward</mat-icon>
                  </button>
                </div>
              </div>
            </mat-step>

            <mat-step [completed]="furthestStep() >= 1">
              <ng-template matStepLabel>
                <span class="step-label"><mat-icon>touch_app</mat-icon> Usage</span>
              </ng-template>
              <div class="step-form">
                <app-batch-usage-step
                  [usage]="intent().usage"
                  [gpuRequired]="intent().gpuRequired"
                  [preferredRegion]="intent().preferredRegion"
                  [regions]="regions()"
                  [capacity]="intent().capacity"
                  [architecture]="intent().architecture"
                  [pool]="intent().pool"
                  [enabledPools]="enabledPools()"
                  [armImageReady]="armImageReady()"
                  (usageChanged)="patch({usage: $event})"
                  (gpuRequiredChanged)="patch({gpuRequired: $event})"
                  (preferredRegionChanged)="patch({preferredRegion: $event})"
                  (capacityChanged)="patch({capacity: $event})"
                  (architectureChanged)="patch({architecture: $event})"
                  (poolChanged)="patch({pool: $event})" />
                <div class="step-actions">
                  <button type="button" mat-stroked-button matStepperPrevious>Back</button>
                  <button type="button" mat-flat-button matStepperNext>
                    Next
                    <mat-icon>arrow_forward</mat-icon>
                  </button>
                </div>
              </div>
            </mat-step>

            <mat-step [completed]="furthestStep() >= 2 && shapeValid()">
              <ng-template matStepLabel>
                <span class="step-label"><mat-icon>tune</mat-icon> Shape</span>
              </ng-template>
              <div class="step-form">
                <app-batch-shape-step
                  [fields]="shapeFields"
                  [intent]="intent()"
                  (countPreset)="setCount($event)"
                  (ttlPreset)="patch({ttlHours: $event})"
                  (admissionChanged)="patch({admission: $event})"
                  (placementChanged)="patch({placement: $event})" />
                <div class="step-actions">
                  <button type="button" mat-stroked-button matStepperPrevious>Back</button>
                  <button type="button"
                    mat-flat-button
                    matStepperNext
                    [disabled]="!shapeValid()">
                    See the plan
                    <mat-icon>arrow_forward</mat-icon>
                  </button>
                </div>
              </div>
            </mat-step>

            <mat-step>
              <ng-template matStepLabel>
                <span class="step-label"><mat-icon>route</mat-icon> Plan</span>
              </ng-template>
              <div class="step-form">
                <app-batch-plan-step
                  [intent]="intent()"
                  [planning]="planning()"
                  [admission]="admission()"
                  [planError]="planError()" />
                <div class="step-actions">
                  <button type="button" mat-stroked-button matStepperPrevious>Back</button>
                  <button type="button"
                    mat-flat-button
                    (click)="createBatch()"
                    [disabled]="isCreating() || planning() || planError() !== null">
                    <mat-icon>rocket_launch</mat-icon>
                    {{ isCreating() ? 'Launching…' : launchLabel() }}
                  </button>
                </div>
              </div>
            </mat-step>
          </mat-stepper>
        </div>
      </div>
    </app-page-layout>
  `,
  styles: `
    .wizard-content {
      max-width: 900px;
      background: var(--cco-surface);
      border-radius: var(--cco-radius-sm);
      box-shadow: 0 1px 2px 0 rgba(0, 0, 0, 0.3), 0 2px 6px 2px rgba(0, 0, 0, 0.15);
    }

    .step-progress {
      padding: var(--cco-space-16) var(--cco-space-24);
      border-bottom: 1px solid var(--cco-border);
      background: var(--cco-surface-variant);
      border-radius: var(--cco-radius-sm) var(--cco-radius-sm) 0 0;
    }

    .step-indicator {
      display: block;
      font-size: var(--cco-text-sm);
      font-weight: 500;
      color: var(--cco-text-secondary);
      text-transform: uppercase;
      letter-spacing: 0.06em;
      margin-bottom: var(--cco-space-8);
    }

    .step-progress mat-progress-bar {
      height: 4px;
      border-radius: 2px;
    }

    .step-label {
      display: inline-flex;
      align-items: center;
      gap: var(--cco-space-8);
    }

    .step-label mat-icon {
      font-size: var(--cco-icon-md);
      width: var(--cco-icon-md);
      height: var(--cco-icon-md);
    }

    .step-form {
      padding: var(--cco-space-24);
    }

    .step-actions {
      display: flex;
      justify-content: flex-end;
      gap: var(--cco-space-12);
      margin-top: var(--cco-space-24);
      padding: var(--cco-space-16) 0;
      border-top: 1px solid var(--cco-border);
      position: sticky;
      bottom: 0;
      background: var(--cco-surface);
      z-index: 5;
    }

    .step-actions button mat-icon {
      margin-left: var(--cco-space-4);
    }
  `,
})
export class BatchCreateWizardComponent {
  private readonly store = inject(BatchStore);
  private readonly batchApi = inject(BatchApiService);
  private readonly buildStore = inject(BuildStore);
  private readonly gceCatalog = inject(GceCatalogService);
  private readonly router = inject(Router);
  private readonly lifetimes = inject(PlatformPolicyService).lifetimes;
  private readonly poolFacts = inject(PoolFactsService);

  protected readonly intent = signal<BatchIntent>(defaultBatchIntent());

  protected readonly buildsLoading = this.buildStore.loading;
  protected readonly builds = this.buildStore.availableBuildsForDevice;
  protected readonly defaultBuildName = computed(
    () => this.buildStore.defaultBuild()?.name ?? null
  );

  /** Regions with enabled catalog zones, offered as placement preferences. */
  protected readonly regions = toSignal(this.gceCatalog.getAvailableRegions(), {
    initialValue: [] as string[],
  });

  /** What this deployment can place into, for the Usage step's pool cluster. */
  protected readonly enabledPools = this.poolFacts.pools;
  protected readonly armImageReady = this.poolFacts.armImageReady;

  protected readonly form = form(this.intent, draft => {
    required(draft.namePrefix, {message: 'A name prefix is required'});
    pattern(draft.namePrefix, RESOURCE_NAME_PATTERN, {
      message: 'Must start with a lowercase letter; lowercase letters, numbers, and hyphens only',
    });
    maxLength(draft.namePrefix, MAX_NAME_PREFIX, {
      message: `At most ${MAX_NAME_PREFIX} characters (members add a numbered suffix)`,
    });
    min(draft.count, 1);
    max(draft.count, MAX_BATCH_COUNT);
    // No upper bound tied to count. The floor slider cannot exceed it, and
    // lowering count afterwards would otherwise raise an error against a
    // control the atomic policy may not even be showing. The serializer clamps.
    min(draft.minReady, 1);
    min(draft.readyTimeoutSeconds, 0);
    min(draft.ttlHours, 1);
    max(draft.ttlHours, COMPILED_LIFETIMES.maxDeviceTtlHours);
    required(draft.trackBranch, {
      message: 'Branch is required',
      when: ctx => ctx.valueOf(draft.buildKind) === 'track',
    });
    required(draft.trackTarget, {
      message: 'Target is required',
      when: ctx => ctx.valueOf(draft.buildKind) === 'track',
    });
  });

  /**
   * FieldTree is invariant in its model type; each step binds only the subset
   * BatchIntent structurally carries.
   */
  protected readonly templateFields = this.form as unknown as FieldTree<TemplateDraftFields>;
  protected readonly shapeFields = this.form as unknown as FieldTree<ShapeDraftFields>;

  protected readonly templateValid = computed(() => {
    const intent = this.intent();
    if (!this.form.namePrefix().valid()) {
      return false;
    }
    if (intent.buildKind === 'track') {
      return this.form.trackBranch().valid() && this.form.trackTarget().valid();
    }
    return intent.buildKind !== 'registered' || intent.buildId !== '';
  });

  protected readonly shapeValid = computed(
    () =>
      this.form.count().valid() &&
      this.form.minReady().valid() &&
      this.form.readyTimeoutSeconds().valid() &&
      this.form.ttlHours().valid()
  );

  /** Stepper position; the plan only runs while the last step is active. */
  protected readonly stepIndex = signal(0);

  /**
   * How far the interview has been taken. A step is "completed" to the stepper
   * both to unlock the one after it and to draw its header, and the two middle
   * steps are valid before they are ever opened, so declaring them complete
   * outright put the edit pencil on steps the user had not seen. This tracks
   * the high water mark rather than the current index, so going back does not
   * re-lock the headers ahead.
   */
  protected readonly furthestStep = signal(0);

  private readonly planRequest = computed(() => {
    if (this.stepIndex() !== 3 || !this.templateValid() || !this.shapeValid()) {
      return undefined;
    }
    return toCreateBatchRequest(this.intent());
  });

  private readonly plan = resource({
    params: () => this.planRequest(),
    loader: ({params}) => firstValueFrom(this.batchApi.planCreate(params)),
  });

  protected readonly planning = computed(() => this.plan.isLoading());
  protected readonly admission = computed(() => (this.plan.hasValue() ? this.plan.value() : null));
  protected readonly planError = computed(() => {
    const error = this.plan.error();
    return error ? getErrorMessage(error, 'This batch would be rejected') : null;
  });

  /** Names the size the plan actually admitted, which may be short of count. */
  protected readonly launchLabel = computed(() => {
    const size = this.admission()?.admitted ?? this.intent().count;
    return `Launch ${size} devices`;
  });

  /**
   * Retry-safety for the create. A repeat with the same key resumes the same
   * fleet instead of provisioning a second one. The backend fingerprints
   * the spec against the key and rejects a reuse with different content, so
   * the key is re-minted whenever the request body changes. Editing the form
   * therefore starts a new launch; retrying an unedited one resumes.
   */
  private readonly idempotencyKey = linkedSignal<string, string>({
    source: () => JSON.stringify(toCreateBatchRequest(this.intent())),
    computation: () => crypto.randomUUID(),
  });

  /**
   * Derived from the store so a failed create re-enables the button. The
   * component only unmounts on the success-navigate path.
   */
  private readonly submitted = signal(false);
  protected readonly isCreating = computed(() => this.submitted() && this.store.loading());

  constructor() {
    this.buildStore.loadAll({});

    // Adopt the operator's session default while the draft still carries the
    // compiled seed. Compared by value, because the preset chips patch the
    // intent directly, so a click must never be overwritten by a late fetch.
    effect(() => {
      const preset = this.lifetimes().deviceTtlHours;
      this.intent.update(i =>
        i.ttlHours === COMPILED_LIFETIMES.deviceTtlHours && i.ttlHours !== preset
          ? {...i, ttlHours: preset}
          : i
      );
    });
  }

  protected patch(change: Partial<BatchIntent>): void {
    this.intent.update(intent => ({...intent, ...change}));
  }

  protected setCount(count: number): void {
    this.patch({count});
  }

  protected setBuildKind(buildKind: BatchIntent['buildKind']): void {
    this.intent.update(intent => ({
      ...intent,
      buildKind,
      // Leaving the registered list drops the pick, so a stale id cannot ride
      // along into a request that no longer names it.
      buildId: buildKind === 'registered' ? intent.buildId : '',
    }));
  }

  protected selectBuild(build: Build): void {
    this.patch({buildId: build.id});
  }

  protected onStepChange(index: number): void {
    this.stepIndex.set(index);
    this.furthestStep.update(furthest => Math.max(furthest, index));
  }

  protected goBack(): void {
    void this.router.navigate(['/batches']);
  }

  protected createBatch(): void {
    if (this.isCreating()) {
      return;
    }
    this.submitted.set(true);
    this.store.create({
      request: toCreateBatchRequest(this.intent()),
      idempotencyKey: this.idempotencyKey(),
    });
  }
}
