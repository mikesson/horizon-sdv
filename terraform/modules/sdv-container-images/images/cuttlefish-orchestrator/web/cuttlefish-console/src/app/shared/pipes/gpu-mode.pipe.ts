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
 * GPU mode labels for display.
 * Maps internal GPU mode identifiers to human-readable names.
 */
const GPU_MODE_LABELS: Record<string, string> = {
  gfxstream: 'GFXStream',
  drm_virgl: 'VirGL',
  swiftshader: 'SwiftShader',
  auto: 'Auto',
};

/**
 * Pipe to format GPU mode identifiers for display.
 *
 * Usage:
 * ```html
 * {{ device.gpuMode | appGpuMode }}
 * <!-- Outputs: "GFXStream" for "gfxstream" -->
 * ```
 */
@Pipe({
  name: 'appGpuMode',
})
export class GpuModePipe implements PipeTransform {
  transform(mode: string | null | undefined): string {
    if (!mode) {
      return 'Unknown';
    }
    return GPU_MODE_LABELS[mode] ?? mode;
  }
}
