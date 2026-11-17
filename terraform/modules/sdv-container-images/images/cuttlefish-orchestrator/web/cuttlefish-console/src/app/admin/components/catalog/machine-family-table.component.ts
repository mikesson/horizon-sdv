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
import {MatButtonModule} from '@angular/material/button';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatSelectModule} from '@angular/material/select';
import {MatSlideToggleModule} from '@angular/material/slide-toggle';
import {MatTooltipModule} from '@angular/material/tooltip';
import type {AdminFamily, FamilyShape} from '../../models/machine-family.model';
import type {CapacityChange} from './family-capacity.component';
import {FamilyCapacityComponent} from './family-capacity.component';
import type {ExclusionChange} from './family-exclusions.component';
import {FamilyExclusionsComponent} from './family-exclusions.component';
import {FamilyShapeMatrixComponent} from './family-shape-matrix.component';

/** One family admitted or withdrawn, which is the whole of machine curation. */
export interface FamilyToggle {
  family: AdminFamily;
  isEnabled: boolean;
}

/** The catalog default moved to another shape of a family. */
export interface DefaultShapeChoice {
  family: AdminFamily;
  machineType: string;
}

interface Row {
  family: AdminFamily;
  /** "62 shapes · 41 viable", the server's two counts. */
  counts: string;
  /** The shapes the default may move to: viable, and not withheld. */
  choices: FamilyShape[];
  defaultShape: string;
  gpuLabel: string;
}

/**
 * Machine curation, one row per family.
 *
 * The family is the unit because that is what the operator decides: admitting a
 * series is one act, and every shape under it arrives from Compute Engine
 * already sized. A table of shapes would ask the same question a hundred times
 * and would go stale the morning Google ships a generation.
 */
@Component({
  selector: 'app-machine-family-table',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    FamilyCapacityComponent,
    FamilyExclusionsComponent,
    FamilyShapeMatrixComponent,
    MatButtonModule,
    MatFormFieldModule,
    MatIconModule,
    MatSelectModule,
    MatSlideToggleModule,
    MatTooltipModule,
  ],
  template: `
    @for (row of rows(); track row.family.family) {
      <div class="family" [class.withdrawn]="!row.family.isEnabled">
        <div class="head">
          <button
            type="button"
            class="expander"
            [attr.aria-expanded]="expanded() === row.family.family"
            [attr.aria-label]="'Shapes in ' + row.family.family"
            (click)="toggleExpanded(row.family.family)">
            <mat-icon class="chevron" [class.open]="expanded() === row.family.family">
              expand_more
            </mat-icon>
          </button>

          <div class="ident">
            <span class="fname mono">{{ row.family.family }}</span>
            <span class="fsub">{{ row.family.name }}</span>
          </div>

          <span class="chip arch">{{ row.family.architecture }}</span>
          <span class="cpu">{{ row.family.cpuType || 'CPU unstated' }}</span>
          <span class="disk mono">{{ row.family.bootDiskType || 'zonal default' }}</span>
          <span class="counts num">{{ row.counts }}</span>

          <button
            type="button"
            mat-button
            class="gpu"
            [disabled]="row.family.gpuMode !== 'attachable'"
            [matTooltip]="
              row.family.gpuMode === 'attachable'
                ? 'Change what a GPU host of this family gets'
                : 'GPUs do not attach to this family'
            "
            (click)="editGpuPolicy.emit(row.family)">
            {{ row.gpuLabel }}
          </button>

          <mat-form-field appearance="outline" subscriptSizing="dynamic" class="default-select">
            <mat-label>Default shape</mat-label>
            <mat-select
              [value]="row.defaultShape"
              [disabled]="row.choices.length === 0"
              (valueChange)="chooseDefault(row.family, $event)">
              <mat-option value="">No default here</mat-option>
              @for (shape of row.choices; track shape.machineType) {
                <mat-option [value]="shape.machineType">{{ shape.machineType }}</mat-option>
              }
            </mat-select>
          </mat-form-field>

          <button
            type="button"
            mat-button
            class="sort"
            matTooltip="Where this family sits in the pickers"
            (click)="changeSortOrder.emit(row.family)">
            {{ row.family.sortOrder }}
            <mat-icon>edit</mat-icon>
          </button>

          <mat-slide-toggle
            [checked]="row.family.isEnabled"
            [attr.aria-label]="'Offer the ' + row.family.family + ' family'"
            (change)="toggleEnabled.emit({family: row.family, isEnabled: $event.checked})" />
        </div>

        @if (expanded() === row.family.family) {
          <div class="body">
            <app-family-shape-matrix [shapes]="row.family.shapes" />
            <app-family-exclusions
              [shapes]="row.family.shapes"
              (exclusionChange)="exclusionChange.emit($event)" />
            <app-family-capacity
              [shapes]="row.family.shapes"
              (capacityChange)="capacityChange.emit($event)" />
          </div>
        }
      </div>
    } @empty {
      <p class="empty">
        No machine families are curated yet. Admitting one is what makes the catalog fetch its
        shapes from Compute Engine.
      </p>
    }
  `,
  styles: `
    :host {
      display: block;
    }

    .num {
      font-variant-numeric: tabular-nums;
    }

    .mono {
      font-family: var(--cco-font-mono);
    }

    .family {
      border-bottom: 1px solid var(--cco-border);
    }

    .family.withdrawn .ident,
    .family.withdrawn .counts {
      color: var(--cco-text-secondary);
    }

    .head {
      display: flex;
      align-items: center;
      gap: var(--cco-space-12);
      padding: var(--cco-space-8) 0;
      flex-wrap: wrap;
    }

    .expander {
      display: inline-flex;
      border: none;
      background: none;
      cursor: pointer;
      padding: 0;
      color: var(--cco-text-secondary);
    }

    .chevron {
      transform: rotate(-90deg);
      transition: transform var(--cco-duration-fast) ease;
    }

    .chevron.open {
      transform: rotate(0deg);
    }

    .ident {
      display: flex;
      flex-direction: column;
      min-width: 150px;
    }

    .fname {
      font-size: var(--cco-text-body);
      text-transform: uppercase;
    }

    .fsub {
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .chip {
      font-size: var(--cco-text-label);
      font-weight: 500;
      text-transform: uppercase;
      letter-spacing: 0.03em;
      border-radius: var(--cco-radius-pill);
      padding: 1px 10px;
      background: var(--cco-surface-variant);
      color: var(--cco-text-secondary);
    }

    .cpu,
    .disk,
    .counts {
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
    }

    .cpu {
      min-width: 150px;
    }

    .disk {
      min-width: 150px;
    }

    .counts {
      min-width: 140px;
    }

    .gpu {
      font-size: var(--cco-text-sm);
    }

    .default-select {
      width: 210px;
      font-size: var(--cco-text-sm);
    }

    .sort mat-icon {
      font-size: var(--cco-icon-sm);
      width: var(--cco-icon-sm);
      height: var(--cco-icon-sm);
      color: var(--cco-text-secondary);
    }

    .body {
      padding: 0 0 var(--cco-space-16) var(--cco-space-32);
      display: flex;
      flex-direction: column;
      gap: var(--cco-space-8);
    }

    .empty {
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-sm);
    }
  `,
})
export class MachineFamilyTableComponent {
  readonly families = input.required<AdminFamily[]>();

  readonly toggleEnabled = output<FamilyToggle>();
  readonly chooseDefaultShape = output<DefaultShapeChoice>();
  readonly changeSortOrder = output<AdminFamily>();
  readonly editGpuPolicy = output<AdminFamily>();
  readonly capacityChange = output<CapacityChange>();
  readonly exclusionChange = output<ExclusionChange>();

  /** The one open family, by series; '' when every row is collapsed. */
  protected readonly expanded = signal('');

  protected readonly rows = computed<Row[]>(() =>
    this.families().map(family => ({
      family,
      counts: countsLabel(family),
      choices: family.shapes.filter(shape => shape.viable && !shape.isExcluded),
      defaultShape: family.shapes.find(shape => shape.isDefault)?.machineType ?? '',
      gpuLabel: gpuLabel(family),
    }))
  );

  protected toggleExpanded(family: string): void {
    this.expanded.update(open => (open === family ? '' : family));
  }

  protected chooseDefault(family: AdminFamily, machineType: string): void {
    // The empty option is the current state of a family holding no default, not
    // a move: the default is elected onto a shape, never cleared off one.
    if (machineType) {
      this.chooseDefaultShape.emit({family, machineType});
    }
  }
}

/** Both counts, because the gap between them is the interesting number. */
function countsLabel(family: AdminFamily): string {
  const shapes = family.shapeCount === 1 ? '1 shape' : `${family.shapeCount} shapes`;
  return `${shapes} · ${family.viableCount} viable`;
}

function gpuLabel(family: AdminFamily): string {
  if (family.gpuMode !== 'attachable') {
    return 'No attached GPU';
  }
  const type = family.defaultGpuType || 'unset';
  return `up to ${family.maxGpus} × ${type}`;
}
