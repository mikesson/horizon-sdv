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

import type {InstalledPackage} from '../../services/adb/adb-packages';

/** An installed package enriched with display fields for the app list. */
export interface AppInfo {
  packageName: string;
  displayName: string;
  versionCode?: number;
  isSystemApp: boolean;
}

export function toAppInfo(pkg: InstalledPackage): AppInfo {
  return {
    packageName: pkg.packageName,
    displayName: displayName(pkg.packageName),
    versionCode: pkg.versionCode,
    isSystemApp: pkg.isSystem,
  };
}

/** "com.example.myapp" -> "Myapp". */
export function displayName(packageName: string): string {
  const parts = packageName.split('.');
  const lastPart = parts[parts.length - 1];
  return lastPart.charAt(0).toUpperCase() + lastPart.slice(1);
}
