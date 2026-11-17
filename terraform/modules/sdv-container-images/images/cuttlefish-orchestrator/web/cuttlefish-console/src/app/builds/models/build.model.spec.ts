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
import type {Build} from './build.model';
import {artifactRoleLabels, isTrackingBuild} from './build.model';

function build(overrides: Partial<Build>): Build {
  return {
    id: 'b1',
    name: 'test',
    source: 'android_build',
    createdAt: new Date(),
    updatedAt: new Date(),
    ...overrides,
  };
}

describe('isTrackingBuild', () => {
  it('is true for a branch-only Android CI registration', () => {
    expect(isTrackingBuild(build({branch: 'aosp-android-latest-release'}))).toBe(true);
  });

  it('is false when pinned to a build id', () => {
    expect(
      isTrackingBuild(build({branch: 'aosp-android-latest-release', buildId: '12345678'}))
    ).toBe(false);
  });

  it('is false for url-source builds regardless of branch', () => {
    expect(isTrackingBuild(build({source: 'url', branch: 'some-branch'}))).toBe(false);
  });

  it('is false without a branch', () => {
    expect(isTrackingBuild(build({}))).toBe(false);
  });
});

describe('artifactRoleLabels', () => {
  it('lists the composed roles in canonical order', () => {
    const b = build({
      source: 'url',
      artifactSources: [
        {type: 'gcs', url: 'gs://x/kernel', role: 'kernel_build'},
        {type: 'gcs', url: 'gs://x/host', role: 'host_package'},
      ],
    });

    expect(artifactRoleLabels(b)).toEqual(['Host Package', 'Kernel']);
  });

  it('is empty for a build with no artifact sources', () => {
    expect(artifactRoleLabels(build({source: 'android_build'}))).toEqual([]);
  });
});
