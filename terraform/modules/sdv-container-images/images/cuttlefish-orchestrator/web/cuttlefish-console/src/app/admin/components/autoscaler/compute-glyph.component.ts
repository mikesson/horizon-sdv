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

import {ChangeDetectionStrategy, Component, input} from '@angular/core';

/**
 * The mark for a Compute Engine VM. A chip outline, with pins added when the
 * machine carries an accelerator.
 *
 * Drawn here instead of taken from Google's architecture icon set, for reasons
 * of provenance (the licence would allow it). That set is published as bulk SVG
 * and its stated permission covers exactly this use, referencing Google
 * technology in an architecture drawing, but it has to be BUNDLED to be used.
 * A strict local-asset policy means no CDN, and an approximation of a product
 * mark passed off as the mark itself is worse than a house glyph that claims
 * nothing. This is the console's own vocabulary, and it is one file, so
 * dropping the official SVGs in later is a change to this component and to
 * nothing else.
 *
 * Where it may appear is narrow on purpose. A node that denotes a Compute
 * Engine VM, meaning the host box and the provision node, is the sanctioned
 * use. Status treatments, buttons and the device cells are not. Product marks
 * as a generic UI vocabulary is outside any stated purpose, and Material
 * Symbols already cover it.
 */
@Component({
  selector: 'app-compute-glyph',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <!-- aria-hidden throughout, because the host header names the machine type
         and the accelerator in text, so a screen reader that also announced
         this would read the same fact twice. -->
    <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true" focusable="false">
      <rect x="3.5" y="3.5" width="9" height="9" rx="1.5" />
      <rect class="core" x="6.25" y="6.25" width="3.5" height="3.5" rx="0.5" />
      @if (accelerated()) {
        <path d="M1.5 6h2M1.5 10h2M12.5 6h2M12.5 10h2M6 1.5v2M10 1.5v2M6 12.5v2M10 12.5v2" />
      }
    </svg>
  `,
  styles: `
    :host {
      display: inline-flex;
      align-items: center;
      color: var(--cco-text-secondary);
    }

    svg {
      fill: none;
      stroke: currentcolor;
      stroke-width: 1.1;
      stroke-linecap: round;
    }

    .core {
      fill: currentcolor;
      stroke: none;
      opacity: 0.55;
    }
  `,
})
export class ComputeGlyphComponent {
  /** Draws the accelerator pins. It means the machine carries a GPU, never "is busy". */
  readonly accelerated = input<boolean>(false);
}
