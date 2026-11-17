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

/** Aggregated device counts, as produced by the DeviceStore `stats` selector. */
export interface DeviceStats {
  total: number;
  running: number;
  stopped: number;
  error: number;
  creating: number;
  pinned: number;
  ephemeral: number;
  expiringSoon: number;
}

/**
 * The KPI strip for the device list: always Total/Running/Stopped, plus
 * Pinned/Expiring/Creating/Error only when non-zero.
 */
export function buildDeviceKpis(stats: DeviceStats | null): KpiItem[] {
  if (!stats) {
    return [];
  }

  const items: KpiItem[] = [
    {
      label: 'Total Devices',
      value: stats.total,
      icon: 'phone_android',
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

  if (stats.pinned > 0) {
    items.push({
      label: 'Pinned',
      value: stats.pinned,
      icon: 'push_pin',
      iconColor: 'var(--cco-accent)',
    });
  }

  if (stats.expiringSoon > 0) {
    items.push({
      label: 'Expiring Soon',
      value: stats.expiringSoon,
      icon: 'schedule',
      iconColor: 'var(--cco-warning)',
      colorClass: 'warning',
    });
  }

  if (stats.creating > 0) {
    items.push({
      label: 'Creating',
      value: stats.creating,
      icon: 'hourglass_empty',
      iconColor: 'var(--cco-accent)',
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

  return items;
}
