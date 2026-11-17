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

import type {InstallState} from '../../services/adb/device-install.service';

/** Upload progress as a 0-100 integer; 0 before any bytes are known. */
export function installPercent(state: InstallState | null): number {
  const progress = state?.progress;
  if (!progress || progress.totalBytes === 0) {
    return 0;
  }
  return Math.min(100, Math.round((100 * progress.sentBytes) / progress.totalBytes));
}

/**
 * Human-readable status line: bytes transferred with throughput and ETA once
 * uploading, or a phase word (Preparing… / Installing…) before progress lands.
 */
export function installStatusLabel(state: InstallState | null): string {
  if (!state?.progress) {
    return state?.phase === 'committing' ? 'Installing…' : 'Preparing…';
  }
  const speed = state.bytesPerSecond > 0 ? ` · ${mb(state.bytesPerSecond)} MB/s` : '';
  const eta = state.etaSeconds && state.etaSeconds > 0 ? ` · ${state.etaSeconds}s left` : '';
  return `${mb(state.progress.sentBytes)} / ${mb(state.progress.totalBytes)} MB${speed}${eta}`;
}

function mb(bytes: number): string {
  return (bytes / (1024 * 1024)).toFixed(1);
}
