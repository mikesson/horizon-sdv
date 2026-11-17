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
import {HostImageConfigSchema} from './host-image-config.schema';

const ROW = {
  machine_family: 'c4a',
  image_family: 'cco-host-arm64',
  enabled: true,
  created_at: '2026-10-18T00:00:00Z',
  updated_at: '2026-10-18T00:00:00Z',
};

describe('host image config schema', () => {
  it('carries a refusal through to the page', () => {
    const parsed = HostImageConfigSchema.parse({
      ...ROW,
      resolved: {
        ok: false,
        project: 'images',
        reason: 'arch_mismatch',
        message: 'machine family c4a cannot boot a host',
      },
    });

    expect(parsed.resolved?.ok).toBe(false);
    expect(parsed.resolved?.reason).toBe('arch_mismatch');
    expect(parsed.resolved?.project).toBe('images');
    expect(parsed.resolved?.message).toContain('c4a');
  });

  it('carries a resolved image through to the page', () => {
    const parsed = HostImageConfigSchema.parse({
      ...ROW,
      resolved: {
        ok: true,
        project: 'images',
        image_name: 'cco-host-arm64-20261018',
        self_link: 'https://www.googleapis.com/compute/v1/projects/images/global/images/x',
        architecture: 'arm',
      },
    });

    expect(parsed.resolved?.imageName).toBe('cco-host-arm64-20261018');
    expect(parsed.resolved?.selfLink).toContain('/global/images/');
    expect(parsed.resolved?.architecture).toBe('arm');
  });

  it('rejects a reason the server never sends', () => {
    const result = HostImageConfigSchema.safeParse({
      ...ROW,
      resolved: {ok: false, project: 'images', reason: 'fallback_used'},
    });

    expect(result.success).toBe(false);
  });
});
