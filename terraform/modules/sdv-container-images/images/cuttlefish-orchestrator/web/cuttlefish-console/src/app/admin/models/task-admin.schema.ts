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
import type {FailedTask, QueueStats} from './task-admin.model';

export const FailedTaskSchema = z
  .object({
    id: z.string(),
    type: z.string(),
    resource_id: z.string(),
    attempts: z.number(),
    max_attempts: z.number(),
    last_error: z.string(),
    updated_at: z.string(),
  })
  .transform(
    (data): FailedTask => ({
      id: data.id,
      type: data.type,
      resourceId: data.resource_id,
      attempts: data.attempts,
      maxAttempts: data.max_attempts,
      lastError: data.last_error,
      updatedAt: new Date(data.updated_at),
    })
  );

export const FailedTaskListSchema = z
  .object({
    tasks: z.array(FailedTaskSchema).nullable(),
  })
  .transform((data): FailedTask[] => data.tasks ?? []);

export const QueueStatsSchema = z
  .object({
    pending: z.number(),
    leased: z.number(),
    failed: z.number(),
    oldest_pending_age_seconds: z.number(),
  })
  .transform(
    (data): QueueStats => ({
      pending: data.pending,
      leased: data.leased,
      failed: data.failed,
      oldestPendingAgeSeconds: data.oldest_pending_age_seconds,
    })
  );
