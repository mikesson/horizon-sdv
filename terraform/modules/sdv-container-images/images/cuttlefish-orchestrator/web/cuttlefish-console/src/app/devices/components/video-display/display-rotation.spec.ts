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

import {describe, expect, it} from 'vitest';
import {rotationStyle, scaleDisplayCoordinates} from './display-rotation';

describe('rotationStyle', () => {
  const PORTRAIT = 9 / 16;

  function denominator(constraint: string): number {
    const match = /calc\(100% \/ (.+)\)/.exec(constraint);
    if (!match) {
      throw new Error(`Unexpected constraint format: ${constraint}`);
    }
    return Number(match[1]);
  }

  it('counter-rotates: CSS rotation runs opposite to Android screen rotation', () => {
    expect(rotationStyle(90, PORTRAIT).transform).toContain('rotate(-90deg)');
    expect(rotationStyle(270, PORTRAIT).transform).toContain('rotate(-270deg)');
  });

  it('constrains a 90° portrait rotation by the swapped aspect', () => {
    const style = rotationStyle(90, PORTRAIT);
    // width <= parent / (sin(90)/ar); height <= parent / (sin(90)*ar).
    expect(denominator(style.maxWidth)).toBeCloseTo(16 / 9, 10);
    expect(denominator(style.maxHeight)).toBeCloseTo(9 / 16, 10);
  });

  it('is a no-op sizing at 180°', () => {
    const style = rotationStyle(180, PORTRAIT);
    expect(denominator(style.maxWidth)).toBeCloseTo(1, 10);
    expect(denominator(style.maxHeight)).toBeCloseTo(1, 10);
    expect(style.transform).toContain('rotate(-180deg)');
  });
});

describe('scaleDisplayCoordinates', () => {
  it('scales both axes by the width ratio', () => {
    const video = {videoWidth: 720, offsetWidth: 360};
    expect(scaleDisplayCoordinates(video, 100, 50)).toEqual({x: 200, y: 100});
  });

  it('truncates fractional device coordinates', () => {
    const video = {videoWidth: 1080, offsetWidth: 400};
    expect(scaleDisplayCoordinates(video, 33, 21)).toEqual({
      x: Math.trunc(33 * 2.7),
      y: Math.trunc(21 * 2.7),
    });
  });

  it('maps clicks to ~0,0 before the first frame, like the reference client', () => {
    const video = {videoWidth: 0, offsetWidth: 360};
    expect(scaleDisplayCoordinates(video, 100, 50)).toEqual({x: 0, y: 0});
  });
});
