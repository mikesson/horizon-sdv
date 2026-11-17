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

/** Information about a display attached to the device. */
export interface DisplayInfo {
  /** Display ID (0 = primary, 1+ = additional). */
  displayId: number;
  /** Human-readable label for the display. */
  label: string;
  /** Display width in pixels. */
  width: number;
  /** Display height in pixels. */
  height: number;
  /** Pixel density (DPI). */
  density: number;
  /** Rotation in degrees (0, 90, 180, 270). */
  rotation: number;
}

/** A display with an associated video stream. */
export interface VideoDisplay {
  /** Display ID. */
  displayId: number;
  /** Media stream for this display's video. */
  stream: MediaStream;
  /** Display metadata. */
  info: DisplayInfo;
}

/** Layout mode for multi-display container. */
export type DisplayLayout = 'single' | 'grid' | 'primary-thumbnails';

/**
 * Gets the optimal layout based on display count.
 */
export function getOptimalLayout(displayCount: number): DisplayLayout {
  if (displayCount <= 1) {
    return 'single';
  }
  if (displayCount <= 3) {
    return 'grid';
  }
  return 'primary-thumbnails';
}
