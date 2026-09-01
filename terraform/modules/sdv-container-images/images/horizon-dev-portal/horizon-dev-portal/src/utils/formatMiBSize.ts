// Copyright (c) 2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

const BYTES_PER_MIB = 1048576;
const BYTES_PER_GB = 1_000_000_000;

export function mibToBytes(mib: number): number {
  return Math.round(mib * BYTES_PER_MIB);
}

export function mibToDecimalGB(mib: number): number {
  return mibToBytes(mib) / BYTES_PER_GB;
}

/** Compact GB label for in-field adornment; null when not a positive MiB value. */
export function formatMiBToDecimalGBAdornment(raw: string): string | null {
  const trimmed = raw.trim();
  if (!/^[0-9]+(\.[0-9]+)?$/.test(trimmed)) {
    return null;
  }
  const mib = Number(trimmed);
  if (!Number.isFinite(mib) || mib <= 0) {
    return null;
  }
  return `= ${mibToDecimalGB(mib).toFixed(3)} GB`;
}
