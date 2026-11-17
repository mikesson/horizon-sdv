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
import {SNAPSHOT_STATE} from '../../shared/constants';
import type {Snapshot} from './snapshot.model';

/** Zod schema for one snapshot inventory entry (snake_case → camelCase). */
export const SnapshotSchema = z
  .object({
    id: z.string(),
    device_id: z.string(),
    host_id: z.string(),
    state: z.enum([SNAPSHOT_STATE.CREATING, SNAPSHOT_STATE.READY, SNAPSHOT_STATE.LOST]),
    created_by: z.string().optional(),
    created_at: z.string(),
    updated_at: z.string(),
  })
  .transform(
    (d): Snapshot => ({
      id: d.id,
      deviceId: d.device_id,
      hostId: d.host_id,
      state: d.state,
      createdBy: d.created_by,
      createdAt: new Date(d.created_at),
      updatedAt: new Date(d.updated_at),
    })
  );

/** Zod schema for a device's snapshot inventory, newest first. */
export const SnapshotListSchema = z
  .object({
    snapshots: z.array(SnapshotSchema),
  })
  .transform(d => d.snapshots);
