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
import {buildDeviceKpis, type DeviceStats} from './device-kpis';

function makeStats(overrides: Partial<DeviceStats> = {}): DeviceStats {
  return {
    total: 0,
    running: 0,
    stopped: 0,
    error: 0,
    creating: 0,
    pinned: 0,
    ephemeral: 0,
    expiringSoon: 0,
    ...overrides,
  };
}

describe('buildDeviceKpis', () => {
  it('returns an empty list when stats is null', () => {
    expect(buildDeviceKpis(null)).toEqual([]);
  });

  it('always includes Total, Running, and Stopped', () => {
    const labels = buildDeviceKpis(makeStats({total: 3, running: 2, stopped: 1})).map(k => k.label);
    expect(labels).toEqual(['Total Devices', 'Running', 'Stopped']);
  });

  it('omits Pinned/Expiring/Creating/Error when zero', () => {
    const labels = buildDeviceKpis(makeStats()).map(k => k.label);
    expect(labels).not.toContain('Pinned');
    expect(labels).not.toContain('Expiring Soon');
    expect(labels).not.toContain('Creating');
    expect(labels).not.toContain('Error');
  });

  it('appends the optional tiles only when their count is positive', () => {
    const items = buildDeviceKpis(
      makeStats({total: 9, pinned: 2, expiringSoon: 1, creating: 3, error: 1})
    );
    const byLabel = new Map(items.map(k => [k.label, k.value]));
    expect(byLabel.get('Pinned')).toBe(2);
    expect(byLabel.get('Expiring Soon')).toBe(1);
    expect(byLabel.get('Creating')).toBe(3);
    expect(byLabel.get('Error')).toBe(1);
    expect(items.map(k => k.label)).toEqual([
      'Total Devices',
      'Running',
      'Stopped',
      'Pinned',
      'Expiring Soon',
      'Creating',
      'Error',
    ]);
  });
});
