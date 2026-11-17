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
import {displayName, toAppInfo} from './app-row';

describe('displayName', () => {
  it('capitalizes the last dotted segment', () => {
    expect(displayName('com.example.myapp')).toBe('Myapp');
    expect(displayName('android')).toBe('Android');
  });
});

describe('toAppInfo', () => {
  it('derives the display name and maps isSystem to isSystemApp', () => {
    expect(toAppInfo({packageName: 'com.a.b', versionCode: 7, isSystem: true})).toEqual({
      packageName: 'com.a.b',
      displayName: 'B',
      versionCode: 7,
      isSystemApp: true,
    });
  });

  it('carries an undefined versionCode through', () => {
    expect(toAppInfo({packageName: 'x.y', isSystem: false}).versionCode).toBeUndefined();
  });
});
