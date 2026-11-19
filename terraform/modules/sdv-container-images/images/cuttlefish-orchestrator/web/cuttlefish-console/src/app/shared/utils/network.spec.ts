/**
 * Copyright 2026 Google LLC
 * SPDX-License-Identifier: Apache-2.0
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
import {isPrivateIPv4} from './network';

describe('isPrivateIPv4', () => {
  it('accepts valid RFC1918 addresses across 10/8, 172.16/12, and 192.168/16', () => {
    for (const ip of [
      '10.0.0.1',
      '10.255.255.254',
      '172.16.0.1',
      '172.31.255.1',
      '192.168.0.1',
      ' 10.1.2.3 ',
    ]) {
      expect(isPrivateIPv4(ip)).toBe(true);
    }
  });

  it('rejects public IPs and out-of-range 172/8 neighbors', () => {
    for (const ip of ['34.90.1.2', '172.15.255.1', '172.32.0.1', '192.169.0.1', '11.0.0.1']) {
      expect(isPrivateIPv4(ip)).toBe(false);
    }
  });

  it('rejects malformed or out-of-range octets even under 10.* or 192.168.*', () => {
    for (const ip of [
      '',
      'Loading...',
      '10.0.1',
      '10.0.0.1.2',
      '10.999.0.1',
      '192.168.1',
      '192.168.256.1',
      '172.16.0.abc',
    ]) {
      expect(isPrivateIPv4(ip)).toBe(false);
    }
  });
});
