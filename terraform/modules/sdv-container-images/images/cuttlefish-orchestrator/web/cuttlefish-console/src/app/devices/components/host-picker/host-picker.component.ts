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
import {MatCardModule} from '@angular/material/card';
import {MatIconModule} from '@angular/material/icon';
import type {Host} from '../../../hosts/models/host.model';
import {EmptyStateComponent} from '../../../shared/components/empty-state/empty-state.component';

/** Selectable-card list of host VMs with status dot and capacity. */
@Component({
  selector: 'app-host-picker',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatCardModule, MatIconModule, EmptyStateComponent],
  template: `
    @if (hosts().length === 0) {
      <app-empty-state
        variant="compact"
        icon="warning"
        message="No available hosts. Create a host first."
      />
    } @else {
      <div class="host-list" role="listbox" aria-label="Host VM selection">
        @for (host of hosts(); track host.id) {
          <mat-card
            class="host-card"
            [class.selected]="selectedId() === host.id"
            role="option"
            [attr.aria-selected]="selectedId() === host.id"
            [attr.aria-label]="host.name + ' in ' + host.zone + ', ' + host.currentDevices + ' of ' + host.maxDevices + ' devices'"
            tabindex="0"
            (click)="selectionChange.emit(host)"
            (keydown.enter)="selectionChange.emit(host)"
            (keydown.space)="selectionChange.emit(host); $event.preventDefault()">
            <div class="host-content">
              <div class="host-status" [class.running]="host.status === 'running'" [attr.aria-label]="host.status === 'running' ? 'Running' : 'Not running'"></div>
              <div class="host-info">
                <span class="host-name">{{ host.name }}</span>
                <span class="host-zone">{{ host.zone }}</span>
              </div>
              @if (host.gpuCount) {
                <div class="host-gpu">
                  <mat-icon>memory</mat-icon>
                  <span>{{ host.gpuCount }}x {{ gpuLabel(host.gpuType) }}</span>
                </div>
              }
              <span class="host-capacity" aria-hidden="true">{{ host.currentDevices }}/{{ host.maxDevices }}</span>
            </div>
          </mat-card>
        }
      </div>
    }
  `,
  styles: `
    .host-list {
      display: flex;
      flex-direction: column;
      gap: 8px;
    }

    .host-card {
      padding: 12px;
      cursor: pointer;
      border: 2px solid var(--cco-border);
      border-radius: 8px;
    }

    .host-card:hover { border-color: var(--cco-accent); }

    .host-card.selected {
      border-color: var(--cco-accent);
      background: var(--cco-accent-surface);
    }

    .host-content {
      display: flex;
      align-items: center;
      gap: 12px;
    }

    .host-status {
      width: 10px;
      height: 10px;
      border-radius: 50%;
      background: var(--cco-text-secondary);
    }

    .host-status.running { background: var(--cco-success); }

    .host-info {
      flex: 1;
      display: flex;
      flex-direction: column;
    }

    .host-name { font-weight: 500; font-size: 12px; }
    .host-zone { font-size: 11px; color: var(--cco-text-secondary); }
    .host-capacity { font-size: 12px; color: var(--cco-text-secondary); }

    .host-gpu {
      display: flex;
      align-items: center;
      gap: 4px;
      font-size: 11px;
      color: var(--cco-text-secondary);
    }

    .host-gpu mat-icon {
      font-size: 16px;
      width: 16px;
      height: 16px;
    }

  `,
})
export class HostPickerComponent {
  readonly hosts = input.required<Host[]>();
  readonly selectedId = input('');
  readonly selectionChange = output<Host>();

  protected gpuLabel(gpuType: string): string {
    const match = /tesla-([a-z0-9]+)/i.exec(gpuType);
    return match ? match[1].toUpperCase() : gpuType || 'GPU';
  }
}
