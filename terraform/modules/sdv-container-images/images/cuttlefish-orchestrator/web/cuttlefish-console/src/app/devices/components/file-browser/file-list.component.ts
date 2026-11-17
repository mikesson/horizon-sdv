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
import {MatButtonModule} from '@angular/material/button';
import {MatIconModule} from '@angular/material/icon';
import {MatMenuModule} from '@angular/material/menu';
import type {FileRow} from './file-row';

/**
 * Presentational directory listing with a per-row action menu. Double-clicking
 * a directory opens it and a file downloads it; the parent performs the ADB
 * work behind the emitted intents.
 */
@Component({
  selector: 'app-file-list',
  imports: [MatIconModule, MatButtonModule, MatMenuModule],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="file-list">
      @for (entry of entries(); track entry.path) {
        <div
          class="file-entry"
          [class.directory]="entry.isDirectory"
          (dblclick)="onDoubleClick(entry)"
          [matMenuTriggerFor]="contextMenu"
          [matMenuTriggerData]="{entry}"
        >
          <mat-icon class="file-icon">{{ entry.icon }}</mat-icon>
          <div class="file-info">
            <span class="file-name">{{ entry.name }}</span>
            <span class="file-meta">{{ entry.sizeLabel }} {{ entry.dateLabel }}</span>
          </div>
        </div>
      } @empty {
        <div class="empty">
          <mat-icon>folder_open</mat-icon>
          <span>Empty directory</span>
        </div>
      }
    </div>

    <mat-menu #contextMenu="matMenu" class="dark-menu-panel">
      <ng-template matMenuContent let-entry="entry">
        @if (entry.isDirectory) {
          <button type="button" mat-menu-item (click)="openEntry.emit(entry.path)">
            <mat-icon>folder_open</mat-icon>
            <span>Open</span>
          </button>
        }
        @if (entry.isFile) {
          <button type="button" mat-menu-item (click)="download.emit(entry)">
            <mat-icon>download</mat-icon>
            <span>Download</span>
          </button>
        }
        <button type="button" mat-menu-item (click)="deleteEntry.emit(entry)">
          <mat-icon color="warn">delete</mat-icon>
          <span>Delete</span>
        </button>
      </ng-template>
    </mat-menu>
  `,
  styles: `
      .file-list {
        display: flex;
        flex-direction: column;
        gap: 2px;
      }

      .file-entry {
        display: flex;
        align-items: center;
        gap: 12px;
        padding: 8px 12px;
        border-radius: 4px;
        cursor: pointer;
        transition: background var(--cco-duration-fast) ease;
      }

      .file-entry:hover {
        background: var(--cco-surface-variant);
      }

      .file-entry.directory .file-icon {
        color: var(--cco-accent);
      }

      .file-icon {
        color: var(--cco-text-secondary);
        font-size: 20px;
        width: 20px;
        height: 20px;
      }

      .file-info {
        display: flex;
        flex-direction: column;
        flex: 1;
        min-width: 0;
      }

      .file-name {
        color: var(--cco-text);
        font-size: 14px;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
      }

      .file-meta {
        color: var(--cco-text-disabled);
        font-size: 11px;
      }

      .empty {
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        height: 100%;
        color: var(--cco-text-secondary);
        gap: 16px;
      }

      .empty mat-icon {
        font-size: 48px;
        width: 48px;
        height: 48px;
        color: var(--cco-text-disabled);
      }
    `,
})
export class FileListComponent {
  readonly entries = input.required<FileRow[]>();

  readonly openEntry = output<string>();
  readonly download = output<FileRow>();
  readonly deleteEntry = output<FileRow>();

  protected onDoubleClick(entry: FileRow): void {
    if (entry.isDirectory) {
      this.openEntry.emit(entry.path);
    } else {
      this.download.emit(entry);
    }
  }
}
