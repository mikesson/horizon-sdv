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

import {ChangeDetectionStrategy, Component, computed, input, output} from '@angular/core';
import type {FieldTree} from '@angular/forms/signals';
import {FormField} from '@angular/forms/signals';
import {MatButtonToggleModule} from '@angular/material/button-toggle';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatInputModule} from '@angular/material/input';
import type {Build} from '../../../builds/models/build.model';
import {BuildPickerComponent} from '../../../devices/components/build-picker/build-picker.component';
import {LoadingStateComponent} from '../../../shared/components/loading-state/loading-state.component';
import {StepHeaderComponent} from '../../../shared/components/step-header/step-header.component';
import type {BatchIntent} from './batch-intent.model';
import {memberNameRange} from './batch-intent.model';

/** The subset of BatchIntent this step binds. */
export interface TemplateDraftFields {
  namePrefix: string;
  trackBranch: string;
  trackTarget: string;
}

/**
 * Step 1 (Template): the one device description every member shares, meaning
 * its name prefix and which Android to run.
 */
@Component({
  selector: 'app-batch-template-step',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    FormField,
    MatButtonToggleModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
    BuildPickerComponent,
    LoadingStateComponent,
    StepHeaderComponent,
  ],
  template: `
    <app-step-header
      icon="dataset"
      title="Template"
      description="Every member of the batch is created from this one description" />

    <mat-form-field appearance="outline" class="prefix-field" subscriptSizing="dynamic">
      <mat-label>Name prefix</mat-label>
      <input matInput [formField]="fields().namePrefix" placeholder="ci-nightly">
      <mat-hint>{{ nameHint() }}</mat-hint>
      @for (error of fields().namePrefix().errors(); track error.kind) {
        <mat-error>{{ error.message }}</mat-error>
      }
    </mat-form-field>

    <h3 class="section-title">Android build</h3>
    <mat-button-toggle-group
      class="build-mode-toggle"
      [value]="intent().buildKind"
      (change)="buildKindChanged.emit($event.value)">
      <mat-button-toggle value="default">
        <mat-icon>star</mat-icon>
        Platform default
      </mat-button-toggle>
      <mat-button-toggle value="registered">
        <mat-icon>inventory_2</mat-icon>
        Registered build
      </mat-button-toggle>
      <mat-button-toggle value="track">
        <mat-icon>autorenew</mat-icon>
        Track a branch
      </mat-button-toggle>
    </mat-button-toggle-group>

    @switch (intent().buildKind) {
      @case ('default') {
        <p class="mode-hint">
          @if (defaultBuildName(); as name) {
            Every member runs <strong>{{ name }}</strong>, the platform default build.
          } @else {
            Every member runs the latest AOSP phone build, registered in Builds
            on the way through.
          }
        </p>
      }
      @case ('registered') {
        @if (buildsLoading()) {
          <app-loading-state size="section" label="Loading builds…" />
        } @else {
          <app-build-picker
            [builds]="builds()"
            [selectedId]="intent().buildId"
            (selectionChange)="buildSelected.emit($event)" />
        }
      }
      @case ('track') {
        <p class="mode-hint">
          The newest build on the branch, resolved once for the whole batch, so
          every member runs the same one.
        </p>
        <div class="track-fields">
          <mat-form-field appearance="outline" subscriptSizing="dynamic">
            <mat-label>Branch</mat-label>
            <input matInput [formField]="fields().trackBranch">
            @for (error of fields().trackBranch().errors(); track error.kind) {
              <mat-error>{{ error.message }}</mat-error>
            }
          </mat-form-field>
          <mat-form-field appearance="outline" subscriptSizing="dynamic">
            <mat-label>Target</mat-label>
            <input matInput [formField]="fields().trackTarget">
            @for (error of fields().trackTarget().errors(); track error.kind) {
              <mat-error>{{ error.message }}</mat-error>
            }
          </mat-form-field>
        </div>
      }
      @default {}
    }
  `,
  styles: `
    .prefix-field {
      width: 100%;
      max-width: 360px;
    }

    .section-title {
      margin: var(--cco-space-32) 0 var(--cco-space-12);
      font-size: var(--cco-text-body);
      font-weight: 500;
    }

    .build-mode-toggle {
      margin-bottom: var(--cco-space-16);
    }

    .mode-hint {
      margin: 0 0 var(--cco-space-16);
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
      line-height: 1.4;
    }

    .track-fields {
      display: flex;
      flex-wrap: wrap;
      gap: var(--cco-space-16);
    }

    .track-fields mat-form-field {
      flex: 1 1 260px;
    }
  `,
})
export class BatchTemplateStepComponent {
  readonly fields = input.required<FieldTree<TemplateDraftFields>>();
  readonly intent = input.required<BatchIntent>();
  readonly builds = input.required<Build[]>();
  readonly buildsLoading = input.required<boolean>();
  readonly defaultBuildName = input.required<string | null>();

  readonly buildKindChanged = output<BatchIntent['buildKind']>();
  readonly buildSelected = output<Build>();

  /** Shows the names admission will assign, so the prefix is not a guess. */
  protected readonly nameHint = computed(() => {
    const {namePrefix, count} = this.intent();
    return namePrefix
      ? `Members: ${memberNameRange(namePrefix, count)}`
      : 'Members are numbered from this prefix';
  });
}
