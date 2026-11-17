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

import type {Device} from './device.model';
import {instanceReading} from './instance-state';
import {slotsLabel} from './slot-space';

/**
 * Loudest first.
 *
 * The mark table names the states; it does not rank them, because a tile draws
 * one state and never has to choose. A lane header does: it names the worst
 * thing happening inside a group, and putting `1 absent` after `1 stopped`
 * would bury the one that cannot be fixed by a restart.
 */
const EXCEPTION_ORDER = [
  'absent',
  'failed',
  'unreachable',
  'stopped',
  'stopping',
  'starting',
  'cancelled',
  'no data',
  'unspecified',
];

/** The state a healthy instance is in; everything else is worth naming. */
const RUNNING_WORD = 'running';

/** What a lane header renders, with its two colours kept apart on purpose. */
export interface LaneSummary {
  /** The device's own status, as a word. */
  readonly statusWord: string;
  readonly statusColor: string;
  /** `1 unreachable`, `1 absent, 1 stopped`, or empty when nothing is wrong. */
  readonly exception: string;
  readonly exceptionColor: string;
  /** `slots 02, 05, 08`, what a collapsed lane carries instead of a count. */
  readonly slots: string;
}

/**
 * A device's own severity.
 *
 * Read off `reconciling`, the output-only field the backend already computes,
 * so the console reimplements no state machine: everything mid-transition
 * shares one treatment, a settled `running` is unstyled, and only the two
 * settled bad outcomes carry a loud token.
 */
function deviceColor(device: Device): string {
  if (device.reconciling) {
    return 'var(--cco-info)';
  }
  switch (device.status) {
    case 'error':
      return 'var(--cco-error)';
    case 'stopped':
      return 'var(--cco-warning)';
    default:
      return 'var(--cco-text)';
  }
}

/**
 * The lane header for one device.
 *
 * Names the exception rather than reducing the group to a fraction or a dot: a
 * fraction misreports normal progress, since `2 of 3 running` looks like a
 * shortfall when the third is starting, and a single rolled-up mark averages
 * the anomaly away entirely. No exception, no clause. The good case is the
 * device word alone, so there is no rollup phrase to invent and no rollup word
 * like `up` that names no state in the table.
 *
 * The two colours come from independent sources on purpose. The device word
 * takes its own severity and the exception takes the worst instance state it
 * names, so `running · 1 unreachable` renders as a calm word beside a loud
 * one, which is exactly the situation it describes.
 */
export function laneSummary(device: Device): LaneSummary {
  const instances = device.instances ?? [];

  const counts = new Map<string, number>();
  let worstColor = '';
  let worstRank = EXCEPTION_ORDER.length;

  for (const instance of instances) {
    const reading = instanceReading(instance.state);
    if (reading.word === RUNNING_WORD) {
      continue;
    }
    counts.set(reading.word, (counts.get(reading.word) ?? 0) + 1);
    const rank = EXCEPTION_ORDER.indexOf(reading.word);
    if (rank >= 0 && rank < worstRank) {
      worstRank = rank;
      worstColor = reading.color;
    }
  }

  const exception = [...counts.entries()]
    .sort(([a], [b]) => EXCEPTION_ORDER.indexOf(a) - EXCEPTION_ORDER.indexOf(b))
    .map(([word, count]) => `${count} ${word}`)
    .join(', ');

  return {
    statusWord: device.status,
    statusColor: deviceColor(device),
    exception,
    exceptionColor: worstColor,
    slots: slotsLabel(instances.map(instance => instance.slot ?? 0)),
  };
}
