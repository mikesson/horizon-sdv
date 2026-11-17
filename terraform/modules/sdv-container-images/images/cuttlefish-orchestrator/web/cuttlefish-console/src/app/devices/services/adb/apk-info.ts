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

/** Reads package metadata straight out of an APK file (no device needed). */

import {ZipArchive} from '../../../shared/utils/zip-reader';
import type {ManifestInfo} from './axml-parser';
import {parseAndroidManifest} from './axml-parser';

export interface ApkInfo extends ManifestInfo {
  fileName: string;
  sizeBytes: number;
  /** Native ABIs bundled under lib/<abi>/, e.g. ['arm64-v8a']. */
  abis: string[];
}

const LIB_ABI_PATTERN = /^lib\/([^/]+)\//;

/**
 * Extracts manifest + ABI info from an APK blob. Rejects with a friendly
 * message when the file isn't a parseable APK.
 */
async function readApkInfo(file: File): Promise<ApkInfo> {
  let archive: ZipArchive;
  try {
    archive = ZipArchive.open(new Uint8Array(await file.arrayBuffer()));
  } catch (error) {
    throw new Error(
      `${file.name} is not a readable APK: ${error instanceof Error ? error.message : 'unknown error'}`
    );
  }

  const manifestEntry = archive.list().find(entry => entry.name === 'AndroidManifest.xml');
  if (!manifestEntry) {
    throw new Error(`${file.name} has no AndroidManifest.xml`);
  }

  const manifest = parseAndroidManifest(await archive.read('AndroidManifest.xml'));

  const abis = new Set<string>();
  for (const entry of archive.list()) {
    const match = LIB_ABI_PATTERN.exec(entry.name);
    if (match) {
      abis.add(match[1]);
    }
  }

  return {
    ...manifest,
    fileName: file.name,
    sizeBytes: file.size,
    abis: [...abis].sort(),
  };
}

/** Best-effort info; returns null instead of throwing for non-APK inputs. */
export async function tryReadApkInfo(file: File): Promise<ApkInfo | null> {
  try {
    return await readApkInfo(file);
  } catch {
    return null;
  }
}
