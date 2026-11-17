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
 * A long-running device verb's outcome, derived from `done` plus the presence of
 * an error.
 *
 * Three states rather than two. A restart or powerwash leaves the device row
 * identical before and after, so `done` alone cannot distinguish "it worked" from
 * "it failed", and those are exactly the two answers a user is waiting for.
 */
export type OperationState = 'running' | 'done' | 'failed';

/** The RFC 9457 problem document a failed verb records. */
interface OperationProblem {
  readonly type?: string;
  readonly title: string;
  readonly status: number;
  readonly detail?: string;
}

/** Progress, derived server-side from the live task row rather than stored. */
interface OperationMetadata {
  readonly verb: string;
  readonly resourceType: string;
  readonly resourceId: string;
  readonly createTime: Date;
  /** The instance the verb acted on; absent for a device-wide verb. */
  readonly instanceName?: string;
  /** Delivery count so far; absent once the task is gone. */
  readonly attempt?: number;
  /** Most recent transient failure, or a dead-lettered task's final cause. */
  readonly lastError?: string;
  readonly nextAttemptAt?: Date;
}

/** A durable handle for a long-running device verb. */
export interface Operation {
  /** Bare id, with the `operations/` collection prefix stripped. */
  readonly id: string;
  /** The relative resource name as served, e.g. `operations/9f3c…`. */
  readonly name: string;
  readonly done: boolean;
  readonly metadata: OperationMetadata;
  readonly response?: Record<string, unknown>;
  readonly error?: OperationProblem;
}

/** Resolves an operation to the three states the UI distinguishes. */
export function operationState(op: Operation): OperationState {
  if (!op.done) {
    return 'running';
  }
  return op.error ? 'failed' : 'done';
}

/** The verb's short name, without the `device.` prefix the API uses. */
export function operationVerbLabel(op: Operation): string {
  const verb = op.metadata.verb;
  const dot = verb.indexOf('.');
  return dot >= 0 ? verb.slice(dot + 1) : verb;
}

/**
 * What to show a user about a failed operation, preferring the stored problem's
 * detail and falling back to the last transient error.
 */
export function operationFailureDetail(op: Operation): string {
  if (op.error) {
    return op.error.detail || op.error.title;
  }
  return op.metadata.lastError ?? '';
}
