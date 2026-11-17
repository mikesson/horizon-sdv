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
import type {User} from '../../auth/models/user.model';
import {foreignCreatorWarning} from './creator';

const me: User = {email: 'me@example.com', displayName: 'Me'};

describe('foreignCreatorWarning', () => {
  it('warns for a foreign creator', () => {
    expect(foreignCreatorWarning('other@example.com', me)).toBe(
      'Created by other@example.com, another user.'
    );
  });

  it('is silent for own resources', () => {
    expect(foreignCreatorWarning('me@example.com', me)).toBeUndefined();
  });

  it('is silent when the creator is unknown', () => {
    expect(foreignCreatorWarning(undefined, me)).toBeUndefined();
    expect(foreignCreatorWarning('', me)).toBeUndefined();
    expect(foreignCreatorWarning('unknown', me)).toBeUndefined();
  });

  it('is silent when the user is not loaded', () => {
    expect(foreignCreatorWarning('other@example.com', null)).toBeUndefined();
  });
});
