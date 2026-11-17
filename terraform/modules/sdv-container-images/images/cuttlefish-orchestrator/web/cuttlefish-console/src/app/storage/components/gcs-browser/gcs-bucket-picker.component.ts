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

import {ChangeDetectionStrategy, Component, input, output, signal} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatChipsModule} from '@angular/material/chips';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatInputModule} from '@angular/material/input';
import {MatListModule} from '@angular/material/list';
import {EmptyStateComponent} from '../../../shared/components/empty-state/empty-state.component';
import type {Bucket} from '../../models/storage.model';
import {bucketNameFrom} from './gcs-file.utils';

/**
 * Bucket selection step of the GCS browser: the accessible-bucket list plus
 * a manual name entry that also serves as the fallback when listing fails.
 */
@Component({
  selector: 'app-gcs-bucket-picker',
  imports: [
    EmptyStateComponent,
    MatButtonModule,
    MatChipsModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
    MatListModule,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (listError()) {
      <div class="manual-entry-section">
        <div class="manual-entry-header">
          <mat-icon>warning</mat-icon>
          <span>{{ listError() }}</span>
        </div>
        <p class="manual-entry-hint">Enter your bucket name directly:</p>
        <div class="manual-entry-form">
          <mat-form-field appearance="outline" class="bucket-input">
            <mat-label>Bucket Name</mat-label>
            <input matInput [value]="manualBucketName()" (input)="onManualBucketInput($event)" placeholder="e.g., my-builds-bucket" (keyup.enter)="goToManualBucket()" />
            <mat-icon matPrefix>folder_special</mat-icon>
            <mat-hint>The bucket name, or a pasted gs:// URL</mat-hint>
          </mat-form-field>
          <button type="button" mat-flat-button (click)="goToManualBucket()" [disabled]="!manualBucketName()">
            <mat-icon>arrow_forward</mat-icon>
            Go to Bucket
          </button>
        </div>
      </div>
    } @else {
      <div class="section-header">
        <mat-icon>dns</mat-icon>
        <span>Select a bucket</span>
      </div>

      <!-- Manual Entry Option - Always available -->
      <div class="manual-entry-inline">
        <mat-form-field appearance="outline" class="bucket-input-inline">
          <mat-label>Or enter bucket name</mat-label>
          <input matInput [value]="manualBucketName()" (input)="onManualBucketInput($event)" placeholder="my-bucket-name" (keyup.enter)="goToManualBucket()" />
          <mat-icon matPrefix>edit</mat-icon>
        </mat-form-field>
        <button type="button" mat-stroked-button (click)="goToManualBucket()" [disabled]="!manualBucketName()">
          Go
        </button>
      </div>

      <mat-list class="bucket-list">
        @for (bucket of buckets(); track bucket.name) {
          <mat-list-item
            class="bucket-item"
            (click)="bucketChosen.emit(bucket.name)"
            >
            <mat-icon matListItemIcon>folder_special</mat-icon>
            <div matListItemTitle>{{ bucket.name }}</div>
            @if (bucket.location) {
              <div matListItemLine>
                <mat-chip class="location-chip">{{ bucket.location }}</mat-chip>
              </div>
            }
            <mat-icon matListItemMeta>chevron_right</mat-icon>
          </mat-list-item>
        }
      </mat-list>
      @if (buckets().length === 0) {
        <app-empty-state
          icon="folder_off"
          title="No accessible buckets found"
          message="Use the input above to enter your bucket name directly"
        />
      }
    }
  `,
  styles: `
    .section-header {
      display: flex;
      align-items: center;
      gap: 8px;
      padding: 16px;
      font-weight: 500;
      color: var(--cco-text-secondary);
    }

    .bucket-list {
      padding: 0;
    }

    .bucket-item {
      cursor: pointer;
      border-bottom: 1px solid var(--cco-surface-variant);
    }

    .bucket-item:hover {
      background: var(--cco-surface-variant);
    }

    .location-chip {
      font-size: 11px;
      min-height: 20px;
      padding: 2px 8px;
    }




    .manual-entry-section {
      padding: 24px;
      text-align: center;
    }

    .manual-entry-header {
      display: flex;
      align-items: center;
      justify-content: center;
      gap: 8px;
      margin-bottom: 16px;
      font-weight: 500;
      color: var(--cco-warning);
    }

    .manual-entry-header mat-icon {
      color: var(--cco-warning);
    }

    .manual-entry-hint {
      margin: 0 0 16px;
      color: var(--cco-text-secondary);
      font-size: 14px;
    }

    .manual-entry-form {
      display: flex;
      align-items: flex-start;
      gap: 12px;
      justify-content: center;
    }

    .bucket-input {
      width: 300px;
    }

    .manual-entry-inline {
      display: flex;
      align-items: center;
      gap: 8px;
      padding: 8px 16px;
      background: var(--cco-surface-variant);
      border-radius: 8px;
      margin-bottom: 16px;
    }

    .bucket-input-inline {
      flex: 1;
      max-width: 300px;
    }

    .bucket-input-inline ::ng-deep .mat-mdc-form-field-subscript-wrapper {
      display: none;
    }
  `,
})
export class GcsBucketPickerComponent {
  readonly buckets = input.required<Bucket[]>();
  readonly listError = input.required<string | null>();

  readonly bucketChosen = output<string>();

  protected readonly manualBucketName = signal('');

  protected onManualBucketInput(event: Event): void {
    this.manualBucketName.set((event.target as HTMLInputElement).value);
  }

  protected goToManualBucket(): void {
    if (!this.manualBucketName()) {
      return;
    }

    this.bucketChosen.emit(bucketNameFrom(this.manualBucketName()));
  }
}
