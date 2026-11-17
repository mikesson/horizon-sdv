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
import {displayGeometry, healthyInstanceCount, instanceReading} from './instance-state';

/**
 * Every value a tile can be asked to draw: the ten upstream instance states,
 * the `Unknown` the host client synthesizes, and absent in both its spellings.
 * The table is the contract. A mark swapped between two rows, a word
 * abbreviated, or a colour moved to another token all show up as a diff here.
 */
const TABLE = [
  {state: 'Running', mark: 'disc', word: 'running', color: 'var(--cco-success)'},
  {state: 'Preparing', mark: 'disc-half', word: 'starting', color: 'var(--cco-info)'},
  {state: 'Starting', mark: 'disc-half', word: 'starting', color: 'var(--cco-info)'},
  {state: 'Stopping', mark: 'ring-half', word: 'stopping', color: 'var(--cco-warning)'},
  {state: 'Stopped', mark: 'ring', word: 'stopped', color: 'var(--cco-warning)'},
  {state: 'Unreachable', mark: 'ring-broken', word: 'unreachable', color: 'var(--cco-error)'},
  {state: undefined, mark: 'square-dashed', word: 'absent', color: 'var(--cco-error)'},
  {state: '', mark: 'square-dashed', word: 'absent', color: 'var(--cco-error)'},
  {state: 'Prepare failed', mark: 'triangle', word: 'failed', color: 'var(--cco-error)'},
  {state: 'Boot failed', mark: 'triangle', word: 'failed', color: 'var(--cco-error)'},
  {state: 'Cancelled', mark: 'cross', word: 'cancelled', color: 'var(--cco-text-disabled)'},
  {state: 'Unspecified', mark: 'dash', word: 'unspecified', color: 'var(--cco-text-disabled)'},
  {state: 'Unknown', mark: 'dash', word: 'no data', color: 'var(--cco-text-disabled)'},
];

describe('instanceReading', () => {
  it('draws every renderable value as its own mark, word and colour', () => {
    const read = TABLE.map(row => ({state: row.state, ...instanceReading(row.state)}));

    expect(read).toEqual(TABLE);
  });

  // The pairs above that share a reading share it on purpose. Preparing and
  // Starting are one wait, and both failures route to one recreate. Everything
  // else must stay separable, so the thirteen rows fold to exactly ten readings
  // over nine marks. Merging any two states collapses a count.
  it('keeps ten readings over nine marks', () => {
    const readings = TABLE.map(row => JSON.stringify(instanceReading(row.state)));
    const marks = TABLE.map(row => instanceReading(row.state).mark);
    const words = TABLE.map(row => instanceReading(row.state).word);

    expect(new Set(readings).size).toBe(10);
    expect(new Set(marks).size).toBe(9);
    expect(new Set(words).size).toBe(10);
  });

  // An absent instance routes to a recreate; an unreadable fleet status routes
  // nowhere, because it is no evidence at all. One reading for both hides which
  // rung the backend is about to take, so all three columns separate them, and
  // `Unknown` declines in the disabled token instead of taking the loudest
  // mark on the page.
  it('separates an absent instance from an unreadable fleet status', () => {
    const absent = instanceReading('');
    const noData = instanceReading('Unknown');

    expect(absent.mark).not.toBe(noData.mark);
    expect(absent.word).not.toBe(noData.word);
    expect(absent.color).not.toBe(noData.color);
    expect(noData.color).toBe('var(--cco-text-disabled)');
  });

  // The wire field is omitempty, so an absent instance actually arrives as
  // undefined, never as ''. Reading only '' as absent would leave the split
  // invisible on every real response.
  it('treats an omitted state as absent', () => {
    expect(instanceReading(undefined)).toEqual(instanceReading(''));
    expect(instanceReading(undefined).word).toBe('absent');
  });

  // The two route to different remediation. Absent leads to a recreate, and
  // Unreachable to the backend's cheap restart rung, which fires on exactly
  // Unreachable. One mark for both would hide which one is about to happen.
  it('gives absent and Unreachable different marks', () => {
    expect(instanceReading(undefined).mark).not.toBe(instanceReading('Unreachable').mark);
  });

  // Terminal, but never a fault: a start nobody completed is not a broken
  // instance, so it declines rather than accusing the instance.
  it('declines a cancelled start rather than flagging it', () => {
    expect(instanceReading('Cancelled').color).toBe('var(--cco-text-disabled)');
    expect(instanceReading('Cancelled').color).not.toBe(instanceReading('Boot failed').color);
  });

  // The failure this guards: a state upstream adds later reading as healthy,
  // which is exactly how a dead sibling would hide behind a green group.
  it('never reads an unrecognized state as running', () => {
    expect(instanceReading('Hibernating')).toEqual(instanceReading('Unspecified'));
    expect(instanceReading('Hibernating').mark).not.toBe(instanceReading('Running').mark);
  });

  // A mark without a word names nothing, and a colour off the console's own
  // tokens is a colour that does not follow the theme.
  it('always answers with a word and a token', () => {
    for (const row of TABLE) {
      const reading = instanceReading(row.state);

      expect(reading.word).not.toBe('');
      expect(reading.color).toMatch(/^var\(--cco-[a-z-]+\)$/);
    }
  });
});

describe('healthyInstanceCount', () => {
  it('counts the instances the host reported as running', () => {
    expect(
      healthyInstanceCount([
        {name: 'a', state: 'Running'},
        {name: 'b', state: 'Boot failed'},
      ])
    ).toBe(1);
  });

  // This is why the tile shows "2 of 3". A group whose fold reads running only
  // when every member does must not be summarised by its first member.
  it('does not answer from the first instance alone', () => {
    expect(
      healthyInstanceCount([
        {name: 'a', state: 'Running'},
        {name: 'b', state: 'Running'},
        {name: 'c', state: 'Unreachable'},
      ])
    ).toBe(2);
  });

  it('is zero for a device the host has not answered for', () => {
    expect(healthyInstanceCount([])).toBe(0);
    expect(healthyInstanceCount([{name: 'a'}])).toBe(0);
  });
});

describe('displayGeometry', () => {
  // The strings are the host's own, density tail and all. Parsing the leading
  // pair is what lets a watch be drawn square beside a portrait phone.
  it('reads the host rendering, ignoring the density tail', () => {
    expect(displayGeometry({name: 'phone', displays: ['720 x 1280 ( 320 )']})).toEqual({
      width: 720,
      height: 1280,
    });
    expect(displayGeometry({name: 'watch', displays: ['320 x 320 ( 240 )']})).toEqual({
      width: 320,
      height: 320,
    });
  });

  // A group exists because its members are shaped differently, so the
  // two must not come back equal. The failure this catches silently redraws
  // every guest as the same box.
  it('distinguishes a watch from a phone', () => {
    const phone = displayGeometry({name: 'phone', displays: ['720 x 1280 ( 320 )']});
    const watch = displayGeometry({name: 'watch', displays: ['320 x 320 ( 240 )']});
    expect(phone && phone.width / phone.height).not.toBeCloseTo(
      watch ? watch.width / watch.height : 0
    );
  });

  it('answers undefined rather than guessing a shape', () => {
    expect(displayGeometry({name: 'a'})).toBeUndefined();
    expect(displayGeometry({name: 'a', displays: []})).toBeUndefined();
    expect(displayGeometry({name: 'a', displays: ['unknown']})).toBeUndefined();
    expect(displayGeometry({name: 'a', displays: ['0 x 0 ( 0 )']})).toBeUndefined();
  });
});
