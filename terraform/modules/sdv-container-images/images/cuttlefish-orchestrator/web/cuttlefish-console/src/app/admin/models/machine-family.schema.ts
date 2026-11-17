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
import type {AdminFamily, FamilyShape} from './machine-family.model';

/** One discovered shape in the curation view, matching backend FamilyShape. */
export const FamilyShapeSchema = z
  .object({
    machine_type: z.string(),
    vcpus: z.number(),
    memory_gb: z.number(),
    fit: z.number(),
    viable: z.boolean(),
    is_excluded: z.boolean(),
    is_default: z.boolean(),
    max_devices_override: z.number().optional(),
  })
  .transform(
    (data): FamilyShape => ({
      machineType: data.machine_type,
      vcpus: data.vcpus,
      memoryGb: data.memory_gb,
      fit: data.fit,
      viable: data.viable,
      isExcluded: data.is_excluded,
      isDefault: data.is_default,
      maxDevicesOverride: data.max_devices_override ?? 0,
    })
  );

/** One curated family, matching backend AdminFamily. */
export const AdminFamilySchema = z
  .object({
    family: z.string(),
    name: z.string(),
    description: z.string().nullable().optional(),
    category: z.enum([
      'general_purpose',
      'compute_optimized',
      'memory_optimized',
      'accelerator_optimized',
      'storage_optimized',
      'cost_optimized',
    ]),
    architecture: z.enum(['x86', 'arm']),
    cpu_type: z.string().nullable().optional(),
    boot_disk_type: z.string().nullable().optional(),
    gpu_mode: z.enum(['none', 'attachable', 'integrated']),
    max_gpus: z.number().optional().default(0),
    default_gpu_type: z.string().nullable().optional(),
    default_gpu_count: z.number().optional().default(0),
    is_enabled: z.boolean(),
    sort_order: z.number(),
    shape_count: z.number(),
    viable_count: z.number(),
    shapes: z
      .array(FamilyShapeSchema)
      .nullable()
      .transform(shapes => shapes ?? []),
  })
  .transform(
    (data): AdminFamily => ({
      family: data.family,
      name: data.name,
      description: data.description ?? undefined,
      category: data.category,
      architecture: data.architecture,
      cpuType: data.cpu_type ?? undefined,
      bootDiskType: data.boot_disk_type ?? undefined,
      gpuMode: data.gpu_mode,
      maxGpus: data.max_gpus ?? 0,
      defaultGpuType: data.default_gpu_type ?? undefined,
      defaultGpuCount: data.default_gpu_count ?? 0,
      isEnabled: data.is_enabled,
      sortOrder: data.sort_order,
      shapeCount: data.shape_count,
      viableCount: data.viable_count,
      shapes: data.shapes,
    })
  );

/** The curation family listing from GET /v2/admin/machine-families. */
export const AdminFamilyListSchema = z
  .object({
    families: z
      .array(AdminFamilySchema)
      .nullable()
      .transform(families => families ?? []),
  })
  .transform(data => data.families);
