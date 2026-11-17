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
import type {Operation} from './operation.model';

/** Strips the `operations/` collection prefix the name field carries. */
function bareId(name: string): string {
  return name.startsWith('operations/') ? name.slice('operations/'.length) : name;
}

/** Exported for the drift checker; the console reads it through OperationSchema. */
export const OperationProblemSchema = z.object({
  type: z.string().optional(),
  title: z.string(),
  status: z.number(),
  detail: z.string().optional(),
});

/** Exported for the drift checker; the console reads it through OperationSchema. */
export const OperationMetadataSchema = z.object({
  verb: z.string(),
  resource_type: z.string(),
  resource_id: z.string(),
  create_time: z.string(),
  instance_name: z.string().optional(),
  attempt: z.number().optional(),
  last_error: z.string().optional(),
  next_attempt_at: z.string().optional(),
});

/**
 * Zod schema for an operation handle (snake_case → camelCase).
 *
 * `response` is an open record. The server serves one Operation type for every
 * verb, so the OpenAPI schema types the field as an untyped object and the
 * shape is decided by `metadata.verb`. Validating a per-verb shape here would
 * claim a guarantee the wire does not carry.
 */
export const OperationSchema = z
  .object({
    name: z.string(),
    done: z.boolean(),
    metadata: OperationMetadataSchema,
    response: z.record(z.string(), z.unknown()).optional(),
    error: OperationProblemSchema.optional(),
  })
  .transform(
    (d): Operation => ({
      id: bareId(d.name),
      name: d.name,
      done: d.done,
      metadata: {
        verb: d.metadata.verb,
        resourceType: d.metadata.resource_type,
        resourceId: d.metadata.resource_id,
        createTime: new Date(d.metadata.create_time),
        instanceName: d.metadata.instance_name,
        attempt: d.metadata.attempt,
        lastError: d.metadata.last_error,
        nextAttemptAt: d.metadata.next_attempt_at
          ? new Date(d.metadata.next_attempt_at)
          : undefined,
      },
      response: d.response,
      error: d.error,
    })
  );

/** Zod schema for a page of operations. */
export const OperationListSchema = z
  .object({
    operations: z.array(OperationSchema),
    count: z.number(),
  })
  .transform(d => d.operations);
