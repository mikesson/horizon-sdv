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

import {ChangeDetectionStrategy, Component, input, output} from '@angular/core';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatSelectModule} from '@angular/material/select';
import type {CapacityModel} from '../../../shared/models/requirements.model';

/**
 * The capacity choice for a host that automatic placement provisions, shared
 * by the Simple wizard and the Advanced editor. Only automatic placement sends
 * it, so callers hide it once a host is chosen.
 */
@Component({
  selector: 'app-capacity-field',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatFormFieldModule, MatSelectModule],
  template: `
    <mat-form-field appearance="outline" class="capacity-field">
      <mat-label>Capacity</mat-label>
      <mat-select [value]="capacity()" (selectionChange)="capacityChanged.emit($event.value)">
        <mat-option value="auto">Auto (platform policy decides)</mat-option>
        <mat-option value="standard">On-demand (never preempted)</mat-option>
        <mat-option value="spot">Spot (discounted, may be preempted)</mat-option>
      </mat-select>
    </mat-form-field>
    <p class="capacity-hint">
      Spot hosts cost a fraction of on-demand but Google Cloud can reclaim
      them at any time; the platform re-provisions the host and recreates
      its devices automatically.
    </p>
  `,
  styles: `
    :host {
      display: block;
    }

    .capacity-field {
      width: 100%;
      max-width: 360px;
    }

    .capacity-hint {
      margin: 4px 0 0;
      font-size: 12px;
      color: var(--cco-text-secondary);
      line-height: 1.4;
    }
  `,
})
export class CapacityFieldComponent {
  readonly capacity = input.required<CapacityModel>();
  readonly capacityChanged = output<CapacityModel>();
}
