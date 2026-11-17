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

/** Expands APK-set archives (.apks/.xapk/.apkm) into their APK entries. */

import {ZipArchive} from '../../../shared/utils/zip-reader';
import {tryReadApkInfo} from './apk-info';
import type {InstallFile} from './install-plan';

const ARCHIVE_PATTERN = /\.(apks|xapk|apkm)$/i;

export function isApkArchive(fileName: string): boolean {
  return ARCHIVE_PATTERN.test(fileName);
}

/**
 * Reads every top-level `.apk` out of an APK-set archive. OBB expansion
 * files inside the archive are reported as warnings (we can't install them).
 * Nested directories (bundletool's `splits/` layout) are handled by matching
 * any `*.apk` entry.
 */
export async function extractApksArchive(
  archiveFile: File,
  warnings: string[]
): Promise<InstallFile[]> {
  let archive: ZipArchive;
  try {
    archive = ZipArchive.open(new Uint8Array(await archiveFile.arrayBuffer()));
  } catch (error) {
    warnings.push(
      `Could not read ${archiveFile.name}: ${error instanceof Error ? error.message : 'unknown error'}`
    );
    return [];
  }

  const entries = archive.list();
  const apkEntries = entries.filter(
    entry => !entry.isDirectory && entry.name.toLowerCase().endsWith('.apk')
  );
  if (entries.some(entry => entry.name.toLowerCase().endsWith('.obb'))) {
    warnings.push(
      `${archiveFile.name} contains OBB expansion files, which are not installed automatically`
    );
  }
  if (apkEntries.length === 0) {
    warnings.push(`${archiveFile.name} contains no APK files`);
    return [];
  }

  const files: InstallFile[] = [];
  for (const entry of apkEntries) {
    const bytes = await archive.read(entry.name);
    const name = entry.name.split('/').pop() ?? entry.name;
    const file = new File([bytes as BlobPart], name, {
      type: 'application/vnd.android.package-archive',
    });
    files.push({blob: file, name, size: file.size, info: await tryReadApkInfo(file)});
  }
  return files;
}
