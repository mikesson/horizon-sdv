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

import {ChangeDetectionStrategy, Component, computed, inject, input, output} from '@angular/core';
import type {FieldTree} from '@angular/forms/signals';
import {FormField} from '@angular/forms/signals';
import {MatButtonModule} from '@angular/material/button';
import {MatDialog} from '@angular/material/dialog';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatInputModule} from '@angular/material/input';
import {MatSlideToggleModule} from '@angular/material/slide-toggle';
import type {Build} from '../../../builds/models/build.model';
import {GcsBrowserComponent} from '../../../storage/components/gcs-browser/gcs-browser.component';
import type {
  GcsBrowserDialogData,
  GcsBrowserDialogResult,
} from '../../../storage/models/storage.model';
import {BuildFieldComponent} from './build-field.component';
import {browseStart} from './gcs-browse';
import type {AdvancedDraft} from './instance-draft.model';
import {isGcsFolder} from './instance-draft.model';
import {firstInstanceBuild, isSdvDraft, sdvBuildHint} from './sdv-preset';

const BUILD_HINT =
  'A registered build, or paste a gs://, https:// or @ab/ link to an image zip or a build folder';

/**
 * Message for an SDV pair that has no host tools from any build. Also the
 * validation error the editor raises for it.
 */
export const HOST_TOOLS_REQUIRED_MESSAGE =
  "An image zip carries no host tools, so this SDV pair needs them here. Use the IVI build's cvd-host_package.tar.gz, or give the first instance a build folder.";

/** The only place the Advanced editor chooses builds and host tools. */
@Component({
  selector: 'app-builds-section',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    BuildFieldComponent,
    FormField,
    MatButtonModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
    MatSlideToggleModule,
  ],
  template: `
    <section class="config-section">
      <div class="builds-header">
        <h3>
          <mat-icon>android</mat-icon>
          Builds
        </h3>
        @if (showSwitch()) {
          <mat-slide-toggle
            class="same-build-toggle"
            [checked]="draft().sameBuild"
            (change)="sameBuildChange.emit($event.checked)">
            Same build for every instance
          </mat-slide-toggle>
        }
      </div>

      @if (note()) {
        <p class="builds-note" role="status">{{ note() }}</p>
      }

      @if (draft().sameBuild) {
        <app-build-field
          label="Build"
          [field]="form().deviceBuild"
          [builds]="builds()"
          [hint]="buildHint"
          [platformDefault]="platformDefault()"
          (browse)="browseBuild(form().deviceBuild)" />
      } @else {
        @for (inst of draft().instances; track inst.id; let i = $index) {
          <app-build-field
            [label]="inst.name || 'device'"
            [field]="form().instances[i].build"
            [builds]="builds()"
            [hint]="rowHints()[i]"
            (browse)="browseBuild(form().instances[i].build)" />
        }
      }

      @if (loading()) {
        <p class="builds-note">Loading registered builds…</p>
      }

      <div class="host-tools-row">
        <mat-form-field appearance="outline" class="host-tools-field">
          <mat-label>{{ hostToolsRequired() ? 'Host tools' : 'Host tools (optional)' }}</mat-label>
          <input matInput spellcheck="false" [formField]="form().hostPackage" placeholder="gs://bucket/path/cvd-host_package.tar.gz">
          <mat-hint>{{ hostToolsHint() }}</mat-hint>
          @for (error of form().hostPackage().errors(); track error.kind) {
            <mat-error>{{ error.message }}</mat-error>
          }
        </mat-form-field>
        <button
          type="button"
          mat-stroked-button
          class="browse-button"
          aria-label="Browse Cloud Storage for Host tools"
          (click)="browseHostTools()">
          Browse
        </button>
      </div>
    </section>
  `,
  styles: `
    .config-section {
      margin: 16px 0;
    }

    .builds-header {
      display: flex;
      flex-wrap: wrap;
      justify-content: space-between;
      align-items: center;
      gap: 8px 16px;
      margin-bottom: 12px;
    }

    .builds-header h3 {
      display: flex;
      align-items: center;
      gap: 8px;
      margin: 0;
      font-size: 14px;
      font-weight: 500;
      color: var(--cco-text);
    }

    .builds-header h3 mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
      color: var(--cco-accent);
    }

    .builds-note {
      margin: 0 0 12px;
      font-size: 12px;
      color: var(--cco-text-secondary);
    }

    .host-tools-row {
      display: flex;
      align-items: flex-start;
      gap: 8px;
      margin-top: 8px;
    }

    .host-tools-field {
      flex: 1;
      min-width: 0;
    }

    /* Lines up with the outline field's input box; its hint hangs below. */
    .browse-button {
      margin-top: 10px;
    }
  `,
})
export class BuildsSectionComponent {
  private readonly dialog = inject(MatDialog);

  readonly form = input.required<FieldTree<AdvancedDraft>>();
  readonly builds = input.required<readonly Build[]>();
  readonly loading = input(false);
  /** The platform default's label, or null when the placement cannot use it. */
  readonly platformDefault = input<string | null>(null);
  readonly hostToolsRequired = input(false);
  /** Said once after a switch change that replaced builds. */
  readonly note = input('');

  readonly sameBuildChange = output<boolean>();

  protected readonly buildHint = BUILD_HINT;

  protected readonly draft = computed(() => this.form()().value());

  /** One instance needs no switch, unless it is off and the user must be able to turn it on. */
  protected readonly showSwitch = computed(
    () => this.draft().instances.length > 1 || !this.draft().sameBuild
  );

  protected readonly rowHints = computed(() =>
    this.draft().instances.map(inst => sdvBuildHint(inst) || BUILD_HINT)
  );

  protected readonly hostToolsHint = computed(() => {
    if (this.hostToolsRequired()) {
      return HOST_TOOLS_REQUIRED_MESSAGE;
    }
    const draft = this.draft();
    if (isSdvDraft(draft)) {
      return isGcsFolder(firstInstanceBuild(draft))
        ? `A cvd-host_package.tar.gz URL. Empty takes the host tools from the build folder of ${draft.instances[0]?.name || 'device'}.`
        : "A cvd-host_package.tar.gz URL. The IVI build's package is the usual choice.";
    }
    return "A cvd-host_package.tar.gz URL. Empty uses the host tools of the device build, of the first registered instance build that has them, or of the first instance's build folder.";
  });

  /** A build takes an image zip, or the folder being viewed. */
  protected browseBuild(field: FieldTree<string>): void {
    this.pick(field, {title: 'Choose an image zip or a build folder', selectMode: 'folder'});
  }

  protected browseHostTools(): void {
    this.pick(this.form().hostPackage, {
      title: 'Choose a cvd-host_package.tar.gz',
      selectMode: 'file',
    });
  }

  /**
   * Opens the storage browser where the field's own gs:// value lives, else
   * where another field's does, since the builds of one device usually share
   * a bucket.
   */
  private pick(field: FieldTree<string>, data: Omit<GcsBrowserDialogData, 'initialBucket'>): void {
    const draft = this.draft();
    const others = [
      draft.deviceBuild,
      ...draft.instances.map(inst => inst.build),
      draft.hostPackage,
    ];
    this.dialog
      .open<GcsBrowserComponent, GcsBrowserDialogData, GcsBrowserDialogResult>(
        GcsBrowserComponent,
        {
          width: '800px',
          maxHeight: '80vh',
          data: {...data, ...browseStart([field().value(), ...others])},
        }
      )
      .afterClosed()
      .subscribe(result => {
        if (result?.gsUri) {
          field().value.set(result.gsUri);
          field().markAsTouched();
        }
      });
  }
}
