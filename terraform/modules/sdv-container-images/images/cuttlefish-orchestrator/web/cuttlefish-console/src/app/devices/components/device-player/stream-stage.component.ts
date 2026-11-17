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

import {
  ChangeDetectionStrategy,
  Component,
  computed,
  inject,
  input,
  output,
  signal,
  viewChild,
} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatIconModule} from '@angular/material/icon';
import {MatSlideToggleModule} from '@angular/material/slide-toggle';
import {MatSnackBar} from '@angular/material/snack-bar';
import {bytesToBlob, downloadBlob} from '../../../shared/utils/download';
import type {DeviceInstance} from '../../models/device.model';
import type {VideoDisplay} from '../../models/display.model';
import {AdbService} from '../../services/adb/adb.service';
import {screenshot} from '../../services/adb/adb-exec';
import type {StreamState} from '../../services/streaming/streaming.types';
import {FloatingNavBarComponent} from '../floating-nav-bar';
import type {ScreenshotKind} from '../floating-nav-bar/floating-nav-bar.component';
import {MultiDisplayContainerComponent} from '../multi-display-container';
import {InstanceThumbComponent} from './instance-thumb.component';

/**
 * The video area of the player: stream toggle, connection placeholders, the
 * multi-display renderer, and the floating nav bar with screenshot capture.
 */
@Component({
  selector: 'app-stream-stage',
  imports: [
    MatIconModule,
    MatButtonModule,
    MatSlideToggleModule,
    MultiDisplayContainerComponent,
    FloatingNavBarComponent,
    InstanceThumbComponent,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <!-- One entry per Android VM, present only where there is more than one.
         The focused instance lays its own displays out below exactly as a
         single-instance device always has. -->
    @if (instances().length > 1) {
      <div class="instance-strip">
        @for (inst of instances(); track inst.name) {
          <app-instance-thumb
            [deviceId]="deviceId()"
            [instance]="inst"
            [focused]="inst.name === instance()"
            [live]="liveInstances().has(inst.name)"
            (click)="instanceFocus.emit(inst.name)"
          />
        }
        <!-- A group small enough to hold every session at once is the pairing
             case groups exist for; one click puts them all on screen together
             instead of walking them one at a time. -->
        @if (canStreamAll()) {
          <button
            type="button"
            mat-icon-button
            class="stream-all"
            (click)="streamAllRequested.emit()"
            matTooltip="Stream every instance"
            aria-label="Stream every instance"
          >
            <mat-icon>grid_view</mat-icon>
          </button>
        }
      </div>
    }

    <div class="stream-toggle">
      <mat-slide-toggle
        [checked]="streamEnabled()"
        (change)="streamEnabledChange.emit($event.checked)"
        color="primary"
      >
        <mat-icon>{{ streamEnabled() ? 'videocam' : 'videocam_off' }}</mat-icon>
        {{ streamEnabled() ? 'Stream Active' : 'Stream Disabled' }}
      </mat-slide-toggle>
    </div>

    @if (streamEnabled()) {
      @if (streamState() === 'failed') {
        <div class="stream-failed-placeholder">
          <mat-icon>error_outline</mat-icon>
          <p>Streaming connection failed</p>
          @if (streamError(); as error) {
            <p class="hint">{{ error }}</p>
          }
          <button type="button" mat-stroked-button (click)="retryRequested.emit()">
            <mat-icon>refresh</mat-icon>
            Retry
          </button>
        </div>
      } @else if (displays().length > 0) {
        <app-multi-display-container
          [deviceId]="deviceId()"
          [displays]="displays()"
          [rotation]="rotation()"
        />
        <!-- Floating navigation bar overlay -->
        <app-floating-nav-bar
          [deviceId]="deviceId()"
          [instance]="instance()"
          [screenshotBusy]="screenshotBusy()"
          (screenshotRequested)="onScreenshotRequested($event)"
        />
      } @else {
        <div class="connecting-placeholder">
          <mat-icon>connected_tv</mat-icon>
          <p>Connecting to device...</p>
          <p class="hint">Establishing WebRTC connection</p>
        </div>
      }
    } @else {
      <div class="stream-disabled-placeholder">
        <mat-icon>videocam_off</mat-icon>
        <p>WebRTC stream is disabled</p>
        <p class="hint">Toggle the switch above to start streaming</p>
      </div>
    }
  `,
  styles: `
    :host {
      display: flex;
      align-items: center;
      justify-content: center;
      width: 100%;
      height: 100%;
    }

    .instance-strip {
      position: absolute;
      top: 16px;
      right: 16px;
      z-index: 10;
      display: flex;
      gap: 8px;
      padding: 8px;
      background: rgba(0, 0, 0, 0.7);
      border-radius: 8px;
      max-width: 60%;
      overflow-x: auto;
    }

    .stream-all {
      align-self: center;
      color: var(--cco-text-secondary);
    }

    .stream-toggle {
      position: absolute;
      top: 16px;
      left: 16px;
      z-index: 10;
      background: rgba(0, 0, 0, 0.7);
      padding: 8px 16px;
      border-radius: 8px;
    }

    .stream-toggle mat-slide-toggle {
      color: var(--cco-text-secondary);
    }

    .stream-toggle mat-icon {
      margin-right: 8px;
      vertical-align: middle;
    }

    .stream-disabled-placeholder,
    .connecting-placeholder,
    .stream-failed-placeholder {
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      width: 100%;
      height: 100%;
      min-height: 400px;
      text-align: center;
    }

    .stream-disabled-placeholder {
      color: var(--cco-text-disabled);
    }

    .connecting-placeholder {
      color: var(--cco-text-disabled);
    }

    .stream-failed-placeholder {
      color: var(--cco-error);
    }

    .stream-disabled-placeholder mat-icon,
    .connecting-placeholder mat-icon,
    .stream-failed-placeholder mat-icon {
      font-size: 64px;
      width: 64px;
      height: 64px;
      margin-bottom: 16px;
    }

    .connecting-placeholder mat-icon {
      animation: pulse 2s ease-in-out infinite;
    }

    .stream-disabled-placeholder p,
    .connecting-placeholder p,
    .stream-failed-placeholder p {
      margin: 4px 0;
    }

    .stream-disabled-placeholder .hint,
    .connecting-placeholder .hint {
      font-size: 12px;
      color: var(--cco-border);
    }

    .stream-failed-placeholder .hint {
      font-size: 12px;
      color: var(--cco-text-secondary);
      max-width: 480px;
    }

    .stream-failed-placeholder button {
      margin-top: 16px;
    }

    @keyframes pulse {
      0%, 100% { opacity: 0.5; }
      50% { opacity: 1; }
    }
  `,
})
export class StreamStageComponent {
  private readonly adbService = inject(AdbService);
  private readonly snackBar = inject(MatSnackBar);

  readonly deviceId = input.required<string>();
  readonly instance = input.required<string>();

  /** The Android VM this panel talks to; ADB sessions are per instance. */
  private readonly target = computed(() => ({
    deviceId: this.deviceId(),
    instance: this.instance(),
  }));

  readonly deviceName = input.required<string>();
  readonly displays = input.required<VideoDisplay[]>();
  readonly rotation = input(0);
  readonly streamEnabled = input.required<boolean>();
  readonly streamState = input.required<StreamState | null>();
  readonly streamError = input.required<string | null>();

  /** Every Android VM of the device; one entry means no strip. */
  readonly instances = input<DeviceInstance[]>([]);

  /** Which of them currently hold a WebRTC session. */
  readonly liveInstances = input<ReadonlySet<string>>(new Set<string>());

  /** How many sessions the page will hold; beyond it, connecting evicts. */
  readonly maxLiveSessions = input(0);

  protected readonly canStreamAll = computed(
    () =>
      this.instances().length <= this.maxLiveSessions() &&
      this.liveInstances().size < this.instances().length
  );

  readonly streamEnabledChange = output<boolean>();
  readonly retryRequested = output<void>();
  readonly instanceFocus = output<string>();
  readonly streamAllRequested = output<void>();

  protected readonly screenshotBusy = signal(false);

  private readonly displayContainer = viewChild(MultiDisplayContainerComponent);

  protected async onScreenshotRequested(kind: ScreenshotKind): Promise<void> {
    if (this.screenshotBusy()) {
      return;
    }

    this.screenshotBusy.set(true);
    try {
      const stamp = Date.now();
      const name = this.deviceName();
      switch (kind) {
        case 'focused': {
          const capture = await this.displayContainer()?.captureFocused();
          if (capture) {
            downloadBlob(capture.blob, `${name}-display${capture.displayId}-${stamp}.png`);
          }
          break;
        }
        case 'all': {
          for (const capture of (await this.displayContainer()?.captureAll()) ?? []) {
            downloadBlob(capture.blob, `${name}-display${capture.displayId}-${stamp}.png`);
          }
          break;
        }
        case 'exact': {
          const session = this.adbService.session(this.target())();
          if (!session) {
            throw new Error('ADB session not available');
          }
          const png = await screenshot(await session.ensureConnected());
          downloadBlob(bytesToBlob(png, 'image/png'), `${name}-screencap-${stamp}.png`);
          break;
        }
        default:
          break;
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Screenshot failed';
      this.snackBar.open(`Screenshot failed: ${message}`, 'Close', {duration: 5000});
    } finally {
      this.screenshotBusy.set(false);
    }
  }
}
