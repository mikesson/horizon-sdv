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
import {parentPath, toPathParts} from './path-parts';

describe('toPathParts', () => {
  it('returns just the root for /', () => {
    expect(toPathParts('/')).toEqual([{name: '', path: '/'}]);
  });

  it('builds cumulative crumbs leading with the root', () => {
    expect(toPathParts('/sdcard/Download')).toEqual([
      {name: '', path: '/'},
      {name: 'sdcard', path: '/sdcard'},
      {name: 'Download', path: '/sdcard/Download'},
    ]);
  });

  it('ignores empty segments from doubled or trailing slashes', () => {
    expect(toPathParts('//sdcard//')).toEqual([
      {name: '', path: '/'},
      {name: 'sdcard', path: '/sdcard'},
    ]);
  });
});

describe('parentPath', () => {
  it('keeps root at root', () => {
    expect(parentPath('/')).toBe('/');
  });

  it('drops the last segment', () => {
    expect(parentPath('/sdcard/Download')).toBe('/sdcard');
  });

  it('returns root when the last segment is top-level', () => {
    expect(parentPath('/sdcard')).toBe('/');
  });
});
