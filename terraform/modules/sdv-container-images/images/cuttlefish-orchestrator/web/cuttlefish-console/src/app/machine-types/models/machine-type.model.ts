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
 * Machine type category for workload classification (matches the backend
 * domain/machinetype.Category enum).
 */
export type MachineTypeCategory =
  | 'general_purpose'
  | 'compute_optimized'
  | 'memory_optimized'
  | 'accelerator_optimized'
  | 'storage_optimized'
  | 'cost_optimized';

/** CPU architecture. */
type MachineTypeArchitecture = 'x86' | 'arm';

/**
 * How a machine type exposes GPUs (matches backend domain/machinetype GPUMode).
 * - none: no GPU support (E2, C4A).
 * - attachable: GPUs are attached via guestAccelerators (N1); optional and
 *   caller-controlled (0 = CPU-only).
 * - integrated: the GPU is built into the machine type (G2/G4/A-series); fixed
 *   count, not separately attachable.
 */
export type GpuMode = 'none' | 'attachable' | 'integrated';

/** Represents a GCP Compute Engine machine type from the backend catalog. */
export interface MachineType {
  /** Machine type ID (e.g., "n1-standard-8"). */
  id: string;
  /** Machine family (e.g., "n1", "g2", "c4a"). */
  family: string;
  /** Display name. */
  name: string;
  /** Detailed description. */
  description?: string;
  /** Workload category. */
  category: MachineTypeCategory;
  /** CPU architecture. */
  architecture: MachineTypeArchitecture;
  /** CPU type description. */
  cpuType?: string;
  /** Number of vCPUs. */
  vcpus: number;
  /** Memory in GB. */
  memoryGb: number;
  /**
   * Default-shaped Android VMs a host of this shape holds. The server derives
   * it from the packing rule; the console never computes capacity itself.
   */
  fit: number;
  /** True when the fit is an operator's override rather than the arithmetic. */
  fitOverridden?: boolean;
  /** Whether this machine type has any GPU (attachable or integrated). */
  gpuSupport: boolean;
  /** Maximum number of GPUs (attach cap for N1; fixed count for integrated). */
  maxGpus: number;
  /** Recommended GPU type to attach when a GPU is requested (attachable only). */
  defaultGpuType?: string;
  /** Recommended attach count; 0 means CPU-only by default (GPUs are opt-in). */
  defaultGpuCount: number;
  /** How GPUs work with this machine type. */
  gpuMode: GpuMode;
  /** GPU type built into the machine (integrated mode only). */
  integratedGpuType?: string;
  /** Number of built-in GPUs (integrated mode only). */
  integratedGpuCount: number;
  /** Is this the default machine type for new hosts? */
  isDefault: boolean;
  /** Is this machine type available for use? */
  isEnabled: boolean;
  /** Display order in the UI. */
  sortOrder: number;
  /** Zones where available (undefined = all zones). */
  zones?: string[];
  /** Creation timestamp. */
  createdAt?: Date;
  /** Last update timestamp. */
  updatedAt?: Date;
}

/** Filter options for listing machine types. */
export interface MachineTypeFilter {
  family?: string;
  category?: MachineTypeCategory;
  architecture?: MachineTypeArchitecture;
  gpuSupport?: boolean;
  /** Tri-state on the wire: omitting it lists enabled and disabled alike. */
  enabled?: boolean;
}

/** A GPU that can be attached to an N1 machine type, with valid per-VM counts. */
export interface GpuOption {
  type: string;
  label: string;
  validCounts: number[];
}

/**
 * GPUs that ATTACH to N1 via guestAccelerators. Integrated accelerators (L4 on
 * G2, RTX PRO 6000 on G4, A100 on A2) are part of their machine type and are
 * rejected by GCE as N1 attachments, so they are excluded here.
 */
export const N1_ATTACHABLE_GPUS: GpuOption[] = [
  {type: 'nvidia-tesla-t4', label: 'NVIDIA T4', validCounts: [1, 2, 4]},
  {type: 'nvidia-tesla-v100', label: 'NVIDIA V100', validCounts: [1, 2, 4, 8]},
  {type: 'nvidia-tesla-p4', label: 'NVIDIA P4', validCounts: [1, 2, 4]},
  {type: 'nvidia-tesla-p100', label: 'NVIDIA P100', validCounts: [1, 2, 4]},
];

/** Valid attach counts for a GPU type, bounded by the machine's max GPUs. */
export function gpuValidCounts(gpuType: string, maxGpus: number): number[] {
  const counts = N1_ATTACHABLE_GPUS.find(g => g.type === gpuType)?.validCounts ?? [1, 2, 4];
  return maxGpus > 0 ? counts.filter(c => c <= maxGpus) : counts;
}

/** Output from the machine configuration component. */
export interface MachineConfigOutput {
  /** Selected machine type ID (e.g., 'n1-standard-8'). */
  machineType: string;
  /** GPU type to attach (attachable machines only; empty = none). */
  gpuType?: string;
  /** Number of GPUs to attach (attachable machines only; 0 = CPU-only). */
  gpuCount?: number;
}
