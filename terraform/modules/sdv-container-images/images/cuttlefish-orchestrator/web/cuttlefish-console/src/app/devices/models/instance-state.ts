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

import type {DeviceInstance} from './device.model';

/**
 * The CVD wire state that means an Android VM is up.
 *
 * The backend's own fold lets a group read as running only when every instance
 * is in this state, so counting it here answers the same question the server
 * answered, and not a second, looser one.
 */
const RUNNING = 'Running';

/**
 * The shapes a status mark draws.
 *
 * Fill tracks liveness, so the circle family reads as a gradient from solid to
 * gone. A value that is not a state the instance is in leaves the family
 * altogether. Absent draws a square, because the instance is not there at all
 * rather than there and unwell.
 */
type InstanceMark =
  | 'disc'
  | 'disc-half'
  | 'ring-half'
  | 'ring'
  | 'ring-broken'
  | 'square-dashed'
  | 'triangle'
  | 'cross'
  | 'dash';

/** How an instance's raw wire state should read: a mark, a word and a colour. */
interface InstanceReading {
  readonly mark: InstanceMark;
  /**
   * The canonical name for the state, never abbreviated. An abbreviation
   * invented to fit a narrow tile becomes a second name for the state, and then
   * every other surface has to choose between the two.
   */
  readonly word: string;
  /** A read of the console's own status tokens, so one mark serves both themes. */
  readonly color: string;
}

/**
 * Groups the CVD wire states into what a tile can draw.
 *
 * Absent is not no-data. An instance missing from a fleet listing that did
 * answer routes to a recreate, so it reads as a fault; `Unknown`, which the
 * host client synthesizes when it could not read fleet status at all, carries
 * no evidence either way and declines instead. Giving a missing response the
 * loudest mark on the page trains people to ignore the mark. Absent and
 * Unreachable stay different shapes for the same reason: one routes to a
 * recreate and the other to the backend's cheap restart rung, which fires on
 * exactly Unreachable.
 *
 * Stopping and Stopped share the circle family and separate by fill, colour and
 * word rather than by shape alone. They are the same kind of event in opposite
 * directions, and the word is what says which.
 *
 * An unrecognized state reads as Unspecified, never as running: upstream has
 * grown this enum before, and a new state reading as healthy is how a dead
 * sibling hides. It is also where the backend's own precedence ranks a state
 * its table does not name.
 */
export function instanceReading(state: string | undefined): InstanceReading {
  switch (state) {
    case RUNNING:
      return {mark: 'disc', word: 'running', color: 'var(--cco-success)'};
    case 'Preparing':
    case 'Starting':
      return {mark: 'disc-half', word: 'starting', color: 'var(--cco-info)'};
    case 'Stopping':
      return {mark: 'ring-half', word: 'stopping', color: 'var(--cco-warning)'};
    case 'Stopped':
      return {mark: 'ring', word: 'stopped', color: 'var(--cco-warning)'};
    case 'Unreachable':
      return {mark: 'ring-broken', word: 'unreachable', color: 'var(--cco-error)'};
    // The backend omits an empty state, so an absent instance reaches the
    // console as undefined; '' is the same fact sent literally.
    case undefined:
    case '':
      return {mark: 'square-dashed', word: 'absent', color: 'var(--cco-error)'};
    // Which of the two failed is in the operation's error, not on the tile: both
    // route to the same recreate, and a tile wide enough for "prepare failed"
    // would be a tile that abbreviates.
    case 'Prepare failed':
    case 'Boot failed':
      return {mark: 'triangle', word: 'failed', color: 'var(--cco-error)'};
    // A cancelled start is terminal but was never a fault, so it declines
    // alongside Unknown rather than accusing the instance.
    case 'Cancelled':
      return {mark: 'cross', word: 'cancelled', color: 'var(--cco-text-disabled)'};
    case 'Unknown':
      return {mark: 'dash', word: 'no data', color: 'var(--cco-text-disabled)'};
    case 'Unspecified':
      return {mark: 'dash', word: 'unspecified', color: 'var(--cco-text-disabled)'};
    default:
      return {mark: 'dash', word: 'unspecified', color: 'var(--cco-text-disabled)'};
  }
}

/** How many of a device's instances the host reported as up. */
export function healthyInstanceCount(instances: readonly DeviceInstance[]): number {
  return instances.filter(i => i.state === RUNNING).length;
}

/** The pixel dimensions of an instance's first display. */
interface DisplayGeometry {
  readonly width: number;
  readonly height: number;
}

/** Leading "720 x 1280" of the host's rendering; the density tail is ignored. */
const GEOMETRY = /^\s*(\d+)\s*x\s*(\d+)/;

/**
 * An instance's true proportions, parsed from the host's own display string.
 *
 * Drawn at these proportions, a group looks like what it is. A watch is square
 * beside a phone and a head unit is wide beside a cluster, and that mix is why
 * a group exists. Undefined when the host has reported no display: a
 * placeholder shape would be a guess, and a guessed silhouette is worse than
 * an obviously absent one.
 */
export function displayGeometry(instance: DeviceInstance): DisplayGeometry | undefined {
  const match = GEOMETRY.exec(instance.displays?.[0] ?? '');
  if (!match) {
    return undefined;
  }
  const width = Number(match[1]);
  const height = Number(match[2]);
  return width > 0 && height > 0 ? {width, height} : undefined;
}
