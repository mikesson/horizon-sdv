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

import {ChangeDetectionStrategy, Component, inject, signal} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MAT_DIALOG_DATA, MatDialogModule, MatDialogRef} from '@angular/material/dialog';
import {MatIconModule} from '@angular/material/icon';
import {MatProgressSpinnerModule} from '@angular/material/progress-spinner';
import {getErrorMessage} from '../../../shared/utils';
import type {Build} from '../../models/build.model';
import {BuildApiService} from '../../services/build-api.service';

export interface QuickStartDialogData {
  /** Register the build as the platform default (empty-registry bootstrap). */
  makeDefault: boolean;
}

/**
 * One-click registration of the public AOSP quick-start build. It carries the
 * same content as build-create's Quick Start mode, as a dialog so create flows
 * that need a registered build (e.g. a chosen host) never lose their state
 * to a page detour. Closes with the created Build, or undefined on cancel.
 */
@Component({
  selector: 'app-quick-start-dialog',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatButtonModule, MatDialogModule, MatIconModule, MatProgressSpinnerModule],
  template: `
    <h2 mat-dialog-title>
      <mat-icon>smartphone</mat-icon>
      Register the quick-start build
    </h2>

    <mat-dialog-content>
      <p>
        Registers the <strong>latest public AOSP trunk build for Phone</strong>
        as a tracking build, so it resolves to the newest build on its branch
        at every device launch.
      </p>
      <div class="build-facts">
        <div class="fact">
          <span class="fact-label">Branch</span>
          <code>aosp-android-latest-release</code>
        </div>
        <div class="fact">
          <span class="fact-label">Target</span>
          <code>aosp_cf_x86_64_only_phone-userdebug</code>
        </div>
      </div>

      @if (error(); as message) {
        <div class="error-note">
          <mat-icon>error_outline</mat-icon>
          <span>{{ message }}</span>
        </div>
      }
    </mat-dialog-content>

    <mat-dialog-actions align="end">
      <button type="button" mat-button mat-dialog-close [disabled]="registering()">Cancel</button>
      <button type="button" mat-flat-button (click)="register()" [disabled]="registering()">
        @if (registering()) {
          <mat-spinner aria-label="Loading" diameter="18" />
        } @else {
          <mat-icon>add</mat-icon>
        }
        Register Build
      </button>
    </mat-dialog-actions>
  `,
  styles: `
    h2[mat-dialog-title] {
      display: flex;
      align-items: center;
      gap: 8px;
    }

    h2 mat-icon {
      color: var(--cco-accent);
    }

    mat-dialog-content p {
      color: var(--cco-text-secondary);
      max-width: 420px;
    }

    .build-facts {
      display: flex;
      flex-direction: column;
      gap: 8px;
      padding: 12px;
      background: var(--cco-surface-variant);
      border: 1px solid var(--cco-border);
      border-radius: 8px;
    }

    .fact {
      display: flex;
      align-items: center;
      gap: 12px;
      font-size: 12px;
    }

    .fact-label {
      min-width: 56px;
      color: var(--cco-text-secondary);
    }

    .error-note {
      display: flex;
      align-items: center;
      gap: 8px;
      margin-top: 12px;
      color: var(--cco-error);
      font-size: 12px;
    }

    button mat-spinner {
      display: inline-block;
      margin-right: 8px;
    }
  `,
})
export class QuickStartDialogComponent {
  private readonly buildApi = inject(BuildApiService);
  private readonly dialogRef =
    inject<MatDialogRef<QuickStartDialogComponent, Build | undefined>>(MatDialogRef);
  protected readonly data = inject<QuickStartDialogData>(MAT_DIALOG_DATA);

  protected readonly registering = signal(false);
  protected readonly error = signal<string | null>(null);

  protected register(): void {
    if (this.registering()) {
      return;
    }
    this.registering.set(true);
    this.error.set(null);
    this.buildApi
      .create({
        name: 'AOSP Latest Release Phone',
        source: 'android_build',
        branch: 'aosp-android-latest-release',
        target: 'aosp_cf_x86_64_only_phone-userdebug',
        description: 'Latest public build from aosp-android-latest-release',
        isDefault: this.data.makeDefault,
      })
      .subscribe({
        next: build => this.dialogRef.close(build),
        error: (err: unknown) => {
          this.registering.set(false);
          this.error.set(getErrorMessage(err, 'Failed to register the build'));
        },
      });
  }
}
