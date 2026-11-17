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
import type {InstallState} from '../../services/adb/device-install.service';
import {installPercent, installStatusLabel} from './install-progress';

const MB = 1024 * 1024;

function makeState(overrides: Partial<InstallState> = {}): InstallState {
  return {
    phase: 'uploading',
    units: [],
    progress: null,
    bytesPerSecond: 0,
    etaSeconds: null,
    error: null,
    installedPackage: null,
    ...overrides,
  };
}

describe('installPercent', () => {
  it('is 0 when null, without progress, or with zero total', () => {
    expect(installPercent(null)).toBe(0);
    expect(installPercent(makeState())).toBe(0);
    expect(
      installPercent(
        makeState({progress: {fileName: 'a', sentBytes: 5, totalBytes: 0, committing: false}})
      )
    ).toBe(0);
  });

  it('rounds the ratio and clamps at 100', () => {
    expect(
      installPercent(
        makeState({progress: {fileName: 'a', sentBytes: 1, totalBytes: 3, committing: false}})
      )
    ).toBe(33);
    expect(
      installPercent(
        makeState({progress: {fileName: 'a', sentBytes: 999, totalBytes: 100, committing: false}})
      )
    ).toBe(100);
  });
});

describe('installStatusLabel', () => {
  it('shows a phase word before progress lands', () => {
    expect(installStatusLabel(makeState({phase: 'preparing'}))).toBe('Preparing…');
    expect(installStatusLabel(makeState({phase: 'committing'}))).toBe('Installing…');
  });

  it('shows transferred bytes, and appends speed and ETA only when positive', () => {
    const base = makeState({
      progress: {fileName: 'a', sentBytes: 2 * MB, totalBytes: 8 * MB, committing: false},
    });
    expect(installStatusLabel(base)).toBe('2.0 / 8.0 MB');
    expect(installStatusLabel({...base, bytesPerSecond: 4 * MB, etaSeconds: 3})).toBe(
      '2.0 / 8.0 MB · 4.0 MB/s · 3s left'
    );
  });
});
