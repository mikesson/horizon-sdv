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
import {ChangeDetectionStrategy, Component, inject, input} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatCardModule} from '@angular/material/card';
import {MatIconModule} from '@angular/material/icon';
import {MatProgressSpinnerModule} from '@angular/material/progress-spinner';
import {MatTooltipModule} from '@angular/material/tooltip';
import {SNAPSHOT_STATE} from '../../../shared/constants';
import type {Device} from '../../models/device.model';
import type {Snapshot} from '../../models/snapshot.model';
import {DeviceActionsService} from '../../services/device-actions.service';
import {DeviceStore} from '../../store/device.signal-store';

/**
 * The device's snapshot inventory, with delete.
 *
 * Take and restore stay in the API and cfctl. A restored guest's player,
 * screenshots and recordings stay dead until a restart, so the console offers
 * only what keeps the device usable here.
 */
@Component({
  selector: 'app-device-snapshots-card',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    DatePipe,
    MatButtonModule,
    MatCardModule,
    MatIconModule,
    MatProgressSpinnerModule,
    MatTooltipModule,
  ],
  template: `
    <mat-card class="snapshots-card">
      <mat-card-header>
        <mat-card-title>Snapshots</mat-card-title>
        <button type="button"
          mat-icon-button
          aria-label="Refresh snapshots"
          matTooltip="Refresh"
          (click)="store.loadSnapshots(device().id)"
        >
          <mat-icon>refresh</mat-icon>
        </button>
      </mat-card-header>
      <mat-card-content>
        <p class="cli-note">
          <mat-icon class="note-icon">info</mat-icon>
          <span>
            Snapshots are taken and restored with <code>cfctl device snapshot</code> for ADB and CI
            use, because a restored device never serves its screen again.
          </span>
        </p>
        @if (snapshots().length === 0) {
          <p class="empty">No snapshots.</p>
        } @else {
          <ul class="snap-list">
            @for (snap of snapshots(); track snap.id) {
              <li class="snap-row" [class.lost]="snap.state === SNAPSHOT_STATE.LOST">
                <span class="snap-icon">
                  @switch (snap.state) {
                    @case (SNAPSHOT_STATE.CREATING) {
                      <mat-spinner aria-label="Loading" diameter="16" />
                    }
                    @case (SNAPSHOT_STATE.LOST) {
                      <mat-icon
                        class="icon-lost"
                        matTooltip="The host's disk was recreated after this snapshot was taken; its data is gone"
                        >link_off</mat-icon
                      >
                    }
                    @default {
                      <mat-icon class="icon-ready">photo_camera</mat-icon>
                    }
                  }
                </span>
                <span class="snap-id" [matTooltip]="snap.id">{{ snap.id.slice(0, 8) }}</span>
                <span class="snap-state">{{ snap.state }}</span>
                <span class="snap-created">{{ snap.createdAt | date: 'short' }}</span>
                <button type="button"
                  mat-icon-button
                  aria-label="Delete this snapshot"
                  matTooltip="Delete snapshot"
                  (click)="actions.deleteSnapshot(device(), snap)"
                >
                  <mat-icon>delete</mat-icon>
                </button>
              </li>
            }
          </ul>
        }
      </mat-card-content>
    </mat-card>
  `,
  styles: `
    mat-card-header {
      display: flex;
      align-items: center;
      justify-content: space-between;
    }
    .snap-list {
      list-style: none;
      margin: 0;
      padding: 0;
      display: flex;
      flex-direction: column;
      gap: 0.25rem;
    }
    .snap-row {
      display: grid;
      grid-template-columns: 1.5rem 6rem 5rem 1fr auto;
      align-items: center;
      gap: 0.5rem;
      font-size: 0.875rem;
    }
    .snap-row.lost {
      color: var(--cco-text-secondary);
    }
    .snap-icon {
      display: flex;
      align-items: center;
    }
    .snap-id {
      font-family: monospace;
    }
    .snap-state {
      text-transform: capitalize;
    }
    .snap-created {
      color: var(--cco-text-secondary);
    }
    .icon-ready {
      color: var(--cco-success);
      font-size: 1rem;
      width: 1rem;
      height: 1rem;
    }
    .icon-lost {
      color: var(--cco-text-secondary);
      font-size: 1rem;
      width: 1rem;
      height: 1rem;
    }
    .empty {
      color: var(--cco-text-secondary);
      margin: 0;
    }
    .cli-note {
      display: flex;
      align-items: flex-start;
      gap: 0.5rem;
      margin: 0 0 0.75rem;
      color: var(--cco-text-secondary);
      font-size: 0.875rem;
    }
    .note-icon {
      flex: none;
      font-size: 1rem;
      width: 1rem;
      height: 1rem;
    }
  `,
})
export class DeviceSnapshotsCardComponent {
  protected readonly actions = inject(DeviceActionsService);
  protected readonly store = inject(DeviceStore);

  readonly device = input.required<Device>();
  readonly snapshots = input.required<Snapshot[]>();

  protected readonly SNAPSHOT_STATE = SNAPSHOT_STATE;
}
