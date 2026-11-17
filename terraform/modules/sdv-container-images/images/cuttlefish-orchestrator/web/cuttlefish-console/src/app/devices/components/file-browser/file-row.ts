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

import type {FileEntry} from '../../services/adb/adb-files';

/** A directory entry enriched with precomputed display fields. */
export interface FileRow extends FileEntry {
  icon: string;
  sizeLabel: string;
  dateLabel: string;
}

export function toFileRow(entry: FileEntry): FileRow {
  return {
    ...entry,
    icon: fileIcon(entry),
    sizeLabel: entry.isFile ? formatSize(entry.size) : '',
    dateLabel: entry.mtime.toLocaleDateString(undefined, {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    }),
  };
}

export function fileIcon(entry: FileEntry): string {
  if (entry.isDirectory) {
    return 'folder';
  }
  if (entry.isSymlink) {
    return 'link';
  }

  const ext = entry.name.split('.').pop()?.toLowerCase();
  switch (ext) {
    case 'apk':
      return 'android';
    case 'jpg':
    case 'jpeg':
    case 'png':
    case 'gif':
    case 'webp':
      return 'image';
    case 'mp4':
    case 'mkv':
    case 'avi':
      return 'movie';
    case 'mp3':
    case 'ogg':
    case 'wav':
      return 'audio_file';
    case 'pdf':
      return 'picture_as_pdf';
    case 'txt':
    case 'log':
      return 'description';
    case 'json':
    case 'xml':
      return 'code';
    case 'zip':
    case 'tar':
    case 'gz':
      return 'folder_zip';
    default:
      return 'insert_drive_file';
  }
}

export function formatSize(bytes: number): string {
  if (bytes === 0) {
    return '0 B';
  }
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return `${Number.parseFloat((bytes / k ** i).toFixed(1))} ${sizes[i]}`;
}
