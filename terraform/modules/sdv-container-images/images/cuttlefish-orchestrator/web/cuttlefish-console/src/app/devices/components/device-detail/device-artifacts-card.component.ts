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

import {DatePipe, DecimalPipe} from '@angular/common';
import {ChangeDetectionStrategy, Component, computed, inject, input, signal} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatCardModule} from '@angular/material/card';
import {MatDialog} from '@angular/material/dialog';
import {MatIconModule} from '@angular/material/icon';
import {MatSnackBar} from '@angular/material/snack-bar';
import {MatTooltipModule} from '@angular/material/tooltip';
import {ArtifactsAdminApiService} from '../../../admin/services/artifacts-admin-api.service';
import type {ConfirmDialogData} from '../../../shared/components';
import {ConfirmDialogComponent} from '../../../shared/components';
import {LoadingStateComponent} from '../../../shared/components/loading-state/loading-state.component';
import {getErrorMessage, resolveBasePath} from '../../../shared/utils';
import type {StorageObject} from '../../../storage/models/storage.model';
import {StorageApiService} from '../../../storage/services/storage-api.service';
import type {Device} from '../../models/device.model';
import {DeviceApiService} from '../../services/device-api.service';

/**
 * Captured artifacts (bugreports) for a device: capture actions plus the live
 * GCS listing under the device's artifact prefix. Artifacts outlive the
 * device, and this card is the device-scoped view of the bucket.
 */
@Component({
  selector: 'app-device-artifacts-card',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    LoadingStateComponent,
    DatePipe,
    DecimalPipe,
    MatButtonModule,
    MatCardModule,
    MatIconModule,
    MatTooltipModule,
  ],
  template: `
    <mat-card class="artifacts-card">
      <mat-card-header>
        <mat-card-title>Artifacts</mat-card-title>
        <div class="header-actions">
          <button type="button"
            mat-stroked-button
            (click)="capture(false)"
            [disabled]="capturing()"
            matTooltip="Host-side logs (works even when Android is down; seconds)">
            <mat-icon>bug_report</mat-icon>
            {{ capturing() ? 'Capturing…' : 'Capture bugreport' }}
          </button>
          <button type="button"
            mat-stroked-button
            (click)="capture(true)"
            [disabled]="capturing()"
            matTooltip="Adds a full on-device adb bugreport (needs a responsive Android; can take minutes)">
            <mat-icon>adb</mat-icon>
            Full (adb)
          </button>
          <button type="button" mat-icon-button (click)="refresh()" matTooltip="Refresh" [disabled]="loading()" aria-label="Refresh">
            <mat-icon>refresh</mat-icon>
          </button>
        </div>
      </mat-card-header>
      <mat-card-content>
        @if (!bucket()) {
          <p class="hint">
            No artifact bucket is configured. Set one under Administration →
            Artifacts (or deploy with CCO_ARTIFACTS_BUCKET).
          </p>
        } @else if (loading()) {
          <app-loading-state size="section" />
        } @else if (artifacts().length === 0) {
          <p class="hint">No artifacts captured for this device yet.</p>
        } @else {
          <table class="artifact-table">
            @for (a of artifacts(); track a.name) {
              <tr>
                <td class="name">{{ a.displayName }}</td>
                <td>{{ (a.size ?? 0) / 1024 / 1024 | number: '1.0-2' }} MB</td>
                <td>{{ a.updatedAt | date: 'MMM d, HH:mm' }}</td>
                <td class="row-actions">
                  <a
                    mat-icon-button
                    [href]="downloadHref(a)"
                    download
                    matTooltip="Download"
                    [attr.aria-label]="'Download ' + a.displayName">
                    <mat-icon>download</mat-icon>
                  </a>
                  <button type="button"
                    mat-icon-button
                    [disabled]="deleting() === a.name"
                    matTooltip="Delete from the bucket"
                    [attr.aria-label]="'Delete ' + a.displayName"
                    (click)="confirmDelete(a)">
                    <mat-icon>delete</mat-icon>
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
      .artifacts-card {
        margin-top: 16px;
      }

      mat-card-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
      }

      .header-actions {
        display: flex;
        gap: 8px;
        align-items: center;
      }


      .hint {
        color: var(--cco-text-secondary);
        font-size: 12px;
        margin: 8px 0 0;
      }

      .artifact-table {
        width: 100%;
        margin-top: 8px;
        border-collapse: collapse;
      }

      .artifact-table td {
        padding: 8px 12px 8px 0;
        font-size: 12px;
      }

      .artifact-table .name {
        font-family: monospace;
      }

      .row-actions {
        text-align: right;
      }
    `,
})
export class DeviceArtifactsCardComponent {
  private readonly deviceApi = inject(DeviceApiService);
  private readonly storageApi = inject(StorageApiService);
  private readonly artifactsAdminApi = inject(ArtifactsAdminApiService);
  private readonly snackBar = inject(MatSnackBar);
  private readonly dialog = inject(MatDialog);

  readonly device = input.required<Device>();

  protected readonly bucket = signal('');
  protected readonly loading = signal(false);
  protected readonly capturing = signal(false);
  protected readonly artifacts = signal<StorageObject[]>([]);
  /** Object name currently being deleted; disables just that row's button. */
  protected readonly deleting = signal<string | null>(null);

  private readonly prefix = computed(
    () => `artifacts/${this.device().name}-${this.device().id}/bugreports/`
  );

  constructor() {
    this.artifactsAdminApi.get().subscribe({
      next: buckets => {
        this.bucket.set(buckets.bugreportBucket || buckets.defaultBucket);
        if (this.bucket()) {
          this.refresh();
        }
      },
      // No toast: the card renders its unconfigured hint instead.
      error: () => this.bucket.set(''),
    });
  }

  /**
   * A bugreport can be the only record of why a device failed, and the object
   * is gone for good, so the delete confirms first.
   */
  protected confirmDelete(artifact: StorageObject): void {
    const data: ConfirmDialogData = {
      title: 'Delete this artifact?',
      message:
        `"${artifact.displayName}" is removed from the bucket permanently. ` +
        'Anyone holding a link to it loses access to the only copy.',
      confirmText: 'Delete',
      confirmColor: 'warn',
      icon: 'delete',
      iconColor: 'warn',
    };
    this.dialog
      .open(ConfirmDialogComponent, {data})
      .afterClosed()
      .subscribe((confirmed: boolean) => {
        if (confirmed) {
          this.deleteArtifact(artifact);
        }
      });
  }

  private deleteArtifact(artifact: StorageObject): void {
    this.deleting.set(artifact.name);
    this.storageApi.deleteObject(this.bucket(), artifact.name).subscribe({
      next: () => {
        this.deleting.set(null);
        // Refetched rather than spliced locally: the bucket is the source of
        // truth and another session may have changed it too.
        this.refresh();
        this.snackBar.open(`Deleted ${artifact.displayName}`, 'Dismiss', {duration: 3000});
      },
      error: (err: unknown) => {
        this.deleting.set(null);
        this.snackBar.open(getErrorMessage(err, 'Failed to delete the artifact'), 'Dismiss', {
          duration: 6000,
        });
      },
    });
  }

  protected refresh(): void {
    if (!this.bucket()) {
      return;
    }
    this.loading.set(true);
    this.storageApi.listObjects(this.bucket(), this.prefix()).subscribe({
      next: res => {
        this.artifacts.set(res.objects.filter(o => !o.isFolder));
        this.loading.set(false);
      },
      error: (err: unknown) => {
        this.loading.set(false);
        this.snackBar.open(getErrorMessage(err, 'Failed to list artifacts'), 'Dismiss', {
          duration: 5000,
        });
      },
    });
  }

  protected capture(includeAdb: boolean): void {
    this.capturing.set(true);
    this.snackBar.open(
      includeAdb ? 'Capturing full bugreport (this can take minutes)…' : 'Capturing bugreport…',
      undefined,
      {duration: 4000}
    );
    this.deviceApi.bugreport(this.device().id, includeAdb).subscribe({
      next: artifact => {
        this.capturing.set(false);
        this.snackBar.open(`Bugreport stored: ${artifact.gsUri}`, 'Close', {duration: 5000});
        this.refresh();
      },
      error: (err: unknown) => {
        this.capturing.set(false);
        this.snackBar.open(getErrorMessage(err, 'Failed to capture bugreport'), 'Dismiss', {
          duration: 6000,
        });
      },
    });
  }

  protected downloadHref(a: StorageObject): string {
    return resolveBasePath(`/v2/storage/buckets/${this.bucket()}/objects/${a.name}`);
  }
}
