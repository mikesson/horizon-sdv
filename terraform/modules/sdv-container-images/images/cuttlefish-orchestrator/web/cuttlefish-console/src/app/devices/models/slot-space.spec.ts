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

import type {SlotOccupant} from './slot-space';
import {buildRuler, lowestSlot, occupancyHeadline, radioRail, slotsLabel} from './slot-space';

function occupant(slot: number, deviceId: string, instanceName = 'phone'): SlotOccupant {
  return {slot, deviceId, deviceName: `dev-${deviceId}`, instanceName};
}

describe('buildRuler', () => {
  it('seats occupants at their true slots and leaves the holes open', () => {
    const notches = buildRuler(10, 10, [occupant(2, 'a'), occupant(5, 'a'), occupant(8, 'a')]);

    // 2, 5, 8 is an ordinary group on a churned host. A ruler keyed on array
    // position would close the holes and mis-seat everything after the first.
    expect(notches).toHaveLength(10);
    expect(notches.filter(n => n.kind === 'foreign').map(n => n.slot)).toEqual([2, 5, 8]);
    expect(notches.filter(n => n.kind === 'free').map(n => n.slot)).toEqual([1, 3, 4, 6, 7, 9, 10]);
  });

  it('separates this device from every other one', () => {
    const notches = buildRuler(4, 10, [occupant(1, 'mine'), occupant(2, 'theirs')], 'mine');

    expect(notches[0].kind).toBe('own');
    expect(notches[1].kind).toBe('foreign');
    expect(notches[1].deviceName).toBe('dev-theirs');
  });

  it('draws the whole unbacked tail, never a sample of it', () => {
    const notches = buildRuler(16, 10, [occupant(1, 'a')]);

    // Six unbacked slots elided to two would understate the over-admission by
    // two thirds, which is the only number the tail exists to carry.
    expect(notches.filter(n => n.kind === 'unbacked').map(n => n.slot)).toEqual([
      11, 12, 13, 14, 15, 16,
    ]);
  });

  it('draws an occupant past the backed range as occupied, not as unbacked', () => {
    const notches = buildRuler(12, 10, [occupant(11, 'a')]);

    // A hatched notch over a running instance would hide the more interesting
    // half of the contradiction.
    expect(notches[10].kind).toBe('foreign');
  });

  it('leaves out an occupant whose slot never resolved', () => {
    const notches = buildRuler(4, 10, [occupant(0, 'a'), occupant(2, 'b')]);

    expect(notches.filter(n => n.kind === 'foreign').map(n => n.slot)).toEqual([2]);
  });

  describe('group tints', () => {
    it('gives one group one tint across all its slots', () => {
      const notches = buildRuler(6, 10, [occupant(1, 'a'), occupant(4, 'a'), occupant(5, 'b')]);

      expect(notches[0].tint).toBe(notches[3].tint);
      expect(notches[4].tint).not.toBe(notches[0].tint);
    });

    it('assigns tints by position down the scale, so a refresh cannot re-tint', () => {
      const first = buildRuler(6, 10, [occupant(2, 'a'), occupant(4, 'b')]);
      // The same hosts, read again with the occupants in another order: slots
      // do not move when a state flips, so neither may the banding.
      const second = buildRuler(6, 10, [occupant(4, 'b'), occupant(2, 'a')]);

      expect(second[1].tint).toBe(first[1].tint);
      expect(second[3].tint).toBe(first[3].tint);
    });

    it('never tints a slot nobody is in', () => {
      const notches = buildRuler(4, 2, [occupant(1, 'a')]);

      expect(notches[1].tint).toBeUndefined();
      expect(notches[3].tint).toBeUndefined();
    });
  });
});

describe('occupancyHeadline', () => {
  it('counts against what the host can actually run', () => {
    // The ordinary host: its own cap is the shorter, nothing is wrong, and a
    // clause on every host would stop being read on the one that matters.
    expect(occupancyHeadline(2, 10, 4)).toBe('2 of 4 slots');
  });

  it('names the promise the ruler is about to refute', () => {
    expect(occupancyHeadline(7, 10, 16)).toBe('7 of 10 backed · 16 promised');
  });

  it('never states a denominator larger than the ruler beside it', () => {
    // The defect this exists to stop: "2 of 10" printed above four notches.
    expect(occupancyHeadline(2, 10, 4)).not.toContain('10');
  });
});

describe('radioRail', () => {
  it('offsets every port from the group lowest slot', () => {
    const rail = radioRail([8, 5]);

    expect(rail).toEqual({anchorSlot: 5, rootcanalHci: 7304, picaUci: 7004, casimirNci: 7804});
  });

  it('anchors a group that starts at slot 1 on the base ports', () => {
    expect(radioRail([1, 2])?.rootcanalHci).toBe(7300);
  });

  it('refuses to fabricate an unanchored port', () => {
    expect(radioRail([0, 0])).toBeNull();
    expect(radioRail([])).toBeNull();
  });
});

describe('slotsLabel', () => {
  it('collapses a contiguous run and lists a fragmented one', () => {
    expect(slotsLabel([5])).toBe('slot 05');
    expect(slotsLabel([1, 2, 3])).toBe('slots 01-03');
    // A range across a hole would claim slots the group does not hold.
    expect(slotsLabel([2, 5, 8])).toBe('slots 02, 05, 08');
    expect(slotsLabel([1, 2, 5])).toBe('slots 01-02, 05');
  });

  it('sorts and de-duplicates before reading runs off the order', () => {
    expect(slotsLabel([3, 1, 2, 2])).toBe('slots 01-03');
  });

  it('says nothing when no slot resolved', () => {
    expect(slotsLabel([0])).toBe('');
  });
});

describe('lowestSlot', () => {
  it('sorts unplaced groups last rather than first', () => {
    expect(lowestSlot([8, 5])).toBe(5);
    expect(lowestSlot([0])).toBeGreaterThan(lowestSlot([99]));
  });
});
