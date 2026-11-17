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

import type {CdkVirtualScrollViewport} from '@angular/cdk/scrolling';
import {ScrollingModule} from '@angular/cdk/scrolling';
import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  inject,
  input,
  linkedSignal,
  signal,
  untracked,
  viewChild,
} from '@angular/core';
import {disabled, FormField, form} from '@angular/forms/signals';
import {MatButtonModule} from '@angular/material/button';
import {MatChipsModule} from '@angular/material/chips';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatInputModule} from '@angular/material/input';
import {MatMenuModule} from '@angular/material/menu';
import {MatProgressSpinnerModule} from '@angular/material/progress-spinner';
import {MatSelectModule} from '@angular/material/select';
import {MatSnackBar} from '@angular/material/snack-bar';
import {MatTooltipModule} from '@angular/material/tooltip';
import {EmptyStateComponent} from '../../../../shared/components/empty-state/empty-state.component';
import {downloadBlob} from '../../../../shared/utils/download';
import type {LogBufferId} from '../../../services/adb/adb-logcat';
import {DEFAULT_LOG_BUFFERS, LOG_BUFFERS} from '../../../services/adb/adb-logcat';
import {LogcatService} from '../../../services/logcat.service';
import type {LogEntry, LogLevel} from '../../../services/logcat-model';
import {applyLogFilter} from '../../../services/logcat-model';

interface LogcatFilterDraft {
  minLevel: LogLevel | '';
  search: string;
  /** Package-name input; applied to the session on Enter. */
  app: string;
}

/**
 * Real-time logcat viewer over ADB with virtual scrolling, level/text
 * filtering, pause/resume, and export. Filtering is a computed over the
 * session's entries signal; the stream lifecycle lives in LogcatService.
 */
@Component({
  selector: 'app-logcat-viewer',
  imports: [
    EmptyStateComponent,
    FormField,
    MatIconModule,
    MatButtonModule,
    MatFormFieldModule,
    MatInputModule,
    MatSelectModule,
    MatTooltipModule,
    MatChipsModule,
    MatMenuModule,
    MatProgressSpinnerModule,
    ScrollingModule,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="logcat-container">
      <!-- Toolbar -->
      <div class="logcat-toolbar">
        <div class="toolbar-left">
          <!-- Level Filter -->
          <mat-form-field appearance="outline" class="level-filter">
            <mat-label>Level</mat-label>
            <mat-select [formField]="filterForm.minLevel" panelClass="dark-select-panel">
              <mat-option value="">All</mat-option>
              <mat-option value="V">Verbose+</mat-option>
              <mat-option value="D">Debug+</mat-option>
              <mat-option value="I">Info+</mat-option>
              <mat-option value="W">Warn+</mat-option>
              <mat-option value="E">Error+</mat-option>
            </mat-select>
          </mat-form-field>

          <!-- Device-side buffer selection (session state, not a form draft) -->
          <mat-form-field appearance="outline" class="buffer-filter">
            <mat-label>Buffers</mat-label>
            <mat-select
              multiple
              [value]="selectedBuffers()"
              (selectionChange)="onBuffersChange($event.value)"
              [disabled]="!session()"
              panelClass="dark-select-panel"
            >
              @for (buffer of logBuffers; track buffer.id) {
                <mat-option [value]="buffer.id">{{ buffer.label }}</mat-option>
              }
            </mat-select>
          </mat-form-field>

          <!-- Device-side app filter (Enter to apply) -->
          <mat-form-field appearance="outline" class="app-filter">
            <mat-label>App</mat-label>
            <input
              matInput
              [formField]="filterForm.app"
              (keydown.enter)="applyAppFilter()"
              placeholder="com.example.app"
            />
            @if (activeAppFilter()) {
              <button type="button"
                mat-icon-button
                matSuffix
                matTooltip="Show all apps"
                (click)="clearAppFilter()" aria-label="Show all apps">
                <mat-icon>close</mat-icon>
              </button>
            }
          </mat-form-field>

          <!-- Search -->
          <mat-form-field appearance="outline" class="search-field">
            <mat-label>Search</mat-label>
            <input matInput [formField]="filterForm.search" placeholder="Filter by text..." />
            @if (filters().search) {
              <button type="button" mat-icon-button matSuffix aria-label="Clear search" (click)="clearSearch()">
                <mat-icon>close</mat-icon>
              </button>
            }
          </mat-form-field>

          <!-- Errors Only Toggle -->
          <button type="button"
            mat-stroked-button
            [class.active]="filterShowOnlyErrors()"
            (click)="toggleErrorsOnly()"
            matTooltip="Show errors only"
          >
            <mat-icon>error</mat-icon>
            Errors
          </button>
        </div>

        <div class="toolbar-right">
          <!-- Connection Status -->
          <span [class]="['connection-status', sessionState()]">
            <span class="status-dot"></span>
            {{ statusText() }}
          </span>

          <!-- Entry Count -->
          <span class="entry-count">
            {{ filteredEntries().length }} / {{ allEntries().length }}
          </span>

          <!-- Action Buttons -->
          <button type="button"
            mat-icon-button
            (click)="toggleAutoScroll()"
            [matTooltip]="
              autoScroll() ? 'Disable auto-scroll' : 'Enable auto-scroll'
            "
            [class.active]="autoScroll()" [attr.aria-label]="
              autoScroll() ? 'Disable auto-scroll' : 'Enable auto-scroll'
            ">
            <mat-icon>vertical_align_bottom</mat-icon>
          </button>

          <button type="button"
            mat-icon-button
            (click)="togglePause()"
            [matTooltip]="session()?.isPaused() ? 'Resume' : 'Pause'"
            [disabled]="sessionState() !== 'streaming' && sessionState() !== 'paused'" [attr.aria-label]="session()?.isPaused() ? 'Resume' : 'Pause'">
            <mat-icon>{{ session()?.isPaused() ? 'play_arrow' : 'pause' }}</mat-icon>
          </button>

          <button type="button" mat-icon-button (click)="clearLogs()" matTooltip="Clear logs" aria-label="Clear logs">
            <mat-icon>delete_sweep</mat-icon>
          </button>

          <button type="button"
            mat-icon-button
            [matMenuTriggerFor]="moreMenu"
            matTooltip="More options" aria-label="More options">
            <mat-icon>more_vert</mat-icon>
          </button>

          <mat-menu #moreMenu="matMenu" class="dark-menu-panel">
            <button type="button" mat-menu-item (click)="exportLogs()">
              <mat-icon>download</mat-icon>
              <span>Export logs</span>
            </button>
            <button type="button" mat-menu-item (click)="copyLogs()">
              <mat-icon>content_copy</mat-icon>
              <span>Copy to clipboard</span>
            </button>
            <button type="button" mat-menu-item [disabled]="!session()" (click)="clearDeviceBuffers()">
              <mat-icon>layers_clear</mat-icon>
              <span>Clear device buffers</span>
            </button>
          </mat-menu>
        </div>
      </div>

      <!-- Log Content -->
      @if (sessionState() === 'waiting_adb') {
        <div class="waiting-state">
          <mat-spinner aria-label="Loading" diameter="32" />
          <span>Waiting for ADB connection...</span>
          <span class="hint">Logcat will start when ADB is ready</span>
        </div>
      } @else if (sessionState() === 'connecting') {
        <div class="waiting-state">
          <mat-spinner aria-label="Loading" diameter="32" />
          <span>Connecting to logcat...</span>
        </div>
      } @else if (sessionState() === 'error') {
        <div class="error-state">
          <mat-icon>error</mat-icon>
          <span>{{ session()?.error() || 'Connection error' }}</span>
          <button type="button" mat-flat-button (click)="reconnect()">
            Retry
          </button>
        </div>
      } @else if (sessionState() === 'disconnected') {
        <app-empty-state icon="link_off" title="Not connected to logcat">
          <button type="button" action mat-flat-button (click)="manualConnect()">
            Connect
          </button>
        </app-empty-state>
      } @else {
        <!-- Log Entries (Virtual Scroll) -->
        <cdk-virtual-scroll-viewport
          #scrollViewport
          itemSize="20"
          class="log-viewport"
          (scrolledIndexChange)="onScroll()"
        >
          <div
            *cdkVirtualFor="let entry of filteredEntries(); trackBy: trackByEntry"
            [class]="['log-entry', entry.level]"
          >
            <span class="timestamp">{{ entry.timeLabel }}</span>
            <span class="level">{{ entry.level }}</span>
            <span class="tag">{{ entry.tag }}</span>
            <span class="message">{{ entry.message }}</span>
          </div>
        </cdk-virtual-scroll-viewport>

        <!-- Empty State (streaming but no entries) -->
        @if (filteredEntries().length === 0) {
          <div class="empty-state overlay">
            <mat-icon>article</mat-icon>
            <span
              >No log entries{{
                filters().search || filters().minLevel
                  ? ' match your filter'
                  : ''
              }}</span
            >
          </div>
        }
      }
    </div>
  `,
  styles: `
      :host {
        display: block;
        height: 100%;
      }

      .logcat-container {
        display: flex;
        flex-direction: column;
        height: 100%;
        background: var(--cco-surface);
        border-radius: 4px;
        overflow: hidden;
      }

      .logcat-toolbar {
        display: flex;
        justify-content: space-between;
        align-items: center;
        padding: 8px 12px;
        background: var(--cco-surface-variant);
        border-bottom: 1px solid var(--cco-border);
        gap: 12px;
        flex-wrap: wrap;
      }

      .toolbar-left,
      .toolbar-right {
        display: flex;
        align-items: center;
        gap: 8px;
      }

      .level-filter {
        width: 110px;
      }

      .buffer-filter {
        width: 150px;
      }

      .app-filter {
        width: 190px;
      }

      .search-field {
        width: 200px;
      }

      button.active {
        background: rgba(25, 118, 210, 0.2);
        color: var(--cco-accent);
      }

      .connection-status {
        display: flex;
        align-items: center;
        gap: 8px;
        font-size: 12px;
        color: var(--cco-text-secondary);
        padding: 4px 8px;
        background: var(--cco-border);
        border-radius: 4px;
      }

      .status-dot {
        width: 8px;
        height: 8px;
        border-radius: 50%;
        background: var(--cco-text-disabled);
      }

      .connection-status.streaming .status-dot {
        background: var(--cco-success);
      }
      .connection-status.connecting .status-dot,
      .connection-status.waiting_adb .status-dot {
        background: var(--cco-warning);
        animation: pulse 1s infinite;
      }
      .connection-status.paused .status-dot {
        background: var(--cco-accent);
      }
      .connection-status.error .status-dot {
        background: var(--cco-error);
      }

      @keyframes pulse {
        0%,
        100% {
          opacity: 1;
        }
        50% {
          opacity: 0.4;
        }
      }

      .entry-count {
        font-size: 12px;
        color: var(--cco-text-disabled);
        font-family: monospace;
      }

      .log-viewport {
        flex: 1;
        overflow: auto;
        font-family: 'JetBrains Mono', 'Fira Code', monospace;
        font-size: 12px;
        line-height: 20px;
      }

      .log-entry {
        display: flex;
        gap: 8px;
        padding: 0 12px;
        white-space: nowrap;
        color: var(--cco-text);
      }

      .log-entry:hover {
        background: var(--cco-surface-variant);
      }

      .log-entry.V {
        color: var(--cco-text-disabled);
      }
      .log-entry.D {
        color: var(--cco-accent);
      }
      .log-entry.I {
        color: var(--cco-success);
      }
      .log-entry.W {
        color: var(--cco-warning);
      }
      .log-entry.E {
        color: var(--cco-error);
      }
      .log-entry.F {
        color: var(--cco-error);
        font-weight: bold;
      }

      .timestamp {
        color: var(--cco-text-secondary);
        flex-shrink: 0;
        width: 100px;
      }

      .level {
        flex-shrink: 0;
        width: 12px;
        font-weight: bold;
      }

      .tag {
        flex-shrink: 0;
        width: 120px;
        overflow: hidden;
        text-overflow: ellipsis;
        color: var(--cco-text-secondary);
      }

      .message {
        flex: 1;
        overflow: hidden;
        text-overflow: ellipsis;
      }

      .waiting-state,

      .waiting-state .hint {
        font-size: 12px;
        color: var(--cco-text-disabled);
      }


      .error-state mat-icon {
        color: var(--cco-error);
      }

    `,
})
export class LogcatViewerComponent {
  private readonly logcatService = inject(LogcatService);
  private readonly snackBar = inject(MatSnackBar);

  protected readonly logBuffers = LOG_BUFFERS;

  readonly deviceId = input.required<string>();
  readonly instance = input.required<string>();

  /** Logcat streams from one Android VM's adbd, so sessions are per instance. */
  private readonly target = computed(() => ({
    deviceId: this.deviceId(),
    instance: this.instance(),
  }));
  readonly autoConnect = input(true);

  protected readonly scrollViewport = viewChild<CdkVirtualScrollViewport>('scrollViewport');

  protected readonly session = computed(() => this.logcatService.session(this.target())());
  protected readonly sessionState = computed(() => this.session()?.state() ?? 'disconnected');
  protected readonly allEntries = computed(() => this.session()?.entries() ?? []);

  protected readonly filterShowOnlyErrors = signal(false);

  protected readonly selectedBuffers = computed(
    () => this.session()?.buffers() ?? DEFAULT_LOG_BUFFERS
  );
  protected readonly activeAppFilter = computed(() => this.session()?.appFilter() ?? null);

  private readonly filterDraft = signal<LogcatFilterDraft>({minLevel: '', search: '', app: ''});
  protected readonly filters = this.filterDraft.asReadonly();
  protected readonly filterForm = form(this.filterDraft, draft => {
    disabled(draft.app, () => !this.session());
  });

  protected readonly filteredEntries = computed(() =>
    applyLogFilter(this.allEntries(), {
      minLevel: this.filters().minLevel || undefined,
      searchText: this.filters().search,
      showOnlyErrors: this.filterShowOnlyErrors(),
    })
  );

  protected readonly autoScroll = signal(true);

  protected readonly statusText = computed(() => {
    switch (this.sessionState()) {
      case 'streaming':
        return 'Live';
      case 'connecting':
        return 'Connecting...';
      case 'waiting_adb':
        return 'Waiting for ADB...';
      case 'paused':
        return 'Paused';
      case 'error':
        return 'Error';
      default:
        return 'Disconnected';
    }
  });

  /** Set by the manual Connect button; resets when the device changes. */
  private readonly connectRequested = linkedSignal<string, boolean>({
    source: this.deviceId,
    computation: () => false,
  });

  constructor() {
    // Desired-state: hold a logcat session while watching is wanted for this
    // device; the cleanup releases it on device change and destroy.
    effect(onCleanup => {
      const target = this.target();
      if (!(this.autoConnect() || this.connectRequested())) {
        return;
      }
      untracked(() => this.logcatService.watch(target));
      onCleanup(() => this.logcatService.unwatch(target));
    });

    // Pin the viewport to the bottom on new entries while auto-scroll is on.
    effect(() => {
      const entries = this.filteredEntries();
      const viewport = this.scrollViewport();
      if (entries.length > 0 && viewport && untracked(this.autoScroll)) {
        queueMicrotask(() => viewport.scrollTo({bottom: 0}));
      }
    });
  }

  protected toggleErrorsOnly(): void {
    this.filterShowOnlyErrors.update(v => !v);
  }

  protected onBuffersChange(buffers: LogBufferId[]): void {
    if (buffers.length === 0) {
      return;
    }
    this.session()?.setBuffers(buffers);
  }

  protected applyAppFilter(): void {
    const packageName = this.filters().app.trim();
    const session = this.session();
    if (!(session && packageName)) {
      return;
    }
    session.setAppFilter(packageName).catch((error: unknown) => {
      const message = error instanceof Error ? error.message : 'Failed to filter by app';
      this.snackBar.open(message, 'Close', {duration: 5000});
    });
  }

  protected clearAppFilter(): void {
    this.filterDraft.update(draft => ({...draft, app: ''}));
    this.session()
      ?.setAppFilter(null)
      .catch(() => {
        // Clearing never resolves a pid; nothing can fail meaningfully.
      });
  }

  protected clearSearch(): void {
    this.filterDraft.update(draft => ({...draft, search: ''}));
  }

  protected clearDeviceBuffers(): void {
    this.session()
      ?.clearDeviceBuffers()
      .catch((error: unknown) => {
        const message = error instanceof Error ? error.message : 'Failed to clear device buffers';
        this.snackBar.open(message, 'Close', {duration: 5000});
      });
  }

  protected toggleAutoScroll(): void {
    const enabled = !this.autoScroll();
    this.autoScroll.set(enabled);
    if (enabled) {
      this.scrollViewport()?.scrollTo({bottom: 0});
    }
  }

  protected togglePause(): void {
    const session = this.session();
    if (!session) {
      return;
    }
    if (session.isPaused()) {
      session.resume();
    } else {
      session.pause();
    }
  }

  protected clearLogs(): void {
    this.session()?.clear();
  }

  protected exportLogs(): void {
    const content = this.filteredEntries()
      .map(e => e.raw)
      .join('\n');
    downloadBlob(
      new Blob([content], {type: 'text/plain'}),
      `logcat-${this.deviceId()}-${new Date().toISOString()}.txt`
    );
  }

  protected copyLogs(): void {
    const content = this.filteredEntries()
      .map(e => e.raw)
      .join('\n');
    void navigator.clipboard.writeText(content);
  }

  protected onScroll(): void {
    const viewport = this.scrollViewport();
    if (!viewport) {
      return;
    }

    // Scrolling away from the bottom disables auto-scroll.
    if (viewport.measureScrollOffset('bottom') > 50) {
      this.autoScroll.set(false);
    }
  }

  protected manualConnect(): void {
    this.connectRequested.set(true);
  }

  protected reconnect(): void {
    this.session()?.restart();
  }

  protected trackByEntry(_index: number, entry: LogEntry): number {
    return entry.id;
  }
}
