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

import {z} from 'zod';
import type {MachineType} from './machine-type.model';

/**
 * Zod schema for machine type category - matches backend domain/machinetype.Category
 */
const MachineTypeCategorySchema = z.enum([
  'general_purpose',
  'compute_optimized',
  'memory_optimized',
  'accelerator_optimized',
  'storage_optimized',
  'cost_optimized',
]);

/**
 * Zod schema for CPU architecture - matches backend domain/machinetype.Architecture
 */
const MachineTypeArchitectureSchema = z.enum(['x86', 'arm']);

/**
 * Zod schema for a single machine type - matches backend MachineType entity
 */
export const MachineTypeSchema = z
  .object({
    id: z.string(),
    family: z.string(),
    name: z.string(),
    description: z.string().nullable().optional(),
    category: MachineTypeCategorySchema,
    architecture: MachineTypeArchitectureSchema,
    cpu_type: z.string().nullable().optional(),
    vcpus: z.number(),
    memory_gb: z.number(),
    fit: z.number().optional().default(0),
    fit_overridden: z.boolean().optional(),
    gpu_support: z.boolean(),
    max_gpus: z.number().optional().default(0),
    default_gpu_type: z.string().nullable().optional(),
    default_gpu_count: z.number().optional().default(0),
    gpu_mode: z.enum(['none', 'attachable', 'integrated']).optional().default('none'),
    integrated_gpu_type: z.string().nullable().optional(),
    integrated_gpu_count: z.number().optional().default(0),
    is_default: z.boolean(),
    is_enabled: z.boolean(),
    sort_order: z.number().optional().default(0),
    zones: z.array(z.string()).nullable().optional(),
    created_at: z.string().optional(),
    updated_at: z.string().optional(),
  })
  .transform(
    (data): MachineType => ({
      id: data.id,
      family: data.family,
      name: data.name,
      description: data.description ?? undefined,
      category: data.category,
      architecture: data.architecture,
      cpuType: data.cpu_type ?? undefined,
      vcpus: data.vcpus,
      memoryGb: data.memory_gb,
      fit: data.fit ?? 0,
      fitOverridden: data.fit_overridden ?? false,
      gpuSupport: data.gpu_support,
      maxGpus: data.max_gpus ?? 0,
      defaultGpuType: data.default_gpu_type ?? undefined,
      defaultGpuCount: data.default_gpu_count ?? 0,
      gpuMode: data.gpu_mode ?? 'none',
      integratedGpuType: data.integrated_gpu_type ?? undefined,
      integratedGpuCount: data.integrated_gpu_count ?? 0,
      isDefault: data.is_default,
      isEnabled: data.is_enabled,
      sortOrder: data.sort_order ?? 0,
      zones: data.zones ?? undefined,
      createdAt: data.created_at ? new Date(data.created_at) : undefined,
      updatedAt: data.updated_at ? new Date(data.updated_at) : undefined,
    })
  );

/**
 * Zod schema for list machine types response
 */
export const MachineTypeListResponseSchema = z.object({
  items: z
    .array(MachineTypeSchema)
    .nullable()
    .transform(items => items ?? []),
});
