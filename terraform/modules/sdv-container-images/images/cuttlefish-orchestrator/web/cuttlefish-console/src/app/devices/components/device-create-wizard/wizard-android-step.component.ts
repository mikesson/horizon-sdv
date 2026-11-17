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
import type {FieldTree} from '@angular/forms/signals';
import {FormField} from '@angular/forms/signals';
import {MatButtonModule} from '@angular/material/button';
import {MatButtonToggleModule} from '@angular/material/button-toggle';
import {MatDividerModule} from '@angular/material/divider';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatInputModule} from '@angular/material/input';
import {MatProgressSpinnerModule} from '@angular/material/progress-spinner';
import type {Build} from '../../../builds/models/build.model';
import type {Host} from '../../../hosts/models/host.model';
import {StepHeaderComponent} from '../../../shared/components/step-header/step-header.component';
import {BuildPickerComponent} from '../build-picker/build-picker.component';
import type {CreateIntent} from '../device-create-page/create-intent.model';
import {HostPickerComponent} from '../host-picker/host-picker.component';

/**
 * Step 1 (Android): which build to run, the device name, and, as a secondary
 * pre-answered question, where it runs. Answered questions render as
 * statements; pickers appear only on "Choose different".
 */
@Component({
  selector: 'app-wizard-android-step',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    StepHeaderComponent,
    FormField,
    MatButtonModule,
    MatButtonToggleModule,
    MatDividerModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
    MatProgressSpinnerModule,
    BuildPickerComponent,
    HostPickerComponent,
  ],
  template: `
    <app-step-header icon="android" title="Android" description="Which Android should this device run?" />

    @if (buildsLoading()) {
      <div class="loading-section">
        <mat-spinner aria-label="Loading" diameter="32" />
        <span>Loading builds...</span>
      </div>
    } @else {
      <div class="statement" [class.statement--action]="buildPickerOpen()">
        <mat-icon class="statement__icon">{{ statementIcon() }}</mat-icon>
        <div class="statement__body">
          @switch (intent().build.kind) {
            @case ('registered') {
              <span>Using <strong>{{ selectedBuild()?.name ?? 'a registered build' }}</strong>.</span>
            }
            @case ('track') {
              <span>
                Tracking the latest build on
                <strong>{{ intent().trackBranch }} / {{ intent().trackTarget }}</strong>,
                re-resolved on every launch.
              </span>
            }
            @default {
              @if (defaultBuild(); as build) {
                <span>Using <strong>{{ build.name }}</strong>, the platform default build.</span>
              } @else {
                <span>
                  We'll use the <strong>latest AOSP phone build</strong>
                  (aosp-android-latest-release) and register it in Builds for you.
                </span>
              }
            }
          }
          <button type="button" mat-button class="statement__change" (click)="toggleBuildPicker()">
            {{ buildPickerOpen() ? 'Done' : 'Choose different' }}
          </button>
        </div>
      </div>

      @if (buildPickerOpen()) {
        <div class="picker-section">
          <mat-button-toggle-group
            class="build-mode-toggle"
            [value]="buildMode()"
            (change)="onBuildModeChange($event.value)">
            <mat-button-toggle value="registered">
              <mat-icon>inventory_2</mat-icon>
              Registered build
            </mat-button-toggle>
            <mat-button-toggle value="track">
              <mat-icon>autorenew</mat-icon>
              Track a branch
            </mat-button-toggle>
          </mat-button-toggle-group>

          @if (buildMode() === 'track') {
            <div class="track-fields">
              <p class="track-hint">
                Launch the newest build published on an AOSP branch and target.
                It is re-resolved every time the device starts, so it never goes stale.
              </p>
              <mat-form-field appearance="outline" class="track-field" subscriptSizing="dynamic">
                <mat-label>Branch</mat-label>
                <input matInput [formField]="fields().trackBranch" placeholder="aosp-android-latest-release">
                @for (error of fields().trackBranch().errors(); track error.kind) {
                  <mat-error>{{ error.message }}</mat-error>
                }
              </mat-form-field>
              <mat-form-field appearance="outline" class="track-field" subscriptSizing="dynamic">
                <mat-label>Target</mat-label>
                <input matInput [formField]="fields().trackTarget" placeholder="aosp_cf_x86_64_only_phone-userdebug">
                @for (error of fields().trackTarget().errors(); track error.kind) {
                  <mat-error>{{ error.message }}</mat-error>
                }
              </mat-form-field>
            </div>
          } @else {
            <app-build-picker
              [builds]="builds()"
              [selectedId]="selectedBuildId()"
              (selectionChange)="onBuildPicked($event)" />
          }

          @if (defaultBuild() && intent().build.kind !== 'default') {
            <button type="button" mat-button (click)="useDefaultBuild()">
              <mat-icon>restart_alt</mat-icon>
              Use the platform default instead
            </button>
          }
        </div>
      }
    }

    <mat-divider />

    <mat-form-field appearance="outline" class="device-name-field" subscriptSizing="dynamic">
      <mat-label>Device name</mat-label>
      <input matInput [formField]="fields().name" placeholder="my-device">
      @for (error of fields().name().errors(); track error.kind) {
        <mat-error>{{ error.message }}</mat-error>
      }
    </mat-form-field>

    <mat-divider />

    <div class="host-choice">
      <h3>Host</h3>
      <mat-button-toggle-group
        [value]="intent().host.kind"
        (change)="onHostKindChange($event.value)"
        class="host-toggle">
        <mat-button-toggle value="auto">
          <mat-icon>auto_awesome</mat-icon>
          Automatic (recommended)
        </mat-button-toggle>
        <mat-button-toggle value="host">
          <mat-icon>dns</mat-icon>
          Choose a host
        </mat-button-toggle>
      </mat-button-toggle-group>

      @if (intent().host.kind === 'auto') {
        <p class="host-hint">
          The platform places the device on existing capacity or provisions a
          host for you. Nothing to set up.
        </p>
      } @else {
        @if (hostsLoading()) {
          <div class="loading-section">
            <mat-spinner aria-label="Loading" diameter="24" />
            <span>Loading hosts...</span>
          </div>
        } @else if (availableHosts().length === 0) {
          <div class="empty-hosts">
            <mat-icon>info</mat-icon>
            <div>
              <strong>No host has free capacity right now</strong>
              <p>Switch to Automatic and the platform will provision one for you.</p>
              <button type="button" mat-stroked-button (click)="onHostKindChange('auto')">
                <mat-icon>auto_awesome</mat-icon>
                Use Automatic
              </button>
            </div>
          </div>
        } @else {
          <app-host-picker
            [hosts]="availableHosts()"
            [selectedId]="selectedHostId()"
            (selectionChange)="hostSelected.emit($event)" />
          @if (intent().build.kind === 'default' && !defaultBuild()) {
            <div class="manual-host-note">
              <mat-icon>info</mat-icon>
              <span>A chosen host needs a registered build.</span>
              <button type="button" mat-stroked-button (click)="quickStartRequested.emit()">
                <mat-icon>add</mat-icon>
                Register quick-start build
              </button>
            </div>
          }
        }
      }
    </div>
  `,
  styles: `




    .statement {
      display: flex;
      align-items: center;
      gap: 12px;
      padding: 16px;
      background: var(--cco-surface-variant);
      border: 1px solid var(--cco-border);
      border-radius: 8px;
      margin-bottom: 16px;
    }

    .statement__icon {
      color: var(--cco-accent);
    }

    .statement__body {
      flex: 1;
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
      color: var(--cco-text);
    }

    .statement__change {
      flex-shrink: 0;
      color: var(--cco-accent);
    }

    .picker-section {
      margin-bottom: 16px;
    }

    .build-mode-toggle {
      margin-bottom: 16px;
    }

    .build-mode-toggle mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
      margin-right: 8px;
    }

    .track-fields {
      display: flex;
      flex-direction: column;
      gap: 8px;
      margin-bottom: 8px;
    }

    .track-hint {
      margin: 0 0 8px;
      font-size: 12px;
      color: var(--cco-text-secondary);
    }

    .track-field {
      width: 100%;
      max-width: 420px;
    }

    .loading-section {
      display: flex;
      align-items: center;
      gap: 12px;
      padding: 24px;
      color: var(--cco-text-secondary);
    }

    .device-name-field {
      width: 100%;
      max-width: 400px;
      margin: 16px 0;
    }

    .host-choice {
      margin: 24px 0 8px;
    }

    .host-choice h3 {
      margin: 0 0 12px;
      font-size: 16px;
      font-weight: 500;
      color: var(--cco-text);
    }

    .host-toggle mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
      margin-right: 8px;
    }

    .host-hint {
      margin: 12px 0 0;
      font-size: 12px;
      color: var(--cco-text-secondary);
    }

    .empty-hosts {
      display: flex;
      gap: 16px;
      padding: 24px;
      margin-top: 16px;
      background: var(--cco-surface-variant);
      border: 1px solid var(--cco-border);
      border-radius: 8px;
    }

    .empty-hosts mat-icon {
      color: var(--cco-accent);
      font-size: 32px;
      width: 32px;
      height: 32px;
    }

    .empty-hosts strong {
      color: var(--cco-text);
    }

    .empty-hosts p {
      margin: 8px 0;
      color: var(--cco-text-secondary);
    }

    app-host-picker {
      display: block;
      margin-top: 16px;
    }

    .manual-host-note {
      display: flex;
      align-items: center;
      gap: 12px;
      margin: 12px 0 0;
      font-size: 12px;
      color: var(--cco-warning);
    }

    .manual-host-note mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
    }
  `,
})
export class WizardAndroidStepComponent {
  readonly fields = input.required<FieldTree<CreateIntent>>();
  readonly intent = input.required<CreateIntent>();
  readonly builds = input.required<Build[]>();
  readonly buildsLoading = input.required<boolean>();
  readonly defaultBuild = input.required<Build | null>();
  readonly selectedBuild = input.required<Build | null>();
  readonly availableHosts = input.required<Host[]>();
  readonly hostsLoading = input.required<boolean>();

  readonly buildSelected = output<Build>();
  readonly defaultBuildChosen = output<void>();
  readonly buildModeChanged = output<'registered' | 'track'>();
  readonly hostSelected = output<Host>();
  readonly hostKindChanged = output<'auto' | 'host'>();
  readonly quickStartRequested = output<void>();

  protected readonly buildPickerOpen = signal(false);

  protected readonly selectedBuildId = computed(() => {
    const build = this.intent().build;
    return build.kind === 'registered' ? build.id : '';
  });

  /** Which picker surface is shown: the registered list or branch tracking. */
  protected readonly buildMode = computed<'registered' | 'track'>(() =>
    this.intent().build.kind === 'track' ? 'track' : 'registered'
  );

  protected readonly statementIcon = computed(() =>
    this.intent().build.kind === 'track' ? 'autorenew' : 'smartphone'
  );

  protected readonly selectedHostId = computed(() => {
    const host = this.intent().host;
    return host.kind === 'host' ? host.id : '';
  });

  protected toggleBuildPicker(): void {
    this.buildPickerOpen.update(open => !open);
  }

  protected onBuildPicked(build: Build): void {
    this.buildSelected.emit(build);
  }

  protected onBuildModeChange(mode: 'registered' | 'track'): void {
    this.buildModeChanged.emit(mode);
  }

  protected useDefaultBuild(): void {
    this.defaultBuildChosen.emit();
    this.buildPickerOpen.set(false);
  }

  protected onHostKindChange(kind: 'auto' | 'host'): void {
    this.hostKindChanged.emit(kind);
  }
}
