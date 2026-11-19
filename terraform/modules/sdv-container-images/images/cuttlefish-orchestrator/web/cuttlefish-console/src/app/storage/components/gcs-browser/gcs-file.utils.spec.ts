/**
 * Copyright 2026 Google LLC
 * SPDX-License-Identifier: Apache-2.0
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
import {bucketNameFrom, fileIcon, formatSize, isApkFile} from './gcs-file.utils';

describe('gcs-file.utils', () => {
  it('selects Material icons by file extension case-insensitively', () => {
    expect(fileIcon('BUILD.ZIP')).toBe('archive');
    expect(fileIcon('images.tar.gz')).toBe('archive');
    expect(fileIcon('ota.tgz')).toBe('archive');
    expect(fileIcon('system.img')).toBe('disc_full');
    expect(fileIcon('manifest.JSON')).toBe('data_object');
    expect(fileIcon('notes.txt')).toBe('description');
    expect(fileIcon('README.md')).toBe('description');
    expect(fileIcon('sample.apk')).toBe('insert_drive_file');
  });

  it('formats unknown or zero sizes as empty string and binary units to one decimal', () => {
    expect(formatSize(undefined)).toBe('');
    expect(formatSize(0)).toBe('');
    expect(formatSize(512)).toBe('512.0 B');
    expect(formatSize(1024)).toBe('1.0 KB');
    expect(formatSize(1024 * 1024 * 2.5)).toBe('2.5 MB');
    expect(formatSize(1024 ** 4)).toBe('1.0 TB');
  });

  it('recognizes Android installable package extensions', () => {
    expect(isApkFile('app.apk')).toBe(true);
    expect(isApkFile('split.APKS')).toBe(true);
    expect(isApkFile('bundle.xapk')).toBe(true);
    expect(isApkFile('merged.apkm')).toBe(true);
    expect(isApkFile('app.apk.bak')).toBe(false);
    expect(isApkFile('system.img')).toBe(false);
  });

  it('extracts the bare bucket name from plain names and gs:// URLs', () => {
    expect(bucketNameFrom('my-bucket')).toBe('my-bucket');
    expect(bucketNameFrom('  gs://my-bucket/path/to/file.zip  ')).toBe('my-bucket');
    expect(bucketNameFrom('my-bucket/nested/')).toBe('my-bucket');
  });
});
