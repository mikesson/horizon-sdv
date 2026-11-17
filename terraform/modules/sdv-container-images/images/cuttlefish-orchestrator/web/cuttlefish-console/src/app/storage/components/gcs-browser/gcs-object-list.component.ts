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
import {ChangeDetectionStrategy, Component, input, output} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatCheckboxModule} from '@angular/material/checkbox';
import {MatDividerModule} from '@angular/material/divider';
import {MatIconModule} from '@angular/material/icon';
import {MatListModule} from '@angular/material/list';
import {MatTooltipModule} from '@angular/material/tooltip';
import {EmptyStateComponent} from '../../../shared/components/empty-state/empty-state.component';
import type {BreadcrumbItem, StorageObject} from '../../models/storage.model';

/** Presentation row for the object list, precomputed by the container. */
export interface GcsObjectRow {
  object: StorageObject;
  icon: string;
  sizeLabel: string;
  selectable: boolean;
  selected: boolean;
  apk: boolean;
}

/**
 * Object browsing step of the GCS browser: breadcrumbs plus the file/folder
 * rows with selection affordances. Pure presentation, so the container owns
 * navigation and selection state.
 */
@Component({
  selector: 'app-gcs-object-list',
  imports: [
    EmptyStateComponent,
    DatePipe,
    MatButtonModule,
    MatCheckboxModule,
    MatDividerModule,
    MatIconModule,
    MatListModule,
    MatTooltipModule,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <!-- Breadcrumb Navigation -->
    <div class="breadcrumb">
      <button type="button" mat-icon-button (click)="backRequested.emit()" matTooltip="Go back" aria-label="Go back">
        <mat-icon>arrow_back</mat-icon>
      </button>
      <button type="button" mat-button class="breadcrumb-item" (click)="navigated.emit('')">
        <mat-icon>folder_special</mat-icon>
        {{ bucket() }}
      </button>
      @for (crumb of breadcrumbs(); track crumb.path; let last = $last) {
        <mat-icon class="separator">chevron_right</mat-icon>
        <button type="button"
          mat-button
          class="breadcrumb-item"
          [class.current]="last"
          (click)="navigated.emit(crumb.path)"
          >
          {{ crumb.name }}
        </button>
      }
    </div>
    <mat-divider />
    <!-- Objects List -->
    <mat-list class="object-list">
      @for (row of rows(); track row.object.name) {
        <mat-list-item
          class="object-item"
          [class.folder]="row.object.isFolder"
          [class.selectable]="row.selectable"
          [class.selected]="row.selected"
          (click)="rowClicked.emit(row.object)"
          >
          <mat-icon matListItemIcon [class.folder-icon]="row.object.isFolder">
            {{ row.icon }}
          </mat-icon>
          <div matListItemTitle>{{ row.object.displayName }}</div>
          @if (!row.object.isFolder) {
            <div matListItemLine>
              <span class="file-size">{{ row.sizeLabel }}</span>
              @if (row.object.updatedAt) {
                <span class="file-date">
                  {{ row.object.updatedAt | date:'short' }}
                </span>
              }
            </div>
          }
          @if (row.apk) {
            <button type="button"
              matListItemMeta
              mat-icon-button
              matTooltip="Install to a running device"
              (click)="$event.stopPropagation(); installRequested.emit(row.object)" aria-label="Install to a running device">
              <mat-icon>install_mobile</mat-icon>
            </button>
          }
          @if (!row.object.isFolder && deletable()) {
            <button type="button"
              matListItemMeta
              mat-icon-button
              [matTooltip]="'Delete ' + row.object.name"
              (click)="$event.stopPropagation(); deleteRequested.emit(row.object)" [attr.aria-label]="'Delete ' + row.object.name">
              <mat-icon>delete</mat-icon>
            </button>
          }
          @if (row.object.isFolder) {
            <mat-icon matListItemMeta>chevron_right</mat-icon>
          }
          @if (!row.object.isFolder && row.selectable) {
            @if (multiSelect()) {
              <mat-checkbox
                matListItemMeta
                [checked]="row.selected"
                (click)="$event.stopPropagation()"
                (change)="selectionToggled.emit(row.object)" />
            } @else {
              <mat-icon matListItemMeta class="check">
                {{ row.selected ? 'check_circle' : 'radio_button_unchecked' }}
              </mat-icon>
            }
          }
        </mat-list-item>
      }
    </mat-list>
    @if (rows().length === 0) {
      <app-empty-state variant="compact" icon="folder_open" message="This folder is empty" />
    }
  `,
  styles: `
    .breadcrumb {
      display: flex;
      align-items: center;
      padding: 8px;
      background: var(--cco-surface-variant);
      flex-wrap: wrap;
    }

    .breadcrumb-item {
      min-width: auto;
      padding: 0 8px;
    }

    .breadcrumb-item.current {
      font-weight: 500;
    }

    .separator {
      color: var(--cco-text-secondary);
      font-size: 16px;
      width: 18px;
      height: 18px;
    }

    .object-list {
      padding: 0;
    }

    .object-item {
      cursor: pointer;
      border-bottom: 1px solid var(--cco-surface-variant);
    }

    .object-item:hover {
      background: var(--cco-surface-variant);
    }

    .object-item.selectable {
      cursor: pointer;
    }

    .object-item.selected {
      background: var(--cco-accent-surface);
    }

    .folder-icon {
      color: var(--cco-warning);
    }

    .file-size {
      font-size: 12px;
      color: var(--cco-text-secondary);
      margin-right: 16px;
    }

    .file-date {
      font-size: 12px;
      color: var(--cco-text-secondary);
    }

    .check {
      color: var(--cco-accent);
    }


  `,
})
export class GcsObjectListComponent {
  readonly bucket = input.required<string>();
  readonly breadcrumbs = input.required<BreadcrumbItem[]>();
  readonly rows = input.required<GcsObjectRow[]>();
  readonly multiSelect = input.required<boolean>();
  /** Off while the browser is picking artifacts for another flow. */
  readonly deletable = input(false);

  readonly navigated = output<string>();
  readonly backRequested = output<void>();
  readonly rowClicked = output<StorageObject>();
  readonly selectionToggled = output<StorageObject>();
  readonly installRequested = output<StorageObject>();
  readonly deleteRequested = output<StorageObject>();
}
