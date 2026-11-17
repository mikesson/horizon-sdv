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
  input,
  signal,
  viewChildren,
} from '@angular/core';
import type {DisplayLayout, VideoDisplay} from '../../models/display.model';
import {getOptimalLayout} from '../../models/display.model';
import {VideoDisplayComponent} from '../video-display';

export interface DisplayCapture {
  displayId: number;
  blob: Blob;
}

/**
 * Container for managing multiple video displays.
 *
 * Automatically adjusts layout based on display count:
 * - 1 display: Single full-size view
 * - 2-3 displays: Grid layout
 * - 4+ displays: Primary view with thumbnail strip
 *
 * @example
 * ```html
 * <app-multi-display-container
 *   [deviceId]="device.id"
 *   [displays]="displays()"
 * />
 * ```
 */
@Component({
  selector: 'app-multi-display-container',
  imports: [VideoDisplayComponent],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @let currentLayout = layout();
    <div [class]="['container', currentLayout]">
      @switch (currentLayout) {
        @case ('single') {
          @if (displays()[0]; as display) {
            <app-video-display
              [rotation]="rotation()"
              class="single-display"
              [deviceId]="deviceId()"
              [displayId]="display.displayId"
              [stream]="display.stream"
              [info]="display.info"
              [focused]="true"
            />
          }
        }
        @case ('grid') {
          @for (display of displays(); track display.displayId) {
            <app-video-display
              [rotation]="rotation()"
              class="grid-item"
              [deviceId]="deviceId()"
              [displayId]="display.displayId"
              [stream]="display.stream"
              [info]="display.info"
              [focused]="focusedDisplayId() === display.displayId"
              (focusDisplay)="onFocusDisplay($event)"
            />
          }
        }
        @case ('primary-thumbnails') {
          <div class="primary-section">
            @if (focusedDisplay(); as display) {
              <app-video-display
              [rotation]="rotation()"
                class="primary-display"
                [deviceId]="deviceId()"
                [displayId]="display.displayId"
                [stream]="display.stream"
                [info]="display.info"
                [focused]="true"
              />
            }
          </div>
          <div class="thumbnail-strip">
            @for (display of displays(); track display.displayId) {
              <div
                class="thumbnail"
                [class.active]="focusedDisplayId() === display.displayId"
              >
                <!-- The focus click rides app-video-display's own output, as it
                     already does in the other layout: its host is tabindex=0 and
                     handles keys, so a wrapper click would only add a second,
                     keyboard-inaccessible copy of the same action. -->
                <app-video-display
                  [rotation]="rotation()"
                  [deviceId]="deviceId()"
                  [displayId]="display.displayId"
                  [stream]="display.stream"
                  [info]="display.info"
                  [focused]="false"
                  (focusDisplay)="onFocusDisplay($event)"
                />
              </div>
            }
          </div>
        }
        @default never;
      }
    </div>
  `,
  styles: `
      :host {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 100%;
        height: 100%;
      }

      .container {
        display: flex;
        gap: 12px;
        padding: 16px;
        box-sizing: border-box;
        align-items: center;
        justify-content: center;
        max-height: 100%;
        max-width: 100%;
      }

      /* Single layout - constrained height like Host Orchestrator */
      .container.single {
        padding: 16px;
      }

      .single-display {
        max-height: calc(100vh - 200px);
        max-width: 100%;
        aspect-ratio: 9 / 16; /* Portrait phone aspect ratio */
      }

      /* Grid layout - side by side like Host Orchestrator */
      .container.grid {
        display: flex;
        flex-direction: row;
        flex-wrap: wrap;
        align-items: center;
        justify-content: center;
      }

      .grid-item {
        max-height: calc(100vh - 200px);
        max-width: 400px;
        flex: 0 1 auto;
        aspect-ratio: 9 / 16; /* Portrait phone aspect ratio */
      }

      /* Primary + thumbnails layout */
      .container.primary-thumbnails {
        flex-direction: column;
        height: 100%;
      }

      .primary-section {
        flex: 1;
        min-height: 0;
        display: flex;
        align-items: center;
        justify-content: center;
      }

      .primary-display {
        max-height: calc(100vh - 280px);
        max-width: 100%;
        aspect-ratio: 9 / 16;
      }

      .thumbnail-strip {
        display: flex;
        gap: 8px;
        height: 120px;
        flex-shrink: 0;
        overflow-x: auto;
        padding: 4px 0;
        justify-content: center;
      }

      .thumbnail {
        flex-shrink: 0;
        width: 80px;
        height: 100%;
        border-radius: 4px;
        overflow: hidden;
        cursor: pointer;
        opacity: 0.7;
        transition: opacity var(--cco-duration-base) ease, box-shadow var(--cco-duration-base) ease;
      }

      .thumbnail:hover {
        opacity: 0.9;
      }

      .thumbnail.active {
        opacity: 1;
        box-shadow: 0 0 0 2px var(--cco-accent);
      }
    `,
})
export class MultiDisplayContainerComponent {
  readonly deviceId = input.required<string>();

  readonly displays = input<VideoDisplay[]>([]);

  /** Guest screen rotation in degrees, applied to every display. */
  readonly rotation = input(0);

  readonly focusedDisplayId = signal(0);

  private readonly displayComponents = viewChildren(VideoDisplayComponent);

  readonly layout = computed<DisplayLayout>(() => {
    return getOptimalLayout(this.displays().length);
  });

  readonly focusedDisplay = computed(() => {
    const displays = this.displays();
    const focusedId = this.focusedDisplayId();
    return displays.find(d => d.displayId === focusedId) ?? displays[0];
  });

  onFocusDisplay(displayId: number): void {
    this.focusedDisplayId.set(displayId);
  }

  /** Captures the focused display's current frame. */
  async captureFocused(): Promise<DisplayCapture> {
    const focusedId = this.focusedDisplay()?.displayId ?? 0;
    const component = this.uniqueDisplayComponents().get(focusedId);
    if (!component) {
      throw new Error('No video frame available yet');
    }
    return {displayId: focusedId, blob: await component.capture()};
  }

  /** Captures every display's current frame. */
  async captureAll(): Promise<DisplayCapture[]> {
    const captures: DisplayCapture[] = [];
    for (const [displayId, component] of this.uniqueDisplayComponents()) {
      captures.push({displayId, blob: await component.capture()});
    }
    return captures.sort((a, b) => a.displayId - b.displayId);
  }

  /**
   * One component per display id: the primary-thumbnails layout renders the
   * focused display twice, and the first (larger) instance wins.
   */
  private uniqueDisplayComponents(): Map<number, VideoDisplayComponent> {
    const unique = new Map<number, VideoDisplayComponent>();
    for (const component of this.displayComponents()) {
      if (!unique.has(component.displayId())) {
        unique.set(component.displayId(), component);
      }
    }
    return unique;
  }
}
