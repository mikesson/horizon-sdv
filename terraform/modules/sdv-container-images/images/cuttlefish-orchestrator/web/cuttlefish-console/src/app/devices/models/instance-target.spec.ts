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
import type {Device} from './device.model';
import {primaryInstance, targetKey, targetPath} from './instance-target';

describe('instance targets', () => {
  it('gives two instances of one device two distinct keys', () => {
    const phone = targetKey({deviceId: 'dev-1', instance: 'phone'});
    const watch = targetKey({deviceId: 'dev-1', instance: 'watch'});

    // Keying by device alone is what would hand the watch's stream and ADB
    // session to the phone's tile. That is one session with two consumers, and
    // whichever connected first wins.
    expect(phone).not.toBe(watch);
  });

  it('keeps one device apart from another with the same instance name', () => {
    // Instance names are unique within a device, never across the fleet: two
    // devices both calling their first instance 'phone' is the normal case.
    expect(targetKey({deviceId: 'dev-1', instance: 'phone'})).not.toBe(
      targetKey({deviceId: 'dev-2', instance: 'phone'})
    );
  });

  it('builds the instance-addressed route', () => {
    expect(targetPath({deviceId: 'dev-1', instance: 'watch'})).toBe(
      '/v2/devices/dev-1/instances/watch'
    );
  });

  it('escapes both segments', () => {
    expect(targetPath({deviceId: 'a/b', instance: 'c d'})).toBe(
      '/v2/devices/a%2Fb/instances/c%20d'
    );
  });

  it('takes the first instance as the one a device-scoped surface acts on', () => {
    const device: Partial<Device> = {
      id: 'dev-1',
      instances: [{name: 'ivi'}, {name: 'cluster'}],
    };

    expect(primaryInstance(device as Device)).toBe('ivi');
  });

  it('reports no instance for a device the host has not answered for', () => {
    const bare: Partial<Device> = {id: 'dev-1'};
    expect(primaryInstance(bare as Device)).toBe('');
  });
});
