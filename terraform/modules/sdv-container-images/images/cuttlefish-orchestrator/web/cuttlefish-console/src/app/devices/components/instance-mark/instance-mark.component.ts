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

import {ChangeDetectionStrategy, Component, computed, input} from '@angular/core';
import {instanceReading} from '../../models/instance-state';

/**
 * One instance's status, as a mark and the word it stands for.
 *
 * The mark and the word are one component so a caller cannot render one
 * without the other. A shape on its own names nothing, and every mark this
 * console draws has to arrive with the word it means. Marks belong to
 * instances only. A device's status is a different state machine, and two
 * near-identical marks a few pixels apart would make claims about different
 * things.
 *
 * The marks are SVG, never Unicode characters. U+2693 and its neighbours
 * carry Emoji_Presentation, so a Unicode mark renders as colour emoji on most
 * platforms, with the wrong weight, the wrong baseline, and a different look
 * on every OS. One drawn set gives one set of metrics and one stroke weight,
 * and `currentColor` carries it into both themes.
 */
@Component({
  selector: 'app-instance-mark',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    '[style.color]': 'reading().color',
  },
  template: `
    @let read = reading();
    <span class="mark-slot">
      <!-- Keyed on the mark so a state change replaces the glyph rather than
           mutating it in place, which is what gives the old one something to
           fade out of and the new one something to fade into. -->
      @for (mark of [read.mark]; track mark) {
    <svg
      class="mark"
      animate.enter="mark-in"
      animate.leave="mark-out"
      viewBox="0 0 12 12"
      fill="none"
      stroke="currentColor"
      stroke-width="1.5"
      stroke-linecap="round"
      aria-hidden="true"
      [attr.data-mark]="read.mark"
    >
      @switch (mark) {
        @case ('disc') {
          <circle cx="6" cy="6" r="4.5" fill="currentColor" stroke="none" />
        }
        @case ('disc-half') {
          <circle cx="6" cy="6" r="4.5" />
          <path d="M6 1.5A4.5 4.5 0 0 0 6 10.5Z" fill="currentColor" stroke="none" />
        }
        @case ('ring-half') {
          <path d="M6 1.5A4.5 4.5 0 0 0 6 10.5" />
        }
        @case ('ring') {
          <circle cx="6" cy="6" r="4.5" />
        }
        @case ('ring-broken') {
          <path d="M1.76 4.5A4.5 4.5 0 0 1 10.24 4.5" />
          <path d="M1.76 7.5A4.5 4.5 0 0 0 10.24 7.5" />
        }
        @case ('square-dashed') {
          <rect
            x="1.7"
            y="1.7"
            width="8.6"
            height="8.6"
            rx="0.5"
            stroke-dasharray="2.5 2"
            stroke-linecap="butt"
          />
        }
        @case ('triangle') {
          <path d="M6 1.6L10.9 10.4H1.1Z" fill="currentColor" stroke="none" />
        }
        @case ('cross') {
          <path d="M3 3L9 9M9 3L3 9" />
        }
        <!-- The dash has no case of its own: a mark this switch cannot name is
             no information, and the dash is what says that. -->
        @default {
          <path d="M2.2 6H9.8" />
        }
      }
    </svg>
      }
    </span>
    <span class="word">{{ read.word }}</span>
  `,
  styles: `
    :host {
      display: inline-flex;
      align-items: center;
      gap: 6px;
    }

    /* Both glyphs occupy one cell, so the outgoing and incoming marks overlap
       instead of the row reflowing mid-transition. */
    .mark-slot {
      display: inline-grid;
      flex-shrink: 0;
      width: 12px;
      height: 12px;
    }

    .mark {
      grid-area: 1 / 1;
      width: 12px;
      height: 12px;
    }

    /* A state change is what people keep this view open for, so it is the one
       thing worth animating. Nothing here is ambient: no pulse, no loop. */
    @keyframes mark-in {
      from { opacity: 0; }
      to { opacity: 1; }
    }

    @keyframes mark-out {
      from { opacity: 1; }
      to { opacity: 0; }
    }

    .mark-in {
      animation: mark-in var(--cco-duration-fast) ease-out;
    }

    .mark-out {
      animation: mark-out var(--cco-duration-fast) ease-in;
    }

    @media (prefers-reduced-motion: reduce) {
      .mark-in,
      .mark-out {
        animation: none;
      }
    }

    /* The vocabulary's longest words are eleven characters and callers size for
       them. Wrapping or truncating one would invent a second name for a state. */
    .word {
      white-space: nowrap;
    }
  `,
})
export class InstanceMarkComponent {
  /** The raw CVD wire state; absent when the host did not list the instance. */
  readonly state = input<string>();

  protected readonly reading = computed(() => instanceReading(this.state()));
}
