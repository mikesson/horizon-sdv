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
  ElementRef,
  inject,
  input,
  linkedSignal,
  output,
  viewChild,
} from '@angular/core';
import type {DisplayInfo} from '../../models/display.model';
import {DeviceInputService} from '../../services/input/device-input.service';
import {captureVideoFrame} from './capture-frame';
import {rotationStyle, scaleDisplayCoordinates} from './display-rotation';

/**
 * Displays a single video stream from a Cuttlefish device display.
 *
 * Handles touch/mouse input forwarding to the correct display
 * and supports focus state for multi-display scenarios.
 *
 * @example
 * ```html
 * <app-video-display
 *   [deviceId]="device.id"
 *   [displayId]="0"
 *   [stream]="videoStream"
 *   [focused]="true"
 *   (focusDisplay)="onFocus($event)"
 * />
 * ```
 */
@Component({
  selector: 'app-video-display',
  imports: [],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    // Focusable so a click captures the keyboard for the device; Esc (or
    // focusing anything else) releases it.
    tabindex: '0',
    // The click lives on the host rather than the inner container so the
    // clickable surface IS the focusable, key-handling one.
    '(click)': 'onClick()',
    '(contextmenu)': 'onContextMenu($event)',
    '(keydown)': 'onKeyDown($event)',
    '(keyup)': 'onKeyUp($event)',
    '(blur)': 'releasePressedKeys()',
  },
  template: `
    <div
      class="display-container"
      [class.focused]="focused()"
    >
      <!-- Header bar like Host Orchestrator -->
      <div class="display-header">
        <span class="display-title">Display {{ displayId() }}</span>
        <span class="keyboard-hint">Keyboard captured, Esc releases</span>
        <span class="display-resolution">{{ resolutionText() }}</span>
      </div>

      <div class="video-wrapper">
        <video
          #videoElement
          autoplay
          playsinline
          muted
          [srcObject]="stream()"
          [style.transform]="rotationStyles()?.transform ?? ''"
          [style.max-width]="rotationStyles()?.maxWidth ?? ''"
          [style.max-height]="rotationStyles()?.maxHeight ?? ''"
          (loadedmetadata)="onVideoResize()"
          (resize)="onVideoResize()"
          (pointerdown)="onPointerDown($event)"
          (pointermove)="onPointerMove($event)"
          (pointerup)="onPointerUp($event)"
          (pointerleave)="onPointerUp($event)"
          (pointercancel)="onPointerUp($event)"
        ></video>
      </div>

      @if (focused()) {
        <div class="focus-indicator"></div>
      }
    </div>
  `,
  styles: `
      :host {
        display: block;
        width: 100%;
        height: 100%;
        /* The app-level focus ring (.focused) is the affordance. */
        outline: none;
      }

      .keyboard-hint {
        display: none;
        font-size: 11px;
        opacity: 0.75;
      }

      :host(:focus) .keyboard-hint {
        display: inline;
      }

      .display-container {
        position: relative;
        width: 100%;
        height: 100%;
        background: var(--cco-surface);
        border-radius: 4px;
        overflow: hidden;
        cursor: pointer;
        transition: box-shadow var(--cco-duration-base) ease;
        display: flex;
        flex-direction: column;
        border: 1px solid var(--cco-border);
      }

      .display-container:hover {
        box-shadow: 0 0 0 1px rgba(255, 255, 255, 0.2);
      }

      .display-container.focused {
        box-shadow: 0 0 0 2px var(--cco-accent);
      }

      /* Header bar like Host Orchestrator */
      .display-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        padding: 8px 12px;
        background: linear-gradient(135deg, var(--cco-accent-surface) 0%, var(--cco-accent-surface) 100%);
        color: white;
        font-size: 12px;
        flex-shrink: 0;
        border-bottom: 1px solid var(--cco-accent-surface);
      }

      .display-title {
        font-weight: 500;
      }

      .display-resolution {
        font-family: monospace;
        font-size: 11px;
        opacity: 0.9;
      }

      .video-wrapper {
        flex: 1;
        min-height: 0;
        display: flex;
        align-items: center;
        justify-content: center;
        background: var(--cco-canvas);
      }

      /* Sized to the buffer's aspect (like the upstream client) so the
         element box has no letterbox: offsetX/offsetY map straight to
         buffer coordinates by the width ratio, and stay correct under
         rotation because browsers inverse-transform pointer offsets. */
      video {
        max-width: 100%;
        max-height: 100%;
        cursor: crosshair;
        touch-action: none;
      }

      .focus-indicator {
        position: absolute;
        inset: 0;
        border: 2px solid var(--cco-accent);
        border-radius: 4px;
        pointer-events: none;
        animation: focusPulse 2s ease-in-out infinite;
      }

      @keyframes focusPulse {
        0%,
        100% {
          opacity: 1;
        }
        50% {
          opacity: 0.5;
        }
      }
    `,
})
export class VideoDisplayComponent {
  private readonly inputService = inject(DeviceInputService);
  private readonly host = inject<ElementRef<HTMLElement>>(ElementRef);

  readonly deviceId = input.required<string>();

  /** Display ID (0 = primary). */
  readonly displayId = input<number>(0);

  readonly stream = input<MediaStream | null>(null);

  readonly info = input<DisplayInfo | null>(null);

  readonly focused = input<boolean>(false);

  /** Device rotation in degrees (0/90/180/270), from the streaming session. */
  readonly rotation = input(0);

  readonly focusDisplay = output<number>();

  readonly videoElement = viewChild<ElementRef<HTMLVideoElement>>('videoElement');

  /** Pointer ids currently down on this display (drag vs hover). */
  private readonly activePointers = new Set<number>();

  /** Intrinsic video dimensions, reset when the stream changes. */
  private readonly videoResolution = linkedSignal<
    MediaStream | null,
    {width: number; height: number} | null
  >({source: this.stream, computation: () => null});

  protected readonly resolutionText = computed(() => {
    const resolution = this.videoResolution();
    if (resolution) {
      return `${resolution.width}x${resolution.height}`;
    }
    const displayInfo = this.info();
    if (displayInfo?.width && displayInfo?.height) {
      return `${displayInfo.width}x${displayInfo.height}`;
    }
    return '';
  });

  /** Counter-rotation fitting the rotated guest into the wrapper; null while
   * unrotated or before the buffer dimensions are known. */
  protected readonly rotationStyles = computed(() => {
    const rotation = this.rotation() % 360;
    const resolution = this.videoResolution();
    if (rotation === 0 || !resolution) {
      return null;
    }
    return rotationStyle(rotation, resolution.width / resolution.height);
  });

  protected onVideoResize(): void {
    const video = this.videoElement()?.nativeElement;
    if (video?.videoWidth && video.videoHeight) {
      this.videoResolution.set({width: video.videoWidth, height: video.videoHeight});
    }
  }

  /** Keys held down, so a blur can release them device-side. */
  private readonly pressedKeys = new Set<string>();

  onClick(): void {
    // Click-to-focus is explicit: browsers differ on whether clicking a
    // non-focusable child focuses the tabindexed ancestor.
    this.host.nativeElement.focus();
    this.focusDisplay.emit(this.displayId());
  }

  protected onKeyDown(event: KeyboardEvent): void {
    if (event.code === 'Escape') {
      this.host.nativeElement.blur();
      return;
    }
    event.preventDefault();
    if (!event.repeat) {
      this.pressedKeys.add(event.code);
    }
    this.inputService.sendKey(this.deviceId(), {type: 'down', code: event.code});
  }

  protected onKeyUp(event: KeyboardEvent): void {
    if (event.code === 'Escape') {
      return;
    }
    event.preventDefault();
    this.pressedKeys.delete(event.code);
    this.inputService.sendKey(this.deviceId(), {type: 'up', code: event.code});
  }

  /** Focus left the display: release anything still held so the device
   * never sees a key stuck down. */
  protected releasePressedKeys(): void {
    for (const code of this.pressedKeys) {
      this.inputService.sendKey(this.deviceId(), {type: 'up', code});
    }
    this.pressedKeys.clear();
  }

  /**
   * Pointer events unify mouse and touch (with per-pointer ids for real
   * multi-touch) and inverse-transform offsetX/offsetY through the rotation.
   * This is the same model as the upstream client. Default actions are never
   * prevented on pointerdown so the display can take keyboard focus.
   */
  protected onPointerDown(event: PointerEvent): void {
    if (!this.focused()) {
      return;
    }
    this.activePointers.add(event.pointerId);
    this.sendTouchPoint(event, 'down');
  }

  protected onPointerMove(event: PointerEvent): void {
    if (!this.activePointers.has(event.pointerId)) {
      // A hover, not a drag.
      return;
    }
    this.sendTouchPoint(event, 'move');
  }

  /** Also handles pointerleave/pointercancel: a drag leaving the element
   * must release device-side or the touch stays stuck down. */
  protected onPointerUp(event: PointerEvent): void {
    if (!this.activePointers.delete(event.pointerId)) {
      return;
    }
    this.sendTouchPoint(event, 'up');
  }

  private sendTouchPoint(event: PointerEvent, type: 'down' | 'move' | 'up'): void {
    const video = this.videoElement()?.nativeElement;
    if (!video) {
      return;
    }
    const {x, y} = scaleDisplayCoordinates(video, event.offsetX, event.offsetY);
    this.inputService.sendTouch(this.deviceId(), {
      type,
      x,
      y,
      pointerId: event.pointerId,
      displayId: this.displayId(),
    });
  }

  onContextMenu(event: MouseEvent): void {
    if (!this.focused()) {
      return;
    }

    event.preventDefault();

    this.inputService.sendControlMessage(this.deviceId(), {
      command: 'back',
      button_state: 'down',
    });

    // Release after short delay (same pattern as floating-nav-bar)
    setTimeout(() => {
      this.inputService.sendControlMessage(this.deviceId(), {
        command: 'back',
        button_state: 'up',
      });
    }, 50);
  }

  /** Captures the current video frame as a PNG. */
  capture(): Promise<Blob> {
    const video = this.videoElement()?.nativeElement;
    if (!video) {
      return Promise.reject(new Error('No video frame available yet'));
    }
    return captureVideoFrame(video);
  }
}
