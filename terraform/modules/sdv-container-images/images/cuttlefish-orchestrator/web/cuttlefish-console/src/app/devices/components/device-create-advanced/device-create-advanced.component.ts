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

/**
 * Device Create Advanced Component
 *
 * Advanced mode for creating CVD devices with full configuration control.
 * Features:
 * - Multi-instance support (1-8 instances)
 * - Template selector (8 built-in templates)
 * - Form-based configuration with live preview
 * - Progressive disclosure via collapsible sections
 * - JSON preview panel with CodeMirror
 * - Host VM selection
 * - Bidirectional form ↔ JSON sync
 */

import {DatePipe} from '@angular/common';
import type {ElementRef, OnInit} from '@angular/core';
import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  inject,
  input,
  linkedSignal,
  output,
  signal,
  viewChild,
} from '@angular/core';
import {
  applyEach,
  FormField,
  form,
  max,
  min,
  pattern,
  required,
  validate,
} from '@angular/forms/signals';
import {MatButtonModule} from '@angular/material/button';
import {MatButtonToggleModule} from '@angular/material/button-toggle';
import {MatCheckboxModule} from '@angular/material/checkbox';
import type {MatChipInputEvent} from '@angular/material/chips';
import {MatDialog} from '@angular/material/dialog';
import {MatDividerModule} from '@angular/material/divider';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatInputModule} from '@angular/material/input';
import {MatProgressSpinnerModule} from '@angular/material/progress-spinner';
import {MatSnackBar, MatSnackBarModule} from '@angular/material/snack-bar';
import {MatTooltipModule} from '@angular/material/tooltip';
import {QuickStartDialogComponent} from '../../../builds/components/quick-start-dialog/quick-start-dialog.component';
import type {Build} from '../../../builds/models/build.model';
import {BuildStore} from '../../../builds/store/build.signal-store';
import type {Host} from '../../../hosts/models/host.model';
import type {ConfirmDialogData} from '../../../shared/components';
import {ConfirmDialogComponent} from '../../../shared/components';
import {COMPILED_LIFETIMES} from '../../../shared/models/platform-lifetimes.schema';
import type {CapacityModel, Interaction} from '../../../shared/models/requirements.model';
import {LoggingService} from '../../../shared/services';
import {PlatformPolicyService} from '../../../shared/services/platform-policy.service';
import {generateResourceName} from '../../../shared/utils/resource-name';
import type {EnvironmentSpecification, TemplateName} from '../../models/cvd-config.schema';
import {EnvironmentSpecificationSchema} from '../../models/cvd-config.schema';
import {PIN_REASON_LABELS} from '../../models/device.model';
import {CapacityFieldComponent} from '../capacity-field/capacity-field.component';
import type {ApplyResult} from '../cvd-json-editor';
import {CvdJsonEditorComponent} from '../cvd-json-editor';
import type {AdvancedSeed} from '../device-create-page/create-intent.mapper';
import type {BuildChoice, HostChoice} from '../device-create-page/create-intent.model';
import {deviceNameSchema} from '../device-create-page/create-intent.model';
import type {LifecycleDraftFields} from '../device-create-wizard/wizard-lifecycle-step.component';
import {WizardLifecycleStepComponent} from '../device-create-wizard/wizard-lifecycle-step.component';
import {HostPickerComponent} from '../host-picker/host-picker.component';
import type {InstanceCardData} from '../instance-card';
import {InstanceCardComponent} from '../instance-card';
import {TemplatePickerComponent} from '../template-picker/template-picker.component';
import {fromSpec, toSpec, uniqueInstanceName} from './advanced-draft.mapper';
import {clearDraft as clearStoredDraft, loadDraft, saveDraft} from './advanced-draft.storage';
import {BuildsSectionComponent, HOST_TOOLS_REQUIRED_MESSAGE} from './builds-section.component';
import type {AdvancedDraft, InstanceDraft} from './instance-draft.model';
import {
  applyTemplateDefaults,
  buildValueError,
  instanceBuildLabel,
  instanceDraftFromTemplate,
  isHostPackageUrl,
  isRegisteredBuildId,
  templateSwitchDiscardsDisplayEdits,
} from './instance-draft.model';
import {InstanceSettingsPanelComponent} from './instance-settings-panel.component';
import {isSdvDraft, sdvHostWarning, sdvNeedsHostPackage, sdvPairDraft} from './sdv-preset';

const MAX_INSTANCES = 8;
const DRAFT_DEBOUNCE_MS = 2000;

/** What the Advanced editor hands the container on Create. */
export interface AdvancedSubmitEvent {
  name: string;
  config: EnvironmentSpecification;
  hostChoice: HostChoice;
  buildChoice: BuildChoice;
  gpuMode: string;
  /** Derived from the instances' rendering choices; steers auto placement. */
  interaction: Interaction;
  /** Read only under automatic placement. */
  capacity: CapacityModel;
  lifecycle: LifecycleDraftFields;
}

@Component({
  selector: 'app-device-create-advanced',
  host: {
    '(document:keydown)': 'onKeyDown($event)',
  },
  imports: [
    DatePipe,
    FormField,
    MatButtonModule,
    MatButtonToggleModule,
    MatIconModule,
    MatTooltipModule,
    MatDividerModule,
    MatCheckboxModule,
    MatFormFieldModule,
    MatInputModule,
    MatSnackBarModule,
    MatProgressSpinnerModule,
    BuildsSectionComponent,
    CapacityFieldComponent,
    CvdJsonEditorComponent,
    HostPickerComponent,
    InstanceCardComponent,
    InstanceSettingsPanelComponent,
    TemplatePickerComponent,
    WizardLifecycleStepComponent,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <!-- Hidden file input for import -->
    <input #fileInput type="file" accept=".json" hidden (change)="onFileSelected($event)">

    <!-- Draft Recovery Banner -->
    @if (hasDraft()) {
      <div class="draft-recovery-banner" role="alert" aria-live="polite">
        <mat-icon aria-hidden="true">history</mat-icon>
        <span>Unsaved draft from {{ draftTime() | date:'short' }}</span>
        <div class="draft-actions">
          <button type="button" mat-button color="primary" (click)="restoreDraft()" aria-label="Restore saved draft">Restore</button>
          <button type="button" mat-button (click)="discardDraft()" aria-label="Discard saved draft">Discard</button>
        </div>
      </div>
    }

    <!-- Processing Overlay -->
    @if (isProcessing()) {
      <div class="processing-overlay">
        <mat-spinner aria-label="Loading" diameter="40" />
        <span>Processing...</span>
      </div>
    }

    <div class="advanced-mode-container">
      <!-- Left Panel: Configuration -->
      <div class="config-panel">
        <section class="config-section">
          <mat-form-field appearance="outline" class="device-name-field">
            <mat-label>Device name</mat-label>
            <input matInput [formField]="nameForm" placeholder="my-device">
            <mat-hint>Names the whole device. Each instance has its own name below.</mat-hint>
            @for (error of nameForm().errors(); track error.kind) {
              <mat-error>{{ error.message }}</mat-error>
            }
          </mat-form-field>
        </section>

        <mat-divider />

        <!-- Instance List -->
        <section class="config-section">
          <div class="section-header">
            <h3>
              <mat-icon>devices</mat-icon>
              Instances ({{ instances().length }}/{{ maxInstances }})
            </h3>
            <button type="button"
              mat-stroked-button
              color="primary"
              class="add-instance-btn"
              [disabled]="instances().length >= maxInstances"
              (click)="addInstance()">
              <mat-icon>add</mat-icon>
              Add Instance
            </button>
          </div>

          <button type="button" mat-button class="preset-link" (click)="startFromSdvPair()">
            <mat-icon>directions_car</mat-icon>
            Start from AAOS SDV Core + IVI
          </button>

          @if (isSdvPair()) {
            <p class="preset-hint">The pair boots in unlocked mode. Each instance needs its own build below.</p>
          }

          @if (instances().length > 1) {
            <!-- A group is host-local: its members share one netsim medium and
                 one vsock namespace, so it takes a host of its own and never
                 lands on warm capacity. Said when the second instance is added,
                 which is when the cost changes. -->
            <p class="group-cost">
              Provisions a dedicated host (~{{ totalResources().vcpus }} vCPU,
              {{ totalResources().memoryGb }} GB); warm capacity is not used.
            </p>
          }

          <div class="instance-list">
            @for (inst of instanceCards(); track inst.id) {
              <app-instance-card
                [instance]="inst"
                [selected]="activeInstanceId() === inst.id"
                [canDelete]="instances().length > 1"
                (cardSelect)="selectInstance(inst.id)"
                (cardDuplicate)="duplicateInstance(inst.id)"
                (cardDelete)="removeInstance(inst.id)" />
            }
          </div>
        </section>

        <mat-divider />

        <app-builds-section
          [form]="form"
          [builds]="builds()"
          [loading]="buildsLoading()"
          [platformDefault]="platformDefaultLabel()"
          [hostToolsRequired]="hostPackageRequired()"
          [note]="buildsNote()"
          (sameBuildChange)="setSameBuild($event)" />

        <mat-divider />

        @if (activeInstance(); as active) {
          <!-- Template Selection for Active Instance -->
          <section class="config-section">
            <h3>
              <mat-icon>category</mat-icon>
              Template for {{ active.name }}
            </h3>
            <p class="section-hint">Change the device template</p>

            <app-template-picker [selected]="active.template" (selectionChange)="changeTemplate($event)" />
          </section>

          <mat-divider />
        }

        @if (activeField(); as fields) {
          <app-instance-settings-panel [field]="fields" />
        }

        <mat-divider />

        <!-- Host Selection -->
        <section class="config-section">
          <h3>
            <mat-icon>dns</mat-icon>
            Host VM
          </h3>

          <mat-button-toggle-group
            [value]="hostChoice().kind"
            (change)="setHostKind($event.value)"
            class="placement-toggle">
            <mat-button-toggle value="auto">
              <mat-icon>auto_awesome</mat-icon>
              Automatic
            </mat-button-toggle>
            <mat-button-toggle value="host">
              <mat-icon>dns</mat-icon>
              Choose a host
            </mat-button-toggle>
          </mat-button-toggle-group>

          @if (hostChoice().kind === 'auto') {
            <p class="placement-hint">
              The platform places the devices on existing capacity or
              provisions a host{{ wantsGpu() ? ' with GPU preference' : '' }}.
            </p>
            <app-capacity-field
              class="capacity"
              [capacity]="capacity()"
              (capacityChanged)="setCapacity($event)" />
          } @else {
            <app-host-picker
              [hosts]="availableHosts()"
              [selectedId]="selectedHostId()"
              (selectionChange)="selectHost($event)" />

            @if (sdvWarning(); as warning) {
              <div class="placement-warning" role="alert">
                <mat-icon>warning</mat-icon>
                <span>{{ warning }}</span>
              </div>
            }

            @if (model().sameBuild && model().deviceBuild.trim() === '') {
              <div class="placement-warning">
                <mat-icon>info</mat-icon>
                <span>A chosen host cannot use the platform default. Pick a build under Builds.</span>
                @if (builds().length === 0) {
                  <button type="button" mat-stroked-button (click)="openQuickStart()">
                    <mat-icon>add</mat-icon>
                    Register quick-start build
                  </button>
                }
              </div>
            }

            <!-- Resource Summary -->
            @if (selectedHost()) {
              <div class="resource-summary">
                <div class="resource-item">
                  <span class="resource-label">Machine Type:</span>
                  <span class="resource-value">{{ selectedHost()?.machineType }}</span>
                </div>
                <div class="resource-item">
                  <span class="resource-label">Instances:</span>
                  <span class="resource-value">{{ instances().length }}</span>
                </div>
                <div class="resource-item">
                  <span class="resource-label">Total Resources:</span>
                  <span class="resource-value">{{ totalResources().vcpus }} vCPUs, {{ totalResources().memoryGb }} GB RAM</span>
                </div>
              </div>
            }
          }
        </section>

        <mat-divider />

        <!-- Lifecycle -->
        <section class="config-section">
          <app-wizard-lifecycle-step
            [fields]="lifecycleForm"
            [draft]="lifecycle()"
            [pinReasons]="pinReasons"
            [lifetimes]="lifetimes()"
            (ttlPreset)="setTtl($event)"
            (tagAdded)="addTag($event)"
            (tagRemoved)="removeTag($event)" />
        </section>

        <mat-divider />

        <!-- Global Settings -->
        <section class="config-section">
          <h3>
            <mat-icon>settings_applications</mat-icon>
            Global Settings
          </h3>
          <div class="checkbox-group">
            <mat-checkbox [formField]="form.netsimBt">Enable Bluetooth Simulator</mat-checkbox>
            <mat-checkbox [formField]="form.netsimUwb">Enable UWB Simulator</mat-checkbox>
          </div>
        </section>
      </div>

      <!-- Right Panel: JSON Editor (lazy loaded for performance) -->
      <div class="preview-panel">
        @defer (on viewport) {
          <app-cvd-json-editor
            [externalValue]="configJson()"
            (applied)="onJsonApply($event)"
            (validChange)="onJsonValidChange($event)" />
        } @placeholder {
          <div class="editor-placeholder" role="status" aria-busy="true" aria-label="Loading JSON editor">
            <mat-spinner diameter="32" aria-hidden="true" />
            <span>Loading editor...</span>
          </div>
        }
      </div>
    </div>

    <!-- Footer Actions -->
    <div class="footer-actions">
      <div class="footer-left">
        <button type="button"
          mat-stroked-button
          matTooltip="Import configuration from file (Ctrl+Shift+I)"
          (click)="fileInput.click()">
          <mat-icon>upload_file</mat-icon>
          Import
        </button>
        <button type="button"
          mat-stroked-button
          matTooltip="Export configuration to file (Ctrl+Shift+E)"
          (click)="exportToFile()">
          <mat-icon>download</mat-icon>
          Export
        </button>
      </div>
      <div class="footer-right">
        <button type="button" mat-stroked-button (click)="canceled.emit()">Cancel</button>
        <button type="button"
          mat-flat-button
          [disabled]="!isValid() || creating()"
          (click)="onCreate()">
          <mat-icon>rocket_launch</mat-icon>
          {{ creating() ? 'Creating…' : createLabel() }}
        </button>
      </div>
    </div>
  `,
  styles: `
    :host {
      display: block;
      /* The processing scrim positions against this, not the viewport. The
         routed page sits inside a query container, and a container establishes
         a containing block for fixed descendants, so a fixed scrim here
         silently resolved against the content area anyway. It covered the form
         while reading as if it covered the app. */
      position: relative;
    }

    /* A bare 1fr track is at least as wide as its content, so a long JSON
       line would widen the editor's column and squeeze the form. The editor
       scrolls the line inside its own box instead. */
    .advanced-mode-container {
      display: grid;
      grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
      gap: 24px;
      min-height: 600px;
    }

    @container page (max-width: 899px) {
      .advanced-mode-container {
        grid-template-columns: minmax(0, 1fr);
      }
    }

    .config-panel {
      min-width: 0;
      background: var(--cco-surface);
      border: 1px solid var(--cco-border);
      border-radius: 8px;
      padding: 24px;
      overflow-y: auto;
      max-height: 800px;
    }

    .preview-panel {
      display: flex;
      flex-direction: column;
      min-width: 0;
    }

    .device-name-field {
      width: 100%;
    }

    .config-section {
      margin: 16px 0;
    }

    .preset-link {
      color: var(--cco-accent);
    }

    .preset-hint {
      margin: 4px 0 8px;
      font-size: 12px;
      color: var(--cco-text-secondary);
    }

    .placement-toggle {
      margin-bottom: 12px;
    }

    .placement-toggle mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
      margin-right: 8px;
    }

    .placement-hint {
      margin: 4px 0 0;
      font-size: 12px;
      color: var(--cco-text-secondary);
    }

    .capacity {
      margin-top: 12px;
    }

    .placement-warning {
      display: flex;
      align-items: center;
      gap: 8px;
      margin: 12px 0 0;
      font-size: 12px;
      color: var(--cco-warning);
    }

    .placement-warning mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
    }

    .section-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-bottom: 12px;
    }

    .section-header h3 {
      margin: 0;
    }

    .config-section h3 {
      display: flex;
      align-items: center;
      gap: 8px;
      margin: 0 0 8px;
      font-size: 14px;
      font-weight: 500;
      color: var(--cco-text);
    }

    .config-section h3 mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
      color: var(--cco-accent);
    }

    .section-hint {
      margin: 0 0 12px;
      font-size: 12px;
      color: var(--cco-text-secondary);
    }

    /* Instance List */
    .instance-list {
      display: flex;
      gap: 12px;
      overflow-x: auto;
      padding: 8px 0;
    }

    .add-instance-btn {
      height: 32px;
      font-size: 12px;
    }

    .add-instance-btn mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
      margin-right: 4px;
    }

    /* Resource Summary */
    .resource-summary {
      margin-top: 12px;
      padding: 12px;
      background: var(--cco-surface-variant);
      border-radius: 8px;
      border: 1px solid var(--cco-border);
    }

    .resource-item {
      display: flex;
      justify-content: space-between;
      font-size: 12px;
      padding: 4px 0;
    }

    .resource-label { color: var(--cco-text-secondary); }
    .resource-value { font-weight: 500; color: var(--cco-text); }

    .group-cost {
      margin: 8px 0 0;
      font-size: 12px;
      color: var(--cco-text-secondary);
      font-variant-numeric: tabular-nums;
    }

    .checkbox-group {
      display: flex;
      flex-direction: column;
      gap: 8px;
    }

    /* Draft Recovery Banner */
    .draft-recovery-banner {
      display: flex;
      align-items: center;
      gap: 12px;
      padding: 12px 16px;
      margin-bottom: 16px;
      background: var(--cco-warning-surface);
      border: 1px solid var(--cco-warning);
      border-radius: 8px;
      color: var(--cco-warning);
    }

    .draft-recovery-banner mat-icon {
      color: var(--cco-warning);
    }

    .draft-recovery-banner span {
      flex: 1;
      font-size: 12px;
    }

    .draft-actions {
      display: flex;
      gap: 8px;
    }

    /* Processing Overlay */
    .processing-overlay {
      position: absolute;
      inset: 0;
      background: color-mix(in srgb, var(--cco-canvas) 88%, transparent);
      display: flex;
      align-items: center;
      justify-content: center;
      gap: 16px;
      z-index: 1000;
      font-size: 14px;
      color: var(--cco-text-secondary);
    }

    /* Footer */
    .footer-actions {
      display: flex;
      justify-content: space-between;
      align-items: center;
      gap: 12px;
      margin-top: 24px;
      padding: 16px 24px;
      border-top: 1px solid var(--cco-border);
      position: sticky;
      bottom: 0;
      background: var(--cco-canvas);
      z-index: 5;
    }

    .footer-left {
      display: flex;
      gap: 8px;
    }

    .footer-right {
      display: flex;
      gap: 12px;
    }

    .footer-actions button mat-icon {
      margin-right: 4px;
    }

    /* Editor Placeholder */
    .editor-placeholder {
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      gap: 16px;
      min-height: 400px;
      background: var(--cco-surface);
      border: 1px solid var(--cco-border);
      border-radius: 8px;
      color: var(--cco-text-secondary);
      font-size: 12px;
    }
  `,
})
export class DeviceCreateAdvancedComponent implements OnInit {
  private readonly snackBar = inject(MatSnackBar);
  private readonly logger = inject(LoggingService);
  private readonly buildStore = inject(BuildStore);
  private readonly dialog = inject(MatDialog);
  /** Platform lifetime policy: presets, bounds, and the TTL the draft seeds. */
  protected readonly lifetimes = inject(PlatformPolicyService).lifetimes;

  readonly fileInput = viewChild.required<ElementRef<HTMLInputElement>>('fileInput');

  readonly availableHosts = input<Host[]>([]);

  /**
   * Starting state from the Simple interview or a handoff link; applied once
   * on init. The restore banner still offers any previously stored draft.
   */
  readonly seed = input<AdvancedSeed | null>(null);

  /**
   * A create is already in flight. The wizard guards its own Launch button
   * this way; without it a second click here reaches the store, which drops
   * the first subscription while the server may already have committed it.
   */
  readonly creating = input(false);

  readonly canceled = output<void>();
  readonly submitted = output<AdvancedSubmitEvent>();

  readonly builds = this.buildStore.entities;
  readonly buildsLoading = this.buildStore.loading;
  readonly defaultBuild = computed(() => this.buildStore.defaultBuild() ?? null);

  /** The catalog, once it is complete enough to say a build is missing. */
  private readonly knownCatalog = computed(() =>
    !this.buildsLoading() && this.buildStore.complete() && !this.buildStore.error()
      ? this.builds()
      : null
  );

  readonly maxInstances = MAX_INSTANCES;

  /** The device's name. Instance names live in the draft and are separate. */
  readonly deviceName = signal(generateResourceName('device'));
  protected readonly nameForm = form(this.deviceName, deviceNameSchema);

  /** Single source of truth for the whole editor. */
  protected readonly model = signal<AdvancedDraft>({
    instances: [instanceDraftFromTemplate('phone', 'device-1')],
    sameBuild: true,
    deviceBuild: '',
    hostPackage: '',
    netsimBt: false,
    netsimUwb: false,
    capacity: 'auto',
  });

  protected readonly form = form(this.model, draft => {
    applyEach(draft.instances, inst => {
      required(inst.name, {message: 'Name is required'});
      pattern(inst.name, /^[a-z][a-z0-9-]*$/, {
        message: 'Must start with letter, contain only a-z, 0-9, -',
      });
      validate(inst.name, ctx => {
        const name = ctx.value();
        const taken = ctx.valueOf(draft.instances).filter(i => i.name === name).length > 1;
        return taken ? {kind: 'duplicate', message: 'Instance names must be unique'} : null;
      });
      validate(inst.build, ctx => {
        if (ctx.valueOf(draft.sameBuild)) {
          return null;
        }
        const value = ctx.value();
        if (value.trim() === '') {
          return {kind: 'required', message: 'Pick a build for this instance'};
        }
        return buildValueError(value, this.knownCatalog());
      });
    });
    validate(draft.deviceBuild, ctx => {
      if (!ctx.valueOf(draft.sameBuild)) {
        return null;
      }
      const value = ctx.value();
      if (value.trim() === '') {
        // An imperative create cannot name the platform default.
        return this.hostChoice().kind === 'host'
          ? {kind: 'required', message: 'A chosen host needs a build. Pick one.'}
          : null;
      }
      return buildValueError(value, this.knownCatalog());
    });
    validate(draft.hostPackage, ctx => {
      const value = ctx.value().trim();
      if (value === '') {
        return this.hostPackageRequired()
          ? {kind: 'required', message: HOST_TOOLS_REQUIRED_MESSAGE}
          : null;
      }
      return isHostPackageUrl(value)
        ? null
        : {kind: 'url', message: 'Enter a gs:// or https:// URL'};
    });
  });

  readonly instances = computed(() => this.model().instances);

  readonly capacity = computed(() => this.model().capacity);

  /** Follows explicit selection but self-heals when the instance list changes. */
  readonly activeInstanceId = linkedSignal<InstanceDraft[], string | null>({
    source: this.instances,
    computation: (insts, previous) =>
      previous && insts.some(i => i.id === previous.value)
        ? previous.value
        : (insts[0]?.id ?? null),
  });

  readonly activeInstance = computed(
    () => this.instances().find(i => i.id === this.activeInstanceId()) ?? null
  );

  /** FieldTree of the active instance, for template [formField] bindings. */
  protected readonly activeField = computed(() => {
    const index = this.instances().findIndex(i => i.id === this.activeInstanceId());
    return index >= 0 ? this.form.instances[index] : undefined;
  });

  /** Server-side placement (declarative) or a user-chosen host. */
  readonly hostChoice = signal<HostChoice>({kind: 'auto'});
  readonly selectedHostId = computed(() => {
    const choice = this.hostChoice();
    return choice.kind === 'host' ? choice.id : '';
  });

  readonly selectedHost = computed(() =>
    this.availableHosts().find(h => h.id === this.selectedHostId())
  );

  /** Lifecycle rides the top-level create request, not the env config. */
  private readonly lifecycleModel = signal<LifecycleDraftFields>({
    ttlHours: COMPILED_LIFETIMES.deviceTtlHours,
    pinned: false,
    pinnedReason: '',
    pinnedReasonDetail: '',
    pinnedUntil: '',
    tags: [],
  });
  readonly lifecycle = this.lifecycleModel.asReadonly();

  protected readonly lifecycleForm = form(this.lifecycleModel, draft => {
    min(draft.ttlHours, 1);
    max(draft.ttlHours, COMPILED_LIFETIMES.maxDeviceTtlHours);
    required(draft.pinnedReason, {
      message: 'Please select a reason',
      when: ctx => ctx.valueOf(draft.pinned),
    });
    required(draft.pinnedReasonDetail, {
      message: 'Please provide details',
      when: ctx => ctx.valueOf(draft.pinned) && ctx.valueOf(draft.pinnedReason) === 'other',
    });
  });

  protected readonly pinReasons = Object.entries(PIN_REASON_LABELS).map(([value, label]) => ({
    value,
    label,
  }));

  /** Whether any instance asks for hardware rendering (steers placement). */
  readonly wantsGpu = computed(() =>
    this.instances().some(i => i.gpuMode === 'gfxstream' || i.gpuMode === 'drm_virgl')
  );

  readonly hasDraft = signal<boolean>(false);
  readonly draftTime = signal<Date | null>(null);

  readonly isProcessing = signal<boolean>(false);

  readonly instanceCards = computed((): InstanceCardData[] =>
    this.instances().map(inst => ({
      id: inst.id,
      name: inst.name || 'device',
      template: inst.template,
      cpus: inst.cpus || 4,
      memoryMb: inst.memoryMb || 4096,
      build: instanceBuildLabel(
        this.model(),
        inst,
        this.builds(),
        this.hostChoice().kind === 'auto'
      ),
    }))
  );

  /** The registered device build, which travels as the request's build_id. */
  readonly selectedBuildId = computed(() => {
    const draft = this.model();
    return draft.sameBuild && isRegisteredBuildId(draft.deviceBuild)
      ? draft.deviceBuild.trim()
      : '';
  });

  readonly buildChoice = computed((): BuildChoice => {
    const id = this.selectedBuildId();
    return id ? {kind: 'registered', id} : {kind: 'default'};
  });

  /** Only automatic placement resolves the platform default. */
  protected readonly platformDefaultLabel = computed(() => {
    if (this.hostChoice().kind === 'host') {
      return null;
    }
    return `Platform default: ${this.defaultBuild()?.name ?? 'latest AOSP phone build'}`;
  });

  /** Said under the switch after a change that replaced instance builds. */
  readonly buildsNote = signal('');

  readonly isSdvPair = computed(() => isSdvDraft(this.model()));

  readonly hostPackageRequired = computed(() => sdvNeedsHostPackage(this.model()));

  readonly sdvWarning = computed(() =>
    this.hostChoice().kind === 'host' ? sdvHostWarning(this.model(), this.selectedHost()) : ''
  );

  /**
   * What the submit button promises.
   *
   * It used to read "Create N Device(s)", wrong twice over: this flow makes one
   * device however many instances it holds, and "(s)" has no plural form in any
   * language. The count moves to a qualifier, where it describes the device
   * rather than counting devices.
   */
  readonly createLabel = computed(() => {
    const count = this.instances().length;
    return count > 1 ? `Create device · ${count} instances` : 'Create device';
  });

  readonly totalResources = computed(() => {
    let vcpus = 0;
    let memoryMb = 0;
    for (const inst of this.instances()) {
      vcpus += inst.cpus || 0;
      memoryMb += inst.memoryMb || 0;
    }
    return {vcpus, memoryGb: Math.round((memoryMb / 1024) * 10) / 10};
  });

  readonly configJson = computed(() => JSON.stringify(toSpec(this.model()), null, 2));

  /** The build rules, including a chosen host's need for a build, live in the form. */
  readonly isValid = computed(() => {
    if (
      this.instances().length === 0 ||
      !this.nameForm().valid() ||
      !this.form().valid() ||
      !this.lifecycleForm().valid()
    ) {
      return false;
    }
    const host = this.hostChoice();
    return host.kind === 'auto' || host.id !== '';
  });

  private readonly draftState = computed(() => {
    const json = this.configJson();
    const hostId = this.selectedHostId();
    const buildId = this.selectedBuildId();
    const {sameBuild, capacity} = this.model();
    return {
      json,
      hostId,
      buildId,
      sameBuild,
      capacity,
      key: JSON.stringify([json, hostId, buildId, sameBuild, capacity]),
    };
  });

  /** Null until init has applied the seed. */
  private readonly openingState = signal<string | null>(null);

  constructor() {
    this.buildStore.loadAll({});
    this.checkForDraft();

    // Debounced autosave at the localStorage edge: trailing write after the
    // last config or host change. The state the editor opened with is not
    // saved, because writing it would replace the stored draft the restore
    // banner offers before the user has touched anything.
    effect(onCleanup => {
      const state = this.draftState();
      const opening = this.openingState();
      if (opening === null || state.key === opening) {
        return;
      }
      const timer = setTimeout(
        () => saveDraft(state.json, state.hostId, state.buildId, state.sameBuild, state.capacity),
        DRAFT_DEBOUNCE_MS
      );
      onCleanup(() => clearTimeout(timer));
    });
  }

  ngOnInit(): void {
    const seed = this.seed();
    if (seed?.name) {
      this.deviceName.set(seed.name);
    }
    if (seed?.draft) {
      this.model.set(seed.draft);
    }
    if (seed?.hostChoice) {
      this.hostChoice.set(seed.hostChoice);
    }
    const buildChoice = seed?.buildChoice;
    if (buildChoice?.kind === 'registered') {
      // A per-instance draft has no device build to take it.
      this.model.update(m => (m.sameBuild ? {...m, deviceBuild: buildChoice.id} : m));
    }
    if (seed?.lifecycle) {
      this.lifecycleModel.set(seed.lifecycle);
    }
    this.openingState.set(this.draftState().key);
  }

  private generateUniqueName(baseName = 'device'): string {
    return uniqueInstanceName(baseName, new Set(this.instances().map(i => i.name)));
  }

  addInstance(): void {
    if (this.instances().length >= MAX_INSTANCES) {
      return;
    }
    const inst = instanceDraftFromTemplate('phone', this.generateUniqueName());
    this.model.update(m => ({...m, instances: [...m.instances, inst]}));
    this.activeInstanceId.set(inst.id);
  }

  removeInstance(id: string): void {
    if (this.instances().length <= 1) {
      return;
    }
    this.model.update(m => ({...m, instances: m.instances.filter(i => i.id !== id)}));
  }

  duplicateInstance(id: string): void {
    if (this.instances().length >= MAX_INSTANCES) {
      return;
    }
    const source = this.instances().find(i => i.id === id);
    if (!source) {
      return;
    }

    const copy: InstanceDraft = {
      ...source,
      id: crypto.randomUUID(),
      name: this.generateUniqueName(source.name || 'device'),
    };
    this.model.update(m => ({...m, instances: [...m.instances, copy]}));
    this.activeInstanceId.set(copy.id);
  }

  selectInstance(id: string): void {
    this.activeInstanceId.set(id);
  }

  changeTemplate(template: TemplateName): void {
    const active = this.activeInstance();
    if (!active || active.template === template) {
      return;
    }
    // A template switch replaces the screen list whole, so it asks first once
    // the user has edited that list. It never asks for a switch that only adds
    // screens they never touched. The picker is a controlled input, so a
    // cancelled switch needs no revert, because the model does not move.
    if (!templateSwitchDiscardsDisplayEdits(active, template)) {
      this.applyTemplate(template);
      return;
    }
    const data: ConfirmDialogData = {
      title: 'Replace the screens you edited?',
      message: `The ${template} template brings its own screen layout, which replaces the screens configured on "${active.name}".`,
      confirmText: 'Replace screens',
      cancelText: 'Keep mine',
      icon: 'devices',
    };
    this.dialog
      .open(ConfirmDialogComponent, {data})
      .afterClosed()
      .subscribe((confirmed: boolean) => {
        if (confirmed) {
          this.applyTemplate(template);
        }
      });
  }

  /**
   * Replaces the instance list with the SDV pair. It asks first, because the
   * instances it replaces may hold edits nothing else keeps.
   */
  startFromSdvPair(): void {
    const data: ConfirmDialogData = {
      title: 'Start from the SDV pair?',
      message:
        'This replaces the instances in the editor with an SDV Core and an SDV IVI instance, each with its own build, and asks automatic placement for an on-demand host. Everything else stays as it is.',
      confirmText: 'Replace instances',
      cancelText: 'Keep mine',
      icon: 'directions_car',
    };
    this.dialog
      .open(ConfirmDialogComponent, {data})
      .afterClosed()
      .subscribe((confirmed: boolean) => {
        if (confirmed) {
          const pair = sdvPairDraft();
          this.model.update(m => ({
            ...m,
            instances: pair.instances,
            sameBuild: pair.sameBuild,
            deviceBuild: pair.deviceBuild,
            capacity: pair.capacity,
          }));
          this.buildsNote.set('');
        }
      });
  }

  private applyTemplate(template: TemplateName): void {
    const activeId = this.activeInstanceId();
    this.model.update(m => ({
      ...m,
      instances: m.instances.map(inst =>
        inst.id === activeId ? applyTemplateDefaults(inst, template) : inst
      ),
    }));
  }

  selectHost(host: Host): void {
    this.hostChoice.set({kind: 'host', id: host.id});
  }

  setCapacity(capacity: CapacityModel): void {
    this.model.update(m => ({...m, capacity}));
  }

  setHostKind(kind: 'auto' | 'host'): void {
    this.hostChoice.set(kind === 'auto' ? {kind: 'auto'} : {kind: 'host', id: ''});
  }

  /**
   * Turning the switch off copies the shared build into every instance. The
   * platform default has no per-instance form, so it copies as empty rows.
   * Turning it on keeps the first instance's build and says so when that
   * replaced different builds on the others.
   */
  setSameBuild(on: boolean): void {
    const draft = this.model();
    if (draft.sameBuild === on) {
      return;
    }
    if (!on) {
      const shared = draft.deviceBuild.trim();
      this.model.set({
        ...draft,
        sameBuild: false,
        deviceBuild: '',
        instances: draft.instances.map(inst => ({...inst, build: shared})),
      });
      this.buildsNote.set('');
      return;
    }
    const builds = draft.instances.map(inst => inst.build.trim());
    const first = builds[0] ?? '';
    this.model.set({
      ...draft,
      sameBuild: true,
      deviceBuild: first,
      instances: draft.instances.map(inst => ({...inst, build: ''})),
    });
    const firstName = draft.instances[0]?.name || 'device';
    this.buildsNote.set(
      builds.some(build => build !== first)
        ? `Every instance now uses the build of ${firstName}. The other builds were replaced.`
        : ''
    );
  }

  /** Registers the AOSP quick-start build inline and selects it. */
  openQuickStart(): void {
    this.dialog
      .open(QuickStartDialogComponent, {
        data: {makeDefault: this.builds().length === 0},
      })
      .afterClosed()
      .subscribe((build?: Build) => {
        if (build) {
          this.buildStore.loadAll({});
          this.model.update(m => ({...m, deviceBuild: build.id}));
        }
      });
  }

  setTtl(hours: number): void {
    this.lifecycleModel.update(draft => ({...draft, ttlHours: hours}));
  }

  addTag(event: MatChipInputEvent): void {
    const value = (event.value || '').trim();
    if (value && !this.lifecycleModel().tags.includes(value)) {
      this.lifecycleModel.update(draft => ({...draft, tags: [...draft.tags, value]}));
    }
    event.chipInput?.clear();
  }

  removeTag(tag: string): void {
    this.lifecycleModel.update(draft => ({...draft, tags: draft.tags.filter(t => t !== tag)}));
  }

  onJsonApply(result: ApplyResult): void {
    if (!(result.success && result.config)) {
      return;
    }
    // Applying the preview unchanged must not move the build switch.
    this.applyImportedConfig(result.config, {
      sameBuild: this.model().sameBuild,
      buildId: this.selectedBuildId(),
      capacity: this.capacity(),
    });
  }

  onJsonValidChange(isValid: boolean): void {
    this.logger.debug('DeviceCreateAdvanced', 'JSON valid', {isValid});
  }

  onCreate(): void {
    if (!this.isValid()) {
      return;
    }

    const draft = this.model();
    this.clearDraft();

    this.submitted.emit({
      name: this.deviceName(),
      config: toSpec(draft),
      hostChoice: this.hostChoice(),
      buildChoice: this.buildChoice(),
      gpuMode: draft.instances[0]?.gpuMode ?? 'auto',
      interaction: this.wantsGpu() ? 'interactive' : 'headless',
      capacity: draft.capacity,
      lifecycle: this.lifecycle(),
    });
  }

  // ==================== Export/Import Methods ====================

  exportToFile(): void {
    const blob = new Blob([this.configJson()], {type: 'application/json'});
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `cvd-config-${new Date().toISOString().replace(/[:.]/g, '-')}.json`;
    a.click();
    URL.revokeObjectURL(url);
    this.snackBar.open('Configuration exported', 'Dismiss', {duration: 3000});
  }

  onFileSelected(event: Event): void {
    const input = event.target as HTMLInputElement;
    const file = input.files?.[0];
    if (!file) {
      return;
    }

    this.isProcessing.set(true);

    const reader = new FileReader();
    reader.onload = () => {
      try {
        const content = reader.result as string;
        const parsed: unknown = JSON.parse(content);
        const result = EnvironmentSpecificationSchema.safeParse(parsed);

        if (result.success) {
          // A config file carries no capacity, so the editor's choice stays.
          this.applyImportedConfig(result.data, {capacity: this.capacity()});
          this.snackBar.open('Configuration imported successfully', 'Dismiss', {duration: 3000});
        } else {
          const firstError = result.error.issues[0];
          const path = firstError.path.join('.');
          this.snackBar.open(
            `Invalid configuration: ${path ? `${path} - ` : ''}${firstError.message}`,
            'Dismiss',
            {duration: 5000}
          );
        }
      } catch (_e) {
        this.snackBar.open('Failed to parse JSON file', 'Dismiss', {duration: 3000});
      } finally {
        this.isProcessing.set(false);
      }
    };

    reader.onerror = () => {
      this.snackBar.open('Failed to read file', 'Dismiss', {duration: 3000});
      this.isProcessing.set(false);
    };

    reader.readAsText(file);
    input.value = ''; // Reset for re-import
  }

  private applyImportedConfig(
    config: EnvironmentSpecification,
    hint: {sameBuild?: boolean; buildId?: string; capacity?: CapacityModel}
  ): void {
    // The selection linkedSignal resets to the first (all-new) instance.
    this.model.set(fromSpec(config, hint));
    this.buildsNote.set('');
  }

  // ==================== Draft Management Methods ====================

  private checkForDraft(): void {
    const draft = loadDraft();
    if (draft) {
      this.hasDraft.set(true);
      this.draftTime.set(new Date(draft.savedAt));
    }
  }

  private saveDraftToStorage(): void {
    saveDraft(
      this.configJson(),
      this.selectedHostId(),
      this.selectedBuildId(),
      this.model().sameBuild,
      this.capacity()
    );
  }

  restoreDraft(): void {
    const draft = loadDraft();
    if (!draft) {
      return;
    }

    // Empty ids in the stored draft mean the declarative choices.
    this.applyImportedConfig(draft.config, {
      sameBuild: draft.sameBuild,
      buildId: draft.buildId,
      capacity: draft.capacity,
    });
    this.hostChoice.set(draft.hostId ? {kind: 'host', id: draft.hostId} : {kind: 'auto'});

    this.hasDraft.set(false);
    this.draftTime.set(null);
    this.snackBar.open('Draft restored', 'Dismiss', {duration: 2000});
  }

  discardDraft(): void {
    this.clearDraft();
    this.snackBar.open('Draft discarded', 'Dismiss', {duration: 2000});
  }

  private clearDraft(): void {
    clearStoredDraft();
    this.hasDraft.set(false);
    this.draftTime.set(null);
  }

  // ==================== Keyboard Shortcuts ====================

  protected onKeyDown(event: KeyboardEvent): void {
    // Ctrl+S: Save draft manually
    if (event.ctrlKey && !event.shiftKey && event.key === 's') {
      event.preventDefault();
      this.saveDraftToStorage();
      this.snackBar.open('Draft saved', 'Dismiss', {duration: 2000});
    }

    // Ctrl+Shift+E: Export to file
    if (event.ctrlKey && event.shiftKey && event.key === 'E') {
      event.preventDefault();
      this.exportToFile();
    }

    // Ctrl+Shift+I: Import from file
    if (event.ctrlKey && event.shiftKey && event.key === 'I') {
      event.preventDefault();
      this.fileInput().nativeElement.click();
    }
  }
}
