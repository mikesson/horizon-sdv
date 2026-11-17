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

/**
 * Touch event for device interaction.
 *
 * Represents a single touch point on the device screen.
 */
export interface TouchEvent {
  /** Touch phase: down (press), up (release), or move (drag). */
  type: 'down' | 'up' | 'move';
  /** X coordinate in device pixels. */
  x: number;
  /** Y coordinate in device pixels. */
  y: number;
  /** Pointer ID for multi-touch tracking. */
  pointerId?: number;
  /** Display ID for multi-display support (default: 0). */
  displayId?: number;
  /** Display label for Cuttlefish (default: "display_0"). */
  displayLabel?: string;
}

/**
 * Keyboard event forwarded to the device.
 */
export interface KeyEvent {
  /** Key phase: down (press) or up (release). */
  type: 'down' | 'up';
  /**
   * DOM `KeyboardEvent.code` (e.g. "KeyA", "Enter"). The Cuttlefish
   * streamer maps DOM code strings to evdev codes device-side.
   */
  code: string;
}
