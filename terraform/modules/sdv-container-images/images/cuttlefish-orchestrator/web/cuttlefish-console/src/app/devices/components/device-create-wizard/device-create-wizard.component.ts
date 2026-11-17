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
  input,
  linkedSignal,
  model,
  output,
  resource,
  signal,
} from '@angular/core';
import {toSignal} from '@angular/core/rxjs-interop';
import type {FieldTree} from '@angular/forms/signals';
import {apply, form, max, min, required, validate} from '@angular/forms/signals';
import {MatButtonModule} from '@angular/material/button';
import type {MatChipInputEvent} from '@angular/material/chips';
import {MatDialog} from '@angular/material/dialog';
import {MatIconModule} from '@angular/material/icon';
import {MatProgressBarModule} from '@angular/material/progress-bar';
import {MatStepperModule} from '@angular/material/stepper';
import {RouterModule} from '@angular/router';
import {firstValueFrom} from 'rxjs';
import {QuickStartDialogComponent} from '../../../builds/components/quick-start-dialog/quick-start-dialog.component';
import type {Build} from '../../../builds/models/build.model';
import {BuildStore} from '../../../builds/store/build.signal-store';
import type {Host} from '../../../hosts/models/host.model';
import {HostStore} from '../../../hosts/store/host.signal-store';
import {COMPILED_LIFETIMES} from '../../../shared/models/platform-lifetimes.schema';
import {GceCatalogService} from '../../../shared/services/gce-catalog.service';
import {PlatformPolicyService} from '../../../shared/services/platform-policy.service';
import {PoolFactsService} from '../../../shared/services/pool-facts.service';
import {getErrorMessage} from '../../../shared/utils';
import {PIN_REASON_LABELS} from '../../models/device.model';
import {DeviceApiService} from '../../services/device-api.service';
import {DeviceStore} from '../../store/device.signal-store';
import type {CreateIntent} from '../device-create-page/create-intent.model';
import {deviceNameSchema, toCreateDeviceRequest} from '../device-create-page/create-intent.model';
import {clearIntentDraft} from '../device-create-page/create-intent.storage';
import {WizardAndroidStepComponent} from './wizard-android-step.component';
import type {LifecycleDraftFields} from './wizard-lifecycle-step.component';
import {WizardLifecycleStepComponent} from './wizard-lifecycle-step.component';
import {WizardPlanStepComponent} from './wizard-plan-step.component';
import {WizardUsageStepComponent} from './wizard-usage-step.component';

/**
 * The interview wizard: Android → Usage → Duration → Plan. The intent is
 * owned by the container (survives the move into Advanced); this shell owns the
 * stepper, the shared form, and the server-resolved plan (an AIP-163
 * validate_only dry run that fires when the Plan step becomes active).
 */
@Component({
  selector: 'app-device-create-wizard',
  imports: [
    RouterModule,
    MatStepperModule,
    MatButtonModule,
    MatIconModule,
    MatProgressBarModule,
    WizardAndroidStepComponent,
    WizardUsageStepComponent,
    WizardLifecycleStepComponent,
    WizardPlanStepComponent,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="wizard-content">
      <div class="step-progress">
        <span class="step-indicator">Step {{ stepper.selectedIndex + 1 }} of 4</span>
        <mat-progress-bar aria-label="Wizard progress"
          mode="determinate"
          [value]="((stepper.selectedIndex + 1) / 4) * 100" />
      </div>

      <mat-stepper
        [linear]="true"
        #stepper
        (selectionChange)="onStepChange($event.selectedIndex)">
        <mat-step [completed]="step1Valid()">
          <ng-template matStepLabel>
            <span class="step-label"><mat-icon>android</mat-icon> Android</span>
          </ng-template>
          <div class="step-form">
            <app-wizard-android-step
              [fields]="form"
              [intent]="intent()"
              [builds]="builds()"
              [buildsLoading]="buildsLoading()"
              [defaultBuild]="defaultBuild()"
              [selectedBuild]="selectedBuild()"
              [availableHosts]="availableHosts()"
              [hostsLoading]="hostsLoading()"
              (buildSelected)="selectBuild($event)"
              (defaultBuildChosen)="useDefaultBuild()"
              (buildModeChanged)="setBuildMode($event)"
              (hostSelected)="selectHost($event)"
              (hostKindChanged)="setHostKind($event)"
              (quickStartRequested)="openQuickStart()" />
            <div class="step-actions">
              <button type="button" mat-stroked-button routerLink="/devices">Cancel</button>
              <button type="button" mat-flat-button matStepperNext [disabled]="!step1Valid()">
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
            <app-wizard-usage-step
              [usage]="intent().usage"
              [preferredRegion]="intent().preferredRegion"
              [regions]="regions()"
              [showPlacement]="intent().host.kind === 'auto'"
              [capacity]="intent().capacity"
              [gpuRequired]="intent().gpuRequired"
              [architecture]="intent().architecture"
              [pool]="intent().pool"
              [enabledPools]="enabledPools()"
              [armImageReady]="armImageReady()"
              (usageChanged)="setUsage($event)"
              (gpuRequiredChanged)="setGpuRequired($event)"
              (preferredRegionChanged)="setPreferredRegion($event)"
              (capacityChanged)="setCapacity($event)"
              (architectureChanged)="setArchitecture($event)"
              (poolChanged)="setPool($event)" />
            <div class="step-actions">
              <button type="button" mat-stroked-button matStepperPrevious>Back</button>
              <button type="button" mat-flat-button matStepperNext>
                Next
                <mat-icon>arrow_forward</mat-icon>
              </button>
            </div>
          </div>
        </mat-step>

        <mat-step [completed]="furthestStep() >= 2 && step3Valid()">
          <ng-template matStepLabel>
            <span class="step-label"><mat-icon>schedule</mat-icon> Duration</span>
          </ng-template>
          <div class="step-form">
            <app-wizard-lifecycle-step
              [fields]="lifecycleFields"
              [draft]="intent()"
              [pinReasons]="pinReasons"
              [lifetimes]="lifetimes()"
              (ttlPreset)="setTtl($event)"
              (tagAdded)="addTag($event)"
              (tagRemoved)="removeTag($event)" />
            <div class="step-actions">
              <button type="button" mat-stroked-button matStepperPrevious>Back</button>
              <button type="button" mat-flat-button matStepperNext [disabled]="!step3Valid()">
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
            <app-wizard-plan-step
              [intent]="intent()"
              [planning]="planning()"
              [resolution]="planResolution()"
              [planError]="planError()"
              [selectedBuild]="effectiveBuild()"
              [selectedHost]="selectedHost()"
              [customizable]="customizable()"
              (customize)="customize.emit()" />
            <div class="step-actions">
              <button type="button" mat-stroked-button matStepperPrevious>Back</button>
              <button type="button"
                mat-flat-button
                (click)="createDevice()"
                [disabled]="isCreating() || planning() || planError() !== null"
                >
                <mat-icon>rocket_launch</mat-icon>
                {{ isCreating() ? 'Launching...' : 'Launch Device' }}
              </button>
            </div>
          </div>
        </mat-step>
      </mat-stepper>
    </div>
  `,
  styles: `
    .wizard-content {
      max-width: 900px;
      background: var(--cco-surface);
      border-radius: 8px;
      box-shadow: 0 1px 2px 0 rgba(0, 0, 0, 0.3), 0 2px 6px 2px rgba(0, 0, 0, 0.15);
    }

    .step-progress {
      padding: 16px 24px;
      border-bottom: 1px solid var(--cco-border);
      background: var(--cco-surface-variant);
      border-radius: 8px 8px 0 0;
    }

    .step-indicator {
      display: block;
      font-size: 12px;
      font-weight: 500;
      color: var(--cco-text-secondary);
      text-transform: uppercase;
      letter-spacing: 0.5px;
      margin-bottom: 8px;
    }

    .step-progress mat-progress-bar {
      height: 4px;
      border-radius: 2px;
    }

    .step-label {
      display: inline-flex;
      align-items: center;
      gap: 8px;
    }

    .step-label mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
    }

    .step-form {
      padding: 24px;
    }

    .step-actions {
      display: flex;
      justify-content: flex-end;
      gap: 12px;
      margin-top: 24px;
      padding: 16px 0;
      border-top: 1px solid var(--cco-border);
      position: sticky;
      bottom: 0;
      background: var(--cco-surface);
      z-index: 5;
    }

    .step-actions button mat-icon {
      margin-left: 4px;
    }
  `,
})
export class DeviceCreateWizardComponent {
  private readonly deviceStore = inject(DeviceStore);
  private readonly deviceApi = inject(DeviceApiService);
  private readonly hostStore = inject(HostStore);
  private readonly buildStore = inject(BuildStore);
  private readonly gceCatalog = inject(GceCatalogService);
  private readonly dialog = inject(MatDialog);
  /** Platform lifetime policy: presets, bounds, and the TTL the draft seeds. */
  protected readonly lifetimes = inject(PlatformPolicyService).lifetimes;
  private readonly poolFacts = inject(PoolFactsService);

  /** The create intent, owned by the container so the move into Advanced keeps it. */
  readonly intent = model.required<CreateIntent>();

  /** Whether the Plan step offers the eject into Advanced mode. */
  readonly customizable = input(false);

  /** Asks the container for a seeded eject into Advanced mode. */
  readonly customize = output<void>();

  protected readonly hostsLoading = this.hostStore.loading;
  protected readonly buildsLoading = this.buildStore.loading;
  protected readonly builds = this.buildStore.availableBuildsForDevice;
  protected readonly defaultBuild = computed(() => this.buildStore.defaultBuild() ?? null);
  protected readonly availableHosts = this.hostStore.availableForDevice;

  /** Regions with enabled catalog zones, offered as placement preferences. */
  protected readonly regions = toSignal(this.gceCatalog.getAvailableRegions(), {
    initialValue: [] as string[],
  });

  /** What this deployment can place into, for the Usage step's pool cluster. */
  protected readonly enabledPools = this.poolFacts.pools;
  protected readonly armImageReady = this.poolFacts.armImageReady;

  protected readonly form = form(this.intent, draft => {
    apply(draft.name, deviceNameSchema);
    min(draft.ttlHours, 1);
    max(draft.ttlHours, COMPILED_LIFETIMES.maxDeviceTtlHours);
    required(draft.trackBranch, {
      message: 'Branch is required',
      when: ctx => ctx.valueOf(draft.build).kind === 'track',
    });
    required(draft.trackTarget, {
      message: 'Target is required',
      when: ctx => ctx.valueOf(draft.build).kind === 'track',
    });
    required(draft.pinnedReason, {
      message: 'Please select a reason',
      when: ctx => ctx.valueOf(draft.pinned),
    });
    required(draft.pinnedReasonDetail, {
      message: 'Please provide details',
      when: ctx => ctx.valueOf(draft.pinned) && ctx.valueOf(draft.pinnedReason) === 'other',
    });
    validate(draft.pinnedUntil, ctx => {
      const value = ctx.value();
      if (!(value && ctx.valueOf(draft.pinned))) {
        return null;
      }
      return new Date(value).getTime() > Date.now()
        ? null
        : {kind: 'pinExpiryPast', message: 'Pick a time in the future'};
    });
  });

  /**
   * FieldTree is invariant in its model type; the lifecycle step binds only
   * the LifecycleDraftFields subset, which CreateIntent structurally carries.
   */
  protected readonly lifecycleFields = this.form as unknown as FieldTree<LifecycleDraftFields>;

  protected readonly selectedBuild = computed(() => {
    const build = this.intent().build;
    return build.kind === 'registered'
      ? (this.builds().find(b => b.id === build.id) ?? null)
      : null;
  });

  /** The build an imperative (chosen-host) create would use. */
  protected readonly effectiveBuild = computed(() => this.selectedBuild() ?? this.defaultBuild());

  protected readonly selectedHost = computed(() => {
    const host = this.intent().host;
    return host.kind === 'host'
      ? (this.availableHosts().find(h => h.id === host.id) ?? null)
      : null;
  });

  /** A tracked build needs both a branch and a target, whatever the host. */
  private readonly buildChoiceValid = computed(() => {
    if (this.intent().build.kind !== 'track') {
      return true;
    }
    return this.form.trackBranch().valid() && this.form.trackTarget().valid();
  });

  /**
   * A chosen host makes the create imperative: it needs a resolved registered
   * build or a tracked branch (buildId=branch). Automatic placement resolves
   * any build choice server-side.
   */
  private readonly buildHostComboValid = computed(() => {
    const {host, build} = this.intent();
    if (host.kind === 'auto') {
      return true;
    }
    if (this.selectedHost() === null) {
      return false;
    }
    return build.kind === 'track' || this.effectiveBuild() !== null;
  });

  protected readonly step1Valid = computed(
    () => this.form.name().valid() && this.buildChoiceValid() && this.buildHostComboValid()
  );
  protected readonly step3Valid = computed(
    () =>
      this.form.ttlHours().valid() &&
      this.form.pinnedReason().valid() &&
      this.form.pinnedReasonDetail().valid() &&
      this.form.pinnedUntil().valid()
  );

  protected readonly pinReasons = Object.entries(PIN_REASON_LABELS).map(([value, label]) => ({
    value,
    label,
  }));

  /** Stepper position; the plan resource only runs while step 4 is active. */
  protected readonly stepIndex = signal(0);

  /**
   * How far the interview has been taken. A step is "completed" to the stepper
   * both to unlock the one after it and to draw its header, and Usage and
   * Duration are valid before they are ever opened, so declaring them complete
   * outright put the edit pencil on two steps the user had not seen. This
   * tracks the high water mark, not the current index, so going back does not
   * re-lock the headers ahead.
   */
  protected readonly furthestStep = signal(0);

  private readonly planRequest = computed(() => {
    if (this.stepIndex() !== 3 || !this.step1Valid() || !this.step3Valid()) {
      return undefined;
    }
    return toCreateDeviceRequest(this.intent(), this.effectiveBuild());
  });

  /**
   * The AIP-163 dry run: undefined params keep the resource idle off the
   * Plan step; entering it fires validate_only, which performs every check
   * the launch would (quota, capacity, GPU availability) without committing.
   */
  private readonly plan = resource({
    params: () => this.planRequest(),
    loader: ({params}) => firstValueFrom(this.deviceApi.planCreate(params)),
  });

  protected readonly planning = computed(() => this.plan.isLoading());
  protected readonly planResolution = computed(() =>
    this.plan.hasValue() ? this.plan.value() : null
  );
  protected readonly planError = computed(() => {
    const error = this.plan.error();
    return error ? getErrorMessage(error, 'The launch would fail') : null;
  });

  /**
   * Derived from the store so a FAILED create re-enables the Launch button.
   * The component only unmounts on the success-navigate path, so a local
   * boolean would stay stuck on "Launching...".
   */
  private readonly submitted = signal(false);
  protected readonly isCreating = computed(() => this.submitted() && this.deviceStore.loading());

  /**
   * Retry-safety for the create. Device names are not unique and nothing else
   * dedupes, so a create the user re-sends after a failed-looking response
   * would otherwise provision a second device and a second billed host. The
   * backend fingerprints the request against the key and 422s a reuse with
   * different content, so the key is re-minted whenever the body changes:
   * editing the form starts a new launch, retrying an unedited one returns
   * whatever the first attempt actually created.
   */
  private readonly idempotencyKey = linkedSignal<string, string>({
    source: () => JSON.stringify(toCreateDeviceRequest(this.intent(), this.effectiveBuild())),
    computation: () => crypto.randomUUID(),
  });

  constructor() {
    this.buildStore.loadAll({});
    this.hostStore.loadAll({});

    // Adopt the operator's session default while the draft still carries the
    // compiled seed. Compared by value, not dirty state. The preset chips
    // patch the intent directly, so a click must never be overwritten when
    // the policy response lands late.
    effect(() => {
      const preset = this.lifetimes().deviceTtlHours;
      this.intent.update(i =>
        i.ttlHours === COMPILED_LIFETIMES.deviceTtlHours && i.ttlHours !== preset
          ? {...i, ttlHours: preset}
          : i
      );
    });
  }

  protected selectBuild(build: Build): void {
    this.intent.update(intent => ({...intent, build: {kind: 'registered', id: build.id}}));
  }

  protected useDefaultBuild(): void {
    this.intent.update(intent => ({...intent, build: {kind: 'default'}}));
  }

  /** Switches the build picker between the registered list and branch tracking. */
  protected setBuildMode(mode: 'registered' | 'track'): void {
    this.intent.update(intent => {
      if (mode === 'track') {
        return {...intent, build: {kind: 'track'}};
      }
      // Leaving tracking: fall back to the platform default until a build card
      // is picked (an existing registered pick keeps its id).
      return intent.build.kind === 'track' ? {...intent, build: {kind: 'default'}} : intent;
    });
  }

  protected selectHost(host: Host): void {
    this.intent.update(intent => ({...intent, host: {kind: 'host', id: host.id}}));
  }

  protected setHostKind(kind: 'auto' | 'host'): void {
    this.intent.update(intent => ({
      ...intent,
      host: kind === 'auto' ? {kind: 'auto'} : {kind: 'host', id: ''},
    }));
  }

  protected setUsage(usage: CreateIntent['usage']): void {
    // A headless device never asks for a GPU, so leaving the constraint set
    // would send a requirement the step no longer shows.
    this.intent.update(intent => ({
      ...intent,
      usage,
      gpuRequired: usage === 'interactive' && intent.gpuRequired,
    }));
  }

  protected setGpuRequired(gpuRequired: boolean): void {
    this.intent.update(intent => ({...intent, gpuRequired}));
  }

  // Architecture and pool are independent of usage: an arm64 guest and a pinned
  // pool mean the same thing interactive or headless, so nothing clears them.
  protected setArchitecture(architecture: CreateIntent['architecture']): void {
    this.intent.update(intent => ({...intent, architecture}));
  }

  protected setPool(pool: CreateIntent['pool']): void {
    this.intent.update(intent => ({...intent, pool}));
  }

  protected setPreferredRegion(preferredRegion: string): void {
    this.intent.update(intent => ({...intent, preferredRegion}));
  }

  protected setCapacity(capacity: CreateIntent['capacity']): void {
    this.intent.update(intent => ({...intent, capacity}));
  }

  protected setTtl(hours: number): void {
    this.intent.update(intent => ({...intent, ttlHours: hours}));
  }

  protected addTag(event: MatChipInputEvent): void {
    const value = (event.value || '').trim();
    if (value && !this.intent().tags.includes(value)) {
      this.intent.update(intent => ({...intent, tags: [...intent.tags, value]}));
    }
    event.chipInput?.clear();
  }

  protected removeTag(tag: string): void {
    this.intent.update(intent => ({...intent, tags: intent.tags.filter(t => t !== tag)}));
  }

  /** Registers the AOSP quick-start build inline and selects it. */
  protected openQuickStart(): void {
    this.dialog
      .open(QuickStartDialogComponent, {
        data: {makeDefault: this.builds().length === 0},
      })
      .afterClosed()
      .subscribe((build?: Build) => {
        if (build) {
          this.buildStore.loadAll({});
          this.selectBuild(build);
        }
      });
  }

  protected onStepChange(index: number): void {
    this.stepIndex.set(index);
    this.furthestStep.update(furthest => Math.max(furthest, index));
  }

  protected createDevice(): void {
    if (this.isCreating()) {
      return;
    }
    this.submitted.set(true);
    clearIntentDraft();
    this.deviceStore.create({
      request: toCreateDeviceRequest(this.intent(), this.effectiveBuild()),
      idempotencyKey: this.idempotencyKey(),
    });
  }
}
