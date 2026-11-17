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
import type {Resolution} from './requirements.model';

/**
 * Zod schema for the declarative-create resolution block, shared by the device
 * and batch plans. The backend returns the same object for both.
 *
 * VALIDATION: Pins the action enums the Plan steps branch on.
 * TRANSFORMATION: snake_case → camelCase; warnings default to [].
 */
export const ResolutionSchema = z
  .object({
    build_id: z.string().optional(),
    build_name: z.string().optional(),
    build_target: z.string().optional(),
    build_action: z.enum(['use', 'register']),
    host_id: z.string().optional(),
    host_name: z.string().optional(),
    host_action: z.enum(['place', 'provision']),
    pool: z.string().optional(),
    machine_type: z.string().optional(),
    zone: z.string().optional(),
    capacity_model: z.string().optional(),
    placement_reasons: z.array(z.string()).optional(),
    warnings: z.array(z.string()).optional(),
  })
  .transform(
    (data): Resolution => ({
      buildId: data.build_id,
      buildName: data.build_name,
      buildTarget: data.build_target,
      buildAction: data.build_action,
      hostId: data.host_id,
      hostName: data.host_name,
      hostAction: data.host_action,
      pool: data.pool,
      machineType: data.machine_type,
      zone: data.zone,
      capacityModel: data.capacity_model,
      placementReasons: data.placement_reasons ?? [],
      warnings: data.warnings ?? [],
    })
  );
