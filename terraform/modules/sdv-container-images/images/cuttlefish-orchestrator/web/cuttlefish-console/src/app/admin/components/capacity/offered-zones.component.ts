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

import {ChangeDetectionStrategy, Component, computed, input, output} from '@angular/core';
import {MatSlideToggleModule} from '@angular/material/slide-toggle';
import {MatTooltipModule} from '@angular/material/tooltip';
import type {CapacityZone} from '../../models/capacity.model';
import type {AdminZone} from '../../models/catalog-admin.model';
import type {ZoneToggle} from './zone-board.component';
import {shapesLabel, shapesTooltip} from './zone-shapes';

/** A zone Compute Engine offers that the fleet does not place into yet. */
interface OfferedZone {
  zone: string;
  status: string;
  shapes: string;
  shapesTooltip: string;
  enabled: boolean;
}

/**
 * The zones Compute Engine offers this project that the placement space does
 * not hold: nothing has run in them and no failure was recorded there, so they
 * are not placement state and carry no verdict. Enabling one puts it on the
 * board, which is the only thing this list is for.
 */
@Component({
  selector: 'app-offered-zones',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatSlideToggleModule, MatTooltipModule],
  template: `
    @if (offered().length > 0) {
      <details class="offered">
        <summary>{{ summary() }}</summary>
        <p class="offered-hint">
          Not in the placement space: nothing has run in them and no failure was recorded there.
          Enabling one puts it on the board.
        </p>
        @for (zone of offered(); track zone.zone) {
          <div class="offered-row">
            <span class="mono">{{ zone.zone }}</span>
            <span class="offered-status">{{ zone.status || 'status unreported' }}</span>
            <span class="offered-shapes" [matTooltip]="zone.shapesTooltip">{{ zone.shapes }}</span>
            <mat-slide-toggle
              [checked]="zone.enabled"
              [attr.aria-label]="'Place into ' + zone.zone"
              (change)="toggleZone.emit({zone: zone.zone, enabled: $event.checked})" />
          </div>
        }
      </details>
    }
  `,
  styles: `
    .offered {
      display: block;
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
    }

    .offered summary {
      cursor: pointer;
    }

    .offered-hint {
      margin: var(--cco-space-8) 0;
    }

    .offered-row {
      display: grid;
      grid-template-columns: minmax(160px, 1fr) 120px 1fr auto;
      align-items: center;
      gap: var(--cco-space-12);
      padding: var(--cco-space-4) 0;
      border-bottom: 1px solid var(--cco-border);
    }

    .mono {
      font-family: var(--cco-font-mono);
    }

    .offered-status,
    .offered-shapes {
      font-size: var(--cco-text-label);
    }
  `,
})
export class OfferedZonesComponent {
  /** Every zone Compute Engine offers the project, with the shapes it stocks. */
  readonly catalog = input.required<AdminZone[]>();
  /** The placement space, whose zones are on the board and not offered again. */
  readonly placed = input.required<CapacityZone[]>();
  /** The machine types a zone's offer is counted against, as the server curates them. */
  readonly curatedShapes = input.required<string[]>();

  readonly toggleZone = output<ZoneToggle>();

  protected readonly offered = computed<OfferedZone[]>(() => {
    const onBoard = new Set(this.placed().map(zone => zone.zone));
    const curated = this.curatedShapes().length;
    return this.catalog()
      .filter(zone => !onBoard.has(zone.zone))
      .map(zone => ({
        zone: zone.zone,
        status: zone.status,
        shapes: shapesLabel(zone, curated),
        shapesTooltip: shapesTooltip(zone),
        enabled: zone.isEnabled,
      }));
  });

  protected readonly summary = computed(() => {
    const count = this.offered().length;
    const zones = count === 1 ? '1 more zone' : `${count} more zones`;
    return `${zones} Compute Engine offers this project`;
  });
}
