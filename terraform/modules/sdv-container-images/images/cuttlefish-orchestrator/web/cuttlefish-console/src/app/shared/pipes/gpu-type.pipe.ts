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

import type {PipeTransform} from '@angular/core';
import {Pipe} from '@angular/core';

/**
 * Pipe to format GPU type identifiers for display.
 * Converts GCE GPU type names (e.g., "nvidia-tesla-t4") to short labels (e.g., "T4").
 *
 * Usage:
 * ```html
 * {{ host.gpuType | appGpuType }}
 * <!-- Outputs: "T4" for "nvidia-tesla-t4" -->
 * ```
 */
const GPU_LABELS: Record<string, string> = {
  'nvidia-tesla-t4': 'T4',
  'nvidia-tesla-v100': 'V100',
  'nvidia-tesla-p4': 'P4',
  'nvidia-tesla-p100': 'P100',
  'nvidia-l4': 'L4',
  'nvidia-rtx-pro-6000': 'RTX PRO 6000',
  'nvidia-a100-40gb': 'A100 40GB',
  'nvidia-a100-80gb': 'A100 80GB',
  'nvidia-h100-80gb': 'H100 80GB',
};

@Pipe({
  name: 'appGpuType',
})
export class GpuTypePipe implements PipeTransform {
  transform(gpuType: string | null | undefined): string {
    if (!gpuType) {
      return 'Unknown';
    }
    // Known types get a curated label; unknown ones fall back to the trailing
    // model token (e.g. nvidia-tesla-t4 -> T4).
    return (
      GPU_LABELS[gpuType] ?? /(?:tesla-)?([a-z0-9]+)$/i.exec(gpuType)?.[1]?.toUpperCase() ?? gpuType
    );
  }
}
