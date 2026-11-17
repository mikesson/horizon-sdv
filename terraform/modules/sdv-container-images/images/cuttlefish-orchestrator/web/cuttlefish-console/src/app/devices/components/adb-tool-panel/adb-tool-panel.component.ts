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

import {ChangeDetectionStrategy, Component, input, output, signal} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatIconModule} from '@angular/material/icon';
import {MatProgressSpinnerModule} from '@angular/material/progress-spinner';
import type {AdbConnectionState} from '../../services/adb/adb-session';

/**
 * The shared chrome of a device tool panel (file browser, app manager, …): a
 * drag-drop container with an accent-tinted drop overlay and the ADB-gated
 * `waiting → loading → error → ready` state machine. Consumers project their
 * toolbar (`[toolbar]`), the ready-state body (default slot), an absolute
 * progress overlay (`[overlay]`), and an optional footer (`[footer]`); the
 * body renders only once ADB is connected and the listing has loaded. The
 * `--panel-accent` colour is set from the `accent` input.
 */
@Component({
  selector: 'app-adb-tool-panel',
  imports: [MatIconModule, MatButtonModule, MatProgressSpinnerModule],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div
      class="adb-tool-panel"
      [class.drag-over]="isDragOver()"
      [style.--panel-accent]="accent()"
      (dragover)="onDragOver($event)"
      (dragleave)="onDragLeave($event)"
      (drop)="onDrop($event)"
    >
      <ng-content select="[toolbar]" />

      <div class="content">
        @if (adbState() !== 'connected') {
          <div class="state waiting-adb">
            <mat-spinner aria-label="Loading" diameter="32" />
            <span>Waiting for ADB connection...</span>
            <span class="hint">{{ waitingHint() }}</span>
          </div>
        } @else if (isLoading()) {
          <div class="state loading">
            <mat-spinner aria-label="Loading" diameter="32" />
            <span>{{ loadingLabel() }}</span>
          </div>
        } @else if (error(); as err) {
          <div class="state error">
            <mat-icon>error</mat-icon>
            <span>{{ err }}</span>
            <button type="button" mat-button (click)="retry.emit()">Retry</button>
          </div>
        } @else {
          <ng-content />
        }
      </div>

      <ng-content select="[footer]" />

      @if (isDragOver()) {
        <div class="drop-overlay">
          <mat-icon>{{ dropIcon() }}</mat-icon>
          <span>{{ dropLabel() }}</span>
        </div>
      }

      <ng-content select="[overlay]" />
    </div>
  `,
  styles: `
      :host {
        display: block;
        height: 100%;
      }

      .adb-tool-panel {
        display: flex;
        flex-direction: column;
        height: 100%;
        background: var(--cco-surface);
        position: relative;
      }

      .content {
        flex: 1;
        overflow: auto;
        padding: 8px;
      }

      .state {
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        height: 100%;
        color: var(--cco-text-secondary);
        gap: 16px;
      }

      .state .hint {
        font-size: 12px;
        color: var(--cco-text-disabled);
      }

      .state.error mat-icon {
        font-size: 48px;
        width: 48px;
        height: 48px;
        color: var(--cco-error);
      }

      .drop-overlay {
        position: absolute;
        inset: 0;
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        background: rgba(0, 0, 0, 0.85);
        color: var(--panel-accent, var(--cco-success));
        gap: 16px;
        z-index: 100;
      }

      .drop-overlay mat-icon {
        font-size: 64px;
        width: 64px;
        height: 64px;
      }

      .adb-tool-panel.drag-over {
        outline: 2px dashed var(--panel-accent, var(--cco-success));
        outline-offset: -2px;
      }
    `,
})
export class AdbToolPanelComponent {
  readonly adbState = input.required<AdbConnectionState>();
  readonly isLoading = input.required<boolean>();
  readonly error = input.required<string | null>();

  readonly waitingHint = input('This panel will load when ADB is ready');
  readonly loadingLabel = input('Loading...');
  readonly dropIcon = input('cloud_upload');
  readonly dropLabel = input('Drop files to upload');
  readonly accent = input('#3fb950');

  readonly filesDropped = output<File[]>();
  readonly retry = output<void>();

  protected readonly isDragOver = signal(false);

  protected onDragOver(event: DragEvent): void {
    event.preventDefault();
    event.stopPropagation();
    this.isDragOver.set(true);
  }

  protected onDragLeave(event: DragEvent): void {
    event.preventDefault();
    event.stopPropagation();
    this.isDragOver.set(false);
  }

  protected onDrop(event: DragEvent): void {
    event.preventDefault();
    event.stopPropagation();
    this.isDragOver.set(false);

    const files = Array.from(event.dataTransfer?.files ?? []);
    if (files.length > 0) {
      this.filesDropped.emit(files);
    }
  }
}
