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
import type {AuditEntry, AuditPage} from './audit.model';

export const AuditEntrySchema = z
  .object({
    id: z.string(),
    timestamp: z.string(),
    actor_email: z.string().optional(),
    action: z.string(),
    resource_type: z.string(),
    resource_id: z.string().optional(),
    changes: z.record(z.string(), z.unknown()).optional(),
    metadata: z.record(z.string(), z.unknown()).optional(),
  })
  .transform(
    (data): AuditEntry => ({
      id: data.id,
      timestamp: new Date(data.timestamp),
      actorEmail: data.actor_email ?? '',
      action: data.action,
      resourceType: data.resource_type,
      resourceId: data.resource_id ?? '',
      changes: data.changes ?? {},
      metadata: data.metadata ?? {},
    })
  );

export const AuditPageSchema = z
  .object({
    items: z.array(AuditEntrySchema).nullable(),
    total: z.number(),
    limit: z.number(),
    offset: z.number(),
  })
  .transform(
    (data): AuditPage => ({
      items: data.items ?? [],
      total: data.total,
    })
  );
