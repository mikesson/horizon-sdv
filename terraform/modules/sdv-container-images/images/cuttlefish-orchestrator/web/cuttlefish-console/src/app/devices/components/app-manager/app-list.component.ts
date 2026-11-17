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

import {ChangeDetectionStrategy, Component, input, output} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatIconModule} from '@angular/material/icon';
import {MatMenuModule} from '@angular/material/menu';
import type {AppInfo} from './app-row';

/**
 * Presentational list of installed apps with a per-row action menu. Row
 * commands are emitted as package names; the parent runs them over ADB.
 */
@Component({
  selector: 'app-app-list',
  imports: [MatIconModule, MatButtonModule, MatMenuModule],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="app-list">
      @for (app of apps(); track app.packageName) {
        <div class="app-entry" [matMenuTriggerFor]="appMenu" [matMenuTriggerData]="{app}">
          <mat-icon class="app-icon">{{ app.isSystemApp ? 'android' : 'apps' }}</mat-icon>
          <div class="app-info">
            <span class="app-name">{{ app.displayName }}</span>
            <span class="package-name">{{ app.packageName }}</span>
          </div>
          @if (app.versionCode) {
            <span class="version-badge">v{{ app.versionCode }}</span>
          }
          @if (app.isSystemApp) {
            <span class="system-badge">System</span>
          }
        </div>
      } @empty {
        <div class="empty">
          <mat-icon>apps</mat-icon>
          <span>No apps found</span>
        </div>
      }
    </div>

    <mat-menu #appMenu="matMenu" class="dark-menu-panel">
      <ng-template matMenuContent let-app="app">
        <button type="button" mat-menu-item (click)="launch.emit(app.packageName)">
          <mat-icon>launch</mat-icon>
          <span>Launch</span>
        </button>
        <button type="button" mat-menu-item (click)="forceStop.emit(app.packageName)">
          <mat-icon>stop</mat-icon>
          <span>Force stop</span>
        </button>
        <button type="button" mat-menu-item (click)="clearData.emit(app.packageName)">
          <mat-icon>delete_sweep</mat-icon>
          <span>Clear data</span>
        </button>
        @if (!app.isSystemApp) {
          <button type="button" mat-menu-item (click)="uninstall.emit(app.packageName)">
            <mat-icon color="warn">delete</mat-icon>
            <span>Uninstall</span>
          </button>
        }
      </ng-template>
    </mat-menu>
  `,
  styles: `
      .app-list {
        display: flex;
        flex-direction: column;
        gap: 2px;
      }

      .app-entry {
        display: flex;
        align-items: center;
        gap: 12px;
        padding: 12px 12px;
        border-radius: 4px;
        cursor: pointer;
        transition: background var(--cco-duration-fast) ease;
      }

      .app-entry:hover {
        background: var(--cco-surface-variant);
      }

      .app-icon {
        color: var(--cco-success);
        font-size: 24px;
        width: 24px;
        height: 24px;
      }

      .app-info {
        display: flex;
        flex-direction: column;
        flex: 1;
        min-width: 0;
      }

      .app-name {
        color: var(--cco-text);
        font-size: 14px;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
      }

      .package-name {
        color: var(--cco-text-disabled);
        font-size: 11px;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
      }

      .system-badge {
        font-size: 11px;
        padding: 2px 8px;
        background: var(--cco-border);
        color: var(--cco-text-secondary);
        border-radius: 3px;
      }

      .version-badge {
        font-size: 11px;
        padding: 2px 8px;
        color: var(--cco-text-disabled);
        font-family: monospace;
      }

      .empty {
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        height: 100%;
        color: var(--cco-text-secondary);
        gap: 16px;
      }

      .empty mat-icon {
        font-size: 48px;
        width: 48px;
        height: 48px;
        color: var(--cco-text-disabled);
      }
    `,
})
export class AppListComponent {
  readonly apps = input.required<AppInfo[]>();

  readonly launch = output<string>();
  readonly forceStop = output<string>();
  readonly clearData = output<string>();
  readonly uninstall = output<string>();
}
