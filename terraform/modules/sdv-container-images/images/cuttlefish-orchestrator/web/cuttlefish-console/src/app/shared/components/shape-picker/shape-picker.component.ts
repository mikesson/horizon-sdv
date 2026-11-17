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

import {UpperCasePipe} from '@angular/common';
import {
  ChangeDetectionStrategy,
  Component,
  computed,
  inject,
  input,
  linkedSignal,
  output,
  signal,
} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatDialog} from '@angular/material/dialog';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatSelectModule} from '@angular/material/select';

import type {MachineConfigOutput, MachineType} from '../../../machine-types';
import {GpuTypePipe} from '../../pipes/gpu-type.pipe';
import type {CustomShapeBuilderData} from '../custom-shape-builder/custom-shape-builder.component';
import {CustomShapeBuilderComponent} from '../custom-shape-builder/custom-shape-builder.component';
import type {GpuSelection} from '../gpu-selector/gpu-selector.component';
import {GpuSelectorComponent} from '../gpu-selector/gpu-selector.component';

interface VariantGroup {
  variant: string;
  types: MachineType[];
}

/**
 * Machine configuration: family first, then the sizes that family offers.
 *
 * The family is the choice a user actually makes. A series has a processor, an
 * architecture and a price, and the widths under it are the same machine in
 * different amounts. Offering a hundred shapes in one flat list asked the
 * question the other way round, and grew a tab strip to cope.
 */
@Component({
  selector: 'app-shape-picker',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    GpuSelectorComponent,
    GpuTypePipe,
    MatButtonModule,
    MatFormFieldModule,
    MatIconModule,
    MatSelectModule,
    UpperCasePipe,
  ],
  template: `
    <div class="picker">
      <mat-form-field appearance="outline" class="family-field" subscriptSizing="dynamic">
        <mat-label>Machine family</mat-label>
        <mat-select [value]="family()" (valueChange)="selectFamily($event)">
          @for (option of families(); track option.family) {
            <mat-option [value]="option.family">
              {{ option.family | uppercase }} · {{ option.label }}
            </mat-option>
          }
        </mat-select>
      </mat-form-field>

      @if (custom(); as name) {
        <div class="custom">
          <mat-icon>tune</mat-icon>
          <span class="mono">{{ name }}</span>
          <span class="custom-note">built on demand by Compute Engine</span>
        </div>
      }

      <div class="sizes">
        @for (group of variantGroups(); track group.variant) {
          @if (variantGroups().length > 1) {
            <div class="variant">{{ group.variant }}</div>
          }
          @for (mt of group.types; track mt.id) {
            <button
              type="button"
              class="size"
              [class.selected]="mt.id === selectedId()"
              (click)="selectMachineType(mt)">
              <span class="radio"></span>
              <span class="sname mono">{{ mt.id }}</span>
              <span class="specs num">
                {{ mt.vcpus }} vCPU · {{ mt.memoryGb }} GB · holds {{ mt.fit }}
                @if (mt.gpuMode === 'integrated' && mt.integratedGpuType) {
                  · {{ mt.integratedGpuCount }}× {{ mt.integratedGpuType | appGpuType }}
                }
              </span>
            </button>
          }
        }
        <button type="button" mat-stroked-button class="custom-open" (click)="openBuilder()">
          <mat-icon>tune</mat-icon>
          Custom…
        </button>
      </div>

      @if (selectedMachineType(); as mt) {
        <app-gpu-selector [machineType]="mt" (selectionChange)="onGpuChange($event)" />
      }
    </div>
  `,
  styles: `
    .picker {
      width: 100%;
      display: flex;
      flex-direction: column;
      gap: var(--cco-space-16);
    }

    .num {
      font-variant-numeric: tabular-nums;
    }

    .mono {
      font-family: var(--cco-font-mono);
    }

    .family-field {
      max-width: 420px;
    }

    .sizes {
      display: flex;
      flex-direction: column;
      gap: 2px;
    }

    .variant {
      margin: var(--cco-space-12) 0 var(--cco-space-4);
      font-size: var(--cco-text-label);
      font-weight: 600;
      text-transform: uppercase;
      letter-spacing: 0.5px;
      color: var(--cco-text-secondary);
    }

    .size {
      display: flex;
      align-items: center;
      gap: var(--cco-space-12);
      width: 100%;
      padding: var(--cco-space-12) var(--cco-space-16);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-sm);
      background: var(--cco-surface);
      text-align: left;
      cursor: pointer;
      transition: border-color var(--cco-duration-base), background var(--cco-duration-base);
    }

    .size:hover {
      border-color: var(--cco-accent);
    }

    .size.selected {
      border-color: var(--cco-accent);
      background: var(--cco-accent-surface);
    }

    .radio {
      width: 16px;
      height: 16px;
      border-radius: 50%;
      border: 2px solid var(--cco-text-secondary);
      flex-shrink: 0;
      position: relative;
    }

    .size.selected .radio {
      border-color: var(--cco-accent);
    }

    .size.selected .radio::after {
      content: '';
      position: absolute;
      inset: 2px;
      border-radius: 50%;
      background: var(--cco-accent);
    }

    .sname {
      font-size: var(--cco-text-sm);
      color: var(--cco-text);
      min-width: 200px;
    }

    .specs {
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .custom {
      display: flex;
      align-items: center;
      gap: var(--cco-space-8);
      padding: var(--cco-space-8) var(--cco-space-12);
      border-radius: var(--cco-radius-sm);
      background: var(--cco-accent-surface);
      font-size: var(--cco-text-sm);
      color: var(--cco-accent);
    }

    .custom-note {
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-label);
    }

    .custom-open {
      align-self: flex-start;
      margin-top: var(--cco-space-8);
    }
  `,
})
export class ShapePickerComponent {
  private readonly dialog = inject(MatDialog);

  readonly machineTypes = input<MachineType[]>([]);
  readonly selectedType = input<string>('');

  readonly configChange = output<MachineConfigOutput>();

  /**
   * A custom shape the builder resolved. No list carries one, because Compute
   * Engine synthesizes the machine on demand, so the name it validated is the
   * whole of what the picker holds about it.
   */
  protected readonly custom = signal('');

  /** Attach state mirrored from the GPU selector (empty/0 = CPU-only). */
  private readonly gpuType = linkedSignal<string, string>({
    source: () => this.selectedId(),
    computation: () => '',
  });
  private readonly gpuCount = linkedSignal<string, number>({
    source: () => this.selectedId(),
    computation: () => 0,
  });

  /**
   * Curation is enforced here rather than at the call sites. The list arrives
   * as an input, so this component cannot assume the caller asked the API for
   * enabled types only. Every derived view below reads it, the selection
   * included, so a withheld shape cannot be emitted.
   */
  private readonly offered = computed(() => this.machineTypes().filter(mt => mt.isEnabled));

  readonly families = computed(() => {
    const byFamily = new Map<string, MachineType[]>();
    for (const mt of this.offered()) {
      byFamily.set(mt.family, [...(byFamily.get(mt.family) ?? []), mt]);
    }
    return [...byFamily.entries()]
      .map(([family, types]) => ({family, types, label: familyLabel(types)}))
      .sort(
        (a, b) => a.types[0].sortOrder - b.types[0].sortOrder || a.family.localeCompare(b.family)
      );
  });

  /** The family in view, seeded by the catalog's default or the caller's pick. */
  protected readonly family = linkedSignal<string, string>({
    source: () => this.initialType()?.family ?? '',
    computation: (family, previous) => previous?.value || family,
  });

  private readonly initialType = computed(() => {
    const types = this.offered();
    const requested = this.selectedType();
    return (
      (requested ? types.find(mt => mt.id === requested) : undefined) ??
      types.find(mt => mt.isDefault) ??
      types[0]
    );
  });

  readonly selectedId = linkedSignal<string, string>({
    source: () => this.initialType()?.id ?? '',
    computation: (id, previous) => previous?.value || id,
  });

  private readonly familyTypes = computed(() =>
    this.offered()
      .filter(mt => mt.family === this.family())
      .sort((a, b) => a.vcpus - b.vcpus || a.id.localeCompare(b.id))
  );

  readonly variantGroups = computed<VariantGroup[]>(() => {
    const groups = new Map<string, MachineType[]>();
    for (const mt of this.familyTypes()) {
      const variant = variantOf(mt.id);
      groups.set(variant, [...(groups.get(variant) ?? []), mt]);
    }
    return [...groups.entries()]
      .map(([variant, types]) => ({variant, types}))
      .sort((a, b) => a.variant.localeCompare(b.variant));
  });

  readonly selectedMachineType = computed(
    () => this.offered().find(mt => mt.id === this.selectedId()) ?? null
  );

  selectFamily(family: string): void {
    this.family.set(family);
    const first = this.familyTypes()[0];
    if (first) {
      this.selectMachineType(first);
    }
  }

  selectMachineType(mt: MachineType): void {
    this.custom.set('');
    this.selectedId.set(mt.id);
    this.emit();
  }

  /**
   * Opens the builder over the family in view. It commits nothing. A custom
   * shape needs no catalog row, so what comes back is a name Compute Engine has
   * already agreed to build.
   */
  openBuilder(): void {
    const data: CustomShapeBuilderData = {
      families: this.families().map(option => option.family),
      initialFamily: this.family(),
    };
    this.dialog
      .open(CustomShapeBuilderComponent, {data, width: '520px'})
      .afterClosed()
      .subscribe((machineType: string | undefined) => {
        if (machineType) {
          this.custom.set(machineType);
          this.selectedId.set(machineType);
          this.configChange.emit({machineType, gpuType: '', gpuCount: 0});
        }
      });
  }

  onGpuChange(selection: GpuSelection): void {
    this.gpuType.set(selection.gpuType);
    this.gpuCount.set(selection.gpuCount);
    this.emit();
  }

  private emit(): void {
    const mt = this.selectedMachineType();
    if (mt) {
      this.configChange.emit({
        machineType: mt.id,
        gpuType: this.gpuType(),
        gpuCount: this.gpuCount(),
      });
    }
  }
}

/**
 * The variant a machine type names: its id with the family and the width taken
 * out, so "c4a-highmem-96-metal" groups under highmem-metal.
 */
function variantOf(machineType: string): string {
  const segments = machineType
    .split('-')
    .slice(1)
    .filter(segment => !/^\d+$/.test(segment));
  return segments.join('-') || 'standard';
}

/** What the family offers, in the two terms a chooser compares on. */
function familyLabel(types: MachineType[]): string {
  const cpu = types[0].cpuType;
  const sizes = types.length === 1 ? '1 size' : `${types.length} sizes`;
  return cpu ? `${cpu}, ${sizes}` : sizes;
}
