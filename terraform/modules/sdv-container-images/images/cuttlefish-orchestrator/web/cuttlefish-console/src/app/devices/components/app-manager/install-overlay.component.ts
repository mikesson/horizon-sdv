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
import {MatButtonModule} from '@angular/material/button';
import {MatIconModule} from '@angular/material/icon';
import {MatProgressBarModule} from '@angular/material/progress-bar';
import type {InstallState} from '../../services/adb/device-install.service';
import {installPercent, installStatusLabel} from './install-progress';

/**
 * Full-panel install status: an error with an optional remedy, a success with
 * an optional launch, or live upload progress with a cancel. This component is
 * presentational. The parent owns the install lifecycle and reacts to the
 * emitted intents.
 */
@Component({
  selector: 'app-install-overlay',
  imports: [MatButtonModule, MatIconModule, MatProgressBarModule],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @let s = state();
    @switch (s.phase) {
      @case ('error') {
        <mat-icon class="error-icon">error</mat-icon>
        <span class="title">{{ s.error?.message }}</span>
        <div class="actions">
          @if (s.error?.remedy; as remedy) {
            <button type="button" mat-flat-button (click)="applyRemedy.emit()">
              {{ remedy.label }}
            </button>
          }
          <button type="button" mat-button (click)="dismiss.emit()">Dismiss</button>
        </div>
      }
      @case ('done') {
        <mat-icon class="done-icon">check_circle</mat-icon>
        <span class="title">Installed</span>
        <div class="actions">
          @if (s.installedPackage) {
            <button type="button" mat-flat-button (click)="launch.emit()">Launch</button>
          }
          <button type="button" mat-button (click)="dismiss.emit()">Close</button>
        </div>
      }
      @default {
        <span class="title">
          {{ s.progress ? 'Uploading ' + s.progress.fileName : 'Preparing…' }}
        </span>
        <mat-progress-bar aria-label="Install progress"
          class="bar"
          [mode]="s.phase === 'committing' ? 'indeterminate' : 'determinate'"
          [value]="percent()"
        />
        <span class="bytes">{{ percent() }}% · {{ statusLabel() }}</span>
        <button type="button" mat-button (click)="cancelInstall.emit()">Cancel</button>
      }
    }
  `,
  styles: `
      :host {
        position: absolute;
        inset: 0;
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        background: rgba(0, 0, 0, 0.85);
        color: var(--cco-success);
        gap: 16px;
        z-index: 100;
      }

      .title {
        font-size: 14px;
        color: var(--cco-text);
        max-width: 80%;
        text-align: center;
        overflow: hidden;
        text-overflow: ellipsis;
      }

      .bar {
        width: 60%;
        max-width: 360px;
      }

      .bytes {
        font-size: 12px;
        color: var(--cco-text-secondary);
        font-family: monospace;
      }

      .actions {
        display: flex;
        gap: 8px;
      }

      .error-icon {
        font-size: 48px;
        width: 40px;
        height: 40px;
        color: var(--cco-error);
      }

      .done-icon {
        font-size: 48px;
        width: 40px;
        height: 40px;
        color: var(--cco-success);
      }
    `,
})
export class InstallOverlayComponent {
  readonly state = input.required<InstallState>();

  readonly cancelInstall = output<void>();
  readonly dismiss = output<void>();
  readonly applyRemedy = output<void>();
  readonly launch = output<void>();

  protected readonly percent = computed(() => installPercent(this.state()));
  protected readonly statusLabel = computed(() => installStatusLabel(this.state()));
}
