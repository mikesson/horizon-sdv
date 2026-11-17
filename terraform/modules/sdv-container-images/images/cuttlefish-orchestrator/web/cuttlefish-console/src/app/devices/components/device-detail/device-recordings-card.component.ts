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

import {
  ChangeDetectionStrategy,
  Component,
  computed,
  inject,
  input,
  resource,
  signal,
} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatCardModule} from '@angular/material/card';
import {MatIconModule} from '@angular/material/icon';
import {MatSnackBar} from '@angular/material/snack-bar';
import {MatTooltipModule} from '@angular/material/tooltip';
import {firstValueFrom} from 'rxjs';
import {LoadingStateComponent} from '../../../shared/components/loading-state/loading-state.component';
import {getErrorMessage} from '../../../shared/utils';
import {resolveBasePath} from '../../../shared/utils/base-url';
import {downloadUrl} from '../../../shared/utils/download';
import type {Device} from '../../models/device.model';
import {DeviceApiService} from '../../services/device-api.service';

/**
 * Screen recordings for a device: the on-host recording list (from the Host
 * Orchestrator), each with a Fetch action that archives it to GCS and
 * downloads it. Recordings live on the host until fetched. They die with the
 * host if never archived, which the empty/hint copy states.
 */
@Component({
  selector: 'app-device-recordings-card',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [LoadingStateComponent, MatButtonModule, MatCardModule, MatIconModule, MatTooltipModule],
  template: `
    <mat-card class="recordings-card">
      <mat-card-header>
        <mat-card-title>Screen recordings</mat-card-title>
        <button type="button" mat-icon-button (click)="refresh()" matTooltip="Refresh" [disabled]="loading()" aria-label="Refresh">
          <mat-icon>refresh</mat-icon>
        </button>
      </mat-card-header>
      <mat-card-content>
        <p class="hint">
          Start and stop recordings from the actions menu. Recordings stay on
          the host until fetched. Fetch archives them to storage before the
          host is reclaimed.
        </p>
        @if (loading()) {
          <app-loading-state size="section" />
        } @else if (recordings().length === 0) {
          <p class="empty">No recordings on the host.</p>
        } @else {
          <table class="recording-table">
            @for (name of recordings(); track name) {
              <tr>
                <td class="name">{{ name }}</td>
                <td class="row-actions">
                  <button type="button"
                    mat-stroked-button
                    (click)="fetch(name)"
                    [disabled]="fetching() === name">
                    <mat-icon>download</mat-icon>
                    {{ fetching() === name ? 'Fetching…' : 'Fetch' }}
                  </button>
                </td>
              </tr>
            }
          </table>
        }
      </mat-card-content>
    </mat-card>
  `,
  styles: `
      .recordings-card {
        margin-top: 16px;
      }

      mat-card-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
      }


      .hint {
        color: var(--cco-text-secondary);
        font-size: 12px;
        margin: 8px 0;
      }

      .empty {
        color: var(--cco-text-secondary);
        font-size: 12px;
        margin: 8px 0 0;
      }

      .recording-table {
        width: 100%;
        border-collapse: collapse;
      }

      .recording-table td {
        padding: 8px 12px 8px 0;
        font-size: 12px;
      }

      .recording-table .name {
        font-family: monospace;
      }

      .row-actions {
        text-align: right;
      }
    `,
})
export class DeviceRecordingsCardComponent {
  private readonly deviceApi = inject(DeviceApiService);
  private readonly snackBar = inject(MatSnackBar);

  readonly device = input.required<Device>();

  /** Recordings belong to one Android VM, so the card is scoped to one. */
  readonly instance = input.required<string>();

  private readonly target = computed(() => ({
    deviceId: this.device().id,
    instance: this.instance(),
  }));

  /**
   * A resource rather than a one-shot load: the selected instance changes
   * under this card when the user picks another tile, and each one has its own
   * recordings on the host.
   */
  private readonly recordingData = resource({
    params: () => (this.instance() ? this.target() : undefined),
    loader: async ({params: target}) => {
      try {
        return await firstValueFrom(this.deviceApi.listRecordings(target));
      } catch (error) {
        this.snackBar.open(getErrorMessage(error, 'Failed to list recordings'), 'Dismiss', {
          duration: 5000,
        });
        throw error instanceof Error ? error : new Error(String(error));
      }
    },
  });

  protected readonly loading = this.recordingData.isLoading;
  protected readonly fetching = signal<string | null>(null);
  protected readonly recordings = computed(() =>
    this.recordingData.hasValue() ? this.recordingData.value() : []
  );

  protected refresh(): void {
    this.recordingData.reload();
  }

  protected fetch(name: string): void {
    this.fetching.set(name);
    this.deviceApi.fetchRecording(this.target(), name).subscribe({
      next: artifact => {
        this.fetching.set(null);
        this.snackBar.open(`Recording archived: ${artifact.gsUri}`, 'Close', {duration: 5000});
        downloadUrl(resolveBasePath(artifact.downloadPath), name);
      },
      error: (err: unknown) => {
        this.fetching.set(null);
        this.snackBar.open(getErrorMessage(err, 'Failed to fetch recording'), 'Dismiss', {
          duration: 6000,
        });
      },
    });
  }
}
