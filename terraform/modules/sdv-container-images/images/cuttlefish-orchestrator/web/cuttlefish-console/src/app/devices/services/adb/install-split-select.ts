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
 * Device-aware split selection for full APK-set installs, mirroring what
 * `bundletool install-apks` does: send only the base plus the config splits
 * that match the device's ABI and screen density, not every split in the
 * archive. A `MISSING_SPLIT` failure means the heuristic was wrong, and the
 * caller falls back to installing all splits.
 */

import type {Adb} from '@yume-chan/adb';
import {getProp} from './adb-props';
import type {InstallFile} from './install-plan';

export interface DeviceProfile {
  /** Preferred first. */
  abis: string[];
  densityBucket: string;
}

const DENSITY_BUCKETS: Array<{name: string; dpi: number}> = [
  {name: 'ldpi', dpi: 120},
  {name: 'mdpi', dpi: 160},
  {name: 'tvdpi', dpi: 213},
  {name: 'hdpi', dpi: 240},
  {name: 'xhdpi', dpi: 320},
  {name: 'xxhdpi', dpi: 480},
  {name: 'xxxhdpi', dpi: 640},
];

const ALL_ABI_TOKENS = ['arm64_v8a', 'armeabi_v7a', 'armeabi', 'x86_64', 'x86', 'mips'];
const ALL_DENSITY_TOKENS = DENSITY_BUCKETS.map(b => b.name);

export async function readDeviceProfile(device: Adb): Promise<DeviceProfile> {
  const abiList = (await getProp(device, 'ro.product.cpu.abilist')).trim();
  const abis = abiList
    ? abiList.split(',').map(abi => abi.trim().replace(/-/g, '_'))
    : [(await getProp(device, 'ro.product.cpu.abi')).trim().replace(/-/g, '_')];

  const dpi = Number.parseInt((await getProp(device, 'ro.sf.lcd_density')).trim(), 10);
  return {abis: abis.filter(Boolean), densityBucket: nearestDensityBucket(dpi)};
}

function nearestDensityBucket(dpi: number): string {
  if (Number.isNaN(dpi)) {
    return 'xxhdpi';
  }
  let best = DENSITY_BUCKETS[0];
  for (const bucket of DENSITY_BUCKETS) {
    if (Math.abs(bucket.dpi - dpi) < Math.abs(best.dpi - dpi)) {
      best = bucket;
    }
  }
  return best.name;
}

/**
 * Filters an app's files to those the device needs: the base, all non-config
 * splits (feature/language modules stay, since dropping a language split only
 * loses a locale), the best-matching ABI split, and the best-matching
 * density split. When nothing looks like a config split, returns the input
 * unchanged.
 */
export function selectSplitsForDevice(
  files: readonly InstallFile[],
  profile: DeviceProfile
): InstallFile[] {
  const hasConfigSplits = files.some(f => splitKind(f) !== 'other');
  if (!hasConfigSplits) {
    return [...files];
  }

  const abiChoice = pickByPreference(files, 'abi', profile.abis);
  const densityChoice = pickByPreference(files, 'density', [profile.densityBucket]);

  return files.filter(file => {
    switch (splitKind(file)) {
      case 'abi':
        return file === abiChoice;
      case 'density':
        return file === densityChoice;
      default:
        // Base, language, and feature splits are always kept.
        return true;
    }
  });
}

type SplitKind = 'abi' | 'density' | 'other';

// Longest first, so a short token can never shadow a longer one that
// contains it (`x86` vs `x86_64`, `hdpi` vs `xxhdpi`).
const ABI_TOKENS_LONGEST_FIRST = [...ALL_ABI_TOKENS].sort((a, b) => b.length - a.length);
const DENSITY_TOKENS_LONGEST_FIRST = [...ALL_DENSITY_TOKENS].sort((a, b) => b.length - a.length);

/** Dashes and underscores compare as equal (`arm64-v8a` == `arm64_v8a`). */
function normalizeToken(value: string): string {
  return value.toLowerCase().replace(/-/g, '_');
}

/**
 * The name to classify a file by: the parsed split name when the manifest
 * was readable (a parsed base is never a config split), the file name only
 * as a fallback for unreadable manifests.
 */
function splitKey(file: InstallFile): string | null {
  if (file.info) {
    return file.info.split ? normalizeToken(file.info.split) : null;
  }
  return normalizeToken(file.name);
}

function findToken(key: string | null, vocabulary: readonly string[]): string | null {
  if (key === null) {
    return null;
  }
  return vocabulary.find(token => key.includes(token)) ?? null;
}

function abiToken(file: InstallFile): string | null {
  return findToken(splitKey(file), ABI_TOKENS_LONGEST_FIRST);
}

function densityToken(file: InstallFile): string | null {
  return findToken(splitKey(file), DENSITY_TOKENS_LONGEST_FIRST);
}

function splitKind(file: InstallFile): SplitKind {
  if (abiToken(file)) {
    return 'abi';
  }
  if (densityToken(file)) {
    return 'density';
  }
  return 'other';
}

function pickByPreference(
  files: readonly InstallFile[],
  kind: SplitKind,
  preferences: string[]
): InstallFile | undefined {
  const tokenOf = kind === 'abi' ? abiToken : densityToken;
  const candidates = files.filter(f => splitKind(f) === kind);
  for (const preference of preferences) {
    const wanted = normalizeToken(preference);
    const match = candidates.find(f => tokenOf(f) === wanted);
    if (match) {
      return match;
    }
  }
  return candidates[0];
}
