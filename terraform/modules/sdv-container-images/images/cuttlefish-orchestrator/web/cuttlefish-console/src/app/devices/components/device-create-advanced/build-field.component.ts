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
  effect,
  input,
  output,
  signal,
  viewChild,
} from '@angular/core';
import type {FieldTree} from '@angular/forms/signals';
import {FormField} from '@angular/forms/signals';
import {MatAutocompleteModule} from '@angular/material/autocomplete';
import {MatButtonModule} from '@angular/material/button';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatInputModule} from '@angular/material/input';
import type {Build} from '../../../builds/models/build.model';
import {getBuildIdentifier} from '../../../builds/models/build.model';
import {buildValueLabel, isRegisteredBuildId} from './instance-draft.model';

/**
 * One build choice: lists the registered builds and takes a pasted gs://,
 * https:// or `@ab/` value in the same field. The value is a registered build
 * id, the pasted text, or '' (the platform default when one is offered).
 * Browse asks the parent, which owns the storage picker.
 */
@Component({
  selector: 'app-build-field',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [FormField, MatAutocompleteModule, MatButtonModule, MatFormFieldModule, MatInputModule],
  template: `
    <div class="field-row">
      <mat-form-field appearance="outline" class="build-field">
        <mat-label>{{ label() }}</mat-label>
        <input
          #input
          matInput
          spellcheck="false"
          autocomplete="off"
          [formField]="field()"
          [matAutocomplete]="auto"
          placeholder="gs://bucket/path/image-img.zip"
          (input)="onInput()"
          (blur)="onBlur()">
        <mat-autocomplete #auto="matAutocomplete" [displayWith]="displayWith" (optionSelected)="onSelected()">
          @if (showPlatformDefault()) {
            <mat-option value="">{{ platformDefault() }}</mat-option>
          }
          @for (option of options(); track option.id) {
            <mat-option [value]="option.id">
              <span class="option-name">{{ option.name }}</span>
              @if (option.source) {
                <span class="option-meta">{{ option.source }}</span>
              }
            </mat-option>
          }
        </mat-autocomplete>
        @if (hint()) {
          <mat-hint>{{ hint() }}</mat-hint>
        }
        @for (error of field()().errors(); track error.kind) {
          <mat-error>{{ error.message }}</mat-error>
        }
      </mat-form-field>
      <button
        type="button"
        mat-stroked-button
        class="browse-button"
        [attr.aria-label]="'Browse Cloud Storage for ' + label()"
        (click)="browse.emit()">
        Browse
      </button>
    </div>
  `,
  styles: `
    .field-row {
      display: flex;
      align-items: flex-start;
      gap: 8px;
    }

    .build-field {
      flex: 1;
      min-width: 0;
    }

    /* Lines up with the outline field's input box; its hint hangs below. */
    .browse-button {
      margin-top: 10px;
    }

    .option-name {
      display: block;
      font-size: 13px;
    }

    .option-meta {
      display: block;
      font-size: 11px;
      color: var(--cco-text-secondary);
      overflow: hidden;
      text-overflow: ellipsis;
    }
  `,
})
export class BuildFieldComponent {
  readonly field = input.required<FieldTree<string>>();
  readonly label = input.required<string>();
  readonly builds = input.required<readonly Build[]>();
  readonly hint = input('');
  /** What an empty value means, offered as the first option; null when '' is no choice. */
  readonly platformDefault = input<string | null>(null);

  /** Asks the parent to pick a gs:// image zip or folder for this field. */
  readonly browse = output<void>();

  private readonly input = viewChild.required<ElementRef<HTMLInputElement>>('input');

  /** The text being typed, which filters the list; null once the field settles. */
  private readonly typed = signal<string | null>(null);

  /** Each registered build with where it comes from, filtered by the typed text. */
  protected readonly options = computed(() => {
    const query = (this.typed() ?? '').trim().toLowerCase();
    if (query !== '' && !isRegisteredBuildId(query)) {
      return [];
    }
    return this.builds()
      .map(build => ({
        id: build.id,
        name: build.name,
        source: getBuildIdentifier(build) || build.target || build.branch || '',
      }))
      .filter(
        option =>
          query === '' ||
          option.name.toLowerCase().includes(query) ||
          option.source.toLowerCase().includes(query)
      );
  });

  protected readonly showPlatformDefault = computed(
    () => this.platformDefault() !== null && (this.typed() ?? '').trim() === ''
  );

  protected readonly displayWith = (value: string): string => this.display(value);

  constructor() {
    // The autocomplete writes its text only when the value changes, so a build
    // name that loads later, or a platform default that comes or goes with the
    // host choice, would leave stale text behind. Text being typed is left
    // alone.
    effect(() => {
      const value = this.field()().value();
      const text = this.display(value);
      if (this.typed() !== value) {
        this.input().nativeElement.value = text;
      }
    });
  }

  private display(value: string): string {
    if (value.trim() === '') {
      return this.platformDefault() ?? '';
    }
    return buildValueLabel(value, this.builds());
  }

  protected onInput(): void {
    this.typed.set(this.input().nativeElement.value);
  }

  protected onBlur(): void {
    this.typed.set(null);
  }

  protected onSelected(): void {
    this.typed.set(null);
  }
}
