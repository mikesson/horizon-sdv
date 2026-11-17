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

import {instanceBuildRefs} from './instance-builds';

describe('instanceBuildRefs', () => {
  it('lists each instance that names its own build, by name', () => {
    expect(
      instanceBuildRefs({
        instances: [
          {name: 'core', '@build': 'b-core'},
          {name: 'ivi', disk: {default_build: 'gs://b/sdv_ivi_cf/T/img.zip'}},
          {name: 'rest'},
        ],
      })
    ).toEqual([
      {instance: 'core', kind: 'registered', value: 'b-core'},
      {instance: 'ivi', kind: 'url', value: 'gs://b/sdv_ivi_cf/T/img.zip'},
    ]);
  });

  it('names an unnamed instance by its position', () => {
    expect(instanceBuildRefs({instances: [{}, {'@build': 'b-2'}]})).toEqual([
      {instance: 'instance 2', kind: 'registered', value: 'b-2'},
    ]);
  });

  it('has nothing to list for a missing or malformed config', () => {
    expect(instanceBuildRefs(undefined)).toEqual([]);
    expect(instanceBuildRefs({instances: 'nope'})).toEqual([]);
    expect(instanceBuildRefs({instances: [null, 3, {'@build': ''}]})).toEqual([]);
  });
});
