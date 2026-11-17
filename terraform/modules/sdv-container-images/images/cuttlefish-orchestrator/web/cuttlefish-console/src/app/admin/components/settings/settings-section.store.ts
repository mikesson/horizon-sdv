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

import type {DestroyRef, WritableSignal} from '@angular/core';
import {computed, signal} from '@angular/core';
import {takeUntilDestroyed} from '@angular/core/rxjs-interop';
import type {Observable} from 'rxjs';
import type {SwitchStatus} from '../../../shared/components/settings-card/settings-switch-row.component';
import {getErrorMessage} from '../../../shared/utils';

/**
 * One settings section's load/edit/save cycle, held per section so a failing
 * endpoint degrades its own card rather than the page.
 *
 * The baseline is what makes "PUT only what changed" possible: every write
 * lands a config_change audit entry, so a Save that re-sends untouched values
 * fills the Activity trail with edits nobody made.
 */
export class SettingsSectionStore<T extends object> {
  private readonly saved = signal<T | null>(null);
  private readonly current = signal<T | null>(null);

  readonly loading = signal(true);
  readonly saving = signal(false);
  readonly error = signal<string | null>(null);

  readonly draft = this.current.asReadonly();

  /**
   * What the server holds. A write that is not the form's Save builds its body
   * from this, never from the draft, or it persists edits nobody saved.
   */
  readonly baseline = this.saved.asReadonly();

  /** Save is offered only when something actually differs from the server's copy. */
  readonly dirty = computed(() => {
    const baseline = this.saved();
    const next = this.current();
    return baseline !== null && next !== null && !valueEqual(baseline, next);
  });

  constructor(
    private readonly fetch: () => Observable<T>,
    private readonly destroyRef: DestroyRef
  ) {}

  load(): void {
    this.loading.set(true);
    this.error.set(null);
    this.fetch()
      .pipe(takeUntilDestroyed(this.destroyRef))
      .subscribe({
        next: value => {
          this.saved.set(value);
          // Deep, never a spread. A spread copies the top level only, so a
          // section holding a list would hand the draft and the baseline the
          // same array, and then no edit to that list could ever read as
          // dirty, because both sides of the comparison moved together.
          this.current.set(structuredClone(value));
          this.loading.set(false);
        },
        error: (err: unknown) => {
          this.error.set(getErrorMessage(err, 'Failed to load this section'));
          this.loading.set(false);
        },
      });
  }

  patch(change: Partial<T>): void {
    const next = this.current();
    if (next) {
      this.current.set({...next, ...change});
    }
  }

  save(write: (next: T, baseline: T) => Observable<unknown>): void {
    const next = this.current();
    const baseline = this.saved();
    if (!(next && baseline) || this.saving()) {
      return;
    }
    this.saving.set(true);
    write(next, baseline)
      .pipe(takeUntilDestroyed(this.destroyRef))
      .subscribe({
        next: () => {
          this.saving.set(false);
          // Reload rather than trusting the draft: a Save makes every value in
          // the section stored, and only the server can say so.
          this.load();
        },
        error: (err: unknown) => {
          this.saving.set(false);
          this.error.set(getErrorMessage(err, 'Failed to save this section'));
        },
      });
  }

  /**
   * A switch applies on flip, and its card is never the form's card.
   *
   * The change lands on the draft and the baseline together, on exactly the
   * keys it names. Writing the whole draft back as the baseline instead would
   * mark a pending edit in the sibling form card as saved without ever having
   * sent it; touching only the draft would light up that card's Save for an
   * edit nobody made. A failure restores the same keys on both, so the
   * provenance flag a flip sets does not survive the flip failing.
   *
   * `failure` is the switch card's own error, not the section's: routing it
   * through `error` would replace the sibling form's fields with a banner and
   * take the operator's unsaved edits with them.
   */
  toggle(
    change: Partial<T>,
    write: Observable<unknown>,
    status: WritableSignal<SwitchStatus>,
    failure: WritableSignal<string | null>
  ): void {
    const draft = this.current();
    const baseline = this.saved();
    if (!(draft && baseline)) {
      return;
    }
    const keys = Object.keys(change) as Array<keyof T>;
    const draftBefore = pick(draft, keys);
    const baselineBefore = pick(baseline, keys);

    status.set('saving');
    failure.set(null);
    // Optimistic, so the control moves under the finger.
    this.current.set({...draft, ...change});
    this.saved.set({...baseline, ...change});

    write.pipe(takeUntilDestroyed(this.destroyRef)).subscribe({
      next: () => {
        status.set('saved');
      },
      error: (err: unknown) => {
        // A revert the user cannot see is worse than no optimism at all, so
        // the switch snaps back visibly and the card says why.
        this.current.update(value => (value ? {...value, ...draftBefore} : value));
        this.saved.update(value => (value ? {...value, ...baselineBefore} : value));
        status.set('failed');
        failure.set(getErrorMessage(err, 'Failed to apply the change'));
      },
    });
  }
}

function pick<T extends object>(source: T, keys: Array<keyof T>): Partial<T> {
  const out: Partial<T> = {};
  for (const key of keys) {
    out[key] = source[key];
  }
  return out;
}

/**
 * Structural equality over the JSON-shaped values a settings section holds.
 *
 * Identity per key is not enough once a section carries a list. Comparing array
 * references calls an edited list unchanged whenever it was mutated in place,
 * and calls an untouched one changed whenever the editor rebuilt it, so Save
 * would either never appear or would fire a PUT, and a config_change audit
 * entry, for an edit that was made and then undone. Both directions are silent.
 */
function valueEqual(a: unknown, b: unknown): boolean {
  if (a === b) {
    return true;
  }
  if (Array.isArray(a) || Array.isArray(b)) {
    return (
      Array.isArray(a) &&
      Array.isArray(b) &&
      a.length === b.length &&
      a.every((item, i) => valueEqual(item, b[i]))
    );
  }
  if (a instanceof Date || b instanceof Date) {
    return a instanceof Date && b instanceof Date && a.getTime() === b.getTime();
  }
  if (a === null || b === null || typeof a !== 'object' || typeof b !== 'object') {
    return false;
  }
  const left = a as Record<string, unknown>;
  const right = b as Record<string, unknown>;
  const keys = Object.keys(left);
  // Key COUNT as well as the left side's keys. A field dropped from one copy
  // would otherwise compare equal to the copy that still carries it.
  return (
    keys.length === Object.keys(right).length &&
    keys.every(key => valueEqual(left[key], right[key]))
  );
}
