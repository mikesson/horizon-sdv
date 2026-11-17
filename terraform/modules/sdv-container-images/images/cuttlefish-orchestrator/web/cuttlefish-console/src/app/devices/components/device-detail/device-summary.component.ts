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
import {MatChipsModule} from '@angular/material/chips';
import {RouterModule} from '@angular/router';
import {SummaryFactComponent} from '../../../shared/components/summary-pane/summary-fact.component';
import {SummaryPaneComponent} from '../../../shared/components/summary-pane/summary-pane.component';
import {DEVICE_STATUS} from '../../../shared/constants';
import {GpuModePipe} from '../../../shared/pipes';
import type {Device} from '../../models/device.model';
import {instanceBuildRefs} from '../../models/instance-builds';
import {LifecycleTimerComponent} from '../lifecycle/lifecycle-timer.component';
import {PinButtonComponent} from '../lifecycle/pin-button.component';

/** What the device is doing, for the detail page's supporting pane. */
@Component({
  selector: 'app-device-summary',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    DatePipe,
    RouterModule,
    MatChipsModule,
    GpuModePipe,
    LifecycleTimerComponent,
    PinButtonComponent,
    SummaryPaneComponent,
    SummaryFactComponent,
  ],
  template: `
    @let d = device();
    <app-summary-pane heading="Summary">
      <app-summary-fact label="Status">
        <mat-chip [class]="d.status">{{ d.status }}</mat-chip>
      </app-summary-fact>

      <app-summary-fact label="GPU mode">
        <mat-chip [class]="d.gpuMode">{{ d.gpuMode | appGpuMode }}</mat-chip>
      </app-summary-fact>

      <app-summary-fact label="Host">
        <a [routerLink]="['/hosts', d.hostId]">{{ d.hostName || d.hostId }}</a>
      </app-summary-fact>

      @if (d.hostZone) {
        <app-summary-fact label="Zone">{{ d.hostZone }}</app-summary-fact>
      }

      @if (d.buildTarget) {
        <app-summary-fact label="Build target">{{ d.buildTarget }}</app-summary-fact>
      }

      @if (instanceBuilds().length > 0) {
        <app-summary-fact label="Instance builds">
          <ul class="instance-builds">
            @for (ref of instanceBuilds(); track ref.instance) {
              <li>
                <span class="instance-name">{{ ref.instance }}</span>
                {{ ref.kind === 'registered' ? 'build ' + ref.value : ref.value }}
              </li>
            }
          </ul>
        </app-summary-fact>
      }

      @if (d.lifecycle) {
        <app-summary-fact label="Lifecycle">
          <app-lifecycle-timer [lifecycle]="d.lifecycle" [showExtendButton]="false" />
        </app-summary-fact>
      }

      <app-summary-fact label="Pin">
        <app-pin-button
          [lifecycle]="d.lifecycle"
          [disabled]="d.status !== DEVICE_STATUS.RUNNING"
          (pin)="pin.emit()"
          (unpin)="unpin.emit()" />
      </app-summary-fact>

      <app-summary-fact label="Created">{{ d.createdAt | date: 'medium' }}</app-summary-fact>

      @if (d.startedAt) {
        <app-summary-fact label="Started">{{ d.startedAt | date: 'medium' }}</app-summary-fact>
      }

      @if (d.stoppedAt) {
        <app-summary-fact label="Stopped">{{ d.stoppedAt | date: 'medium' }}</app-summary-fact>
      }

      @if (d.tags?.length) {
        <app-summary-fact label="Tags">
          <span class="tag-chips">
            @for (tag of d.tags; track tag) {
              <mat-chip>{{ tag }}</mat-chip>
            }
          </span>
        </app-summary-fact>
      }
    </app-summary-pane>
  `,
  styles: `
    :host {
      display: block;
    }

    .instance-builds {
      margin: 0;
      padding: 0;
      list-style: none;
      overflow-wrap: anywhere;
    }

    .instance-name {
      font-weight: 500;
    }

    .tag-chips {
      display: flex;
      flex-wrap: wrap;
      gap: var(--cco-space-4);
    }
  `,
})
export class DeviceSummaryComponent {
  protected readonly DEVICE_STATUS = DEVICE_STATUS;

  readonly device = input.required<Device>();

  /** Declared in the launch config; a device-build instance is not listed. */
  protected readonly instanceBuilds = computed(() => instanceBuildRefs(this.device().envConfig));
  readonly pin = output<void>();
  readonly unpin = output<void>();
}
