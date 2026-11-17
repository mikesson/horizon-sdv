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
  resource,
  signal,
} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MAT_DIALOG_DATA, MatDialog, MatDialogModule, MatDialogRef} from '@angular/material/dialog';
import {MatIconModule} from '@angular/material/icon';
import {MatSnackBar} from '@angular/material/snack-bar';
import {firstValueFrom} from 'rxjs';
import {InstallDevicePickerComponent} from '../../../devices/components/install-device-picker/install-device-picker.component';
import type {InstanceTarget} from '../../../devices/models/instance-target';
import {ArtifactInstallService} from '../../../devices/services/adb/artifact-install.service';
import {LoadingStateComponent} from '../../../shared/components/loading-state/loading-state.component';
import {getErrorMessage} from '../../../shared/utils';
import type {
  BreadcrumbItem,
  GcsBrowserDialogData,
  GcsBrowserDialogResult,
  GcsBrowserMultiSelectResult,
  StorageObject,
  VerifyAccessResponse,
} from '../../models/storage.model';
import {StorageApiService} from '../../services/storage-api.service';
import {GcsBucketPickerComponent} from './gcs-bucket-picker.component';
import {fileIcon, formatSize, isApkFile} from './gcs-file.utils';
import type {GcsObjectRow} from './gcs-object-list.component';
import {GcsObjectListComponent} from './gcs-object-list.component';
import type {SelectedChip} from './gcs-selection-panel.component';
import {GcsSelectionPanelComponent} from './gcs-selection-panel.component';

@Component({
  selector: 'app-gcs-browser',
  imports: [
    LoadingStateComponent,
    MatDialogModule,
    MatButtonModule,
    MatIconModule,
    GcsBucketPickerComponent,
    GcsObjectListComponent,
    GcsSelectionPanelComponent,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <h2 mat-dialog-title>
      <mat-icon>cloud</mat-icon>
      {{ data.title || 'Select from Google Cloud Storage' }}
    </h2>
    
    <mat-dialog-content class="browser-content">
      <!-- Loading State -->
      @if (loading()) {
        <app-loading-state label="Loading…" />
      }

      <!-- Bucket picker (list, manual entry, and listing-error fallback) -->
      @if (!loading() && !currentBucket()) {
        <app-gcs-bucket-picker
          [buckets]="buckets()"
          [listError]="error()"
          (bucketChosen)="openBucket($event)" />
      }

      <!-- Object Browser -->
      @if (!loading() && !error() && currentBucket()) {
        <app-gcs-object-list
          [bucket]="currentBucket()!"
          [breadcrumbs]="breadcrumbs()"
          [rows]="objectRows()"
          [multiSelect]="isMultiSelectMode()"
          (navigated)="navigateTo($event)"
          (backRequested)="goBack()"
          (rowClicked)="handleObjectClick($event)"
          (selectionToggled)="toggleSelection($event)"
          (installRequested)="installToDevice($event)" />

        <app-gcs-selection-panel
          [multiSelect]="isMultiSelectMode()"
          [chips]="selectedChips()"
          [totalSizeLabel]="totalSizeLabel()"
          [requiredHint]="requiredHint()"
          [requiredMet]="hasRequiredTypes()"
          [selectedObject]="selectedObject()"
          [accessResult]="accessResult()"
          [verifying]="verifyingAccess()"
          (cleared)="clearSelection()"
          (chipRemoved)="toggleSelection($event)"
          (verifyRequested)="verifyAccess()" />
      }
    </mat-dialog-content>
    
    <mat-dialog-actions align="end">
      <button type="button" mat-button mat-dialog-close>Cancel</button>
      <button type="button"
        mat-flat-button
        [disabled]="!canSelect()"
        (click)="confirm()"
        >
        <mat-icon>check</mat-icon>
        {{ selectLabel() }}
      </button>
    </mat-dialog-actions>
    `,
  styles: `
    :host {
      display: block;
    }

    h2[mat-dialog-title] {
      display: flex;
      align-items: center;
      gap: 8px;
    }

    h2 mat-icon {
      color: var(--cco-accent);
    }

    .browser-content {
      min-width: 500px;
      max-width: 700px;
      min-height: 400px;
      max-height: 60vh;
    }

  `,
})
export class GcsBrowserComponent {
  private readonly storageApi = inject(StorageApiService);
  private readonly artifactInstall = inject(ArtifactInstallService);
  private readonly dialog = inject(MatDialog);
  private readonly snackBar = inject(MatSnackBar);
  private readonly dialogRef =
    inject<MatDialogRef<GcsBrowserComponent, GcsBrowserDialogResult>>(MatDialogRef);
  data = inject<GcsBrowserDialogData>(MAT_DIALOG_DATA);

  // Navigation state; the bucket and object listings load reactively from it.
  readonly currentBucket = signal<string | null>(this.data.initialBucket || null);
  readonly currentPrefix = signal(this.data.initialPath || '');

  /** Bucket list, loaded only while no bucket is selected. */
  private readonly bucketsResource = resource({
    params: () => (this.currentBucket() === null ? true : undefined),
    loader: () => firstValueFrom(this.storageApi.listBuckets()),
  });

  /** Objects under the current bucket + prefix. */
  private readonly objectsResource = resource({
    params: () => {
      const bucket = this.currentBucket();
      return bucket ? {bucket, prefix: this.currentPrefix()} : undefined;
    },
    loader: async ({params}) => {
      const response = await firstValueFrom(
        this.storageApi.listObjects(params.bucket, params.prefix)
      );
      return response.objects;
    },
  });

  readonly buckets = computed(() =>
    this.bucketsResource.hasValue() ? this.bucketsResource.value() : []
  );
  readonly objects = computed(() =>
    this.objectsResource.hasValue() ? this.objectsResource.value() : []
  );

  readonly breadcrumbs = computed((): BreadcrumbItem[] => {
    const prefix = this.currentPrefix();
    if (!prefix) {
      return [];
    }
    const crumbs: BreadcrumbItem[] = [];
    let path = '';
    for (const part of prefix.split('/').filter(p => p)) {
      path += `${part}/`;
      crumbs.push({name: part, path});
    }
    return crumbs;
  });

  // Single-select mode state
  readonly selectedObject = signal<StorageObject | null>(null);
  readonly accessResult = signal<VerifyAccessResponse | null>(null);
  readonly verifyingAccess = signal(false);

  // Multi-select mode state
  readonly selectedObjects = signal(new Map<string, StorageObject>());

  readonly loading = computed(
    () => this.bucketsResource.isLoading() || this.objectsResource.isLoading()
  );

  readonly error = computed((): string | null => {
    const bucketError = this.bucketsResource.error();
    if (bucketError) {
      return getErrorMessage(bucketError, 'Failed to load buckets');
    }
    const objectError = this.objectsResource.error();
    if (objectError) {
      return getErrorMessage(objectError, 'Failed to load objects');
    }
    return null;
  });

  /** Presentation rows for the object list child. */
  protected readonly objectRows = computed((): GcsObjectRow[] =>
    this.objects().map(obj => ({
      object: obj,
      icon: obj.isFolder ? 'folder' : fileIcon(obj.displayName),
      sizeLabel: formatSize(obj.size),
      selectable: this.isSelectable(obj),
      selected: this.isSelected(obj),
      apk: !obj.isFolder && isApkFile(obj.displayName),
    }))
  );

  protected readonly selectedChips = computed((): SelectedChip[] =>
    Array.from(this.selectedObjects().values()).map(object => ({
      object,
      icon: fileIcon(object.displayName),
    }))
  );

  protected readonly totalSizeLabel = computed(() => formatSize(this.getTotalSelectedSize()));

  protected readonly requiredHint = computed(() =>
    (this.data.requiredTypes ?? []).map(t => `.${t}`).join(' + ')
  );

  openBucket(name: string): void {
    this.currentPrefix.set('');
    this.currentBucket.set(name);
  }

  handleObjectClick(obj: StorageObject): void {
    if (obj.isFolder) {
      this.navigateTo(obj.name);
    } else if (this.isSelectable(obj)) {
      if (this.isMultiSelectMode()) {
        this.toggleSelection(obj);
      } else {
        this.selectObject(obj);
      }
    }
  }

  selectObject(obj: StorageObject): void {
    this.selectedObject.set(obj);
    this.accessResult.set(null); // Reset access verification when selecting new object
  }

  isMultiSelectMode(): boolean {
    return this.data.selectMode === 'multi-file';
  }

  isSelected(obj: StorageObject): boolean {
    if (this.isMultiSelectMode()) {
      return this.selectedObjects().has(obj.name);
    }
    return this.selectedObject()?.name === obj.name;
  }

  toggleSelection(obj: StorageObject): void {
    const current = this.selectedObjects();
    const newMap = new Map(current);
    if (newMap.has(obj.name)) {
      newMap.delete(obj.name);
    } else {
      newMap.set(obj.name, obj);
    }
    this.selectedObjects.set(newMap);
  }

  clearSelection(): void {
    this.selectedObjects.set(new Map());
  }

  getSelectedObjectsArray(): StorageObject[] {
    return Array.from(this.selectedObjects().values());
  }

  getTotalSelectedSize(): number {
    let total = 0;
    for (const obj of this.selectedObjects().values()) {
      total += obj.size || 0;
    }
    return total;
  }

  hasRequiredTypes(): boolean {
    if (!this.data.requiredTypes || this.data.requiredTypes.length === 0) {
      return true;
    }

    const selectedNames = Array.from(this.selectedObjects().values()).map(o =>
      o.displayName.toLowerCase()
    );

    return this.data.requiredTypes.every(type => {
      return selectedNames.some(name => name.endsWith(`.${type.toLowerCase()}`));
    });
  }

  /**
   * Verify GCS access for both orchestrator and host service accounts
   */
  verifyAccess(): void {
    const bucket = this.currentBucket();
    const obj = this.selectedObject();
    if (!(bucket && obj)) {
      return;
    }

    this.verifyingAccess.set(true);
    this.accessResult.set(null);

    this.storageApi.verifyAccess(bucket, obj.name, this.data.hostId).subscribe({
      next: result => {
        this.accessResult.set(result);
        this.verifyingAccess.set(false);
      },
      error: (err: unknown) => {
        this.verifyingAccess.set(false);
        const errorMsg = getErrorMessage(err, 'Failed to verify access');
        this.accessResult.set({
          gsUri: obj?.gsUri || '',
          orchestratorAccess: {
            serviceAccount: 'unknown',
            hasAccess: false,
            error: errorMsg,
            checkedAt: new Date().toISOString(),
          },
          canProceed: false,
          message: `Failed to verify access: ${errorMsg}`,
        });
      },
    });
  }

  isApk(obj: StorageObject): boolean {
    return !obj.isFolder && /\.(apk|apks|xapk|apkm)$/i.test(obj.displayName);
  }

  /** Streams an APK artifact from GCS straight onto a chosen device. */
  async installToDevice(obj: StorageObject): Promise<void> {
    const bucket = this.currentBucket();
    if (!bucket) {
      return;
    }

    const dialogRef = this.dialog.open<
      InstallDevicePickerComponent,
      {fileName: string},
      InstanceTarget
    >(InstallDevicePickerComponent, {data: {fileName: obj.displayName}, width: '420px'});
    const target = await firstValueFrom(dialogRef.afterClosed()).catch(() => undefined);
    if (!target) {
      return;
    }

    const ref = this.snackBar.open(`Installing ${obj.displayName}…`);
    try {
      await this.artifactInstall.installArtifact(target, bucket, obj.name);
      ref.dismiss();
      this.snackBar.open(`Installed ${obj.displayName}`, 'Close', {duration: 3000});
    } catch (error) {
      ref.dismiss();
      this.snackBar.open(`Install failed: ${getErrorMessage(error)}`, 'Close', {duration: 6000});
    }
  }

  isSelectable(obj: StorageObject): boolean {
    if (obj.isFolder) {
      return this.data.selectMode === 'folder';
    }

    // For file/artifact/multi-file mode, check file extension
    const name = obj.displayName.toLowerCase();
    return name.endsWith('.zip') || name.endsWith('.tar.gz') || name.endsWith('.img');
  }

  /**
   * Folder mode picks a selected file, else the folder being viewed. A folder
   * row opens the folder, so the folder itself is picked from inside it.
   */
  private readonly pickingFolder = computed(
    () =>
      this.data.selectMode === 'folder' &&
      this.selectedObject() === null &&
      this.currentBucket() !== null &&
      this.currentPrefix() !== ''
  );

  protected readonly selectLabel = computed(() =>
    this.pickingFolder() ? 'Select this folder' : 'Select'
  );

  canSelect(): boolean {
    if (this.isMultiSelectMode()) {
      return this.selectedObjects().size > 0;
    }
    return this.selectedObject() !== null || this.pickingFolder();
  }

  navigateTo(path: string): void {
    this.selectedObject.set(null);
    this.accessResult.set(null);
    this.currentPrefix.set(path);
  }

  goBack(): void {
    this.selectedObject.set(null);
    this.accessResult.set(null);

    const prefix = this.currentPrefix();
    if (prefix) {
      // Go up one folder
      const parts = prefix.split('/').filter(p => p);
      parts.pop();
      this.currentPrefix.set(parts.length > 0 ? `${parts.join('/')}/` : '');
    } else {
      // Go back to bucket list
      this.currentBucket.set(null);
    }
  }

  retry(): void {
    if (this.currentBucket()) {
      this.objectsResource.reload();
    } else {
      this.bucketsResource.reload();
    }
  }

  confirm(): void {
    const bucket = this.currentBucket();
    if (!bucket) {
      return;
    }

    if (this.isMultiSelectMode()) {
      // Multi-select mode: return array of objects
      if (this.selectedObjects().size === 0) {
        return;
      }

      const objectsList = this.getSelectedObjectsArray();
      const result: GcsBrowserMultiSelectResult = {
        bucket,
        objects: objectsList,
        gsUris: objectsList.map(o => o.gsUri),
        totalSize: this.getTotalSelectedSize(),
        accessVerified: this.accessResult()?.canProceed,
      };
      // Cast dialogRef to handle multi-select result
      (
        this.dialogRef as unknown as MatDialogRef<GcsBrowserComponent, GcsBrowserMultiSelectResult>
      ).close(result);
    } else {
      // Single-select mode
      const obj =
        this.selectedObject() ?? (this.pickingFolder() ? this.currentFolder(bucket) : null);
      if (!obj) {
        return;
      }

      this.dialogRef.close({
        bucket,
        object: obj,
        gsUri: obj.gsUri,
        accessVerified: this.accessResult()?.canProceed,
      });
    }
  }

  private currentFolder(bucket: string): StorageObject {
    const prefix = this.currentPrefix().replace(/\/?$/, '/');
    return {
      name: prefix,
      displayName: prefix.split('/').filter(Boolean).at(-1) ?? prefix,
      bucket,
      isFolder: true,
      gsUri: `gs://${bucket}/${prefix}`,
    };
  }
}
