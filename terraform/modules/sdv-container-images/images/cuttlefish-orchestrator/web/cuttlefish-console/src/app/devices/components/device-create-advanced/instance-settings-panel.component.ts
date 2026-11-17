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

import {ChangeDetectionStrategy, Component, computed, input} from '@angular/core';
import type {FieldTree} from '@angular/forms/signals';
import {FormField} from '@angular/forms/signals';
import {MatButtonModule} from '@angular/material/button';
import {MatCheckboxModule} from '@angular/material/checkbox';
import {MatExpansionModule} from '@angular/material/expansion';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatInputModule} from '@angular/material/input';
import {MatSelectModule} from '@angular/material/select';
import type {InstanceDraft} from './instance-draft.model';
import {defaultDisplay, MAX_DISPLAYS} from './instance-draft.model';

const CPU_CHOICES = [2, 4, 8, 16];
const MEMORY_CHOICES_MB = [2048, 4096, 8192, 16384];

/**
 * The standard choices plus the instance's own value when it is not one of
 * them. An imported or preset value (the SDV Core runs 2096 MB) would
 * otherwise leave the select blank while the config still carries it.
 */
function withCurrent(choices: readonly number[], current: number): number[] {
  return choices.includes(current) ? [...choices] : [...choices, current].sort((a, b) => a - b);
}

/**
 * The per-instance form surface of advanced mode: basic settings plus the
 * collapsible advanced sections, bound directly to one instance's field tree.
 */
@Component({
  selector: 'app-instance-settings-panel',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    FormField,
    MatButtonModule,
    MatCheckboxModule,
    MatExpansionModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
    MatSelectModule,
  ],
  template: `
    <!-- Basic Settings for Active Instance -->
    <section class="config-section">
      <h3>
        <mat-icon>settings</mat-icon>
        Basic Settings
      </h3>

      <form class="settings-form">
        <mat-form-field appearance="outline" class="full-width">
          <mat-label>Instance name</mat-label>
          <input matInput [formField]="field().name" placeholder="my-device">
          <mat-hint>Lowercase letters, numbers, and hyphens</mat-hint>
          @for (error of field().name().errors(); track error.kind) {
            <mat-error>{{ error.message }}</mat-error>
          }
        </mat-form-field>

        <div class="form-row">
          <mat-form-field appearance="outline" class="half-width">
            <mat-label>vCPUs</mat-label>
            <mat-select [formField]="field().cpus">
              @for (cpus of cpuOptions(); track cpus) {
                <mat-option [value]="cpus">{{ cpus }} vCPUs</mat-option>
              }
            </mat-select>
          </mat-form-field>

          <mat-form-field appearance="outline" class="half-width">
            <mat-label>Memory</mat-label>
            <mat-select [formField]="field().memoryMb">
              @for (option of memoryOptions(); track option.mb) {
                <mat-option [value]="option.mb">{{ option.label }}</mat-option>
              }
            </mat-select>
          </mat-form-field>
        </div>

        <mat-checkbox [formField]="field().useSdcard">
          Enable SD Card
        </mat-checkbox>
      </form>
    </section>

    <!-- Collapsible Advanced Sections -->
    <mat-accordion class="config-accordion" multi>
      <!-- Display & Graphics Section -->
      <mat-expansion-panel>
        <mat-expansion-panel-header>
          <mat-panel-title>
            <mat-icon>monitor</mat-icon>
            Display & Graphics
          </mat-panel-title>
        </mat-expansion-panel-header>

        <div class="panel-content">
          @for (display of field().displays; track $index) {
            <div class="display-group">
              <div class="display-header">
                <span>Display {{ $index + 1 }}</span>
                @if (displayCount() > 1) {
                  <button type="button"
                    mat-icon-button
                    [attr.aria-label]="'Remove display ' + ($index + 1)"
                    (click)="removeDisplay($index)">
                    <mat-icon>delete</mat-icon>
                  </button>
                }
              </div>
              <div class="form-row">
                <mat-form-field appearance="outline" class="third-width">
                  <mat-label>Width</mat-label>
                  <input matInput type="number" [formField]="display.width">
                </mat-form-field>
                <mat-form-field appearance="outline" class="third-width">
                  <mat-label>Height</mat-label>
                  <input matInput type="number" [formField]="display.height">
                </mat-form-field>
                <mat-form-field appearance="outline" class="third-width">
                  <mat-label>DPI</mat-label>
                  <input matInput type="number" [formField]="display.dpi">
                </mat-form-field>
              </div>
              <mat-form-field appearance="outline" class="half-width">
                <mat-label>Refresh Rate</mat-label>
                <mat-select [formField]="display.refreshRate">
                  <mat-option [value]="60">60 Hz</mat-option>
                  <mat-option [value]="90">90 Hz</mat-option>
                  <mat-option [value]="120">120 Hz</mat-option>
                </mat-select>
              </mat-form-field>
            </div>
          }

          <button type="button"
            mat-stroked-button
            class="add-display-btn"
            [disabled]="displayCount() >= maxDisplays"
            (click)="addDisplay()">
            <mat-icon>add</mat-icon>
            Add Display
          </button>

          <mat-form-field appearance="outline" class="half-width">
            <mat-label>GPU Rendering</mat-label>
            <mat-select [formField]="field().gpuMode">
              <mat-option value="gfxstream">GFXStream</mat-option>
              <mat-option value="drm_virgl">VirGL</mat-option>
              <mat-option value="swiftshader">SwiftShader</mat-option>
              <mat-option value="auto">Auto</mat-option>
            </mat-select>
          </mat-form-field>
          <mat-checkbox [formField]="field().recordScreen">Record screen</mat-checkbox>
        </div>
      </mat-expansion-panel>

      <!-- Boot Options -->
      <mat-expansion-panel>
        <mat-expansion-panel-header>
          <mat-panel-title>
            <mat-icon>power_settings_new</mat-icon>
            Boot Options
          </mat-panel-title>
        </mat-expansion-panel-header>

        <div class="panel-content">
          <mat-checkbox [formField]="field().enableBootAnimation">Enable boot animation</mat-checkbox>
          <mat-form-field appearance="outline" class="full-width">
            <mat-label>Extra Bootconfig Args</mat-label>
            <textarea matInput [formField]="field().extraBootconfigArgs" rows="2"></textarea>
          </mat-form-field>
        </div>
      </mat-expansion-panel>

      <!-- Security -->
      <mat-expansion-panel>
        <mat-expansion-panel-header>
          <mat-panel-title>
            <mat-icon>security</mat-icon>
            Security
          </mat-panel-title>
        </mat-expansion-panel-header>

        <div class="panel-content">
          <mat-form-field appearance="outline" class="full-width">
            <mat-label>Serial Number</mat-label>
            <input matInput [formField]="field().serialNumber">
          </mat-form-field>
          <mat-checkbox [formField]="field().useRandomSerial">Random serial</mat-checkbox>
          <mat-checkbox [formField]="field().guestEnforceSecurity">Enforce security</mat-checkbox>
        </div>
      </mat-expansion-panel>

      <!-- Network & Connectivity -->
      <mat-expansion-panel>
        <mat-expansion-panel-header>
          <mat-panel-title>
            <mat-icon>wifi</mat-icon>
            Network & Connectivity
          </mat-panel-title>
        </mat-expansion-panel-header>

        <div class="panel-content">
          <!-- Above the group field because it gates it: with no layer there is
               no partition for a tag to name, and a group typed under an
               unresolved switch configures nothing at all. -->
          <mat-form-field appearance="outline" class="full-width">
            <mat-label>Guest-to-guest vsock</mat-label>
            <mat-select [formField]="field().vhostUserVsock">
              <mat-option value="auto">Auto (the host decides at launch)</mat-option>
              <mat-option value="true">On</mat-option>
              <mat-option value="false">Off</mat-option>
            </mat-select>
            <mat-hint>
              Auto resolves off on x86_64 unless the guest image asks for it, and only this
              layer carries VM-to-VM vsock. Without it a guest reaches the host alone.
            </mat-hint>
          </mat-form-field>
          <mat-form-field appearance="outline" class="full-width">
            <mat-label>Vsock Guest Group</mat-label>
            <input matInput [formField]="field().vsockGuestGroup" placeholder="g1+g2">
            <mat-hint>Partitions the layer above; inert while it is off</mat-hint>
          </mat-form-field>
          <mat-form-field appearance="outline" class="full-width">
            <mat-label>WebRTC Device ID</mat-label>
            <input matInput [formField]="field().streamingDeviceId">
          </mat-form-field>
        </div>
      </mat-expansion-panel>

      <!-- Advanced VM -->
      <mat-expansion-panel>
        <mat-expansion-panel-header>
          <mat-panel-title>
            <mat-icon>build</mat-icon>
            Advanced VM
          </mat-panel-title>
        </mat-expansion-panel-header>

        <div class="panel-content">
          <mat-form-field appearance="outline" class="full-width">
            <mat-label>Setup Wizard Mode</mat-label>
            <mat-select [formField]="field().setupWizardMode">
              <mat-option value="DISABLED">Disabled</mat-option>
              <mat-option value="OPTIONAL">Optional</mat-option>
              <mat-option value="REQUIRED">Required</mat-option>
            </mat-select>
          </mat-form-field>
          <mat-form-field appearance="outline" class="full-width">
            <mat-label>Custom UUID</mat-label>
            <input matInput [formField]="field().uuid">
          </mat-form-field>
          <mat-checkbox [formField]="field().crosvmSandbox">Crosvm Sandbox</mat-checkbox>
        </div>
      </mat-expansion-panel>
    </mat-accordion>
  `,
  styles: `
    .config-section {
      margin: 16px 0;
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

    .settings-form {
      display: flex;
      flex-direction: column;
      gap: 16px;
    }

    .form-row {
      display: flex;
      gap: 16px;
    }

    .full-width { width: 100%; }
    .half-width { flex: 1; }
    .third-width { flex: 1; min-width: 0; }

    .config-accordion { margin-top: 16px; }

    .config-accordion mat-expansion-panel {
      margin-bottom: 8px;
      border-radius: 8px !important;
      box-shadow: none !important;
      border: 1px solid var(--cco-border);
    }

    .config-accordion mat-expansion-panel-header {
      height: 44px;
      font-size: 12px;
    }

    .config-accordion mat-panel-title {
      display: flex;
      align-items: center;
      gap: 8px;
      font-weight: 500;
    }

    .config-accordion mat-panel-title mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
      color: var(--cco-accent);
    }

    .panel-content {
      display: flex;
      flex-direction: column;
      gap: 12px;
      padding-top: 8px;
    }

    .display-group {
      border: 1px solid var(--cco-border);
      border-radius: 8px;
      padding: 12px;
    }

    .display-header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      margin-bottom: 8px;
      font-size: 12px;
      font-weight: 500;
      color: var(--cco-text-secondary);
    }

    .add-display-btn {
      align-self: flex-start;
      height: 32px;
      font-size: 12px;
    }

    .add-display-btn mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
      margin-right: 4px;
    }
  `,
})
export class InstanceSettingsPanelComponent {
  readonly field = input.required<FieldTree<InstanceDraft>>();

  protected readonly cpuOptions = computed(() =>
    withCurrent(CPU_CHOICES, this.field().cpus().value())
  );

  protected readonly memoryOptions = computed(() =>
    withCurrent(MEMORY_CHOICES_MB, this.field().memoryMb().value()).map(mb => ({
      mb,
      label: mb % 1024 === 0 ? `${mb / 1024} GB` : `${mb} MB`,
    }))
  );

  protected readonly maxDisplays = MAX_DISPLAYS;
  protected readonly displayCount = computed(() => this.field().displays().value().length);

  protected addDisplay(): void {
    const displays = this.field().displays();
    if (displays.value().length >= MAX_DISPLAYS) {
      return;
    }
    displays.value.update(list => [...list, defaultDisplay()]);
  }

  protected removeDisplay(index: number): void {
    const displays = this.field().displays();
    if (displays.value().length <= 1) {
      return;
    }
    displays.value.update(list => list.filter((_, i) => i !== index));
  }
}
