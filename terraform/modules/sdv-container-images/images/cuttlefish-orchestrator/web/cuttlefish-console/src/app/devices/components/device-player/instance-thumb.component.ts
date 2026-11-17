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

import {ChangeDetectionStrategy, Component, computed, inject, input, resource} from '@angular/core';
import {toSignal} from '@angular/core/rxjs-interop';
import {MatIconModule} from '@angular/material/icon';
import {MatProgressSpinnerModule} from '@angular/material/progress-spinner';
import {firstValueFrom, interval, map} from 'rxjs';
import type {DeviceInstance} from '../../models/device.model';
import {displayGeometry} from '../../models/instance-state';
import {DeviceApiService} from '../../services/device-api.service';
import {DeviceStreamService} from '../../services/streaming/device-stream.service';

/** Every thumbnail is this tall; width follows the guest's own proportions. */
const THUMB_HEIGHT_PX = 108;

/** Bounds on the derived width, so a 32x128 guest cannot become a hairline. */
const MIN_THUMB_WIDTH_PX = 48;
const MAX_THUMB_WIDTH_PX = 168;

/** How often the "12s ago" stamp on a still is recomputed. */
const AGE_TICK_MS = 5000;

/**
 * One instance's entry in the player's instance strip.
 *
 * It must not present a still as though it were live. A screenshot is a lie
 * from the moment it is taken, and a frozen tile that looks live reads as a
 * working device while a hung one reads as fine. It must not put the guest
 * behind a device frame or a rounded watch cutout either. This is a dev tool,
 * every pixel the guest renders has to stay inspectable, so the silhouette
 * comes from proportion alone.
 */
@Component({
  selector: 'app-instance-thumb',
  imports: [MatIconModule, MatProgressSpinnerModule],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @let inst = instance();
    <button
      type="button"
      class="thumb"
      [class.focused]="focused()"
      [attr.aria-current]="focused() ? 'true' : null"
    >
      <span class="frame" [style.width.px]="frameWidth()" [style.height.px]="frameHeight">
        @if (liveStream(); as stream) {
          <!-- Muted on purpose: audio belongs to the focused instance alone,
               and two streams playing at once is a cacophony nobody can
               attribute to a device. -->
          <video [srcObject]="stream" autoplay playsinline muted></video>
          <span class="tag live"><mat-icon>videocam</mat-icon> Live</span>
        } @else if (live()) {
          <mat-spinner aria-label="Connecting" diameter="20" />
        } @else if (shot.isLoading()) {
          <mat-spinner aria-label="Loading screenshot" diameter="20" />
        } @else if (shot.value(); as capture) {
          <!-- eslint-disable-next-line @angular-eslint/template/prefer-ngsrc --
               NgOptimizedImage rejects data: URLs outright, and the capture
               arrives as base64 in the response body rather than at a URL. -->
          <img class="still" [src]="capture.src" [alt]="'Screen of ' + inst.name" />
          <span class="tag still-tag">{{ age() }}</span>
        } @else {
          <span class="badge none"><mat-icon>visibility_off</mat-icon> No screen</span>
        }
      </span>
      <span class="thumb-name">{{ inst.name }}</span>
    </button>
  `,
  styles: `
    :host {
      display: block;
    }

    .thumb {
      display: flex;
      flex-direction: column;
      align-items: center;
      gap: 4px;
      padding: 4px;
      background: transparent;
      border: 2px solid transparent;
      border-radius: 8px;
      cursor: pointer;
      color: var(--cco-text-secondary);
      font: inherit;
    }

    .thumb.focused {
      border-color: var(--cco-accent);
      color: var(--cco-text);
    }

    .frame {
      position: relative;
      display: flex;
      align-items: center;
      justify-content: center;
      overflow: hidden;
      background: var(--cco-canvas);
      border-radius: 4px;
    }

    .frame video,
    .frame img {
      width: 100%;
      height: 100%;
      object-fit: contain;
    }

    /* A still must be preattentively distinct from a live stream, or a frozen
       tile reads as a running device. Dimming does that before any label is
       read; the age stamp says how stale once it is. */
    .frame img.still {
      opacity: 0.55;
      filter: saturate(0.6);
    }

    .tag {
      position: absolute;
      bottom: 2px;
      left: 2px;
      display: inline-flex;
      align-items: center;
      gap: 2px;
      padding: 0 4px;
      border-radius: 3px;
      background: rgba(0, 0, 0, 0.65);
      font-size: 10px;
      font-variant-numeric: tabular-nums;
    }

    .tag.live {
      color: var(--cco-success);
    }

    .tag.still-tag {
      color: var(--cco-text-secondary);
    }

    .badge {
      display: inline-flex;
      flex-direction: column;
      align-items: center;
      gap: 2px;
      font-size: 10px;
    }

    .tag mat-icon,
    .badge mat-icon {
      font-size: 12px;
      width: 12px;
      height: 12px;
    }

    .badge mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
    }

    .thumb-name {
      max-width: 96px;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      font-size: 11px;
    }
  `,
})
export class InstanceThumbComponent {
  private readonly deviceApi = inject(DeviceApiService);
  private readonly streaming = inject(DeviceStreamService);

  readonly deviceId = input.required<string>();
  readonly instance = input.required<DeviceInstance>();
  readonly focused = input(false);

  /** This instance holds a WebRTC session, so the strip shows its video. */
  readonly live = input(false);

  protected readonly frameHeight = THUMB_HEIGHT_PX;

  private readonly target = computed(() => ({
    deviceId: this.deviceId(),
    instance: this.instance().name,
  }));

  /**
   * The instance's lowest-numbered display, live.
   *
   * A session already holds decoded media; showing a badge beside it instead
   * meant the instance being paid for with a peer connection was the one you
   * could see least of.
   */
  protected readonly liveStream = computed(() => {
    if (!this.live()) {
      return null;
    }
    const streams = this.streaming.session(this.target())()?.streams();
    if (streams === undefined || streams.size === 0) {
      return null;
    }
    return [...streams.entries()].sort(([a], [b]) => a - b)[0][1];
  });

  /**
   * Undefined params park the resource. A live instance is already on screen,
   * and a still of it would only be a slower, staler copy.
   */
  protected readonly shot = resource({
    params: () => (this.live() ? undefined : this.target()),
    loader: async ({params: target}) => {
      const shot = await firstValueFrom(this.deviceApi.getScreenshot(target, 0));
      return {
        src: `data:${shot.screenshot_mime_type};base64,${shot.screenshot_bytes}`,
        at: Date.now(),
      };
    },
  });

  private readonly now = toSignal(interval(AGE_TICK_MS).pipe(map(() => Date.now())), {
    initialValue: Date.now(),
  });

  protected readonly age = computed(() => {
    const at = this.shot.value()?.at;
    if (!at) {
      return '';
    }
    const seconds = Math.max(0, Math.round((this.now() - at) / 1000));
    if (seconds < 60) {
      return `${seconds}s ago`;
    }
    const minutes = Math.round(seconds / 60);
    return minutes < 60 ? `${minutes}m ago` : `${Math.round(minutes / 60)}h ago`;
  });

  /** Equal heights, widths from the guest's own aspect ratio. */
  protected readonly frameWidth = computed(() => {
    const geometry = displayGeometry(this.instance());
    if (!geometry) {
      return Math.round(THUMB_HEIGHT_PX * (2 / 3));
    }
    const width = THUMB_HEIGHT_PX * (geometry.width / geometry.height);
    return Math.round(Math.min(MAX_THUMB_WIDTH_PX, Math.max(MIN_THUMB_WIDTH_PX, width)));
  });
}
