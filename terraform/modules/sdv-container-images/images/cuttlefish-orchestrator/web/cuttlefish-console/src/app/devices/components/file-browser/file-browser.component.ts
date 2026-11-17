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

import type {ElementRef} from '@angular/core';
import {
  ChangeDetectionStrategy,
  Component,
  computed,
  inject,
  input,
  linkedSignal,
  resource,
  signal,
  viewChild,
} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatDialog} from '@angular/material/dialog';
import {MatIconModule} from '@angular/material/icon';
import {MatProgressSpinnerModule} from '@angular/material/progress-spinner';
import {MatTooltipModule} from '@angular/material/tooltip';
import {firstValueFrom} from 'rxjs';
import type {InputDialogData} from '../../../shared/components';
import {InputDialogComponent} from '../../../shared/components';
import {bytesToBlob, downloadBlob} from '../../../shared/utils/download';
import {
  createDirectory,
  deleteDirectory,
  deleteFile,
  listDirectory,
  readFile,
  writeFile,
} from '../../services/adb/adb-files';
import {injectAdbPanel} from '../../services/adb/adb-panel';
import {AdbToolPanelComponent} from '../adb-tool-panel';
import {BreadcrumbBarComponent} from './breadcrumb-bar.component';
import {FileListComponent} from './file-list.component';
import type {FileRow} from './file-row';
import {toFileRow} from './file-row';

/**
 * File browser for navigating and managing device files: breadcrumb
 * navigation, upload (picker + drag-and-drop), download, delete, mkdir. The
 * directory listing is a resource keyed on device/path/ADB state, so it loads
 * when ADB connects, reloads on navigation, and idles while ADB is down; the
 * ADB gating, drop target, and session plumbing live in the shared shell +
 * {@link injectAdbPanel}.
 */
@Component({
  selector: 'app-file-browser',
  imports: [
    MatIconModule,
    MatButtonModule,
    MatTooltipModule,
    MatProgressSpinnerModule,
    AdbToolPanelComponent,
    BreadcrumbBarComponent,
    FileListComponent,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <app-adb-tool-panel
      [adbState]="adb.state()"
      [isLoading]="listing.isLoading()"
      [error]="listingError()"
      waitingHint="The file browser will load when ADB is ready"
      dropIcon="cloud_upload"
      dropLabel="Drop files to upload"
      accent="#58a6ff"
      (filesDropped)="uploadFiles($event)"
      (retry)="refresh()"
    >
      <div toolbar class="toolbar">
        <app-breadcrumb-bar [path]="currentPath()" (navigate)="navigateTo($event)" />

        <button type="button" mat-icon-button (click)="refresh()" matTooltip="Refresh" aria-label="Refresh">
          <mat-icon>refresh</mat-icon>
        </button>
        <button type="button" mat-icon-button (click)="createNewFolder()" matTooltip="New folder" aria-label="New folder">
          <mat-icon>create_new_folder</mat-icon>
        </button>
        <button type="button" mat-icon-button (click)="triggerUpload()" matTooltip="Upload file" aria-label="Upload file">
          <mat-icon>upload</mat-icon>
        </button>

        <input
          #fileInput
          type="file"
          multiple
          (change)="onFileSelected($event)"
          style="display: none"
        />
      </div>

      <app-file-list
        [entries]="entries()"
        (openEntry)="navigateTo($event)"
        (download)="downloadFile($event)"
        (deleteEntry)="deleteEntry($event)"
      />

      @if (uploading()) {
        <div overlay class="upload-overlay">
          <mat-spinner aria-label="Loading" diameter="48" />
          <span>Uploading...</span>
        </div>
      }
    </app-adb-tool-panel>
  `,
  styles: `
      .toolbar {
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 8px;
        background: var(--cco-surface-variant);
        border-bottom: 1px solid var(--cco-surface-variant);
      }

      .toolbar button {
        color: var(--cco-text-secondary);
      }

      .upload-overlay {
        position: absolute;
        inset: 0;
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        background: rgba(0, 0, 0, 0.8);
        color: var(--cco-accent);
        gap: 16px;
        z-index: 100;
      }
    `,
})
export class FileBrowserComponent {
  private readonly dialog = inject(MatDialog);

  readonly deviceId = input.required<string>();
  readonly instance = input.required<string>();

  /** The Android VM this panel talks to; ADB sessions are per instance. */
  private readonly target = computed(() => ({
    deviceId: this.deviceId(),
    instance: this.instance(),
  }));

  readonly initialPath = input('/sdcard');

  private readonly fileInput = viewChild.required<ElementRef<HTMLInputElement>>('fileInput');

  protected readonly adb = injectAdbPanel(this.target);

  /** Resets to initialPath whenever that input changes. */
  protected readonly currentPath = linkedSignal(() => this.initialPath());

  protected readonly uploading = signal(false);

  protected readonly listing = resource({
    params: () => {
      const id = this.deviceId();
      const path = this.currentPath();
      return this.adb.state() === 'connected' ? {id, path} : undefined;
    },
    loader: async ({params}) => {
      const entries = await listDirectory(await this.adb.connectedDevice(), params.path);
      entries.sort((a, b) => {
        if (a.isDirectory !== b.isDirectory) {
          return a.isDirectory ? -1 : 1;
        }
        return a.name.localeCompare(b.name);
      });
      return entries.map(toFileRow);
    },
  });

  protected readonly entries = computed<FileRow[]>(() =>
    this.listing.hasValue() ? this.listing.value() : []
  );

  protected readonly listingError = computed(() => {
    const error = this.listing.error();
    return error ? (error instanceof Error ? error.message : 'Failed to load directory') : null;
  });

  protected navigateTo(path: string): void {
    this.currentPath.set(path);
  }

  protected downloadFile(entry: FileRow): Promise<void> {
    return this.adb.run(
      async device => {
        const content = await readFile(device, entry.path);
        downloadBlob(bytesToBlob(content, 'application/octet-stream'), entry.name);
      },
      {success: `Downloaded ${entry.name}`, failure: 'Download failed'}
    );
  }

  protected async deleteEntry(entry: FileRow): Promise<void> {
    const confirmed = await this.adb.confirm({
      title: entry.isDirectory ? 'Delete Folder' : 'Delete File',
      message: entry.isDirectory
        ? `Delete "${entry.name}" and everything in it?`
        : `Delete "${entry.name}"?`,
      confirmText: 'Delete',
      confirmColor: 'warn',
      icon: 'delete',
      iconColor: 'warn',
    });
    if (!confirmed) {
      return;
    }

    await this.adb.run(
      async device => {
        if (entry.isDirectory) {
          await deleteDirectory(device, entry.path);
        } else {
          await deleteFile(device, entry.path);
        }
      },
      {
        success: `Deleted ${entry.name}`,
        failure: 'Delete failed',
        after: () => this.listing.reload(),
      }
    );
  }

  protected async createNewFolder(): Promise<void> {
    const data: InputDialogData = {
      title: 'New Folder',
      message: `Create a folder in ${this.currentPath()}`,
      inputLabel: 'Folder name',
      confirmText: 'Create',
      icon: 'create_new_folder',
      validationPattern: '[^/]+',
      validationMessage: 'Folder names cannot contain "/"',
    };
    const name = await firstValueFrom(
      this.dialog
        .open<InputDialogComponent, InputDialogData, string | null>(InputDialogComponent, {data})
        .afterClosed()
    );
    if (!name) {
      return;
    }

    const path = `${this.currentPath()}/${name}`.replace(/\/+/g, '/');
    await this.adb.run(device => createDirectory(device, path), {
      success: `Created ${name}`,
      failure: 'Failed to create folder',
      after: () => this.listing.reload(),
    });
  }

  protected triggerUpload(): void {
    this.fileInput().nativeElement.click();
  }

  protected async onFileSelected(event: Event): Promise<void> {
    const input = event.target as HTMLInputElement;
    const files = input.files;
    if (!files || files.length === 0) {
      return;
    }

    await this.uploadFiles(Array.from(files));
    input.value = '';
  }

  protected async uploadFiles(files: File[]): Promise<void> {
    this.uploading.set(true);
    await this.adb.run(
      async device => {
        for (const file of files) {
          const content = new Uint8Array(await file.arrayBuffer());
          const path = `${this.currentPath()}/${file.name}`.replace(/\/+/g, '/');
          await writeFile(device, path, content);
        }
      },
      {
        success: `Uploaded ${files.length} file${files.length > 1 ? 's' : ''}`,
        failure: 'Upload failed',
        after: () => this.listing.reload(),
      }
    );
    this.uploading.set(false);
  }

  protected refresh(): void {
    this.listing.reload();
  }
}
