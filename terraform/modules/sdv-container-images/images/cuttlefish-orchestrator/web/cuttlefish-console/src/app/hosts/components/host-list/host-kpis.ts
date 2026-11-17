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

import type {KpiItem} from '../../../shared/components';

/** Aggregated host counts, as produced by the HostStore `stats` selector. */
export interface HostStats {
  total: number;
  running: number;
  stopped: number;
  preempted: number;
  error: number;
  spotVMs: number;
  protected: number;
  totalDevices: number;
  maxDevices: number;
}

/**
 * The KPI strip for the host list: always Total/Running/Stopped and a device
 * capacity tile, plus Preempted/Error/Spot/Protected only when non-zero.
 */
export function buildHostKpis(stats: HostStats | null): KpiItem[] {
  if (!stats) {
    return [];
  }

  const items: KpiItem[] = [
    {
      label: 'Total Hosts',
      value: stats.total,
      icon: 'dns',
      iconColor: 'var(--cco-accent)',
    },
    {
      label: 'Running',
      value: stats.running,
      icon: 'play_circle',
      iconColor: 'var(--cco-success)',
      colorClass: 'success',
    },
    {
      label: 'Stopped',
      value: stats.stopped,
      icon: 'pause_circle',
      iconColor: 'var(--cco-warning)',
      colorClass: 'warning',
    },
  ];

  if (stats.preempted > 0) {
    items.push({
      label: 'Preempted',
      value: stats.preempted,
      icon: 'sync_problem',
      iconColor: 'var(--cco-warning)',
      colorClass: 'warning',
    });
  }

  if (stats.error > 0) {
    items.push({
      label: 'Error',
      value: stats.error,
      icon: 'error',
      iconColor: 'var(--cco-error)',
      colorClass: 'error',
    });
  }

  if (stats.spotVMs > 0) {
    items.push({
      label: 'Spot VMs',
      value: stats.spotVMs,
      icon: 'savings',
      iconColor: 'var(--cco-success)',
    });
  }

  if (stats.protected > 0) {
    items.push({
      label: 'Protected',
      value: stats.protected,
      icon: 'shield',
      iconColor: 'var(--cco-accent)',
    });
  }

  items.push({
    label: 'Devices Used',
    value: `${stats.totalDevices} / ${stats.maxDevices}`,
    icon: 'phone_android',
    iconColor: 'var(--cco-text-secondary)',
  });

  return items;
}
