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

/**
 * Turns a set of dropped files into a concrete install plan: which files
 * belong to which app, and how each app installs (single vs split session).
 */

import {extractApksArchive, isApkArchive} from './apk-archive';
import type {ApkInfo} from './apk-info';
import {tryReadApkInfo} from './apk-info';

/** One installable app: a base APK plus zero or more splits. */
export interface InstallUnit {
  packageName: string | null;
  versionName?: string;
  files: InstallFile[];
}

export interface InstallFile {
  blob: Blob;
  name: string;
  size: number;
  info: ApkInfo | null;
}

export interface InstallPlan {
  units: InstallUnit[];
  /**
   * True when files could not be attributed to packages (unreadable
   * manifests), so grouping is a guess and the UI should let the user choose
   * split-vs-separate.
   */
  ambiguous: boolean;
  warnings: string[];
}

/**
 * Builds an install plan from dropped files. Archives (.apks/.xapk/.apkm)
 * are expanded to their APK entries first. Files are grouped by parsed
 * package name; unreadable files fall into a single ambiguous unit.
 */
export async function buildInstallPlan(files: readonly File[]): Promise<InstallPlan> {
  const warnings: string[] = [];
  const installFiles: InstallFile[] = [];

  for (const file of files) {
    if (isApkArchive(file.name)) {
      const extracted = await extractApksArchive(file, warnings);
      installFiles.push(...extracted);
    } else if (file.name.toLowerCase().endsWith('.apk')) {
      installFiles.push({
        blob: file,
        name: file.name,
        size: file.size,
        info: await tryReadApkInfo(file),
      });
    } else {
      warnings.push(`Skipped ${file.name}: not an APK or APK archive`);
    }
  }

  if (installFiles.length === 0) {
    return {units: [], ambiguous: false, warnings};
  }

  return groupIntoUnits(installFiles, warnings);
}

function groupIntoUnits(files: InstallFile[], warnings: string[]): InstallPlan {
  const readable = files.filter(f => f.info?.package);
  const unreadable = files.filter(f => !f.info?.package);

  // Every file's package is known, so group exactly.
  if (unreadable.length === 0) {
    const byPackage = new Map<string, InstallFile[]>();
    for (const file of files) {
      const pkg = file.info?.package ?? '';
      byPackage.set(pkg, [...(byPackage.get(pkg) ?? []), file]);
    }
    const units = [...byPackage.entries()].map(([packageName, unitFiles]) => ({
      packageName,
      versionName: unitFiles.find(f => !f.info?.split)?.info?.versionName,
      files: sortBaseFirst(unitFiles),
    }));
    return {units, ambiguous: false, warnings};
  }

  // Some manifests were unreadable: fall back to one ambiguous unit so the
  // user decides split-vs-separate.
  if (readable.length > 0) {
    warnings.push('Some APKs could not be read; grouping may be inaccurate');
  }
  return {
    units: [{packageName: readable[0]?.info?.package ?? null, files: sortBaseFirst(files)}],
    ambiguous: true,
    warnings,
  };
}

/** pm requires the base APK first in a split session. */
function sortBaseFirst(files: InstallFile[]): InstallFile[] {
  return [...files].sort((a, b) => {
    const aSplit = a.info?.split ? 1 : 0;
    const bSplit = b.info?.split ? 1 : 0;
    return aSplit - bSplit;
  });
}
