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
import type {FileEntry} from '../../services/adb/adb-files';
import {fileIcon, formatSize, toFileRow} from './file-row';

function makeEntry(overrides: Partial<FileEntry> = {}): FileEntry {
  return {
    name: 'file.txt',
    path: '/sdcard/file.txt',
    size: 0,
    isDirectory: false,
    isFile: true,
    isSymlink: false,
    mode: 0o644,
    mtime: new Date('2026-07-09T12:34:00Z'),
    ...overrides,
  };
}

describe('fileIcon', () => {
  it('maps directories and symlinks first, before extension', () => {
    expect(fileIcon(makeEntry({name: 'photos.apk', isDirectory: true}))).toBe('folder');
    expect(fileIcon(makeEntry({name: 'link.apk', isSymlink: true, isFile: false}))).toBe('link');
  });

  it('maps known extensions case-insensitively', () => {
    expect(fileIcon(makeEntry({name: 'app.APK'}))).toBe('android');
    expect(fileIcon(makeEntry({name: 'pic.JPEG'}))).toBe('image');
    expect(fileIcon(makeEntry({name: 'clip.mkv'}))).toBe('movie');
    expect(fileIcon(makeEntry({name: 'notes.log'}))).toBe('description');
    expect(fileIcon(makeEntry({name: 'data.json'}))).toBe('code');
    expect(fileIcon(makeEntry({name: 'bundle.gz'}))).toBe('folder_zip');
  });

  it('falls back to a generic file icon for unknown or missing extensions', () => {
    expect(fileIcon(makeEntry({name: 'README'}))).toBe('insert_drive_file');
    expect(fileIcon(makeEntry({name: 'archive.bin'}))).toBe('insert_drive_file');
  });
});

describe('formatSize', () => {
  it('renders bytes through gigabytes with one decimal', () => {
    expect(formatSize(0)).toBe('0 B');
    expect(formatSize(512)).toBe('512 B');
    expect(formatSize(1024)).toBe('1 KB');
    expect(formatSize(1536)).toBe('1.5 KB');
    expect(formatSize(5 * 1024 * 1024)).toBe('5 MB');
  });
});

describe('toFileRow', () => {
  it('blanks the size label for directories and formats it for files', () => {
    expect(toFileRow(makeEntry({isDirectory: true, isFile: false, size: 4096})).sizeLabel).toBe('');
    expect(toFileRow(makeEntry({size: 2048})).sizeLabel).toBe('2 KB');
  });

  it('carries the underlying entry fields through', () => {
    const row = toFileRow(makeEntry({name: 'a.png', path: '/sdcard/a.png'}));
    expect(row.name).toBe('a.png');
    expect(row.path).toBe('/sdcard/a.png');
    expect(row.icon).toBe('image');
    expect(row.dateLabel).not.toBe('');
  });
});
