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

import {DatePipe} from '@angular/common';
import {ChangeDetectionStrategy, Component, computed, input, output} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatChipsModule} from '@angular/material/chips';
import {MatIconModule} from '@angular/material/icon';
import {MatTooltipModule} from '@angular/material/tooltip';
import {SummaryFactComponent} from '../../../shared/components/summary-pane/summary-fact.component';
import {SummaryPaneComponent} from '../../../shared/components/summary-pane/summary-pane.component';
import type {Host} from '../../models/host.model';
import {formatGpuType} from './gpu-type';

/** What the host is doing, for the detail page's supporting pane. */
@Component({
  selector: 'app-host-summary',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    DatePipe,
    MatButtonModule,
    MatChipsModule,
    MatIconModule,
    MatTooltipModule,
    SummaryPaneComponent,
    SummaryFactComponent,
  ],
  template: `
    @let h = host();
    <app-summary-pane heading="Summary">
      <app-summary-fact label="Status">
        <mat-chip [class]="h.status">{{ h.status }}</mat-chip>
      </app-summary-fact>

      <app-summary-fact label="Zone">{{ h.zone }}</app-summary-fact>

      <app-summary-fact label="GPU">{{ h.gpuCount }}× {{ gpuType() }}</app-summary-fact>

      <app-summary-fact label="Devices">
        <span [class.at-capacity]="h.currentDevices >= h.maxDevices">
          {{ h.currentDevices }} / {{ h.maxDevices }}
        </span>
        <button type="button"
          mat-icon-button
          class="capacity-edit"
          aria-label="Change device capacity"
          matTooltip="Change device capacity"
          (click)="editCapacity.emit()">
          <mat-icon>edit</mat-icon>
        </button>
      </app-summary-fact>

      <app-summary-fact label="VM type">
        <span class="badge" [class.spot]="h.provisioningModel === 'SPOT'">
          @if (h.provisioningModel === 'SPOT') {
            <mat-icon>savings</mat-icon>
            Spot
          } @else {
            <mat-icon>check_circle</mat-icon>
            Standard
          }
        </span>
        @if (h.provisioningModel === 'SPOT' && h.preemptionCount > 0) {
          <span class="aside-note">preempted {{ h.preemptionCount }}×</span>
        }
      </app-summary-fact>

      @if (h.lifecycle?.mode === 'protected') {
        <app-summary-fact label="Lifecycle">
          <span class="badge">
            <mat-icon>shield</mat-icon>
            Protected
          </span>
          <span class="aside-note">{{ h.lifecycle.protectedDeviceCount }} pinned</span>
        </app-summary-fact>
      }

      @if (h.lifecycle?.expiresAt; as expiresAt) {
        <app-summary-fact label="Lease expires">{{ expiresAt | date: 'medium' }}</app-summary-fact>
      }

      @if (h.vmTerminatesAt; as stopsAt) {
        <app-summary-fact label="VM stops">
          <span class="vm-stop">
            {{ stopsAt | date: 'medium' }}
            <span class="aside-note">A host restart resets this time.</span>
          </span>
        </app-summary-fact>
      }

      <app-summary-fact label="Created">{{ h.createdAt | date: 'medium' }}</app-summary-fact>

      <app-summary-fact label="Last updated">{{ h.updatedAt | date: 'medium' }}</app-summary-fact>
    </app-summary-pane>
  `,
  styles: `
    :host {
      display: block;
    }

    .at-capacity {
      color: var(--cco-error);
    }

    .capacity-edit mat-icon {
      font-size: var(--cco-icon-sm);
      width: var(--cco-icon-sm);
      height: var(--cco-icon-sm);
      color: var(--cco-text-secondary);
    }

    .badge {
      display: inline-flex;
      align-items: center;
      gap: var(--cco-space-4);
      color: var(--cco-accent);
      font-weight: 500;
    }

    .badge.spot {
      color: var(--cco-success);
    }

    .badge mat-icon {
      font-size: var(--cco-icon-md);
      width: var(--cco-icon-md);
      height: var(--cco-icon-md);
    }

    .aside-note {
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
    }

    .vm-stop {
      display: flex;
      flex-direction: column;
      gap: var(--cco-space-2);
    }

    /* The global ramp covers running/stopped/creating/error; a host also
       reports the transitional and preempted states, and a chip with no rule
       falls through to the generated palette. */
    mat-chip.mat-mdc-chip.provisioning,
    mat-chip.mat-mdc-chip.starting,
    mat-chip.mat-mdc-chip.stopping,
    mat-chip.mat-mdc-chip.deleting {
      background-color: var(--cco-accent-surface);
      --mat-chip-label-text-color: var(--cco-accent);
    }

    mat-chip.mat-mdc-chip.preempted {
      background-color: var(--cco-warning-surface);
      --mat-chip-label-text-color: var(--cco-warning);
    }

    mat-chip.mat-mdc-chip.deleted {
      background-color: var(--cco-surface-variant);
      --mat-chip-label-text-color: var(--cco-text-secondary);
    }
  `,
})
export class HostSummaryComponent {
  readonly host = input.required<Host>();
  readonly editCapacity = output<void>();

  protected readonly gpuType = computed(() => formatGpuType(this.host().gpuType));
}
