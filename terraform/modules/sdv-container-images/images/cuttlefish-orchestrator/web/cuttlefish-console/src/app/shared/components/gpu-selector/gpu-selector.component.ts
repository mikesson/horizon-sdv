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
  input,
  linkedSignal,
  output,
} from '@angular/core';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatSelectModule} from '@angular/material/select';
import {MatSlideToggleModule} from '@angular/material/slide-toggle';

import type {GpuMode, MachineType} from '../../../machine-types';
import {gpuValidCounts, N1_ATTACHABLE_GPUS} from '../../../machine-types';
import {GpuTypePipe} from '../../pipes/gpu-type.pipe';

/** GPU attachment chosen for a host ({'', 0} means CPU-only / no attachment). */
export interface GpuSelection {
  gpuType: string;
  gpuCount: number;
}

/**
 * GPU control that adapts to the selected machine type's GPU mode so only valid
 * options are offered:
 *   - attachable (N1): an opt-in toggle (default CPU-only) revealing a
 *     GPU-type + count picker constrained to N1-attachable accelerators.
 *   - integrated (G2/G4): read-only, because the GPU is part of the machine type.
 *   - none (E2/C4A): nothing to configure.
 *
 * Emits only on user interaction. Switching machine types resets the toggle;
 * the parent re-emits the CPU-only default on selection, so the two stay in
 * sync without this component emitting on input change.
 */
@Component({
  selector: 'app-gpu-selector',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatFormFieldModule, MatSelectModule, MatSlideToggleModule, MatIconModule, GpuTypePipe],
  template: `
    @let mode = gpuMode();
    @switch (mode) {
      @case ('attachable') {
        <div class="gpu-panel">
          <div class="gpu-toggle-row">
            <mat-slide-toggle [checked]="attach()" (change)="onToggle($event.checked)">
              Attach a GPU
            </mat-slide-toggle>
            @if (!attach()) {
              <span class="gpu-hint">CPU only (swiftshader). Enable for hardware graphics acceleration.</span>
            }
          </div>

          @if (attach()) {
            <div class="gpu-fields">
              <mat-form-field appearance="outline">
                <mat-label>GPU type</mat-label>
                <mat-select [value]="gpuType()" (selectionChange)="onTypeChange($event.value)">
                  @for (gpu of attachableGpus; track gpu.type) {
                    <mat-option [value]="gpu.type">{{ gpu.label }}</mat-option>
                  }
                </mat-select>
              </mat-form-field>

              <mat-form-field appearance="outline" class="count-field">
                <mat-label>Count</mat-label>
                <mat-select [value]="gpuCount()" (selectionChange)="onCountChange($event.value)">
                  @for (n of validCounts(); track n) {
                    <mat-option [value]="n">{{ n }}</mat-option>
                  }
                </mat-select>
              </mat-form-field>
            </div>
            <p class="gpu-note">
              <mat-icon>info</mat-icon>
              The chosen GPU must be available in the selected zone.
            </p>
          }
        </div>
      }
      @case ('integrated') {
        <div class="gpu-panel integrated">
          <mat-icon>developer_board</mat-icon>
          <span>
            Includes {{ machineType()?.integratedGpuCount }}×
            {{ machineType()?.integratedGpuType | appGpuType }}, built into this machine type.
          </span>
        </div>
      }
      <!-- 'none': CPU-only machine type, nothing to configure -->
      @default {}
    }
  `,
  styles: `
    .gpu-panel {
      padding: 16px;
      background: var(--cco-surface-variant);
      border: 1px solid var(--cco-border);
      border-radius: 8px;
      margin-top: 16px;
    }

    .gpu-toggle-row {
      display: flex;
      align-items: center;
      gap: 12px;
      flex-wrap: wrap;
    }

    .gpu-hint {
      font-size: 12px;
      color: var(--cco-text-secondary);
    }

    .gpu-fields {
      display: grid;
      grid-template-columns: 1fr 120px;
      gap: 16px;
      margin-top: 16px;
    }

    .count-field {
      width: 100%;
    }

    .gpu-note,
    .gpu-panel.integrated {
      display: flex;
      align-items: center;
      gap: 8px;
      font-size: 12px;
      color: var(--cco-text-secondary);
    }

    .gpu-note {
      margin: 8px 0 0;
    }

    .gpu-panel.integrated {
      font-size: 12px;
      color: var(--cco-text);
    }

    .gpu-note mat-icon,
    .gpu-panel.integrated mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
      color: var(--cco-text-secondary);
      flex-shrink: 0;
    }

    @container page (max-width: 600px) {
      .gpu-fields {
        grid-template-columns: 1fr;
      }
    }
  `,
})
export class GpuSelectorComponent {
  readonly machineType = input<MachineType | null>(null);

  readonly selectionChange = output<GpuSelection>();

  readonly attachableGpus = N1_ATTACHABLE_GPUS;

  readonly gpuMode = computed<GpuMode>(() => this.machineType()?.gpuMode ?? 'none');

  /** Attach state and picks reset whenever the machine type changes. */
  private readonly machineId = computed(() => this.machineType()?.id ?? '');

  readonly attach = linkedSignal<string, boolean>({
    source: () => this.machineId(),
    computation: () => false,
  });

  readonly gpuType = linkedSignal<string, string>({
    source: () => this.machineId(),
    computation: () => this.machineType()?.defaultGpuType ?? N1_ATTACHABLE_GPUS[0].type,
  });

  readonly gpuCount = linkedSignal<string, number>({
    source: () => this.machineId(),
    computation: () => 1,
  });

  readonly validCounts = computed(() =>
    gpuValidCounts(this.gpuType(), this.machineType()?.maxGpus ?? 0)
  );

  onToggle(on: boolean): void {
    this.attach.set(on);
    this.emit();
  }

  onTypeChange(gpuType: string): void {
    this.gpuType.set(gpuType);
    if (!this.validCounts().includes(this.gpuCount())) {
      this.gpuCount.set(this.validCounts()[0] ?? 1);
    }
    this.emit();
  }

  onCountChange(count: number): void {
    this.gpuCount.set(count);
    this.emit();
  }

  private emit(): void {
    const attached = this.gpuMode() === 'attachable' && this.attach();
    this.selectionChange.emit(
      attached ? {gpuType: this.gpuType(), gpuCount: this.gpuCount()} : {gpuType: '', gpuCount: 0}
    );
  }
}
