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
import {browseStart} from './gcs-browse';

describe('browseStart', () => {
  it('opens the folder that lists a picked file', () => {
    expect(browseStart(['gs://b/sdv/core/sdv_core_cf-img-user.zip'])).toEqual({
      initialBucket: 'b',
      initialPath: 'sdv/core/',
    });
  });

  it('opens the parent of a picked folder, so the folder shows among its siblings', () => {
    expect(browseStart(['gs://b/sdv/core/'])).toEqual({initialBucket: 'b', initialPath: 'sdv/'});
    expect(browseStart(['gs://b/core/'])).toEqual({initialBucket: 'b', initialPath: ''});
  });

  it('falls through to the first value that is a gs:// URL', () => {
    expect(
      browseStart(['', 'b-core', 'https://example.com/img.zip', 'gs://other/ivi/img.zip'])
    ).toEqual({initialBucket: 'other', initialPath: 'ivi/'});
  });

  it('starts at the bucket list with no gs:// value', () => {
    expect(browseStart([])).toEqual({});
    expect(browseStart(['', '@ab/1/target', 'b-core'])).toEqual({});
  });
});
