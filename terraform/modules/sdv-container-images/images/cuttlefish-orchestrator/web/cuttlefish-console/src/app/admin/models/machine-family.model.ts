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

import type {GpuMode, MachineTypeCategory} from '../../machine-types/models';

/**
 * One shape Compute Engine offers under a family, from
 * GET /v2/admin/machine-families.
 *
 * Read-only by construction: discovery owns which shapes exist and how big they
 * are, so the only field here an operator can move is the exclusion.
 */
export interface FamilyShape {
  machineType: string;
  vcpus: number;
  memoryGb: number;
  /** What the shape's cores and memory hold, always the arithmetic. */
  fit: number;
  /**
   * Whether the catalog offers the shape: the arithmetic, or the stored
   * capacity where one outranks it.
   */
  viable: boolean;
  isExcluded: boolean;
  isDefault: boolean;
  /** The operator's stored per-shape capacity; 0 when the computed fit rules. */
  maxDevicesOverride: number;
}

/** One curated machine family, admitted or not. */
export interface AdminFamily {
  family: string;
  name: string;
  description?: string;
  category: MachineTypeCategory;
  architecture: 'x86' | 'arm';
  cpuType?: string;
  bootDiskType?: string;
  gpuMode: GpuMode;
  maxGpus: number;
  defaultGpuType?: string;
  defaultGpuCount: number;
  isEnabled: boolean;
  sortOrder: number;
  /** Shapes Compute Engine reports for the family. */
  shapeCount: number;
  /** Of those, the ones the catalog offers. */
  viableCount: number;
  shapes: FamilyShape[];
}

/** Curation patch for PATCH /v2/admin/machine-families/{family}. */
export interface MachineFamilyPatch {
  isEnabled?: boolean;
  sortOrder?: number;
  /** GPU policy; the server accepts it only where GPUs attach. */
  maxGpus?: number;
  defaultGpuType?: string;
  defaultGpuCount?: number;
}
