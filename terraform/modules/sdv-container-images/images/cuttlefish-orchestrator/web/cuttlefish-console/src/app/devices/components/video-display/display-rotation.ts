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

export interface RotationStyle {
  transform: string;
  maxWidth: string;
  maxHeight: string;
}

/**
 * Computes the style that fits a rotated video element inside its parent.
 * A direct port of the upstream Cuttlefish client's getStyleAfterRotation,
 * so a rotated guest renders exactly as it does in the reference client.
 *
 * The device streams the same buffer regardless of rotation; the client
 * counter-rotates the element (CSS rotations run opposite to Android screen
 * rotations). Pointer offsetX/offsetY are inverse-transformed by the
 * browser, so input coordinates stay in unrotated buffer space untouched.
 */
export function rotationStyle(rotationDeg: number, aspectRatio: number): RotationStyle {
  const r = (Math.PI * rotationDeg) / 180;
  const ar = aspectRatio;

  // width <= parent_width / max(abs(cos(r) ± sin(r)/ar))
  const widthDenominator = Math.max(
    Math.abs(Math.sin(r) / ar + Math.cos(r)),
    Math.abs(Math.sin(r) / ar - Math.cos(r))
  );
  const maxWidth = `calc(100% / ${widthDenominator})`;

  // height <= parent_height / max(abs(cos(r) ± sin(r)*ar))
  const heightDenominator = Math.max(
    Math.abs(Math.cos(r) - Math.sin(r) * ar),
    Math.abs(Math.cos(r) + Math.sin(r) * ar)
  );
  const maxHeight = `calc(100% / ${heightDenominator})`;

  const leftFactor =
    (Math.max(Math.abs(Math.cos(r) + Math.sin(r) / ar), Math.abs(Math.cos(r) - Math.sin(r) / ar)) -
      1) /
    2;
  const topFactor =
    (Math.max(Math.abs(Math.cos(r) - Math.sin(r) * ar), Math.abs(Math.cos(r) + Math.sin(r) * ar)) -
      1) /
    2;

  // CSS rotations run in the opposite direction to Android screen rotations.
  const transform =
    `translate(calc(100% * ${leftFactor}), calc(100% * ${topFactor})) ` +
    `rotate(${-rotationDeg}deg)`;

  return {transform, maxWidth, maxHeight};
}

/**
 * Maps a pointer position on the video element to device buffer
 * coordinates. A direct port of upstream scaleDisplayCoordinates. Both
 * axes scale by the width ratio (the element keeps the buffer's aspect).
 * Before the first frame the buffer size is unknown and clicks map to ~0,0,
 * matching the reference client ("the user is clicking blind anyways").
 */
export function scaleDisplayCoordinates(
  video: {videoWidth: number; offsetWidth: number},
  offsetX: number,
  offsetY: number
): {x: number; y: number} {
  const actualWidth = video.videoWidth ? video.videoWidth : 1;
  const elementWidth = video.offsetWidth ? video.offsetWidth : 1;
  const scaling = actualWidth / elementWidth;
  return {x: Math.trunc(offsetX * scaling), y: Math.trunc(offsetY * scaling)};
}
