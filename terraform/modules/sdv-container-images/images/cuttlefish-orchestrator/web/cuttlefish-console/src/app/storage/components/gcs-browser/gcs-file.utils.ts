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

/** Material icon name for a GCS object, chosen by file extension. */
export function fileIcon(displayName: string): string {
  const name = displayName.toLowerCase();
  if (name.endsWith('.zip') || name.endsWith('.tar.gz') || name.endsWith('.tgz')) {
    return 'archive';
  }
  if (name.endsWith('.img')) {
    return 'disc_full';
  }
  if (name.endsWith('.json')) {
    return 'data_object';
  }
  if (name.endsWith('.txt') || name.endsWith('.md')) {
    return 'description';
  }
  return 'insert_drive_file';
}

/** Human-readable size, empty string when unknown. */
export function formatSize(bytes?: number): string {
  if (!bytes) {
    return '';
  }
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let size = bytes;
  let unitIndex = 0;
  while (size >= 1024 && unitIndex < units.length - 1) {
    size /= 1024;
    unitIndex++;
  }
  return `${size.toFixed(1)} ${units[unitIndex]}`;
}

/** Whether the file is an installable Android package. */
export function isApkFile(displayName: string): boolean {
  return /\.(apk|apks|xapk|apkm)$/i.test(displayName);
}

/**
 * The bucket name out of whatever an operator pasted. The Cloud console hands
 * out full `gs://bucket/path/` URLs, and every field here wants the bare name,
 * so accept both rather than sending `gs://gs://bucket` to the API.
 */
export function bucketNameFrom(input: string): string {
  const withoutScheme = input.trim().replace(/^gs:\/\//, '');
  const [bucket] = withoutScheme.split('/');
  return bucket;
}
