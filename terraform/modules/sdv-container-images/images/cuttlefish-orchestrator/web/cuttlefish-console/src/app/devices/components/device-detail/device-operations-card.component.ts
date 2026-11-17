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
import {ChangeDetectionStrategy, Component, computed, input} from '@angular/core';
import {MatCardModule} from '@angular/material/card';
import {MatIconModule} from '@angular/material/icon';
import {MatProgressSpinnerModule} from '@angular/material/progress-spinner';
import {MatTooltipModule} from '@angular/material/tooltip';
import type {Operation, OperationState} from '../../models/operation.model';
import {
  operationFailureDetail,
  operationState,
  operationVerbLabel,
} from '../../models/operation.model';

/** One row's worth of rendered operation state. */
interface OperationRow {
  readonly id: string;
  readonly verb: string;
  readonly instance: string;
  readonly state: OperationState;
  readonly startedAt: Date;
  readonly detail: string;
  readonly attempt?: number;
}

/**
 * Recent restart and powerwash outcomes for a device.
 *
 * This card is why those verbs are safe to make asynchronous. They leave the
 * device row identical before and after, so the status poll can never report
 * them. Before handles existed a failure produced a loud 5xx and nothing else,
 * and a durable operation nobody looks at would trade that for silence. This is
 * where the outcome lands instead.
 */
@Component({
  selector: 'app-device-operations-card',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [DatePipe, MatCardModule, MatIconModule, MatProgressSpinnerModule, MatTooltipModule],
  template: `
    <mat-card class="operations-card">
      <mat-card-header>
        <mat-card-title>Operations</mat-card-title>
      </mat-card-header>
      <mat-card-content>
        @if (rows().length === 0) {
          <p class="empty">No restart or powerwash has run on this device.</p>
        } @else {
          <ul class="op-list">
            @for (row of rows(); track row.id) {
              <li class="op-row" [class.failed]="row.state === 'failed'">
                <span class="op-icon">
                  @switch (row.state) {
                    @case ('running') {
                      <mat-spinner aria-label="Loading" diameter="16" />
                    }
                    @case ('failed') {
                      <mat-icon class="icon-failed">error</mat-icon>
                    }
                    @default {
                      <mat-icon class="icon-done">check_circle</mat-icon>
                    }
                  }
                </span>
                <span class="op-verb">{{ row.verb }}</span>
                <!-- Always a cell, so the grid stays aligned when only some
                     rows name an instance; empty at N=1, where naming the
                     device's only Android VM says nothing. -->
                <span class="op-instance">{{ row.instance }}</span>
                <span class="op-started">{{ row.startedAt | date: 'short' }}</span>
                @if (row.state === 'running' && row.attempt && row.attempt > 1) {
                  <span class="op-attempt" matTooltip="Retrying after a transient failure">
                    attempt {{ row.attempt }}
                  </span>
                }
                @if (row.detail) {
                  <span class="op-detail" [matTooltip]="row.detail">{{ row.detail }}</span>
                }
              </li>
            }
          </ul>
        }
      </mat-card-content>
    </mat-card>
  `,
  styles: `
    .op-list {
      list-style: none;
      margin: 0;
      padding: 0;
      display: flex;
      flex-direction: column;
      gap: 0.5rem;
    }
    .op-row {
      display: grid;
      grid-template-columns: 1.5rem 7rem auto auto 1fr;
      align-items: center;
      gap: 0.5rem;
      font-size: 0.875rem;
    }
    .op-icon {
      display: flex;
      align-items: center;
    }
    .op-verb {
      font-weight: 500;
      text-transform: capitalize;
    }
    .op-instance {
      font-family: monospace;
      font-size: 0.8125rem;
      color: var(--cco-text-secondary);
    }
    .op-started {
      color: var(--cco-text-secondary);
    }
    .op-attempt {
      color: var(--cco-warning);
    }
    .op-detail {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      color: var(--cco-error);
    }
    .icon-failed {
      color: var(--cco-error);
      font-size: 1rem;
      width: 1rem;
      height: 1rem;
    }
    .icon-done {
      color: var(--cco-success);
      font-size: 1rem;
      width: 1rem;
      height: 1rem;
    }
    .empty {
      color: var(--cco-text-secondary);
      margin: 0;
    }
  `,
})
export class DeviceOperationsCardComponent {
  readonly operations = input.required<Operation[]>();

  /**
   * How many Android VMs the device has. Every per-instance verb records the
   * instance it acted on, including the sole one, so the count is what decides
   * whether repeating it here informs anyone.
   */
  readonly instanceCount = input(1);

  protected readonly rows = computed<OperationRow[]>(() =>
    this.operations().map(op => ({
      id: op.id,
      verb: operationVerbLabel(op),
      instance: this.instanceCount() > 1 ? (op.metadata.instanceName ?? '') : '',
      state: operationState(op),
      startedAt: op.metadata.createTime,
      // A settled success has nothing to say; the detail column is for the two
      // cases a user needs to read.
      detail: operationState(op) === 'done' ? '' : operationFailureDetail(op),
      attempt: op.metadata.attempt,
    }))
  );
}
