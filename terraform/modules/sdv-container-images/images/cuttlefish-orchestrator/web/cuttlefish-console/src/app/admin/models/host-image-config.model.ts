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
 * Host image configuration mapping a machine family to a GCE image family.
 * Used by platform administrators to configure which images are used for
 * different machine type prefixes when provisioning host VMs.
 */
export interface HostImageConfig {
  id: string;
  /** Machine family identifier (e.g., "n1", "g2", "c4a") - primary key */
  machineFamily: string;
  /** GCE image family name (e.g., "cco-host") */
  imageFamily: string;
  sourceProject?: string;
  description?: string;
  enabled: boolean;
  createdAt: Date;
  updatedAt: Date;
  createdBy?: string;
  updatedBy?: string;
  /** The image this row boots now, or why it boots nothing. */
  resolved?: HostImageStatus;
}

/** Why a machine family cannot boot a host. Mirrors the server's enum. */
export const HOST_IMAGE_REASONS = [
  'not_found',
  'permission_denied',
  'arch_mismatch',
  'no_config',
] as const;

type HostImageReason = (typeof HOST_IMAGE_REASONS)[number];

/**
 * The server's resolution of one row: the image family looked up in the row's
 * source project, else the deployment's CCO_HOST_IMAGE_PROJECT.
 */
export interface HostImageStatus {
  ok: boolean;
  /** The project the image family resolved in. */
  project: string;
  imageName?: string;
  selfLink?: string;
  /** 'x86' or 'arm'; absent when the image declares none. */
  architecture?: string;
  reason?: HostImageReason;
  /** The full refusal, naming the setting that fixes it. */
  message?: string;
}

/**
 * Response from list host image configs endpoint.
 */
export interface HostImageConfigListResponse {
  items: HostImageConfig[];
  limit: number;
  offset: number;
  total: number;
}

/**
 * Request to create a host image configuration.
 */
export interface CreateHostImageConfigRequest {
  machineFamily: string;
  imageFamily: string;
  sourceProject?: string;
  description?: string;
  enabled?: boolean;
}

/**
 * Request to update a host image configuration.
 */
export interface UpdateHostImageConfigRequest {
  imageFamily: string;
  sourceProject?: string;
  description?: string;
  enabled?: boolean;
}

/**
 * Machine family category for UI grouping.
 */
export type MachineFamilyCategory =
  | 'general_purpose'
  | 'compute_optimized'
  | 'memory_optimized'
  | 'gpus'
  | 'arm';

/**
 * Get the category for a machine family.
 */
export function getMachineFamilyCategory(family: string): MachineFamilyCategory {
  const gpuFamilies = ['g2', 'g4', 'a2', 'a3'];
  const computeFamilies = ['c2', 'c3', 'c3d', 'c4'];
  const memoryFamilies = ['m1', 'm2', 'm3'];
  const armFamilies = ['c4a', 't2a'];

  if (gpuFamilies.includes(family)) {
    return 'gpus';
  }
  if (computeFamilies.includes(family)) {
    return 'compute_optimized';
  }
  if (memoryFamilies.includes(family)) {
    return 'memory_optimized';
  }
  if (armFamilies.includes(family)) {
    return 'arm';
  }
  return 'general_purpose';
}

/**
 * Get display label for a category.
 */
export function getCategoryLabel(category: MachineFamilyCategory): string {
  const labels: Record<MachineFamilyCategory, string> = {
    general_purpose: 'General Purpose',
    compute_optimized: 'Compute Optimized',
    memory_optimized: 'Memory Optimized',
    gpus: 'GPU Accelerated',
    arm: 'ARM',
  };
  return labels[category];
}

/**
 * Get display name for a machine family.
 */
export function getMachineFamilyDisplayName(family: string): string {
  const names: Record<string, string> = {
    n1: 'N1 (Intel Skylake/Haswell)',
    n2: 'N2 (Intel Cascade Lake)',
    n2d: 'N2D (AMD EPYC)',
    n4: 'N4 (Intel Emerald Rapids)',
    e2: 'E2 (Cost-Optimized)',
    c2: 'C2 (Intel Cascade Lake)',
    c3: 'C3 (Intel Sapphire Rapids)',
    c3d: 'C3D (AMD EPYC Genoa)',
    c4: 'C4 (Intel Emerald Rapids)',
    c4a: 'C4A (Google Axion ARM)',
    g2: 'G2 (NVIDIA L4)',
    g4: 'G4 (NVIDIA RTX PRO 6000)',
    a2: 'A2 (NVIDIA A100)',
    a3: 'A3 (NVIDIA H100)',
    m1: 'M1 (Memory Optimized)',
    m2: 'M2 (Ultra Memory)',
    m3: 'M3 (Memory Optimized)',
    t2d: 'T2D (AMD EPYC Scale-Out)',
  };
  return names[family] ?? family.toUpperCase();
}
