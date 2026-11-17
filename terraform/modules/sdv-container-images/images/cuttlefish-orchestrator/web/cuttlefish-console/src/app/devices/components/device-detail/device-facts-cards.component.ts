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
import {ChangeDetectionStrategy, Component, input} from '@angular/core';
import {MatCardModule} from '@angular/material/card';
import {FactRowComponent} from '../../../shared/components/fact-row/fact-row.component';
import type {Device} from '../../models/device.model';
import {PIN_REASON_LABELS} from '../../models/device.model';

/**
 * Why a device is pinned. Everything else it *is* (host, build, timestamps,
 * lifecycle) is the supporting pane's job, next door in app-device-summary.
 */
@Component({
  selector: 'app-device-facts-cards',
  imports: [DatePipe, MatCardModule, FactRowComponent],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @let d = device();
    @if (d.lifecycle.mode === 'pinned') {
      <mat-card class="lifecycle-card">
        <mat-card-header>
          <mat-card-title>Pin Details</mat-card-title>
        </mat-card-header>
        <mat-card-content>
          @if (d.lifecycle.pinnedReason) {
            <app-fact-row label="Reason">{{ getPinReasonLabel(d.lifecycle.pinnedReason) }}</app-fact-row>
          }
          @if (d.lifecycle.pinnedReasonDetail) {
            <app-fact-row label="Details">{{ d.lifecycle.pinnedReasonDetail }}</app-fact-row>
          }
          @if (d.lifecycle.pinnedBy) {
            <app-fact-row label="Pinned by">{{ d.lifecycle.pinnedBy }}</app-fact-row>
          }
          @if (d.lifecycle.pinnedAt) {
            <app-fact-row label="Pinned at">{{ d.lifecycle.pinnedAt | date: 'medium' }}</app-fact-row>
          }
          @if (d.lifecycle.pinnedUntil) {
            <app-fact-row label="Pinned until">{{ d.lifecycle.pinnedUntil | date: 'medium' }}</app-fact-row>
          }
        </mat-card-content>
      </mat-card>
    }
  `,
})
export class DeviceFactsCardsComponent {
  readonly device = input.required<Device>();

  getPinReasonLabel(reason: string): string {
    return PIN_REASON_LABELS[reason as keyof typeof PIN_REASON_LABELS] ?? reason;
  }
}
