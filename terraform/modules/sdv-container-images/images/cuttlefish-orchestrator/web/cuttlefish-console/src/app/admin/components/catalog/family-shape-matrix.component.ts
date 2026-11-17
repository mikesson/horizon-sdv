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
import {MatTooltipModule} from '@angular/material/tooltip';
import type {FamilyShape} from '../../models/machine-family.model';

interface Cell {
  shape: FamilyShape;
  /** "30 GB". The width is the row, so the cell states what varies. */
  memory: string;
  /** "3 devices", or why the shape holds none. */
  fit: string;
  muted: boolean;
  title: string;
}

interface Row {
  vcpus: number;
  label: string;
  /** One entry per variant column; null where the family offers no such shape. */
  cells: Array<Cell | null>;
}

/**
 * What a family holds, as a grid: the machine's width down the side, the
 * variant across the top.
 *
 * Read-only, and that is the message. Discovery owns which shapes exist and how
 * big they are, because Compute Engine reported them and will report tomorrow's
 * the same way, so there is nothing here to curate. The one per-shape decision an
 * operator still makes is withholding a shape, and that lives in its own
 * control beside this grid rather than as a switch in every cell.
 */
@Component({
  selector: 'app-family-shape-matrix',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatTooltipModule],
  template: `
    @if (rows().length === 0) {
      <p class="empty">
        Compute Engine has reported no shapes for this family. Enabling it is what makes the
        catalog go and ask.
      </p>
    } @else {
      <div class="scroll">
        <table>
          <thead>
            <tr>
              <th scope="col" class="corner">vCPU</th>
              @for (variant of variants(); track variant) {
                <th scope="col">{{ variant }}</th>
              }
            </tr>
          </thead>
          <tbody>
            @for (row of rows(); track row.vcpus) {
              <tr>
                <th scope="row" class="size num">{{ row.label }}</th>
                @for (cell of row.cells; track $index) {
                  <td [class.muted]="cell?.muted">
                    @if (cell) {
                      <span class="mem num" [matTooltip]="cell.title">{{ cell.memory }}</span>
                      <span class="fit">{{ cell.fit }}</span>
                    } @else {
                      <span class="absent">-</span>
                    }
                  </td>
                }
              </tr>
            }
          </tbody>
        </table>
      </div>
      <p class="legend">
        Every shape Compute Engine offers here. A muted cell is a machine whose cores and memory
        hold no Android VM, so the catalog withholds it. A capacity override below admits it
        anyway.
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

    .scroll {
      overflow-x: auto;
    }

    table {
      border-collapse: collapse;
      font-size: var(--cco-text-sm);
    }

    th,
    td {
      border: 1px solid var(--cco-border);
      padding: var(--cco-space-4) var(--cco-space-8);
      text-align: left;
      white-space: nowrap;
    }

    thead th {
      font-weight: 500;
      font-size: var(--cco-text-label);
      text-transform: uppercase;
      letter-spacing: 0.03em;
      color: var(--cco-text-secondary);
      background: var(--cco-surface-variant);
    }

    .corner,
    .size {
      background: var(--cco-surface-variant);
      font-weight: 500;
      color: var(--cco-text-secondary);
    }

    td {
      display: table-cell;
    }

    .mem {
      display: block;
      color: var(--cco-text);
    }

    .fit {
      display: block;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    td.muted .mem,
    td.muted .fit {
      color: var(--cco-text-disabled);
    }

    .absent {
      color: var(--cco-text-disabled);
    }

    .legend,
    .empty {
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-label);
      margin: var(--cco-space-8) 0 0;
    }
  `,
})
export class FamilyShapeMatrixComponent {
  readonly shapes = input.required<FamilyShape[]>();

  protected readonly variants = computed(() => {
    const seen = new Set<string>();
    for (const shape of this.shapes()) {
      seen.add(variantOf(shape.machineType));
    }
    return [...seen].sort((a, b) => a.localeCompare(b));
  });

  protected readonly rows = computed<Row[]>(() => {
    const variants = this.variants();
    const byWidth = new Map<number, Map<string, FamilyShape>>();
    for (const shape of this.shapes()) {
      const row = byWidth.get(shape.vcpus) ?? new Map<string, FamilyShape>();
      row.set(variantOf(shape.machineType), shape);
      byWidth.set(shape.vcpus, row);
    }
    return [...byWidth.entries()]
      .sort(([a], [b]) => a - b)
      .map(([vcpus, row]) => ({
        vcpus,
        label: String(vcpus),
        cells: variants.map(variant => {
          const shape = row.get(variant);
          return shape ? cellOf(shape) : null;
        }),
      }));
  });
}

/**
 * The variant a machine type names, which is its id with the family and the
 * width taken out: "c4a-highmem-96-metal" is the highmem-metal variant at 96
 * vCPUs. The width comes off the shape's own vCPU count instead, so a family
 * whose names never carried one still gets a row.
 */
function variantOf(machineType: string): string {
  const segments = machineType
    .split('-')
    .slice(1)
    .filter(segment => !/^\d+$/.test(segment));
  return segments.join('-') || 'standard';
}

function cellOf(shape: FamilyShape): Cell {
  return {
    shape,
    memory: `${shape.memoryGb} GB`,
    fit: fitLabel(shape),
    muted: !shape.viable,
    title: shape.machineType,
  };
}

/**
 * What the machine holds, in devices. It is the server's number, never one
 * worked out here. A shape that holds none says so in the same breath as the
 * remedy, and an overridden one shows both numbers, the arithmetic and the
 * word that outranks it.
 */
function fitLabel(shape: FamilyShape): string {
  if (shape.maxDevicesOverride > 0) {
    return `computed ${shape.fit} · set by hand ${shape.maxDevicesOverride}`;
  }
  if (!shape.viable) {
    return 'no room for a device · set a capacity override to admit it';
  }
  if (shape.fit === 0) {
    return 'size unstated';
  }
  return shape.fit === 1 ? '1 device' : `${shape.fit} devices`;
}
