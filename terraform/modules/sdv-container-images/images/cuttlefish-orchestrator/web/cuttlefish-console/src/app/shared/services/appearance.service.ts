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

import {effect, Service, signal} from '@angular/core';

/** The Cloud Console's three appearance options, in its own order. */
export type Appearance = 'light' | 'dark' | 'system';

// Mirrored by an inline script in index.html, which stamps the attribute before
// the first paint. Change one and change the other.
const STORAGE_KEY = 'cco-appearance';

function isAppearance(value: string | null): value is Appearance {
  return value === 'light' || value === 'dark' || value === 'system';
}

/**
 * Chooses which colour scheme the console renders in.
 *
 * All this does is set an attribute. The token layer declares every colour as a
 * `light-dark()` pair, so the browser resolves them from whatever `color-scheme`
 * is in force. There is no second stylesheet and nothing to keep in sync.
 *
 * `system` is the default, and it removes the attribute rather than resolving
 * the OS preference into a stored value. That is what makes the console follow
 * the OS live, without a reload, when it changes.
 */
@Service()
export class AppearanceService {
  private readonly stored = localStorage.getItem(STORAGE_KEY);

  readonly appearance = signal<Appearance>(isAppearance(this.stored) ? this.stored : 'system');

  constructor() {
    effect(() => {
      const value = this.appearance();
      const root = document.documentElement;
      if (value === 'system') {
        root.removeAttribute('data-cco-theme');
        localStorage.removeItem(STORAGE_KEY);
      } else {
        root.setAttribute('data-cco-theme', value);
        localStorage.setItem(STORAGE_KEY, value);
      }
    });
  }

  set(value: Appearance): void {
    this.appearance.set(value);
  }
}
