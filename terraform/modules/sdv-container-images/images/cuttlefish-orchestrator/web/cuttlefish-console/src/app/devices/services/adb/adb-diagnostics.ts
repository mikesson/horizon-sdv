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

/** QA diagnostics over a connected `Adb` device: bugreport, animation
 * scales, adbd root. */

import type {Adb} from '@yume-chan/adb';
import {BugReport, Settings} from '@yume-chan/android-bin';
import {exec} from './adb-exec';

export interface BugReportResult {
  blob: Blob;
  /** bugreportz produces a zip; the legacy fallback is a flat text file. */
  extension: 'zip' | 'txt';
}

/**
 * Captures a bugreport, preferring the zipped streaming variant and falling
 * back per device capabilities. Expect this to take a minute or more.
 */
export async function captureBugReport(
  device: Adb,
  onProgress?: (receivedBytes: number) => void
): Promise<BugReportResult> {
  const bugReport = await BugReport.queryCapabilities(device);
  const {type, stream} = bugReport.automatic();

  const reader = stream.getReader();
  const chunks: Uint8Array[] = [];
  let receivedBytes = 0;
  while (true) {
    const {done, value} = await reader.read();
    if (done) {
      break;
    }
    // Copy: ya-webadb chunks may be views into reused buffers.
    chunks.push(value.slice());
    receivedBytes += value.byteLength;
    onProgress?.(receivedBytes);
  }

  const zipped = type === 'bugreportz';
  return {
    blob: new Blob(chunks as BlobPart[], {type: zipped ? 'application/zip' : 'text/plain'}),
    extension: zipped ? 'zip' : 'txt',
  };
}

const ANIMATION_SCALE_KEYS = [
  'window_animation_scale',
  'transition_animation_scale',
  'animator_duration_scale',
];

/** Sets all three global animation scales (0 = disabled, 1 = default). */
export async function setAnimationsEnabled(device: Adb, enabled: boolean): Promise<void> {
  const settings = new Settings(device);
  const value = enabled ? '1' : '0';
  for (const key of ANIMATION_SCALE_KEYS) {
    await settings.put('global', key, value);
  }
}

/** True unless the window animation scale is zeroed. */
export async function areAnimationsEnabled(device: Adb): Promise<boolean> {
  const settings = new Settings(device);
  const value = await settings.get('global', 'window_animation_scale');
  const scale = Number.parseFloat(value.trim());
  return Number.isNaN(scale) || scale > 0;
}

/** True when adbd runs as root (`id -u` == 0). */
export async function isAdbRoot(device: Adb): Promise<boolean> {
  const output = await exec(device, 'id -u');
  return output.trim() === '0';
}

/**
 * Asks adbd to restart with or without root (userdebug builds only).
 *
 * adbd restarting kills the ADB transport AND the Cuttlefish proxy's socket
 * behind the adb data channel, which cannot recover, so the caller must
 * restart the device stream afterwards. The reply (or the transport dying
 * before it arrives) both mean the restart is underway.
 *
 * UNVERIFIED against a live device: whether the Cuttlefish proxy path
 * honors `root:` end-to-end still needs a real Cuttlefish host to confirm.
 */
export function restartAdbd(device: Adb, root: boolean): Promise<string> {
  return device.createSocketAndWait(root ? 'root:' : 'unroot:');
}
