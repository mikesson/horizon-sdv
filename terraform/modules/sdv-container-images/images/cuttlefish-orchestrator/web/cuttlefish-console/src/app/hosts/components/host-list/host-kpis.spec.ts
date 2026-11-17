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
import {buildHostKpis, type HostStats} from './host-kpis';

function makeStats(overrides: Partial<HostStats> = {}): HostStats {
  return {
    total: 0,
    running: 0,
    stopped: 0,
    preempted: 0,
    error: 0,
    spotVMs: 0,
    protected: 0,
    totalDevices: 0,
    maxDevices: 0,
    ...overrides,
  };
}

describe('buildHostKpis', () => {
  it('returns an empty list when stats is null', () => {
    expect(buildHostKpis(null)).toEqual([]);
  });

  it('always includes Total, Running, Stopped, and a Devices Used capacity tile', () => {
    const labels = buildHostKpis(makeStats({total: 3, totalDevices: 2, maxDevices: 8})).map(
      k => k.label
    );
    expect(labels).toEqual(['Total Hosts', 'Running', 'Stopped', 'Devices Used']);
  });

  it('shows the device capacity as "used / max"', () => {
    const items = buildHostKpis(makeStats({totalDevices: 5, maxDevices: 12}));
    expect(items.find(k => k.label === 'Devices Used')?.value).toBe('5 / 12');
  });

  it('omits Preempted/Error/Spot/Protected when zero', () => {
    const labels = buildHostKpis(makeStats()).map(k => k.label);
    expect(labels).not.toContain('Preempted');
    expect(labels).not.toContain('Error');
    expect(labels).not.toContain('Spot VMs');
    expect(labels).not.toContain('Protected');
  });

  it('appends the optional tiles (before the capacity tile) only when positive', () => {
    const labels = buildHostKpis(
      makeStats({total: 9, preempted: 1, error: 2, spotVMs: 3, protected: 1})
    ).map(k => k.label);
    expect(labels).toEqual([
      'Total Hosts',
      'Running',
      'Stopped',
      'Preempted',
      'Error',
      'Spot VMs',
      'Protected',
      'Devices Used',
    ]);
  });
});
