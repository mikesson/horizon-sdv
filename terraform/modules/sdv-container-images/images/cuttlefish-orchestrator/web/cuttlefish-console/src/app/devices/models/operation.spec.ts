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

import {describe, expect, it} from 'vitest';
import type {Operation} from './operation.model';
import {operationFailureDetail, operationState, operationVerbLabel} from './operation.model';
import {OperationListSchema, OperationSchema} from './operation.schema';

const wireOperation = {
  name: 'operations/9f3c',
  done: false,
  metadata: {
    verb: 'device.powerwash',
    resource_type: 'device',
    resource_id: 'dev-1',
    create_time: '2026-07-26T10:00:00Z',
    attempt: 2,
    last_error: 'host unreachable',
    next_attempt_at: '2026-07-26T10:00:30Z',
  },
};

describe('OperationSchema', () => {
  it('transforms snake_case to camelCase and strips the collection prefix', () => {
    const op = OperationSchema.parse(wireOperation);

    expect(op.id).toBe('9f3c');
    expect(op.name).toBe('operations/9f3c');
    expect(op.metadata.resourceType).toBe('device');
    expect(op.metadata.resourceId).toBe('dev-1');
    expect(op.metadata.createTime).toBeInstanceOf(Date);
    expect(op.metadata.createTime.toISOString()).toBe('2026-07-26T10:00:00.000Z');
    expect(op.metadata.attempt).toBe(2);
    expect(op.metadata.lastError).toBe('host unreachable');
    expect(op.metadata.nextAttemptAt).toBeInstanceOf(Date);
  });

  it('keeps the response payload as an open record', () => {
    // The server serves one Operation type for every verb, so the wire types
    // `response` as an untyped object and the shape is selected by metadata.verb.
    // Validating a per-verb shape here would claim a guarantee the wire lacks.
    const op = OperationSchema.parse({
      ...wireOperation,
      done: true,
      response: {restarted_at: '2026-07-26T10:01:00Z'},
    });
    expect(op.response).toEqual({restarted_at: '2026-07-26T10:01:00Z'});
  });

  it('parses a terminal failure with its problem document', () => {
    const op = OperationSchema.parse({
      ...wireOperation,
      done: true,
      error: {title: 'Bad Gateway', status: 502, detail: 'cvd powerwash failed'},
    });
    expect(op.error?.status).toBe(502);
    expect(op.error?.detail).toBe('cvd powerwash failed');
  });

  it('rejects a malformed payload rather than yielding a half-built operation', () => {
    const result = OperationSchema.safeParse({name: 'operations/x', done: 'yes'});
    expect(result.success).toBe(false);
  });

  it('unwraps a list page to its operations', () => {
    const ops = OperationListSchema.parse({operations: [wireOperation], count: 1});
    expect(ops).toHaveLength(1);
    expect(ops[0].id).toBe('9f3c');
  });
});

describe('operationState', () => {
  // The console-side twin of the highest-consequence client bug. With only two
  // states, a settled FAILURE renders as a completed verb, and for restart and
  // powerwash the device row is identical either way, so nothing else on the page
  // would contradict it.
  it('distinguishes a settled failure from a settled success', () => {
    const base = OperationSchema.parse(wireOperation);

    const running: Operation = {...base, done: false, error: undefined};
    const done: Operation = {...base, done: true, error: undefined};
    const failed: Operation = {
      ...base,
      done: true,
      error: {title: 'Bad Gateway', status: 502, detail: 'boom'},
    };

    expect(operationState(running)).toBe('running');
    expect(operationState(done)).toBe('done');
    expect(operationState(failed)).toBe('failed');
    expect(operationState(done)).not.toBe(operationState(failed));
  });
});

describe('operationVerbLabel', () => {
  it('drops the resource prefix the API uses', () => {
    const op = OperationSchema.parse(wireOperation);
    expect(operationVerbLabel(op)).toBe('powerwash');
  });

  it('leaves an unprefixed verb alone', () => {
    const op = OperationSchema.parse({
      ...wireOperation,
      metadata: {...wireOperation.metadata, verb: 'powerwash'},
    });
    expect(operationVerbLabel(op)).toBe('powerwash');
  });
});

describe('operationFailureDetail', () => {
  it('prefers the problem detail', () => {
    const op = OperationSchema.parse({
      ...wireOperation,
      done: true,
      error: {title: 'Bad Gateway', status: 502, detail: 'cvd powerwash failed'},
    });
    expect(operationFailureDetail(op)).toBe('cvd powerwash failed');
  });

  it('falls back to the title when the problem carries no detail', () => {
    const op = OperationSchema.parse({
      ...wireOperation,
      done: true,
      error: {title: 'Bad Gateway', status: 502},
    });
    expect(operationFailureDetail(op)).toBe('Bad Gateway');
  });

  it('falls back to the last transient error while still running', () => {
    const op = OperationSchema.parse(wireOperation);
    expect(operationFailureDetail(op)).toBe('host unreachable');
  });
});
