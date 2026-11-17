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

/** App-management operations over a connected `Adb` device. */

import type {Adb} from '@yume-chan/adb';
import {escapeArg} from '@yume-chan/adb';
import {PackageManager} from '@yume-chan/android-bin';

import {exec, execFull} from './adb-exec';

export interface InstalledPackage {
  packageName: string;
  versionCode?: number;
  isSystem: boolean;
}

/**
 * Lists installed packages with version codes, flagging system apps.
 * Uses android-bin's structured `pm list packages` under the hood.
 */
export async function listInstalledPackages(
  device: Adb,
  thirdPartyOnly = false
): Promise<InstalledPackage[]> {
  const packageManager = new PackageManager(device);

  const thirdPartyNames = new Set<string>();
  if (!thirdPartyOnly) {
    for await (const pkg of packageManager.listPackages({listThirdParty: true})) {
      thirdPartyNames.add(pkg.packageName);
    }
  }

  const result: InstalledPackage[] = [];
  for await (const pkg of packageManager.listPackages({
    listThirdParty: thirdPartyOnly || undefined,
    showVersionCode: true,
  })) {
    result.push({
      packageName: pkg.packageName,
      versionCode: pkg.versionCode,
      isSystem: thirdPartyOnly ? false : !thirdPartyNames.has(pkg.packageName),
    });
  }
  return result.sort((a, b) => a.packageName.localeCompare(b.packageName));
}

/** Uninstalls an app. */
export async function uninstallApp(
  device: Adb,
  packageName: string,
  keepData = false
): Promise<string> {
  const flag = keepData ? '-k' : '';
  const result = await execFull(device, `pm uninstall ${flag} ${escapeArg(packageName)}`);

  if (result.stdout.includes('Success')) {
    return 'Uninstall successful';
  }
  throw new Error(result.stdout || result.stderr || 'Uninstall failed');
}

/** Launches an app by resolving and starting its main activity. */
export async function launchApp(device: Adb, packageName: string): Promise<void> {
  const result = await exec(
    device,
    `cmd package resolve-activity --brief ${escapeArg(packageName)} | tail -n 1`
  );

  const activity = result.trim();
  if (!activity || activity.includes('No activity')) {
    throw new Error(`No launchable activity found for ${packageName}`);
  }

  await exec(device, `am start -n ${escapeArg(activity)}`);
}

/** Force stops an app. */
export async function forceStopApp(device: Adb, packageName: string): Promise<void> {
  await exec(device, `am force-stop ${escapeArg(packageName)}`);
}

/** Clears app data. `pm clear` reports failure in its output, not its exit code. */
export async function clearAppData(device: Adb, packageName: string): Promise<void> {
  const result = await execFull(device, `pm clear ${escapeArg(packageName)}`);
  if (!result.stdout.includes('Success')) {
    throw new Error(
      result.stdout.trim() || result.stderr.trim() || `Failed to clear data for ${packageName}`
    );
  }
}
