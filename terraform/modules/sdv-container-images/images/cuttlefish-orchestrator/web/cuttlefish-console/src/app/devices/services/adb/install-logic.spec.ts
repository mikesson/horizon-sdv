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
import type {ApkInfo} from './apk-info';
import {classifyInstallError} from './install-errors';
import type {InstallFile} from './install-plan';
import type {DeviceProfile} from './install-split-select';
import {selectSplitsForDevice} from './install-split-select';

function file(name: string, split?: string, pkg = 'com.example.app'): InstallFile {
  const info: ApkInfo = {
    package: pkg,
    split,
    fileName: name,
    sizeBytes: 100,
    abis: [],
  };
  return {blob: new Blob([new Uint8Array(100)] as BlobPart[]), name, size: 100, info};
}

describe('classifyInstallError', () => {
  it('offers a downgrade remedy for version downgrade', () => {
    const result = classifyInstallError(new Error('Failure [INSTALL_FAILED_VERSION_DOWNGRADE]'));
    expect(result.code).toBe('INSTALL_FAILED_VERSION_DOWNGRADE');
    expect(result.remedy).toEqual({kind: 'retry-downgrade', label: expect.any(String)});
  });

  it('offers uninstall-and-retry for a signature mismatch', () => {
    const result = classifyInstallError(
      new Error('Failure [INSTALL_FAILED_UPDATE_INCOMPATIBLE: signatures do not match]')
    );
    expect(result.remedy?.kind).toBe('uninstall-retry');
  });

  it('offers install-all-splits for a missing split', () => {
    const result = classifyInstallError(new Error('Failure [INSTALL_FAILED_MISSING_SPLIT]'));
    expect(result.remedy?.kind).toBe('retry-all-splits');
  });

  it('explains storage and SDK failures without a remedy', () => {
    expect(
      classifyInstallError(new Error('Failure [INSTALL_FAILED_INSUFFICIENT_STORAGE]')).remedy
    ).toBeUndefined();
    expect(classifyInstallError(new Error('Failure [INSTALL_FAILED_OLDER_SDK]')).message).toMatch(
      /minSdkVersion/
    );
  });

  it('passes through unrecognized errors', () => {
    expect(classifyInstallError(new Error('boom')).message).toBe('boom');
  });
});

describe('selectSplitsForDevice', () => {
  const profile: DeviceProfile = {abis: ['arm64-v8a', 'armeabi-v7a'], densityBucket: 'xxhdpi'};

  it('keeps base, best ABI, best density, and all language splits', () => {
    const files = [
      file('base.apk'),
      file('config.arm64_v8a.apk', 'config.arm64_v8a'),
      file('config.armeabi_v7a.apk', 'config.armeabi_v7a'),
      file('config.xxhdpi.apk', 'config.xxhdpi'),
      file('config.hdpi.apk', 'config.hdpi'),
      file('config.en.apk', 'config.en'),
      file('config.fr.apk', 'config.fr'),
    ];

    const names = selectSplitsForDevice(files, profile).map(f => f.name);
    expect(names).toContain('base.apk');
    expect(names).toContain('config.arm64_v8a.apk');
    expect(names).not.toContain('config.armeabi_v7a.apk');
    expect(names).toContain('config.xxhdpi.apk');
    expect(names).not.toContain('config.hdpi.apk');
    expect(names).toContain('config.en.apk');
    expect(names).toContain('config.fr.apk');
  });

  it('falls back to the preferred ABI when the exact one is absent', () => {
    const files = [file('base.apk'), file('config.armeabi_v7a.apk', 'config.armeabi_v7a')];
    // Device prefers arm64 but only v7a is present, so take what exists.
    expect(selectSplitsForDevice(files, profile).map(f => f.name)).toEqual([
      'base.apk',
      'config.armeabi_v7a.apk',
    ]);
  });

  it('returns files untouched when there are no config splits', () => {
    const files = [file('base.apk'), file('feature_pay.apk', 'pay')];
    expect(selectSplitsForDevice(files, profile)).toHaveLength(2);
  });

  it('never lets a higher density shadow the requested one by substring', () => {
    const hdpiProfile: DeviceProfile = {abis: ['arm64_v8a'], densityBucket: 'hdpi'};
    // xxhdpi listed first: 'config.xxhdpi' contains 'hdpi' as a substring,
    // but the device must still get the exact hdpi split.
    const files = [
      file('base.apk'),
      file('config.xxhdpi.apk', 'config.xxhdpi'),
      file('config.xhdpi.apk', 'config.xhdpi'),
      file('config.hdpi.apk', 'config.hdpi'),
    ];

    const names = selectSplitsForDevice(files, hdpiProfile).map(f => f.name);
    expect(names).toContain('config.hdpi.apk');
    expect(names).not.toContain('config.xxhdpi.apk');
    expect(names).not.toContain('config.xhdpi.apk');
  });

  it('never gives an x86 device the x86_64 split by substring', () => {
    const x86Profile: DeviceProfile = {abis: ['x86'], densityBucket: 'mdpi'};
    const files = [
      file('base.apk'),
      file('config.x86_64.apk', 'config.x86_64'),
      file('config.x86.apk', 'config.x86'),
    ];

    const names = selectSplitsForDevice(files, x86Profile).map(f => f.name);
    expect(names).toContain('config.x86.apk');
    expect(names).not.toContain('config.x86_64.apk');
  });

  it('classifies a parsed base as base even when its file name looks like a config split', () => {
    // A readable manifest without split= is authoritative; only unreadable
    // files fall back to the name heuristic.
    const files = [
      file('mipsuite-x86.apk'),
      file('config.arm64_v8a.apk', 'config.arm64_v8a'),
      file('config.x86.apk', 'config.x86'),
    ];
    const arm64Profile: DeviceProfile = {abis: ['arm64-v8a'], densityBucket: 'mdpi'};

    const names = selectSplitsForDevice(files, arm64Profile).map(f => f.name);
    expect(names).toContain('mipsuite-x86.apk');
    expect(names).toContain('config.arm64_v8a.apk');
    expect(names).not.toContain('config.x86.apk');
  });
});
