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

import type {OnInit} from '@angular/core';
import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  inject,
  linkedSignal,
  signal,
} from '@angular/core';
import {toSignal} from '@angular/core/rxjs-interop';
import {
  FormField,
  form,
  max,
  maxLength,
  min,
  pattern,
  required,
  validate,
} from '@angular/forms/signals';
import {MatButtonModule} from '@angular/material/button';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatInputModule} from '@angular/material/input';
import {MatProgressSpinnerModule} from '@angular/material/progress-spinner';
import {MatSelectModule} from '@angular/material/select';
import {MatSlideToggleModule} from '@angular/material/slide-toggle';
import {MatSliderModule} from '@angular/material/slider';
import {MatTooltipModule} from '@angular/material/tooltip';
import {Router, RouterModule} from '@angular/router';
import type {MachineConfigOutput} from '../../../machine-types';
import {MachineTypeStore} from '../../../machine-types';
import {QuotaService} from '../../../quota';
import {RegionZonePickerComponent, ShapePickerComponent} from '../../../shared/components';
import {COMPILED_LIFETIMES} from '../../../shared/models/platform-lifetimes.schema';
import {GceCatalogService} from '../../../shared/services';
import {PlatformPolicyService} from '../../../shared/services/platform-policy.service';
import {generateResourceName, RESOURCE_NAME_PATTERN} from '../../../shared/utils/resource-name';
import type {CreateHostRequest, ProvisioningModel} from '../../models/host.model';
import {HostStore} from '../../store/host.signal-store';

interface ProvisioningModelOption {
  value: ProvisioningModel;
  label: string;
  description: string;
  icon: string;
}

interface HostFormModel {
  name: string;
  zone: string;
  machineType: string;
  gpuType: string;
  gpuCount: number;
  maxDevices: number;
  ttlHours: number;
  provisioningModel: ProvisioningModel;
  exclusive: boolean;
  /** '' = no constraint. Passed to GCE verbatim. */
  minCpuPlatform: string;
  /** 0 = the platform's default boot disk. */
  bootDiskSizeGb: number;
}

/**
 * Host creation component with Google Cloud Console-style UX: a sectioned
 * form (Name → Region/zone → Machine configuration → Device limits →
 * Provisioning model) seeded from catalog and operator defaults, valid with
 * zero input once those load.
 */
@Component({
  selector: 'app-host-create',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    RouterModule,
    FormField,
    MatButtonModule,
    MatIconModule,
    MatFormFieldModule,
    MatInputModule,
    MatSelectModule,
    MatProgressSpinnerModule,
    MatSlideToggleModule,
    MatSliderModule,
    MatTooltipModule,
    RegionZonePickerComponent,
    ShapePickerComponent,
  ],
  template: `
    <div class="create-host-page">
      <!-- Page Header -->
      <header class="page-header">
        <button type="button" mat-icon-button aria-label="Back" (click)="goBack()" class="back-button">
          <mat-icon>arrow_back</mat-icon>
        </button>
        <div class="header-text">
          <h1>Create a Host VM</h1>
          <p>Deploy a new virtual machine for Cuttlefish devices</p>
        </div>
      </header>

      <!-- Main Content Area -->
      <div class="content-layout">
        <!-- Left: Main Form -->
        <!-- div, not main: the shell already provides the page landmark -->
        <div class="form-area">
          <form>
            <!-- Section: Name -->
            <section class="form-section">
              <h2 class="section-title">
                <mat-icon>badge</mat-icon>
                Name
              </h2>
              <div class="name-input-container">
                <mat-form-field appearance="outline" class="name-field" subscriptSizing="dynamic">
                  <mat-label>Host name</mat-label>
                  <input
                    matInput
                    [formField]="hostForm.name"
                    placeholder="my-host">
                  @for (error of hostForm.name().errors(); track error.kind) {
                    <mat-error>{{ error.message }}</mat-error>
                  }
                </mat-form-field>
              </div>
              <div class="gce-name-preview">
                <mat-icon>info</mat-icon>
                <span>
                  A GCE instance named <code>cf-&lt;name&gt;-&lt;id&gt;</code> is derived from
                  this. The exact name is assigned at create and shown on the host page.
                </span>
              </div>
            </section>

            <!-- Section: Region and Zone -->
            <section class="form-section">
              <h2 class="section-title">
                <mat-icon>public</mat-icon>
                Region and zone
              </h2>
              <app-region-zone-picker
                [initialZone]="draft().zone"
                [machineType]="draft().machineType"
                [gpuType]="draft().gpuType"
                (regionChange)="onRegionChange($event)"
                (zoneChange)="onZoneChange($event)" />
            </section>

            <!-- Section: Machine Configuration -->
            <section class="form-section">
              <h2 class="section-title">
                <mat-icon>memory</mat-icon>
                Machine configuration
              </h2>
              <app-shape-picker
                [machineTypes]="machineTypes()"
                [selectedType]="draft().machineType"
                (configChange)="onMachineConfigChange($event)" />
            </section>

            <!-- Section: Advanced options (device limits + provisioning model) -->
            <section class="form-section">
              <button
                type="button"
                class="advanced-toggle"
                (click)="toggleAdvanced()"
                [attr.aria-expanded]="advancedOpen()">
                <mat-icon>tune</mat-icon>
                <span class="advanced-title">Advanced options</span>
                <span class="advanced-summary">{{ advancedSummary() }}</span>
                <mat-icon class="chevron" [class.open]="advancedOpen()">expand_more</mat-icon>
              </button>
            </section>

            @if (advancedOpen()) {
            <!-- Section: Device Limits -->
            <section class="form-section">
              <h2 class="section-title">
                <mat-icon>devices</mat-icon>
                Device limits
              </h2>
              <div class="device-limit-control">
                <label class="slider-label" for="host-max-devices">Maximum concurrent devices</label>
                <p class="slider-hint">Number of Cuttlefish devices this host can run simultaneously</p>
                <div class="slider-row">
                  <mat-slider min="1" [max]="lifetimes().maxDevicesPerHost" step="1" discrete>
                    <input matSliderThumb id="host-max-devices" [formField]="hostForm.maxDevices">
                  </mat-slider>
                  <mat-form-field appearance="outline" class="count-input">
                    <input matInput type="number" [formField]="hostForm.maxDevices">
                  </mat-form-field>
                </div>
              </div>
              <div class="device-limit-control">
                <p class="slider-hint">The host is deleted automatically after its TTL. Device activity extends it.</p>
                <mat-form-field appearance="outline" class="count-input">
                  <mat-label>Time to live (hours)</mat-label>
                  <input matInput type="number" [formField]="hostForm.ttlHours">
                </mat-form-field>
              </div>
            </section>

            <!-- Section: Placement fencing -->
            <section class="form-section">
              <h2 class="section-title">
                <mat-icon>lock</mat-icon>
                Placement
              </h2>
              <mat-slide-toggle color="primary" [formField]="hostForm.exclusive">
                Reserve this host
              </mat-slide-toggle>
              <p class="slider-hint">
                Automatic placement skips a reserved host, so only devices that
                name it explicitly land here. Use it to keep CI off a host you
                are testing on. It does not stop you filling the host yourself.
              </p>
            </section>

            <!-- Section: VM shape passthroughs -->
            <section class="form-section">
              <h2 class="section-title">
                <mat-icon>memory</mat-icon>
                VM shape
              </h2>
              <p class="slider-hint">
                Passed to Compute Engine as given. Leave both empty unless a
                workload needs them.
              </p>
              <div class="shape-fields">
                <mat-form-field appearance="outline">
                  <mat-label>Minimum CPU platform</mat-label>
                  <input matInput [formField]="hostForm.minCpuPlatform" placeholder="Intel Cascade Lake">
                  <mat-hint>Only some machine families honour this</mat-hint>
                  @for (error of hostForm.minCpuPlatform().errors(); track error.kind) {
                    <mat-error>{{ error.message }}</mat-error>
                  }
                </mat-form-field>
                <mat-form-field appearance="outline">
                  <mat-label>Boot disk (GB)</mat-label>
                  <input matInput type="number" [formField]="hostForm.bootDiskSizeGb">
                  <mat-hint>0 uses the 400 GB default</mat-hint>
                  @for (error of hostForm.bootDiskSizeGb().errors(); track error.kind) {
                    <mat-error>{{ error.message }}</mat-error>
                  }
                </mat-form-field>
              </div>
            </section>

            <!-- Section: Provisioning Model -->
            <section class="form-section">
              <h2 class="section-title">
                <mat-icon>schedule</mat-icon>
                Provisioning model
              </h2>
              <mat-form-field appearance="outline" class="provisioning-model-field">
                <mat-label>VM provisioning model</mat-label>
                <mat-select [formField]="hostForm.provisioningModel">
                  <!-- Custom trigger to show icon + label when closed -->
                  <mat-select-trigger>
                    @if (selectedProvisioningModel(); as option) {
                      <div class="provisioning-trigger">
                        <mat-icon>{{ option.icon }}</mat-icon>
                        <span>{{ option.label }}</span>
                      </div>
                    }
                  </mat-select-trigger>
                  @for (option of provisioningModelOptions; track option.value) {
                    <mat-option [value]="option.value">
                      <div class="provisioning-option">
                        <div class="provisioning-option-header">
                          <mat-icon>{{ option.icon }}</mat-icon>
                          <span class="provisioning-option-label">{{ option.label }}</span>
                        </div>
                        <span class="provisioning-option-desc">{{ option.description }}</span>
                      </div>
                    </mat-option>
                  }
                </mat-select>
              </mat-form-field>
              @if (draft().provisioningModel === 'SPOT') {
                <div class="spot-warning">
                  <mat-icon>warning</mat-icon>
                  <div>
                    <strong>Spot VMs can be preempted</strong>
                    <p>GCE may terminate Spot VMs at any time when capacity is needed. Use for fault-tolerant workloads that can handle interruptions. Failed hosts can be retried.</p>
                  </div>
                </div>
              }
            </section>
            }

            <!-- Error Display -->
            @if (error(); as errorMsg) {
              <div class="error-banner">
                <mat-icon>error</mat-icon>
                <div class="error-content">
                  <strong>{{ getErrorTypeLabel() }}</strong>
                  <p>{{ errorMsg }}</p>
                  @if (catalogError()?.suggestion) {
                    <p class="error-suggestion">
                      <mat-icon>lightbulb</mat-icon>
                      {{ catalogError()?.suggestion }}
                    </p>
                  }
                  @if (catalogError()?.alternativeZones?.length) {
                    <div class="alternative-zones">
                      <span>Try these zones:</span>
                      @for (zone of catalogError()!.alternativeZones; track zone) {
                        <button mat-stroked-button type="button" (click)="selectZone(zone)">
                          {{ zone }}
                        </button>
                      }
                    </div>
                  }
                </div>
              </div>
            }

            <!-- Form Actions -->
            <div class="form-actions">
              @if (quotaHint(); as hint) {
                <span class="quota-hint" [class.exhausted]="hint.exhausted">
                  @if (hint.exhausted) {
                    <mat-icon>warning</mat-icon>
                  }
                  {{ hint.text }}
                </span>
              }
              <button mat-stroked-button type="button" routerLink="/hosts">Cancel</button>
              <button
                mat-flat-button
                type="button"
                (click)="onSubmit()"
                [disabled]="hostForm().invalid() || loading()">
                @if (loading()) {
                  <mat-spinner aria-label="Loading" diameter="20" />
                } @else {
                  Create
                }
              </button>
            </div>
          </form>
        </div>
      </div>
    </div>
  `,
  styles: `
    .create-host-page {
      min-height: 100vh;
      background: var(--cco-surface-variant);
    }

    /* Page Header */
    .page-header {
      display: flex;
      align-items: center;
      gap: 16px;
      padding: 16px 24px;
      background: var(--cco-surface);
      border-bottom: 1px solid var(--cco-border);
      box-shadow: 0 1px 2px 0 rgba(0, 0, 0, 0.1);
      position: sticky;
      top: 0;
      z-index: 10;
    }

    .back-button {
      color: var(--cco-text-secondary);
    }

    .header-text h1 {
      margin: 0;
      font-size: 22px;
      font-weight: 400;
      color: var(--cco-text);
    }

    .header-text p {
      margin: 4px 0 0;
      font-size: 14px;
      color: var(--cco-text-secondary);
    }

    /* Content Layout */
    .content-layout {
      max-width: 1200px;
      margin: 0 auto;
      padding: 24px;
    }

    /* Form Area */
    .form-area {
      background: var(--cco-surface);
      border-radius: 8px;
      border: 1px solid var(--cco-border);
      box-shadow: 0 1px 2px 0 rgba(0, 0, 0, 0.3), 0 1px 3px 1px rgba(0, 0, 0, 0.15);
      padding: 0;
    }

    /* Form Sections */
    .form-section {
      padding: 24px;
      border-bottom: 1px solid var(--cco-border);
    }

    .form-section:last-of-type {
      border-bottom: none;
    }

    .section-title {
      display: flex;
      align-items: center;
      gap: 12px;
      margin: 0 0 24px;
      font-size: 16px;
      font-weight: 500;
      color: var(--cco-text);
    }

    .section-title mat-icon {
      color: var(--cco-text-secondary);
    }

    /* Name Input */
    .name-input-container {
      display: flex;
      align-items: center;
      gap: 0;
      margin-bottom: 12px;
    }

    .name-field {
      flex: 1;
    }

    .gce-name-preview {
      display: flex;
      align-items: center;
      gap: 8px;
      padding: 12px 16px;
      background: var(--cco-accent-surface);
      border-radius: 4px;
      font-size: 12px;
      color: var(--cco-accent);
    }

    .gce-name-preview mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
    }

    .gce-name-preview code {
      font-family: 'Roboto Mono', monospace;
      font-weight: 500;
    }

    /* Slider Controls */
    .slider-label {
      display: block;
      font-size: 14px;
      font-weight: 500;
      color: var(--cco-text);
      margin-bottom: 4px;
    }

    .slider-hint {
      font-size: 12px;
      color: var(--cco-text-secondary);
      margin: 0 0 12px;
    }

    .slider-row {
      display: flex;
      align-items: center;
      gap: 16px;
    }

    .slider-row mat-slider {
      flex: 1;
    }

    .count-input {
      width: 80px;
    }

    .count-input input {
      text-align: center;
    }

    /* Error Banner */
    .error-banner {
      display: flex;
      gap: 16px;
      margin: 24px;
      padding: 16px;
      background: var(--cco-error-surface);
      border-left: 4px solid var(--cco-error);
      border-radius: 4px;
    }

    .error-banner > mat-icon {
      color: var(--cco-error);
      flex-shrink: 0;
    }

    .error-content {
      flex: 1;
    }

    .error-content strong {
      color: var(--cco-error);
    }

    .error-content p {
      margin: 8px 0;
      color: var(--cco-text);
    }

    .error-suggestion {
      display: flex;
      align-items: flex-start;
      gap: 8px;
      background: var(--cco-warning-surface);
      padding: 12px;
      border-radius: 4px;
    }

    .error-suggestion mat-icon {
      color: var(--cco-warning);
      font-size: 18px;
      width: 18px;
      height: 18px;
    }

    .alternative-zones {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: 8px;
      margin-top: 12px;
    }

    /* Form Actions */
    .advanced-toggle {
      display: flex;
      align-items: center;
      gap: 8px;
      width: 100%;
      padding: 4px 0;
      background: none;
      border: none;
      cursor: pointer;
      font: inherit;
      color: var(--cco-text);
      text-align: left;
    }

    .advanced-toggle > mat-icon:first-child {
      color: var(--cco-accent);
    }

    .advanced-title {
      font-size: 16px;
      font-weight: 500;
    }

    .advanced-summary {
      color: var(--cco-text-secondary);
      font-size: 12px;
      margin-left: 8px;
      flex: 1;
    }

    .advanced-toggle .chevron {
      color: var(--cco-text-secondary);
      transform: rotate(-90deg);
      transition: transform var(--cco-duration-fast) ease;
    }

    .advanced-toggle .chevron.open {
      transform: rotate(0deg);
    }

    .quota-hint {
      display: inline-flex;
      align-items: center;
      gap: 8px;
      margin-right: auto;
      font-size: 12px;
      color: var(--cco-text-secondary);
    }

    .quota-hint.exhausted {
      color: var(--cco-warning);
    }

    .quota-hint mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
    }

    .form-actions {
      display: flex;
      justify-content: flex-end;
      gap: 12px;
      padding: 16px 24px;
      background: var(--cco-surface-variant);
      border-top: 1px solid var(--cco-border);
      position: sticky;
      bottom: 0;
      border-radius: 0 0 8px 8px;
      z-index: 5;
    }

    .form-actions button mat-spinner {
      display: inline-block;
    }

    /* Provisioning Model Selector */
    .shape-fields {
      display: flex;
      flex-wrap: wrap;
      gap: var(--cco-space-16);
    }

    .shape-fields mat-form-field {
      flex: 1 1 240px;
      max-width: 320px;
    }

    .provisioning-model-field {
      width: 100%;
      max-width: 500px;
    }

    .provisioning-trigger {
      display: flex;
      align-items: center;
      gap: 8px;
    }

    .provisioning-trigger mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
      color: var(--cco-text-secondary);
    }

    .provisioning-option {
      display: flex;
      flex-direction: column;
      gap: 2px;
      padding: 4px 0;
    }

    .provisioning-option-header {
      display: flex;
      align-items: center;
      gap: 8px;
    }

    .provisioning-option-header mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
      color: var(--cco-text-secondary);
    }

    .provisioning-option-label {
      font-weight: 500;
      color: var(--cco-text);
    }

    .provisioning-option-desc {
      font-size: 12px;
      color: var(--cco-text-secondary);
      margin-left: 24px;
    }

    .spot-warning {
      display: flex;
      gap: 12px;
      margin-top: 16px;
      padding: 16px;
      background: var(--cco-warning-surface);
      border-left: 4px solid var(--cco-warning);
      border-radius: 4px;
    }

    .spot-warning > mat-icon {
      color: var(--cco-warning);
      flex-shrink: 0;
    }

    .spot-warning strong {
      color: var(--cco-warning);
    }

    .spot-warning p {
      margin: 4px 0 0;
      color: var(--cco-text-secondary);
      font-size: 12px;
    }

  `,
})
export class HostCreateComponent implements OnInit {
  private readonly hostStore = inject(HostStore);
  private readonly machineTypeStore = inject(MachineTypeStore);
  private readonly gceCatalog = inject(GceCatalogService);
  private readonly quotaService = inject(QuotaService);
  private readonly router = inject(Router);
  /** Platform lifetime policy: the TTL prefill and the device-capacity cap. */
  protected readonly lifetimes = inject(PlatformPolicyService).lifetimes;

  /** Operator placement zone pin (system_config placement.zone_pin); '' when unset. */
  private readonly zonePin = toSignal(this.gceCatalog.getZonePin(), {initialValue: ''});

  loading = this.hostStore.loading;
  error = this.hostStore.error;
  catalogError = this.hostStore.catalogError;

  readonly provisioningModelOptions: ProvisioningModelOption[] = [
    {
      value: 'STANDARD',
      label: 'Standard',
      description: 'VM runs for any duration you set. Ideal for most workloads.',
      icon: 'check_circle',
    },
    {
      value: 'SPOT',
      label: 'Spot',
      description: 'VM runs until preempted. Ideal for fault-tolerant workloads.',
      icon: 'savings',
    },
  ];

  private readonly model = signal<HostFormModel>({
    name: generateResourceName('host'),
    zone: '',
    machineType: '',
    gpuType: '',
    gpuCount: 0,
    maxDevices: 4,
    ttlHours: 10,
    provisioningModel: 'STANDARD',
    exclusive: false,
    minCpuPlatform: '',
    bootDiskSizeGb: 0,
  });

  /** Read-only view of the form model for template and child-input bindings. */
  protected readonly draft = this.model.asReadonly();

  protected readonly hostForm = form(this.model, draft => {
    required(draft.name);
    pattern(draft.name, RESOURCE_NAME_PATTERN, {
      message: 'Start with a letter; lowercase letters, numbers, and hyphens only',
    });
    required(draft.zone);
    required(draft.machineType);
    min(draft.gpuCount, 0);
    max(draft.gpuCount, 8);
    required(draft.maxDevices);
    min(draft.maxDevices, 1);
    max(draft.maxDevices, COMPILED_LIFETIMES.maxDevicesPerHost);
    required(draft.ttlHours);
    min(draft.ttlHours, 1);
    max(draft.ttlHours, COMPILED_LIFETIMES.maxHostTtlHours);
    required(draft.provisioningModel);
    maxLength(draft.minCpuPlatform, 100);
    // 0 means "the default"; the backend rejects anything between 1 and 9.
    min(draft.bootDiskSizeGb, 0);
    max(draft.bootDiskSizeGb, 65536);
    validate(draft.bootDiskSizeGb, ctx =>
      ctx.value() > 0 && ctx.value() < 10
        ? {kind: 'bootDiskTooSmall', message: 'Use 0 for the default, or at least 10 GB'}
        : null
    );
  });

  constructor() {
    // Adopt the catalog's default machine type until the user picks one.
    effect(() => {
      const defaultType = this.machineTypeStore.defaultType();
      if (defaultType && !this.hostForm.machineType().dirty()) {
        this.model.update(m => ({
          ...m,
          machineType: defaultType.id,
          gpuType: defaultType.defaultGpuType || '',
          gpuCount: defaultType.defaultGpuCount || 0,
        }));
      }
    });

    // Adopt the operator's zone pin while nothing is selected yet; when
    // the config is unset (or loses the race) the region-zone picker's
    // first-available fallback owns the seed instead.
    effect(() => {
      const zone = this.zonePin();
      if (zone && !this.model().zone) {
        this.model.update(m => ({...m, zone}));
      }
    });

    // Adopt the operator's default host lease until the user edits the TTL,
    // the same way as the machine-type default above.
    effect(() => {
      const hours = this.lifetimes().hostTtlHours;
      if (!this.hostForm.ttlHours().dirty()) {
        this.model.update(m => ({...m, ttlHours: hours}));
      }
    });
  }

  /** The catalog as loaded; the store asks the API for enabled types only. */
  protected readonly machineTypes = this.machineTypeStore.entities;

  /** The create request as it goes on the wire; the key is derived from it. */
  private readonly createRequest = computed<CreateHostRequest>(() => {
    const value = this.model();
    return {
      name: value.name,
      zone: value.zone,
      machineType: value.machineType,
      gpuType: value.gpuType,
      gpuCount: value.gpuCount,
      maxDevices: value.maxDevices,
      ttlHours: value.ttlHours,
      provisioningModel: value.provisioningModel,
      exclusive: value.exclusive,
      minCpuPlatform: value.minCpuPlatform.trim(),
      bootDiskSizeGb: value.bootDiskSizeGb,
    };
  });

  /**
   * Retry-safety for the create. Host names are not unique and nothing else
   * dedupes, so a create the user re-sends after a failed-looking response
   * would otherwise provision a second billed VM. The backend fingerprints the
   * request against the key and 422s a reuse with different content, so the key
   * is re-minted whenever the form changes the body. Editing starts a new
   * create, and retrying an unedited one returns the host the first attempt
   * made.
   */
  private readonly idempotencyKey = linkedSignal<string, string>({
    source: () => JSON.stringify(this.createRequest()),
    computation: () => crypto.randomUUID(),
  });

  protected readonly selectedProvisioningModel = computed(() =>
    this.provisioningModelOptions.find(o => o.value === this.draft().provisioningModel)
  );

  /** Rarely-changed settings stay collapsed; their values stay visible. */
  protected readonly advancedOpen = signal(false);

  protected readonly advancedSummary = computed(() => {
    const draft = this.draft();
    const model = this.selectedProvisioningModel()?.label ?? draft.provisioningModel;
    const parts = [`${draft.maxDevices} devices`, `${draft.ttlHours}h TTL`, model];
    if (draft.exclusive) {
      parts.push('exclusive');
    }
    return parts.join(' · ');
  });

  /**
   * Fleet-quota line next to the Create button. It informs and never blocks,
   * since the server stays authoritative (quota may free up between render
   * and submit).
   */
  protected readonly quotaHint = computed(() => {
    const quota = this.quotaService.currentQuota();
    if (!quota || quota.maxHosts <= 0) {
      return null;
    }
    const exhausted = quota.usedHosts >= quota.maxHosts;
    return {
      exhausted,
      text: exhausted
        ? `Host quota exhausted (${quota.usedHosts} of ${quota.maxHosts} used). This create will be rejected`
        : `Host quota: ${quota.usedHosts} of ${quota.maxHosts} used`,
    };
  });

  protected toggleAdvanced(): void {
    this.advancedOpen.update(open => !open);
  }

  ngOnInit(): void {
    this.machineTypeStore.loadAll(undefined);
    this.quotaService.refresh();
  }

  onRegionChange(_region: string): void {
    // Region changed - zone will be auto-selected by the picker
  }

  onZoneChange(zone: string): void {
    this.model.update(m => ({...m, zone}));
  }

  onMachineConfigChange(config: MachineConfigOutput): void {
    this.hostForm.machineType().markAsDirty();
    this.model.update(m => ({
      ...m,
      machineType: config.machineType,
      gpuType: config.gpuType ?? '',
      gpuCount: config.gpuCount ?? 0,
    }));
  }

  getErrorTypeLabel(): string {
    const errorType = this.catalogError()?.errorType;
    const labels: Record<string, string> = {
      quota_exceeded: 'Quota Exceeded',
      resource_unavailable: 'Resource Unavailable',
      zone_unavailable: 'Zone Unavailable',
      permission_denied: 'Permission Denied',
      configuration_error: 'Configuration Error',
    };
    return labels[errorType || ''] || 'Error';
  }

  selectZone(zone: string): void {
    this.model.update(m => ({...m, zone}));
  }

  goBack(): void {
    void this.router.navigate(['/hosts']);
  }

  onSubmit(): void {
    // The disabled button already covers the pointer path; this covers the
    // ones that bypass it (Enter in a field, a repeated programmatic submit).
    if (this.hostForm().invalid() || this.loading()) {
      return;
    }

    this.hostStore.create({
      request: this.createRequest(),
      idempotencyKey: this.idempotencyKey(),
    });
  }
}
