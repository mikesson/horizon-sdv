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

/**
 * A host's instance-slot space: which slots exist, which are taken, and which
 * the host promised but its image cannot back.
 *
 * cvd allocates instance ids host-globally, lowest-free-first, one at a time,
 * so a group placed on a churned host holds non-contiguous slots. Holding 2, 5
 * and 8 for three instances is ordinary. Every function here works from the
 * slot each instance reported and never from its position in an array.
 */

/** What a single notch on the ruler is standing on. */
type NotchKind =
  /** Taken by the device being viewed. */
  | 'own'
  /** Taken by some other device on the host; named only at host scope. */
  | 'foreign'
  /** Backed by a TAP set and unoccupied. */
  | 'free'
  /** Inside the host's promised range but past what the image backs. */
  | 'unbacked';

/** One instance's claim on one slot. */
export interface SlotOccupant {
  readonly slot: number;
  readonly deviceId: string;
  readonly deviceName: string;
  readonly instanceName: string;
}

/**
 * How many tints the ruler cycles through before repeating.
 *
 * Small on purpose. The tint bands contiguous runs so a group reads as one
 * thing; it is not an identifier, so a host holding more groups than this
 * repeating a hue costs nothing that the name beside it does not already
 * settle. A palette long enough to be unique would have to reach for hues too
 * close to each other, and too close to the status colours.
 */
export const NOTCH_TINTS = 6;

/** One cell of the ruler. */
export interface Notch {
  readonly slot: number;
  readonly kind: NotchKind;
  readonly deviceId?: string;
  readonly deviceName?: string;
  readonly instanceName?: string;
  /**
   * Which tint the owning group takes, by order of first appearance down the
   * slot scale. Reinforcement only. Colour-alone distinction is WCAG 1.4.1,
   * so identity rides the truncated name and the accessible name, and this
   * only bands a run so the eye can follow it.
   */
  readonly tint?: number;
}

/**
 * The ruler for one host, one notch per slot from 1 to `maxDevices`.
 *
 * The whole unbacked tail is returned rather than a sample of it: eliding six
 * unbacked slots to two would understate the over-admission by two thirds, and
 * that number is the only reason the tail is drawn at all.
 *
 * An occupant reporting slot 0 has no recoverable slot and is left out. Those
 * are exactly the instances CCO did not place, so no notch is the accurate
 * answer and a guessed slot would not be.
 */
export function buildRuler(
  maxDevices: number,
  backedSlots: number,
  occupants: readonly SlotOccupant[],
  ownDeviceId?: string
): Notch[] {
  const bySlot = new Map<number, SlotOccupant>();
  for (const o of occupants) {
    if (o.slot > 0) {
      bySlot.set(o.slot, o);
    }
  }

  // Assigned by first appearance down the scale rather than by device id, so
  // the banding is stable under a refresh: slots do not move when a state
  // flips, and a hash of the id would re-tint the whole ruler the moment an
  // unrelated group was created.
  const tints = new Map<string, number>();

  const notches: Notch[] = [];
  for (let slot = 1; slot <= maxDevices; slot++) {
    const occupant = bySlot.get(slot);
    if (occupant) {
      if (!tints.has(occupant.deviceId)) {
        tints.set(occupant.deviceId, tints.size % NOTCH_TINTS);
      }
      // An occupant past the backed range contradicts the image's TAP count.
      // Draw what is actually there. A hatched notch over a running instance
      // would hide the more interesting half of the contradiction.
      notches.push({
        slot,
        kind: occupant.deviceId === ownDeviceId ? 'own' : 'foreign',
        deviceId: occupant.deviceId,
        deviceName: occupant.deviceName,
        instanceName: occupant.instanceName,
        tint: tints.get(occupant.deviceId),
      });
      continue;
    }
    notches.push({slot, kind: slot > backedSlots ? 'unbacked' : 'free'});
  }
  return notches;
}

/**
 * The host header's capacity line.
 *
 * The denominator is what the host can actually run, the lower of its own
 * admission cap and what the image backs, so it always equals the number of
 * notches drawn beside it. A header counting slots the ruler does not show is
 * the same defect as a ruler counting slots the host cannot run.
 *
 * Which of the two is the limit changes the wording, because only one of them
 * is a problem. Where the image is the shorter, the host has promised capacity
 * nothing can serve, and the promised number is named so the unbacked tail has
 * something to refute. Where the host's own cap is the shorter, which is the
 * ordinary case, there is nothing wrong to report, and a clause that rendered
 * on every host would stop being read on the one where it matters.
 */
export function occupancyHeadline(used: number, backedSlots: number, maxDevices: number): string {
  const usable = Math.min(maxDevices, backedSlots);
  return maxDevices > backedSlots
    ? `${used} of ${usable} backed · ${maxDevices} promised`
    : `${used} of ${usable} slots`;
}

/**
 * Lane order: by lowest owned slot.
 *
 * Slots do not move when a state flips, so this order is stable across a
 * refresh, which keeps an expanded lane from jumping under the cursor while
 * someone is reading it.
 */
export function lowestSlot(slots: readonly number[]): number {
  const placed = slots.filter(s => s > 0);
  return placed.length > 0 ? Math.min(...placed) : Number.MAX_SAFE_INTEGER;
}

/** Two digits, so slot columns align down the page. */
function padSlot(slot: number): string {
  return String(slot).padStart(2, '0');
}

/** A group's simulated radio, and the ports it answers on. */
export interface RadioRail {
  /** The group's lowest slot. Every port below is derived from it. */
  readonly anchorSlot: number;
  readonly rootcanalHci: number;
  readonly picaUci: number;
  readonly casimirNci: number;
}

/**
 * The radio a group runs, anchored at its lowest slot.
 *
 * netsim, rootcanal, pica and casimir each take the group's lowest instance
 * number minus one as their port offset, so two groups on one host cannot hear
 * each other however close their slots are. Same host does not mean same
 * radio. The anchor is not guessable from the order groups appear in, because
 * allocation fragments, which is why anything printing one of these ports has
 * to name the slot it came from.
 *
 * Returns null for a group with no resolved slot: an unanchored port number
 * would be a fabricated address.
 */
export function radioRail(slots: readonly number[]): RadioRail | null {
  const anchorSlot = lowestSlot(slots);
  if (anchorSlot === Number.MAX_SAFE_INTEGER) {
    return null;
  }
  const offset = anchorSlot - 1;
  return {
    anchorSlot,
    rootcanalHci: 7300 + offset,
    picaUci: 7000 + offset,
    casimirNci: 7800 + offset,
  };
}

/**
 * The slots a collapsed lane owns, as words: `slot 05`, `slots 01-03`,
 * `slots 02, 05, 08`.
 *
 * Contiguous runs collapse to a hyphenated range; a fragmented group lists its
 * slots, because a range across a hole would claim slots the group does not
 * hold. This carries the instance count without printing a second number.
 */
export function slotsLabel(slots: readonly number[]): string {
  const placed = [...new Set(slots.filter(s => s > 0))].sort((a, b) => a - b);
  if (placed.length === 0) {
    return '';
  }
  if (placed.length === 1) {
    return `slot ${padSlot(placed[0])}`;
  }

  const runs: string[] = [];
  let start = placed[0];
  let prev = placed[0];
  for (const slot of placed.slice(1)) {
    if (slot !== prev + 1) {
      runs.push(start === prev ? padSlot(start) : `${padSlot(start)}-${padSlot(prev)}`);
      start = slot;
    }
    prev = slot;
  }
  runs.push(start === prev ? padSlot(start) : `${padSlot(start)}-${padSlot(prev)}`);

  return `slots ${runs.join(', ')}`;
}
