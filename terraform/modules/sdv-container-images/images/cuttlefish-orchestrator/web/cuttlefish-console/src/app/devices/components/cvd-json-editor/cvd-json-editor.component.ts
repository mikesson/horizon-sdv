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

/**
 * CVD JSON Editor Component
 *
 * CodeMirror 6 based JSON editor with schema validation and autocomplete
 * for CVD configuration editing in Advanced mode.
 *
 * Supports two modes:
 * - View mode: JSON reflects form state (read-only)
 * - Edit mode: User can directly edit JSON and apply to form
 */

import type {AfterViewInit, ElementRef, OnDestroy} from '@angular/core';
import {
  ChangeDetectionStrategy,
  Component,
  effect,
  inject,
  input,
  linkedSignal,
  output,
  signal,
  viewChild,
} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatButtonToggleModule} from '@angular/material/button-toggle';
import {MatIconModule} from '@angular/material/icon';
import {MatSnackBar, MatSnackBarModule} from '@angular/material/snack-bar';
import {MatTooltipModule} from '@angular/material/tooltip';
import {defaultKeymap, history, historyKeymap} from '@codemirror/commands';
import {HighlightStyle, syntaxHighlighting} from '@codemirror/language';
import {lintGutter} from '@codemirror/lint';
import type {Extension} from '@codemirror/state';
import {EditorState} from '@codemirror/state';
import {
  drawSelection,
  EditorView,
  highlightActiveLine,
  keymap,
  lineNumbers,
} from '@codemirror/view';
import {tags as t} from '@lezer/highlight';
import {jsonSchema} from 'codemirror-json-schema';
import {Subject} from 'rxjs';
import {debounceTime, takeUntil} from 'rxjs/operators';

import type {JsonSchema} from '../../../shared/utils/zod-to-json-schema';
import {getCvdConfigJsonSchema} from '../../../shared/utils/zod-to-json-schema';
import type {EnvironmentSpecification} from '../../models/cvd-config.schema';
import {EnvironmentSpecificationSchema} from '../../models/cvd-config.schema';

// Dark syntax + editor theme (no CodeMirror theme package installed).
const cvdHighlight = HighlightStyle.define([
  {tag: t.propertyName, color: '#9cdcfe'},
  {tag: t.string, color: '#a5d6a7'},
  {tag: [t.number, t.bool, t.null, t.keyword], color: '#82aaff'},
  {tag: [t.punctuation, t.separator], color: '#cfd2d6'},
  {tag: t.comment, color: '#80868b', fontStyle: 'italic'},
  {tag: t.invalid, color: '#f28b82'},
]);

const cvdEditorTheme = EditorView.theme(
  {
    '&': {color: 'var(--cco-text)', backgroundColor: 'transparent'},
    '.cm-content': {caretColor: 'var(--cco-accent)'},
    '.cm-cursor, .cm-dropCursor': {borderLeftColor: 'var(--cco-accent)'},
    '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection': {
      backgroundColor: 'rgba(138, 180, 248, 0.25)',
    },
    '.cm-gutters': {
      backgroundColor: 'transparent',
      color: 'var(--cco-text-secondary)',
      border: 'none',
    },
    '.cm-activeLine': {backgroundColor: 'rgba(255, 255, 255, 0.04)'},
    '.cm-activeLineGutter': {backgroundColor: 'rgba(255, 255, 255, 0.04)'},
  },
  {dark: true}
);

/**
 * Validation error from JSON parsing or schema validation
 */
export interface JsonValidationError {
  line: number;
  column: number;
  message: string;
  path?: string[];
}

/**
 * Sync state between form and JSON
 */
export type SyncState = 'synced' | 'editing' | 'error' | 'valid';

/**
 * Result of applying JSON to form
 */
export interface ApplyResult {
  success: boolean;
  config?: EnvironmentSpecification;
  errors?: JsonValidationError[];
}

@Component({
  selector: 'app-cvd-json-editor',
  imports: [
    MatIconModule,
    MatButtonModule,
    MatTooltipModule,
    MatButtonToggleModule,
    MatSnackBarModule,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="editor-container" [class.edit-mode]="editMode()">
      <div class="editor-toolbar">
        <div class="toolbar-left">
          <span class="toolbar-title">
            <mat-icon>code</mat-icon>
            CVD Configuration
          </span>

          <!-- Sync State Indicator -->
          <span [class]="['sync-indicator', syncState()]">
            @switch (syncState()) {
              @case ('synced') {
                <mat-icon>check_circle</mat-icon>
                <span>Synced</span>
              }
              @case ('editing') {
                <mat-icon>edit</mat-icon>
                <span>Editing...</span>
              }
              @case ('valid') {
                <mat-icon>check</mat-icon>
                <span>Valid</span>
              }
              @case ('error') {
                <mat-icon>error</mat-icon>
                <span>Invalid</span>
              }
              <!-- All SyncState members have cases; swap for default-never
                   once Angular 22.1 ships compile-checked exhaustiveness. -->
              @default {}
            }
          </span>
        </div>

        <div class="toolbar-actions">
          <!-- Mode Toggle -->
          <mat-button-toggle-group
            [value]="editMode() ? 'edit' : 'view'"
            (change)="onModeChange($event.value)"
            class="mode-toggle">
            <mat-button-toggle value="view" matTooltip="View mode - JSON reflects form">
              <mat-icon>visibility</mat-icon>
            </mat-button-toggle>
            <mat-button-toggle value="edit" matTooltip="Edit mode - Directly edit JSON">
              <mat-icon>edit</mat-icon>
            </mat-button-toggle>
          </mat-button-toggle-group>

          <div class="toolbar-divider"></div>

          <!-- Apply Button (only in edit mode with valid JSON) -->
          @if (editMode() && syncState() === 'valid') {
            <button type="button"
              mat-stroked-button
              color="primary"
              class="apply-button"
              matTooltip="Apply JSON changes to form"
              (click)="applyJson()">
              <mat-icon>sync</mat-icon>
              Apply
            </button>
          }

          <button type="button"
            mat-icon-button
            matTooltip="Format JSON"
            (click)="formatJson()" aria-label="Format JSON">
            <mat-icon>format_align_left</mat-icon>
          </button>
          <button type="button"
            mat-icon-button
            matTooltip="Copy to clipboard"
            (click)="copyToClipboard()" aria-label="Copy to clipboard">
            <mat-icon>content_copy</mat-icon>
          </button>
        </div>
      </div>

      <div class="editor-wrapper" #editorContainer></div>

      @if (validationErrors().length > 0) {
        <div class="error-panel">
          <div class="error-header">
            <mat-icon>error_outline</mat-icon>
            <span>{{ validationErrors().length }} error(s)</span>
          </div>
          <ul class="error-list">
            @for (error of validationErrors(); track $index) {
              <li>
                <button type="button" class="error-item" (click)="goToError(error)">
                  <span class="error-location">
                    @if (error.path && error.path.length > 0) {
                      {{ error.path.join('.') }}
                    } @else {
                      Line {{ error.line }}
                    }
                  </span>
                  <span class="error-message">{{ error.message }}</span>
                </button>
              </li>
            }
          </ul>
        </div>
      }
    </div>
  `,
  styles: `
    :host {
      display: block;
      --editor-bg: var(--cco-surface);
      --editor-border: var(--cco-border);
      --editor-text: var(--cco-text);
      --error-bg: var(--cco-error-surface);
      --error-text: var(--cco-error);
      --toolbar-bg: var(--cco-surface-variant);
      --success-color: var(--cco-success);
      --warning-color: var(--cco-warning);
      --primary-color: var(--cco-accent);
    }

    .editor-container {
      display: flex;
      flex-direction: column;
      border: 1px solid var(--editor-border);
      border-radius: 8px;
      overflow: hidden;
      transition: border-color var(--cco-duration-slow);
    }

    .editor-container.edit-mode {
      border-color: var(--primary-color);
      box-shadow: 0 0 0 1px var(--primary-color);
    }

    .editor-toolbar {
      display: flex;
      align-items: center;
      justify-content: space-between;
      padding: 8px 12px;
      background: var(--toolbar-bg);
      border-bottom: 1px solid var(--editor-border);
      flex-wrap: wrap;
      gap: 8px;
    }

    .toolbar-left {
      display: flex;
      align-items: center;
      gap: 12px;
    }

    .toolbar-title {
      display: flex;
      align-items: center;
      gap: 8px;
      font-size: 12px;
      font-weight: 500;
      color: var(--editor-text);
    }

    .toolbar-title mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
      color: var(--cco-text-secondary);
    }

    .sync-indicator {
      display: flex;
      align-items: center;
      gap: 4px;
      font-size: 11px;
      padding: 4px 8px;
      border-radius: 12px;
      font-weight: 500;
    }

    .sync-indicator mat-icon {
      font-size: 14px;
      width: 14px;
      height: 14px;
    }

    .sync-indicator.synced {
      background: var(--cco-success-surface);
      color: var(--success-color);
    }

    .sync-indicator.editing {
      background: var(--cco-warning-surface);
      color: var(--warning-color);
    }

    .sync-indicator.valid {
      background: var(--cco-accent-surface);
      color: var(--primary-color);
    }

    .sync-indicator.error {
      background: var(--error-bg);
      color: var(--error-text);
    }

    .toolbar-actions {
      display: flex;
      align-items: center;
      gap: 4px;
    }

    .toolbar-divider {
      width: 1px;
      height: 24px;
      background: var(--editor-border);
      margin: 0 4px;
    }

    .mode-toggle {
      height: 32px;
    }

    .mode-toggle ::ng-deep .mat-button-toggle-button {
      height: 32px;
      padding: 0 8px;
    }

    .mode-toggle mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
    }

    .apply-button {
      height: 32px;
      font-size: 12px;
    }

    .apply-button mat-icon {
      font-size: 16px;
      width: 16px;
      height: 16px;
      margin-right: 4px;
    }

    .toolbar-actions button[mat-icon-button] {
      width: 32px;
      height: 32px;
    }

    .editor-wrapper {
      min-height: 400px;
      max-height: 600px;
      overflow: auto;
      background: var(--editor-bg);
    }

    .editor-wrapper :global(.cm-editor) {
      height: 100%;
      min-height: 400px;
    }

    .editor-wrapper :global(.cm-scroller) {
      font-family: 'Roboto Mono', 'Consolas', monospace;
      font-size: 12px;
      line-height: 1.5;
    }

    .error-panel {
      background: var(--error-bg);
      border-top: 1px solid var(--cco-error-surface);
      max-height: 150px;
      overflow: auto;
    }

    .error-header {
      display: flex;
      align-items: center;
      gap: 8px;
      padding: 8px 12px;
      font-size: 12px;
      font-weight: 500;
      color: var(--error-text);
    }

    .error-header mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
    }

    .error-list {
      list-style: none;
      margin: 0;
      padding: 0 12px 8px;
    }

    .error-item {
      display: flex;
      gap: 8px;
      padding: 4px 8px;
      border-radius: 4px;
      cursor: pointer;
      font-size: 12px;
      /* A <button type="button"> inside the <li>, so the UA defaults have to be undone. */
      width: 100%;
      text-align: left;
      border: none;
      background: none;
      color: inherit;
      font-family: inherit;
    }

    .error-item:hover {
      background: rgba(197, 34, 31, 0.1);
    }

    .error-location {
      color: var(--error-text);
      font-weight: 500;
      white-space: nowrap;
      font-family: 'Roboto Mono', monospace;
      font-size: 11px;
    }

    .error-message {
      color: var(--cco-text-secondary);
    }
  `,
})
export class CvdJsonEditorComponent implements AfterViewInit, OnDestroy {
  private readonly snackBar = inject(MatSnackBar);

  /**
   * External JSON value (from form)
   */
  readonly externalValue = input<string>('');

  /**
   * Whether to start in edit mode
   */
  readonly startInEditMode = input<boolean>(false);

  /**
   * Emitted when user applies JSON changes
   */
  readonly applied = output<ApplyResult>();

  /**
   * Emitted when content changes (debounced)
   */
  readonly valueChange = output<string>();

  /**
   * Emitted when validation state changes
   */
  readonly validChange = output<boolean>();

  /**
   * Whether editor is in edit mode. Derived-but-writable: adopts
   * startInEditMode when it's true, but stays user-togglable and never forces
   * itself back to false (the linkedSignal form of the old copy-via-effect).
   */
  readonly editMode = linkedSignal<boolean, boolean>({
    source: () => this.startInEditMode(),
    computation: (start, prev) => start || (prev?.value ?? false),
  });

  /**
   * Current sync state
   */
  readonly syncState = signal<SyncState>('synced');

  /**
   * Current validation errors
   */
  readonly validationErrors = signal<JsonValidationError[]>([]);

  /**
   * Whether there are unsaved changes in edit mode
   */
  readonly hasUnsavedChanges = signal<boolean>(false);

  private readonly editorContainer = viewChild<ElementRef>('editorContainer');
  private editorView: EditorView | null = null;
  private readonly schema: JsonSchema;
  private readonly destroy$ = new Subject<void>();
  private readonly contentChange$ = new Subject<string>();
  private lastExternalValue = '';
  private isInternalUpdate = false;

  constructor() {
    this.schema = getCvdConfigJsonSchema();

    effect(() => {
      const value = this.externalValue();
      if (!this.editMode() && this.editorView && value !== this.lastExternalValue) {
        this.lastExternalValue = value;
        this.isInternalUpdate = true;
        this.setValue(value);
        this.isInternalUpdate = false;
        this.syncState.set('synced');
      }
    });

    this.contentChange$.pipe(debounceTime(300), takeUntil(this.destroy$)).subscribe(content => {
      this.handleContentChange(content);
    });
  }

  ngAfterViewInit(): void {
    this.initEditor();
  }

  ngOnDestroy(): void {
    this.destroy$.next();
    this.destroy$.complete();
    this.editorView?.destroy();
  }

  private initEditor(): void {
    const containerRef = this.editorContainer();
    if (!containerRef) {
      return;
    }
    const container = containerRef.nativeElement as HTMLElement;

    const extensions: Extension[] = [
      lineNumbers(),
      highlightActiveLine(),
      drawSelection(),
      history(),
      cvdEditorTheme,
      syntaxHighlighting(cvdHighlight),
      keymap.of([...defaultKeymap, ...historyKeymap]),
      lintGutter(),
      jsonSchema(this.schema),
      EditorView.updateListener.of(update => {
        if (update.docChanged && !this.isInternalUpdate) {
          const value = update.state.doc.toString();
          this.contentChange$.next(value);

          // Immediate feedback: show editing state
          if (this.editMode()) {
            this.syncState.set('editing');
            this.hasUnsavedChanges.set(true);
          }
        }
      }),
    ];

    const startDoc = this.externalValue() || this.getDefaultConfig();
    this.lastExternalValue = startDoc;

    this.editorView = new EditorView({
      state: EditorState.create({
        doc: startDoc,
        extensions,
      }),
      parent: container,
    });

    // Initial validation
    this.validateContent(startDoc);
  }

  private getDefaultConfig(): string {
    return JSON.stringify(
      {
        instances: [
          {
            '@import': 'phone',
            name: 'device-1',
          },
        ],
      },
      null,
      2
    );
  }

  private handleContentChange(content: string): void {
    this.valueChange.emit(content);
    this.validateContent(content);
  }

  private validateContent(content: string): void {
    const errors: JsonValidationError[] = [];
    let isValid = false;

    try {
      const parsed: unknown = JSON.parse(content);

      const result = EnvironmentSpecificationSchema.safeParse(parsed);

      if (result.success) {
        isValid = true;
      } else {
        for (const issue of result.error.issues) {
          errors.push({
            line: 1, // Zod doesn't provide line numbers
            column: 0,
            message: issue.message,
            path: issue.path.map(String),
          });
        }
      }
    } catch (e) {
      if (e instanceof SyntaxError) {
        const match = /at position (\d+)/.exec(e.message);
        const position = match ? Number.parseInt(match[1], 10) : 0;
        const lines = content.substring(0, position).split('\n');
        errors.push({
          line: lines.length,
          column: lines[lines.length - 1]?.length ?? 0,
          message: e.message,
        });
      }
    }

    this.validationErrors.set(errors);
    this.validChange.emit(isValid);

    if (this.editMode()) {
      this.syncState.set(isValid ? 'valid' : 'error');
    }
  }

  onModeChange(mode: 'view' | 'edit'): void {
    const newEditMode = mode === 'edit';

    if (this.editMode() && !newEditMode && this.hasUnsavedChanges()) {
      // Switching from edit to view with unsaved changes
      // Revert to external value
      this.isInternalUpdate = true;
      this.setValue(this.externalValue());
      this.isInternalUpdate = false;
      this.hasUnsavedChanges.set(false);
    }

    this.editMode.set(newEditMode);
    this.syncState.set(newEditMode ? 'editing' : 'synced');

    if (!newEditMode) {
      this.validationErrors.set([]);
    }
  }

  applyJson(): void {
    const content = this.getCurrentValue();

    try {
      const parsed: unknown = JSON.parse(content);
      const result = EnvironmentSpecificationSchema.safeParse(parsed);

      if (result.success) {
        this.applied.emit({
          success: true,
          config: result.data,
        });
        this.hasUnsavedChanges.set(false);
        this.syncState.set('synced');
        // Switch back to view mode after successful apply
        this.editMode.set(false);
      } else {
        const errors: JsonValidationError[] = result.error.issues.map(issue => ({
          line: 1,
          column: 0,
          message: issue.message,
          path: issue.path.map(String),
        }));
        this.applied.emit({
          success: false,
          errors,
        });
      }
    } catch (e) {
      this.applied.emit({
        success: false,
        errors: [
          {
            line: 1,
            column: 0,
            message: e instanceof Error ? e.message : 'Invalid JSON',
          },
        ],
      });
    }
  }

  getCurrentValue(): string {
    return this.editorView?.state.doc.toString() ?? '';
  }

  setValue(value: string): void {
    if (!this.editorView) {
      return;
    }

    this.editorView.dispatch({
      changes: {
        from: 0,
        to: this.editorView.state.doc.length,
        insert: value,
      },
    });
  }

  formatJson(): void {
    if (!this.editorView) {
      return;
    }

    try {
      const current = this.getCurrentValue();
      const parsed: unknown = JSON.parse(current);
      const formatted = JSON.stringify(parsed, null, 2);
      this.setValue(formatted);
    } catch {
      // Invalid JSON, can't format
    }
  }

  async copyToClipboard(): Promise<void> {
    const value = this.getCurrentValue();
    await navigator.clipboard.writeText(value);
    this.snackBar.open('Copied to clipboard', 'Dismiss', {duration: 2000});
  }

  goToError(error: JsonValidationError): void {
    if (!this.editorView) {
      return;
    }

    try {
      const line = this.editorView.state.doc.line(error.line);
      this.editorView.dispatch({
        selection: {anchor: line.from + error.column},
        scrollIntoView: true,
      });
      this.editorView.focus();
    } catch {
      // Line number out of range
    }
  }
}
