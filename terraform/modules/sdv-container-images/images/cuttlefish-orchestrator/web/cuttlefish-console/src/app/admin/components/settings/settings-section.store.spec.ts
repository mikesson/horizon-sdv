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

import type {DestroyRef} from '@angular/core';
import {signal} from '@angular/core';
import {of, Subject, throwError} from 'rxjs';
import {describe, expect, it, vi} from 'vitest';
import type {SwitchStatus} from '../../../shared/components/settings-card/settings-switch-row.component';
import {SettingsSectionStore} from './settings-section.store';

interface Section {
  name: string;
  count: number;
  nameIsSet: boolean;
}

/** A section carrying a list, which is what the fleet card became. */
interface ListSection {
  items: Array<{label: string; size: number}>;
}

/** A DestroyRef that never fires, so a test observable is never cut short. */
const NEVER_DESTROYED = {onDestroy: () => () => undefined} as unknown as DestroyRef;

function store(
  initial: Section = {name: 'a', count: 1, nameIsSet: false}
): SettingsSectionStore<Section> {
  return new SettingsSectionStore<Section>(() => of(initial), NEVER_DESTROYED);
}

function statuses(): {
  status: ReturnType<typeof signal<SwitchStatus>>;
  failure: ReturnType<typeof signal<string | null>>;
} {
  return {status: signal<SwitchStatus>('idle'), failure: signal<string | null>(null)};
}

describe('SettingsSectionStore', () => {
  it('is not dirty until an edit actually changes something', () => {
    const s = store();
    s.load();
    expect(s.dirty()).toBe(false);

    // Same value assigned back is not an edit; every PUT lands an audit entry,
    // so offering Save here would invite writes nobody made.
    s.patch({name: 'a'});
    expect(s.dirty()).toBe(false);

    s.patch({name: 'b'});
    expect(s.dirty()).toBe(true);
  });

  it('hands the write both the edit and the server copy to diff against', () => {
    const s = store();
    s.load();
    s.patch({count: 2});

    const write = vi.fn(() => of({}));
    s.save(write);

    expect(write).toHaveBeenCalledWith(
      {name: 'a', count: 2, nameIsSet: false},
      {name: 'a', count: 1, nameIsSet: false}
    );
  });

  it('reloads after a save so provenance comes from the server, not the draft', () => {
    const fetch = vi.fn(() => of({name: 'a', count: 1, nameIsSet: false}));
    const s = new SettingsSectionStore<Section>(fetch, NEVER_DESTROYED);
    s.load();
    s.patch({count: 9});
    s.save(() => of({}));

    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it('keeps the section usable when its load fails', () => {
    const s = new SettingsSectionStore<Section>(
      () => throwError(() => new Error('boom')),
      NEVER_DESTROYED
    );
    s.load();

    expect(s.loading()).toBe(false);
    expect(s.error()).toBeTruthy();
    expect(s.draft()).toBeNull();
  });

  it('moves the switch under the finger before the write lands', () => {
    const s = store();
    s.load();
    const {status, failure} = statuses();
    const write = new Subject<unknown>();

    s.toggle({name: 'on', nameIsSet: true}, write, status, failure);

    // Still in flight. Applying on flip is optimistic, so the draft already
    // shows the new value.
    expect(s.draft()?.name).toBe('on');
    expect(status()).toBe('saving');
  });

  it('snaps a failed flip back visibly, on every key it wrote', () => {
    const s = store();
    s.load();
    const {status, failure} = statuses();

    s.toggle(
      {name: 'on', nameIsSet: true},
      throwError(() => new Error('nope')),
      status,
      failure
    );

    expect(s.draft()?.name).toBe('a');
    // The provenance flag must not outlive the flip that set it, or the form
    // card's Save lights up for an edit nobody made.
    expect(s.draft()?.nameIsSet).toBe(false);
    expect(s.dirty()).toBe(false);
    expect(status()).toBe('failed');
    expect(failure()).toBeTruthy();
  });

  it('reports a failed flip on the switch, not through the section banner', () => {
    const s = store();
    s.load();
    const {status, failure} = statuses();

    s.toggle(
      {name: 'on'},
      throwError(() => new Error('nope')),
      status,
      failure
    );

    // The section's error replaces the sibling form's fields with a banner and
    // takes any unsaved edit down with them.
    expect(s.error()).toBeNull();
    expect(failure()).toBeTruthy();
  });

  it('treats a successful flip as the new server state', () => {
    const s = store();
    s.load();
    const {status, failure} = statuses();

    s.toggle({name: 'on', nameIsSet: true}, of({}), status, failure);

    expect(s.draft()?.name).toBe('on');
    expect(status()).toBe('saved');
    // The flip is already persisted, so the form's Save must not light up.
    expect(s.dirty()).toBe(false);
  });

  it('leaves an unsaved form edit unsaved when a switch is flipped', () => {
    const s = store();
    s.load();
    // The operator types into the form card and has not pressed Save.
    s.patch({count: 42});
    const {status, failure} = statuses();

    s.toggle({name: 'on', nameIsSet: true}, of({}), status, failure);

    // The flip PUT only the switch. Adopting the whole draft as the baseline
    // would mark count:42 as stored without ever having sent it.
    expect(s.draft()?.count).toBe(42);
    expect(s.dirty()).toBe(true);

    const write = vi.fn(() => of({}));
    s.save(write);
    expect(write).toHaveBeenCalledWith(
      {name: 'on', count: 42, nameIsSet: true},
      {name: 'on', count: 1, nameIsSet: true}
    );
  });

  it('exposes the server copy so a switch write can skip the draft', () => {
    const s = store();
    s.load();
    s.patch({count: 42});

    expect(s.baseline()?.count).toBe(1);
  });

  // The three rows below are what a list section needs and a per-key identity
  // comparison cannot give it. All three fail silently: Save either never
  // appears, or appears for an edit that was undone and writes an audited PUT
  // for a change nobody made.
  describe('a section carrying a list', () => {
    function listStore(): SettingsSectionStore<ListSection> {
      const served: ListSection = {items: [{label: 'morning', size: 3}]};
      return new SettingsSectionStore<ListSection>(() => of(served), NEVER_DESTROYED);
    }

    it('is not dirty when the list is rebuilt with the same contents', () => {
      const s = listStore();
      s.load();
      // What the editor does on every keystroke: a fresh array of fresh
      // objects. Comparing references calls this an edit.
      s.patch({items: [{label: 'morning', size: 3}]});

      expect(s.dirty()).toBe(false);
    });

    it('is dirty when the list actually changes', () => {
      const s = listStore();
      s.load();
      s.patch({items: [{label: 'morning', size: 4}]});

      expect(s.dirty()).toBe(true);
    });

    it('goes clean again when an edit is undone', () => {
      const s = listStore();
      s.load();
      s.patch({
        items: [
          {label: 'morning', size: 4},
          {label: 'evening', size: 1},
        ],
      });
      expect(s.dirty()).toBe(true);

      s.patch({items: [{label: 'morning', size: 3}]});
      expect(s.dirty()).toBe(false);
    });

    it('does not let the draft and the baseline share the served array', () => {
      const s = listStore();
      s.load();
      // Reaching into the draft is exactly what the discipline forbids, and is
      // the point: if the two sides shared an array, this would move the
      // baseline too and dirty would stay false.
      s.draft()?.items.push({label: 'sneaked in', size: 9});

      expect(s.baseline()?.items).toHaveLength(1);
    });
  });
});
