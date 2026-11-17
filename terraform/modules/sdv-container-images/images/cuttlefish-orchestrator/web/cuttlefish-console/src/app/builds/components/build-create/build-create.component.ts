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

import {ChangeDetectionStrategy, Component, computed, inject, signal} from '@angular/core';
import {FormField, form, required} from '@angular/forms/signals';
import {MatButtonModule} from '@angular/material/button';
import {MatCardModule} from '@angular/material/card';
import {MatCheckboxModule} from '@angular/material/checkbox';
import {MatChipsModule} from '@angular/material/chips';
import {MatDialog, MatDialogModule} from '@angular/material/dialog';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatInputModule} from '@angular/material/input';
import {MatProgressSpinnerModule} from '@angular/material/progress-spinner';
import {MatSelectModule} from '@angular/material/select';
import {MatStepperModule} from '@angular/material/stepper';
import {Router, RouterModule} from '@angular/router';
import {firstValueFrom} from 'rxjs';
import {PageHeaderComponent, PageLayoutComponent} from '../../../shared/components';
import {GcsBrowserComponent} from '../../../storage/components/gcs-browser/gcs-browser.component';
import {bucketNameFrom} from '../../../storage/components/gcs-browser/gcs-file.utils';
import type {
  GcsBrowserDialogData,
  GcsBrowserMultiSelectResult,
  StorageObject,
} from '../../../storage/models/storage.model';
import type {ArtifactRole, ArtifactSource, BuildSource} from '../../models/build.model';
import {ARTIFACT_ROLES} from '../../models/build.model';
import {BuildStore} from '../../store/build.signal-store';

interface UrlArtifactDraft {
  url: string;
  role: ArtifactRole;
}

interface BuildFormModel {
  name: string;
  description: string;
  isDefault: boolean;
  source: BuildSource;
  branch: string;
  target: string;
  buildId: string;
  gcsBucketName: string;
  urlArtifacts: UrlArtifactDraft[];
}

@Component({
  selector: 'app-build-create',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    RouterModule,
    FormField,
    MatCardModule,
    MatButtonModule,
    MatIconModule,
    MatFormFieldModule,
    MatInputModule,
    MatSelectModule,
    MatCheckboxModule,
    MatProgressSpinnerModule,
    MatStepperModule,
    MatDialogModule,
    MatChipsModule,
    PageLayoutComponent,
    PageHeaderComponent,
  ],
  template: `
    <app-page-layout variant="form">
      <div page-header>
        <app-page-header
          title="Register Build"
          subtitle="Add an Android build for use with Cuttlefish devices"
          [showBackButton]="true"
          (back)="goBack()" />
      </div>

      <div page-content>
        <mat-card class="form-card">
        <mat-card-content>
          <form>
            
            <!-- 1. Choose Mode: Quick Start vs Custom -->
            <div class="form-section">
              <h3>Choose Registration Mode</h3>
              <div class="source-selector" role="radiogroup" aria-label="Registration mode">
                <!-- Quick Start Card -->
                <button
                  type="button"
                  class="source-option quickstart"
                  role="radio"
                  [attr.aria-checked]="viewMode() === 'quickstart'"
                  [class.selected]="viewMode() === 'quickstart'"
                  (click)="setViewMode('quickstart')"
                  >
                  <div class="source-icon">
                    <mat-icon>smartphone</mat-icon>
                  </div>
                  <div class="source-info">
                    <span class="source-name">Quick Start: Public Phone Image</span>
                    <span class="source-desc">Use the latest public AOSP trunk build for Phone. Best for quick testing.</span>
                    <span class="source-hint">Target: <code>aosp_cf_x86_64_only_phone-userdebug</code></span>
                  </div>
                  @if (viewMode() === 'quickstart') {
                    <mat-icon class="check-icon">check_circle</mat-icon>
                  }
                </button>

                <!-- Custom Build Card -->
                <button
                  type="button"
                  class="source-option custom"
                  role="radio"
                  [attr.aria-checked]="viewMode() === 'custom'"
                  [class.selected]="viewMode() === 'custom'"
                  (click)="setViewMode('custom')"
                  >
                  <div class="source-icon">
                    <mat-icon>tune</mat-icon>
                  </div>
                  <div class="source-info">
                    <span class="source-name">Custom Build</span>
                    <span class="source-desc">Manually configure Branch, Target, and Build ID, or use a GCS path.</span>
                    <span class="source-hint">For Automotive, Wear OS, TV, or specific builds.</span>
                  </div>
                  @if (viewMode() === 'custom') {
                    <mat-icon class="check-icon">check_circle</mat-icon>
                  }
                </button>
              </div>
            </div>

            <!-- 2. Custom Configuration (Only if Custom Mode) -->
            @if (viewMode() === 'custom') {
              <div class="form-section">
                <h3>Build Source</h3>
                <div class="custom-source-toggle">
                  <button type="button" mat-stroked-button
                    [class.active]="customSource() === 'ci'"
                    (click)="setCustomSource('ci')">
                    <mat-icon>android</mat-icon> Android CI (Public)
                  </button>
                  <button type="button" mat-stroked-button
                    [class.active]="customSource() === 'gcs'"
                    (click)="setCustomSource('gcs')">
                    <mat-icon>cloud</mat-icon> GCS Browser
                  </button>
                  <button type="button" mat-stroked-button
                    [class.active]="customSource() === 'url'"
                    (click)="setCustomSource('url')">
                    <mat-icon>link</mat-icon> Direct URLs
                  </button>
                </div>

                <!-- CI Form -->
                @if (customSource() === 'ci') {
                  <div class="ci-form-grid">
                    <mat-form-field appearance="outline">
                      <mat-label>Branch</mat-label>
                      <input matInput [formField]="buildForm.branch" placeholder="e.g., aosp-android-latest-release" />
                      <mat-hint>The Git branch name (e.g. <code>git_main</code>)</mat-hint>
                    </mat-form-field>

                    <mat-form-field appearance="outline">
                      <mat-label>Target</mat-label>
                      <input matInput [formField]="buildForm.target" placeholder="e.g., aosp_cf_x86_64_auto-userdebug" />
                      <mat-hint>The lunch target name</mat-hint>
                    </mat-form-field>

                    <mat-form-field appearance="outline">
                      <mat-label>Build ID (Optional)</mat-label>
                      <input matInput [formField]="buildForm.buildId" placeholder="e.g., 12345678" />
                      <mat-hint>Leave empty to use the latest green build</mat-hint>
                    </mat-form-field>
                  </div>
                }

                <!-- GCS Form -->
                @if (customSource() === 'gcs') {
                  <div class="gcs-form">
                    <!-- Step 1: Enter Bucket Name -->
                    <div class="gcs-step">
                      <div class="step-header">
                        <span class="step-number">1</span>
                        <span class="step-title">Which bucket contains your build files?</span>
                      </div>
                      <div class="bucket-input-row">
                        <mat-form-field appearance="outline" class="bucket-field">
                          <mat-label>Bucket Name</mat-label>
                          <input matInput
                            [formField]="buildForm.gcsBucketName"
                            placeholder="my-builds or gs://my-builds/..." />
                          <mat-hint>The bucket holding your build artifacts; a pasted gs:// URL works too</mat-hint>
                        </mat-form-field>
                      </div>
                    </div>

                    <!-- Step 2: Browse and Select Files -->
                    <div class="gcs-step" [class.disabled]="!draft().gcsBucketName">
                      <div class="step-header">
                        <span class="step-number">2</span>
                        <span class="step-title">Select the required build files</span>
                      </div>
                      <p class="step-description">
                        You need to select <strong>both</strong> of these files:
                      </p>
                      <ul class="required-files">
                        <li [class.found]="hasHostPackage()">
                          <mat-icon>{{ hasHostPackage() ? 'check_circle' : 'radio_button_unchecked' }}</mat-icon>
                          <code>cvd-host_package.tar.gz</code> - Host Orchestrator package
                        </li>
                        <li [class.found]="hasSystemImage()">
                          <mat-icon>{{ hasSystemImage() ? 'check_circle' : 'radio_button_unchecked' }}</mat-icon>
                          <code>*-img-*.zip</code> - System image file
                        </li>
                      </ul>
                      <button type="button" mat-flat-button
                        (click)="openGcsBrowser()"
                        [disabled]="!draft().gcsBucketName">
                        <mat-icon>folder_open</mat-icon>
                        Browse Files in Bucket
                      </button>
                    </div>

                    <!-- Selected Files Display -->
                    @if (selectedFileChips().length > 0) {
                      <div class="selected-files">
                        <div class="selected-header">
                          <mat-icon>check_circle</mat-icon>
                          <span>{{ selectedFileChips().length }} file{{ selectedFileChips().length > 1 ? 's' : '' }} selected from gs://{{ bucketName() }}</span>
                          <button type="button" mat-button color="warn" (click)="clearGcsSelection()">Clear</button>
                        </div>
                        <mat-chip-set class="file-chips">
                          @for (file of selectedFileChips(); track file.name) {
                            <mat-chip class="file-chip">
                              <mat-icon matChipAvatar>{{ file.icon }}</mat-icon>
                              {{ file.displayName }}
                            </mat-chip>
                          }
                        </mat-chip-set>
                        @if (hasHostPackage() && hasSystemImage()) {
                          <div class="complete-indicator">
                            <mat-icon>verified</mat-icon>
                            <span>All required files selected</span>
                          </div>
                        }
                      </div>
                    }

                    <mat-form-field appearance="outline" class="full-width">
                      <mat-label>Target</mat-label>
                      <input matInput [formField]="buildForm.target" placeholder="e.g., aosp_cf_x86_64_only_phone-userdebug" />
                      <mat-hint>The lunch target name for this build</mat-hint>
                    </mat-form-field>

                  </div>
                }

                <!-- URL Form (Direct URLs) -->
                @if (customSource() === 'url') {
                  <div class="url-form">
                    <p class="step-description">
                      Enter the URLs for your build artifacts. You need <strong>both</strong>:
                    </p>
                    <ul class="required-files">
                      <li [class.found]="draft().urlArtifacts.length >= 1">
                        <mat-icon>{{ draft().urlArtifacts.length >= 1 ? 'check_circle' : 'radio_button_unchecked' }}</mat-icon>
                        <code>cvd-host_package.tar.gz</code> - Host Orchestrator package
                      </li>
                      <li [class.found]="draft().urlArtifacts.length >= 2">
                        <mat-icon>{{ draft().urlArtifacts.length >= 2 ? 'check_circle' : 'radio_button_unchecked' }}</mat-icon>
                        <code>*-img-*.zip</code> - System image file
                      </li>
                    </ul>

                    <div class="url-input-section">
                      @for (artifactField of urlArtifactFields(); track $index; let i = $index) {
                        <div class="url-input-row">
                          <mat-form-field appearance="outline" class="url-role-field">
                            <mat-label>Role</mat-label>
                            <mat-select [formField]="artifactField.role">
                              @for (r of artifactRoles; track r.value) {
                                <mat-option [value]="r.value">{{ r.label }}</mat-option>
                              }
                            </mat-select>
                          </mat-form-field>
                          <mat-form-field appearance="outline" class="url-field">
                            <mat-label>URL</mat-label>
                            <input matInput [formField]="artifactField.url"
                              placeholder="gs://bucket/path/file.zip or https://example.com/file.zip" />
                          </mat-form-field>
                          <button type="button" mat-icon-button color="warn" aria-label="Remove artifact" (click)="removeUrlArtifact(i)">
                            <mat-icon>delete</mat-icon>
                          </button>
                        </div>
                      }
                      <button type="button" mat-stroked-button (click)="addUrlArtifact()">
                        <mat-icon>add</mat-icon> Add Artifact URL
                      </button>
                    </div>

                    <mat-form-field appearance="outline" class="full-width">
                      <mat-label>Target</mat-label>
                      <input matInput [formField]="buildForm.target" placeholder="e.g., aosp_cf_x86_64_only_phone-userdebug" />
                      <mat-hint>The lunch target name for this build</mat-hint>
                    </mat-form-field>
                  </div>
                }
              </div>
            }
    
            <!-- 3. Build Details -->
            <div class="form-section">
              <h3>Build Details</h3>
              <mat-form-field appearance="outline" class="full-width">
                <mat-label>Name</mat-label>
                <input matInput [formField]="buildForm.name" placeholder="Friendly name for this build" />
                @for (error of buildForm.name().errors(); track error.kind) {
                  <mat-error>{{ error.message }}</mat-error>
                }
              </mat-form-field>
    
              <mat-form-field appearance="outline" class="full-width">
                <mat-label>Description (optional)</mat-label>
                <textarea matInput [formField]="buildForm.description" rows="2"></textarea>
              </mat-form-field>
    
              <mat-checkbox [formField]="buildForm.isDefault">
                Set as default build
              </mat-checkbox>
            </div>
    
            <!-- Actions -->
            <div class="actions">
              <button mat-button type="button" routerLink="/builds">Cancel</button>
              <button
                mat-flat-button
                type="button"
                (click)="onSubmit()"
                [disabled]="buildForm().invalid() || loading()"
                >
                @if (loading()) {
                  <mat-spinner aria-label="Loading" diameter="20" />
                }
                @if (!loading()) {
                  <span>Register Build</span>
                }
              </button>
            </div>
          </form>
        </mat-card-content>
      </mat-card>
      </div>
    </app-page-layout>
    `,
  styleUrls: ['./build-create.component.scss'],
})
export class BuildCreateComponent {
  private readonly buildStore = inject(BuildStore);
  private readonly router = inject(Router);
  private readonly dialog = inject(MatDialog);

  loading = this.buildStore.loading;
  error = this.buildStore.error;

  // UI State
  protected readonly viewMode = signal<'quickstart' | 'custom'>('quickstart');
  protected readonly customSource = signal<'ci' | 'gcs' | 'url'>('ci');

  // Available artifact roles for the role dropdown.
  readonly artifactRoles = ARTIFACT_ROLES;

  // Multi-file GCS selection state
  private readonly selectedGcsFiles = signal<StorageObject[]>([]);
  private readonly selectedGcsUris = signal<string[]>([]);

  private readonly model = signal<BuildFormModel>(quickStartModel());

  /** Read-only view of the form model for template bindings. */
  protected readonly draft = this.model.asReadonly();

  protected readonly buildForm = form(this.model, draft => {
    required(draft.name, {message: 'Name is required'});
  });

  /** FieldTree per URL-artifact row, for [formField] bindings. */
  protected readonly urlArtifactFields = computed(() =>
    this.draft().urlArtifacts.map((_, i) => this.buildForm.urlArtifacts[i])
  );

  protected readonly selectedFileChips = computed(() =>
    this.selectedGcsFiles().map(file => ({
      name: file.name,
      displayName: file.displayName,
      icon: fileIcon(file.displayName),
    }))
  );

  protected readonly hasHostPackage = computed(() =>
    this.selectedGcsFiles().some(
      f =>
        f.displayName.toLowerCase().includes('cvd-host_package') ||
        f.displayName.toLowerCase().includes('host_package')
    )
  );

  protected readonly hasSystemImage = computed(() =>
    this.selectedGcsFiles().some(
      f =>
        f.displayName.toLowerCase().endsWith('.zip') &&
        (f.displayName.toLowerCase().includes('-img-') ||
          f.displayName.toLowerCase().includes('_img_'))
    )
  );

  setViewMode(mode: 'quickstart' | 'custom'): void {
    this.viewMode.set(mode);
    this.selectedGcsFiles.set([]);
    this.selectedGcsUris.set([]);
    if (mode === 'quickstart') {
      this.model.set(quickStartModel());
    } else {
      this.customSource.set('ci');
      this.model.set({
        name: '',
        description: '',
        isDefault: false,
        source: 'android_build',
        branch: '',
        target: '',
        buildId: '',
        gcsBucketName: '',
        urlArtifacts: [],
      });
    }
  }

  setCustomSource(source: 'ci' | 'gcs' | 'url'): void {
    this.customSource.set(source);
    if (source === 'gcs' || source === 'url') {
      this.model.update(m => ({
        ...m,
        source: 'url',
        target: 'aosp_cf_x86_64_only_phone-userdebug',
        urlArtifacts:
          source === 'url' && m.urlArtifacts.length === 0
            ? [
                {url: '', role: 'host_package'},
                {url: '', role: 'default_build'},
              ]
            : m.urlArtifacts,
      }));
    } else {
      this.model.update(m => ({...m, source: 'android_build'}));
    }
  }

  addUrlArtifact(): void {
    this.model.update(m => ({
      ...m,
      urlArtifacts: [...m.urlArtifacts, {url: '', role: 'default_build'}],
    }));
  }

  removeUrlArtifact(index: number): void {
    this.model.update(m => ({
      ...m,
      urlArtifacts: m.urlArtifacts.filter((_, i) => i !== index),
    }));
  }

  /**
   * Open the GCS browser dialog for multi-file selection
   */
  /** The bucket the operator meant, whatever shape they pasted. */
  protected readonly bucketName = computed(() => bucketNameFrom(this.draft().gcsBucketName));

  async openGcsBrowser(): Promise<void> {
    const bucket = bucketNameFrom(this.draft().gcsBucketName);
    if (!bucket) {
      return;
    }

    const dialogData: GcsBrowserDialogData = {
      title: 'Select Cuttlefish Build Artifacts',
      selectMode: 'multi-file',
      requiredTypes: ['tar.gz', 'zip'],
      initialBucket: bucket,
    };

    const dialogRef = this.dialog.open<
      GcsBrowserComponent,
      GcsBrowserDialogData,
      GcsBrowserMultiSelectResult
    >(GcsBrowserComponent, {
      width: '800px',
      maxHeight: '80vh',
      data: dialogData,
    });

    const result = await firstValueFrom(dialogRef.afterClosed());
    if (result?.objects && result.objects.length > 0) {
      this.handleGcsSelection(result);
    }
  }

  /**
   * Handle the result from GCS browser multi-file selection
   */
  private handleGcsSelection(result: GcsBrowserMultiSelectResult): void {
    this.selectedGcsFiles.set(result.objects);
    this.selectedGcsUris.set(result.gsUris);

    // Try to derive a build name from the image file
    const imageFile = result.objects.find(
      o =>
        o.displayName.toLowerCase().endsWith('.zip') &&
        o.displayName.toLowerCase().includes('-img-')
    );
    if (imageFile && !this.draft().name) {
      const buildName = this.extractBuildName(imageFile.displayName);
      if (buildName) {
        this.model.update(m => ({...m, name: buildName}));
      }
    }
  }

  /**
   * Extract a friendly build name from image filename
   */
  private extractBuildName(filename: string): string | null {
    // Pattern: aosp_cf_x86_64_phone-img-12345678.zip
    const match = /^(.+)-img-(\d+)\.zip$/i.exec(filename);
    if (match) {
      return `${match[1]} (Build ${match[2]})`;
    }
    // Fallback to filename without extension
    return filename.replace(/\.[^.]+$/, '');
  }

  /**
   * Clear GCS file selection
   */
  clearGcsSelection(): void {
    this.selectedGcsFiles.set([]);
    this.selectedGcsUris.set([]);
  }

  goBack(): void {
    void this.router.navigate(['/builds']);
  }

  onSubmit(): void {
    if (this.buildForm().invalid()) {
      return;
    }

    const value = this.model();

    let artifactSources: ArtifactSource[] | undefined;
    if (value.source === 'url') {
      if (this.customSource() === 'gcs' && this.selectedGcsUris().length > 0) {
        // GCS browser selection - convert gs:// URIs to artifact sources with auto-detected roles.
        artifactSources = this.selectedGcsUris().map(uri => ({
          type: 'gcs' as const,
          url: uri,
          role: detectArtifactRoleFromUrl(uri),
        }));
      } else if (this.customSource() === 'url') {
        // Direct URL entry - filter out empty entries
        artifactSources = value.urlArtifacts
          .filter(a => a.url.trim().length > 0)
          .map(a => {
            const url = a.url.trim();
            // The scheme already says which store this is; asking again only
            // invited the two to disagree.
            return {
              type: url.startsWith('gs://') ? ('gcs' as const) : ('https' as const),
              url,
              role: a.role,
            };
          });
      }
    }

    const request = {
      name: value.name,
      source: value.source,
      buildId: value.buildId || undefined,
      target: value.target || undefined,
      branch: value.branch || undefined,
      description: value.description || undefined,
      isDefault: value.isDefault,
      artifactSources,
    };

    this.buildStore.create(request);
  }
}

function quickStartModel(): BuildFormModel {
  // aosp-android-latest-release is the branch that publishes fetchable CF
  // artifacts under the 2026 trunk-stable model; aosp-main no longer does.
  return {
    name: 'AOSP Latest Release Phone',
    description: 'Latest public build from aosp-android-latest-release',
    isDefault: false,
    source: 'android_build',
    branch: 'aosp-android-latest-release',
    target: 'aosp_cf_x86_64_only_phone-userdebug',
    buildId: '',
    gcsBucketName: '',
    urlArtifacts: [],
  };
}

function fileIcon(displayName: string): string {
  const name = displayName.toLowerCase();
  if (name.endsWith('.zip') || name.endsWith('.tar.gz') || name.endsWith('.tgz')) {
    return 'archive';
  }
  if (name.endsWith('.img')) {
    return 'disc_full';
  }
  return 'insert_drive_file';
}

// Role inference for GCS selections. The backend enforces the full contract
// (exactly one host_package .tar.gz + one default_build .zip per build), so a
// misdetected selection fails registration with the precise reason; the
// fallback covers the common two-file case, not arbitrary sets.
function detectArtifactRoleFromUrl(url: string): ArtifactRole {
  const filename = (url.split('/').pop() ?? '').toLowerCase();
  if (/cvd-host[_-]package\.tar\.gz$/.test(filename)) {
    return 'host_package';
  }
  // Any target's image zip (aosp_cf_..., custom targets alike) is named
  // <target>-img-<build>.zip by the Android build system.
  if (/-img-.*\.zip$/.test(filename)) {
    return 'default_build';
  }
  return 'default_build';
}
