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

import {NgTemplateOutlet} from '@angular/common';
import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  inject,
  signal,
  untracked,
} from '@angular/core';
import {toSignal} from '@angular/core/rxjs-interop';
import {MatButtonModule} from '@angular/material/button';
import {MatIconModule} from '@angular/material/icon';
import {MatSnackBar} from '@angular/material/snack-bar';
import {MatTabsModule} from '@angular/material/tabs';
import {MatTooltipModule} from '@angular/material/tooltip';
import {ActivatedRoute, Router} from '@angular/router';
import {ResizableSplitComponent} from '../../../shared/components';
import type {DeviceInstance} from '../../models/device.model';
import type {VideoDisplay} from '../../models/display.model';
import {primaryInstance} from '../../models/instance-target';
import {AdbService} from '../../services/adb/adb.service';
import {DeviceInstallService} from '../../services/adb/device-install.service';
import {buildInstallPlan} from '../../services/adb/install-plan';
import {DeviceStreamService} from '../../services/streaming/device-stream.service';
import {DeviceStore} from '../../store/device.signal-store';
import {AppManagerComponent} from '../app-manager';
import {FileBrowserComponent} from '../file-browser';
import {ShellTerminalComponent} from '../shell-terminal';
import {DisplayPanelComponent} from './display-panel/display-panel.component';
import {LogcatViewerComponent} from './logcat-viewer/logcat-viewer.component';
import {PlayerToolbarComponent} from './player-toolbar.component';
import {StreamStageComponent} from './stream-stage.component';

/**
 * How many WebRTC sessions this page holds at once.
 *
 * Signaling is proxied through Cloud Run, whose WebSocket connections are
 * capped at 60 minutes, and every extra session is another peer connection
 * plus its media. Three covers the workload groups exist for (a phone beside
 * a watch, a car's cluster beside its head unit) without turning an
 * eight-instance group into eight simultaneous streams.
 */
const MAX_LIVE_SESSIONS = 3;

/**
 * The interactive container for an active device session.
 *
 * Orchestration only: route → store selection + ADB claim, the desired-state
 * streaming effect, and the split layout. The header lives in
 * PlayerToolbarComponent and the video area in StreamStageComponent.
 */
@Component({
  selector: 'app-device-player',
  imports: [
    NgTemplateOutlet,
    MatIconModule,
    MatButtonModule,
    MatTabsModule,
    MatTooltipModule,
    LogcatViewerComponent,
    DisplayPanelComponent,
    FileBrowserComponent,
    AppManagerComponent,
    ShellTerminalComponent,
    ResizableSplitComponent,
    PlayerToolbarComponent,
    StreamStageComponent,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <!-- Device audio plays through a hidden element; video renders per display. -->
    <audio [srcObject]="audioStream()" autoplay style="display: none;"></audio>

    <ng-template #videoSection>
      <app-stream-stage
        [deviceId]="deviceId()!"
        [instance]="instance()"
        [deviceName]="device()!.name"
        [displays]="displays()"
        [rotation]="deviceRotation()"
        [streamEnabled]="streamEnabled()"
        [streamState]="streamState()"
        [streamError]="streamError()"
        [instances]="instances()"
        [liveInstances]="liveInstanceSet()"
        [maxLiveSessions]="maxLiveSessions"
        (streamEnabledChange)="toggleStream($event)"
        (retryRequested)="retryConnection()"
        (instanceFocus)="focusInstance($event)"
        (streamAllRequested)="streamAll()"
      />
    </ng-template>

    @if (device(); as device) {
      <div
        class="player-container"
        (dragover)="onApkDragOver($event)"
        (dragleave)="onApkDragLeave($event)"
        (drop)="onApkDrop($event)"
      >
        <app-player-toolbar
          [device]="device"
          [instance]="instance()"
          (appsRequested)="openAppsTab()"
          (instanceFocus)="focusInstance($event)" />

        <!-- Drop an APK anywhere on the player to install it -->
        @if (apkDragOver()) {
          <div class="apk-drop-overlay">
            <mat-icon>install_mobile</mat-icon>
            <span>Drop APK or app bundle to install on {{ device.name }}</span>
          </div>
        }

        <!-- Main Content -->
        <div class="player-content">
          @if (showSidePanel()) {
            <app-resizable-split
              [direction]="'horizontal'"
              [initialRatio]="0.6"
              [minRatio]="0.3"
              [maxRatio]="0.85"
              storageKey="device-player-split-ratio"
            >
              <div primary class="video-section">
                <ng-container [ngTemplateOutlet]="videoSection" />
              </div>

              <!-- Side Panel (Secondary) -->
              <div secondary class="side-panel">
                <!-- Lazy tabs + preserveContent: tools mount on first visit
                     (no ADB connect storm on panel open) and stay alive across
                     tab switches (shell/logcat keep running). -->
                <mat-tab-group class="device-player-tabs" preserveContent>
                  <mat-tab label="Displays">
                    <ng-template matTabContent>
                      <div class="tab-content">
                        <app-display-panel [deviceId]="device.id" [instance]="instance()" />
                        <p class="panel-note">
                          Add displays for multi-screen testing. WebRTC streaming requires at least one display.
                        </p>
                      </div>
                    </ng-template>
                  </mat-tab>
                  <mat-tab label="Logcat">
                    <ng-template matTabContent>
                      <app-logcat-viewer
                        [deviceId]="device.id"
                        [instance]="instance()"
                        [autoConnect]="false"
                      />
                    </ng-template>
                  </mat-tab>
                  <mat-tab label="Files">
                    <ng-template matTabContent>
                      <app-file-browser [deviceId]="device.id" [instance]="instance()" />
                    </ng-template>
                  </mat-tab>
                  <mat-tab label="Apps">
                    <ng-template matTabContent>
                      <app-app-manager [deviceId]="device.id" [instance]="instance()" />
                    </ng-template>
                  </mat-tab>
                  <mat-tab label="Shell">
                    <ng-template matTabContent>
                      <app-shell-terminal [deviceId]="device.id" [instance]="instance()" [autoConnect]="false" />
                    </ng-template>
                  </mat-tab>
                </mat-tab-group>
              </div>
            </app-resizable-split>
          } @else {
            <div class="video-section full-width">
              <ng-container [ngTemplateOutlet]="videoSection" />
            </div>
          }

          <!-- Toggle Panel Button -->
          <button type="button"
            class="toggle-panel-btn"
            mat-icon-button
            (click)="toggleSidePanel()"
            [matTooltip]="showSidePanel() ? 'Hide panel' : 'Show logcat & tools'" [attr.aria-label]="showSidePanel() ? 'Hide panel' : 'Show logcat & tools'">
            <mat-icon>{{ showSidePanel() ? 'chevron_right' : 'chevron_left' }}</mat-icon>
          </button>
        </div>
      </div>
    }

    <!-- Loading State -->
    @if (!device()) {
      <div class="loading-state">
        <mat-icon>hourglass_empty</mat-icon>
        <span>Loading device...</span>
      </div>
    }
    `,
  styles: `
    :host {
      display: block;
      height: 100vh;
      background: var(--cco-canvas);
    }

    .player-container {
      display: flex;
      flex-direction: column;
      height: 100%;
      position: relative;
    }

    .apk-drop-overlay {
      position: absolute;
      inset: 0;
      z-index: 200;
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      gap: 16px;
      background: rgba(13, 17, 23, 0.88);
      color: var(--cco-accent);
      border: 3px dashed var(--cco-accent);
      pointer-events: none;
    }

    .apk-drop-overlay mat-icon {
      font-size: 64px;
      width: 72px;
      height: 72px;
    }

    .player-content {
      flex: 1;
      display: flex;
      position: relative;
      overflow: hidden;
    }

    .video-section {
      display: flex;
      align-items: center;
      justify-content: center;
      background: var(--cco-canvas);
      padding: 16px;
      position: relative;
      height: 100%;
      width: 100%;
    }

    .video-section.full-width {
      flex: 1;
    }

    .side-panel {
      background: var(--cco-surface);
      display: flex;
      flex-direction: column;
      height: 100%;
      width: 100%;
    }

    .side-panel mat-tab-group {
      flex: 1;
      display: flex;
      flex-direction: column;
    }

    /* The .side-panel tab overrides live in global styles.scss. */

    .toggle-panel-btn {
      position: absolute;
      right: 8px;
      top: 50%;
      transform: translateY(-50%);
      background: var(--cco-surface-variant);
      color: var(--cco-text-secondary);
      border-radius: 4px;
      z-index: 10;
    }

    .toggle-panel-btn:hover {
      background: var(--cco-border);
    }

    .loading-state {
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      height: 100%;
      color: var(--cco-text-disabled);
      gap: 16px;
    }

    .loading-state mat-icon {
      font-size: 48px;
      width: 48px;
      height: 48px;
    }

    .tab-content {
      padding: 16px;
    }

    .panel-note {
      margin-top: 16px;
      font-size: 12px;
      color: rgba(255, 255, 255, 0.5);
      font-style: italic;
    }
  `,
})
export class DevicePlayerComponent {
  private readonly deviceStore = inject(DeviceStore);
  private readonly streaming = inject(DeviceStreamService);
  private readonly adbService = inject(AdbService);
  private readonly installService = inject(DeviceInstallService);
  private readonly route = inject(ActivatedRoute);
  private readonly router = inject(Router);
  private readonly snackBar = inject(MatSnackBar);

  private readonly routeParams = toSignal(this.route.paramMap);
  protected readonly deviceId = computed(() => this.routeParams()?.get('id') ?? null);

  private readonly queryParams = toSignal(this.route.queryParamMap);

  protected readonly device = this.deviceStore.selectedDevice;
  protected readonly instances = computed<DeviceInstance[]>(() => this.device()?.instances ?? []);

  /**
   * The Android VM this page interacts with, named by `?instance=`.
   *
   * A query parameter rather than a route segment. The device is the addressed
   * resource and an instance is a view onto part of it, so the URL never mints
   * a second noun. An unknown or absent name falls back to the group's first
   * instance, which is what a single-instance device has always shown.
   */
  protected readonly instance = computed(() => {
    const requested = this.queryParams()?.get('instance') ?? '';
    if (requested && this.instances().some(i => i.name === requested)) {
      return requested;
    }
    const d = this.device();
    return d ? primaryInstance(d) : '';
  });

  /** Which instances hold a session, oldest first. */
  private readonly liveInstances = computed(() => {
    const id = this.deviceId();
    return id ? this.streaming.liveInstances(id)() : [];
  });

  protected readonly liveInstanceSet = computed(() => new Set(this.liveInstances()));

  protected readonly streamEnabled = signal(true);
  protected readonly showSidePanel = signal(true);
  protected readonly apkDragOver = signal(false);

  protected readonly adbState = computed(() => {
    const instance = this.instance();
    const id = this.deviceId();
    return id && instance ? this.adbService.state({deviceId: id, instance})() : 'disconnected';
  });

  private readonly session = computed(() => {
    const id = this.deviceId();
    const instance = this.instance();
    return id && instance ? this.streaming.session({deviceId: id, instance})() : null;
  });

  protected readonly streamState = computed(() => this.session()?.state() ?? null);
  protected readonly streamError = computed(() => this.session()?.error() ?? null);
  protected readonly audioStream = computed(() => this.session()?.audioStream() ?? null);
  protected readonly deviceRotation = computed(() => this.session()?.rotation() ?? 0);

  protected readonly displays = computed<VideoDisplay[]>(() => {
    const session = this.session();
    if (!session) {
      return [];
    }
    return Array.from(session.streams().entries())
      .map(([displayId, stream]) => ({
        displayId,
        stream,
        info: {
          displayId,
          label: `Display ${displayId}`,
          width: 0, // Populated from video metadata once frames arrive
          height: 0,
          density: 0,
          rotation: 0,
        },
      }))
      .sort((a, b) => a.displayId - b.displayId);
  });

  constructor() {
    // Route -> store selection; the cleanup drops every session this page
    // opened, including the unfocused ones, so switching devices cannot leave
    // a sibling's stream running behind a page nobody is looking at.
    effect(onCleanup => {
      const id = this.deviceId();
      if (!id) {
        return;
      }
      untracked(() => {
        this.deviceStore.selectDevice(id);
        this.deviceStore.loadOne(id);
      });
      onCleanup(() => {
        for (const instance of untracked(this.liveInstances)) {
          this.streaming.disconnect({deviceId: id, instance});
        }
      });
    });

    // The ADB claim follows the FOCUSED instance: adbd runs in the guest, so a
    // claim on one Android VM says nothing about its siblings.
    effect(onCleanup => {
      const id = this.deviceId();
      const instance = this.instance();
      if (!(id && instance)) {
        return;
      }
      const claim = untracked(() => this.adbService.acquire({deviceId: id, instance}));
      onCleanup(() => claim.release());
    });

    // Desired-state reconciliation: while streaming is enabled and the focused
    // instance has no session, start one. A failed session stays registered
    // (blocking auto-restart) until the user retries or toggles the stream.
    effect(() => {
      const id = this.deviceId();
      const instance = this.instance();
      if (!(id && instance) || this.device()?.id !== id || !this.streamEnabled()) {
        return;
      }
      const target = {deviceId: id, instance};
      if (this.streaming.session(target)()) {
        return;
      }
      untracked(() => {
        this.evictOldestSession(id, instance);
        this.streaming.connect(target);
      });
    });
  }

  /**
   * Keeps the page under MAX_LIVE_SESSIONS by dropping the session connected
   * longest ago. Switching between instances therefore leaves the recent ones
   * live, which is what matters at N=2, while a walk across eight never
   * accumulates eight peer connections.
   */
  private evictOldestSession(deviceId: string, incoming: string): void {
    const live = this.liveInstances();
    if (live.length < MAX_LIVE_SESSIONS) {
      return;
    }
    for (const instance of live.slice(0, live.length - MAX_LIVE_SESSIONS + 1)) {
      if (instance !== incoming) {
        this.streaming.disconnect({deviceId, instance});
      }
    }
  }

  protected readonly maxLiveSessions = MAX_LIVE_SESSIONS;

  /**
   * Connects every instance at once, for a group small enough to hold them
   * all. Guarded by the same cap the eviction uses, so this can never be the
   * thing that starts a session another one immediately drops.
   */
  protected streamAll(): void {
    const id = this.deviceId();
    const instances = this.instances();
    if (!id || instances.length > MAX_LIVE_SESSIONS) {
      return;
    }
    this.streamEnabled.set(true);
    for (const inst of instances) {
      this.streaming.connect({deviceId: id, instance: inst.name});
    }
  }

  /** Deep-links the focused instance, replacing rather than stacking history. */
  protected focusInstance(instance: string): void {
    void this.router.navigate([], {
      relativeTo: this.route,
      queryParams: {instance},
      queryParamsHandling: 'merge',
      replaceUrl: true,
    });
  }

  protected toggleSidePanel(): void {
    this.showSidePanel.set(!this.showSidePanel());
  }

  protected toggleStream(enabled: boolean): void {
    this.streamEnabled.set(enabled);
    if (!enabled) {
      const id = this.deviceId();
      if (id && this.instance()) {
        this.streaming.disconnect({deviceId: id, instance: this.instance()});
      }
    }
  }

  protected retryConnection(): void {
    const id = this.deviceId();
    if (id && this.instance()) {
      this.streaming.connect({deviceId: id, instance: this.instance()});
    }
  }

  protected onApkDragOver(event: DragEvent): void {
    if (!Array.from(event.dataTransfer?.items ?? []).some(item => item.kind === 'file')) {
      return;
    }
    event.preventDefault();
    this.apkDragOver.set(true);
  }

  protected onApkDragLeave(event: DragEvent): void {
    // Ignore leaves into child elements.
    if (
      event.relatedTarget &&
      (event.currentTarget as Node).contains(event.relatedTarget as Node)
    ) {
      return;
    }
    this.apkDragOver.set(false);
  }

  protected async onApkDrop(event: DragEvent): Promise<void> {
    event.preventDefault();
    this.apkDragOver.set(false);

    const id = this.deviceId();
    const files = Array.from(event.dataTransfer?.files ?? []);
    if (!id || files.length === 0) {
      return;
    }
    if (this.adbState() !== 'connected') {
      this.snackBar.open('Connect ADB before installing (the stream must be up)', 'Close', {
        duration: 4000,
      });
      return;
    }

    const plan = await buildInstallPlan(files);
    if (plan.units.length === 0) {
      this.snackBar.open(plan.warnings[0] ?? 'No installable APK files', 'Close', {duration: 5000});
      return;
    }
    this.installService.install({deviceId: id, instance: this.instance()}, plan, {
      allSplits: plan.units.some(u => u.files.length > 1),
    });
  }

  protected openAppsTab(): void {
    this.showSidePanel.set(true);
  }
}
