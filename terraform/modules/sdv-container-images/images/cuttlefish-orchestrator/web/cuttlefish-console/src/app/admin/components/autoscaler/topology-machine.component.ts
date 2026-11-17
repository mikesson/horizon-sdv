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
import {RouterLink} from '@angular/router';
import {ComputeGlyphComponent} from './compute-glyph.component';
import type {MachineCard} from './topology-canvas.model';

/**
 * One machine: what it is, what is inside it, and what it is doing.
 *
 * Card AREA is capacity, one fixed token per slot wrapping at eight, so a
 * sixteen-slot machine is visibly twice an eight-slot one before any header is
 * read. Nothing here derives anything. The card arrives composed, which is what
 * keeps the rules that cost defects assertable as arithmetic.
 *
 * The colour roles are declared by the canvas and inherited, so one palette
 * serves the cards, the clusters and the engine.
 */
@Component({
  selector: 'app-topology-machine',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [RouterLink, ComputeGlyphComponent],
  host: {
    '[attr.data-state]': 'card().state',
    '[class.compact]': 'compact()',
  },
  template: `
    <div class="head">
      <app-compute-glyph [accelerated]="card().accelerated" />
      @if (card().hostId) {
        <a class="name" [routerLink]="['/hosts', card().hostId]">{{ card().name }}</a>
      } @else {
        <span class="name">{{ card().name }}</span>
      }
      @if (card().compute) {
        <span class="badge" [attr.data-compute]="card().accelerated ? 'gpu' : 'cpu'">
          {{ card().compute }}
        </span>
      }
    </div>

    <p class="shape">{{ card().shape }}</p>

    @if (card().group; as group) {
      <!-- One block instead of slots. The instances of one device share this
           machine's netsim and rootcanal radio medium, so placement fences the
           machine to them and a stranger's device would join their radio. -->
      <div class="group">
        <span class="group-name">{{ group.name }}</span>
        <span class="group-note">{{ group.note }}</span>
      </div>
    } @else if (card().slots.length > 0) {
      <ol class="slots" [attr.aria-label]="'Slots on ' + card().name">
        @for (slot of card().slots; track $index) {
          <li
            class="slot"
            [attr.data-kind]="slot.kind"
            [style.--cco-canvas-span]="slot.span"
            tabindex="0"
            [attr.aria-label]="slot.title"
            [title]="slot.title">
            <span class="slot-label">{{ slot.label }}</span>
            @if (slot.span > 1) {
              <span class="slot-span">{{ slot.span }} instances</span>
            }
          </li>
        }
      </ol>
    }

    <p class="capacity"><span class="key">slots</span> {{ card().slotLine }}</p>
    <p class="word">{{ card().word }}</p>
    @for (line of card().lines; track $index) {
      <p class="line">{{ line }}</p>
    }
  `,
  styles: `
    :host {
      display: flex;
      flex-direction: column;
      gap: var(--cco-space-4);
      padding: var(--cco-space-12);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-lg);
      /* Solid, never translucent. Text sits on this, and a tint over a rail
         underneath is how the contrast gate failed here twice. */
      background: var(--cco-canvas-card);
      /* A change inside one card cannot invalidate layout around it. */
      contain: layout paint;
    }

    :host([data-state="free"]) {
      border-color: var(--cco-canvas-free);
    }

    :host([data-state="leaving"]),
    :host([data-state="arriving"]) {
      border-color: var(--cco-canvas-edge);
    }

    /* A machine that does not exist yet, drawn as an outline. Distinct from a
       free slot, which is a dashed cell INSIDE a real machine. */
    :host([data-state="ghost"]) {
      border-style: dashed;
      border-color: var(--cco-text-disabled);
      background: none;
    }

    :host([data-state="ghost"]) .name {
      font-family: inherit;
      color: var(--cco-text-secondary);
    }

    .head {
      display: flex;
      align-items: center;
      gap: var(--cco-space-8);
    }

    .name {
      font-family: var(--cco-font-mono);
      color: var(--cco-text);
      text-decoration: none;
    }

    a.name:hover,
    a.name:focus-visible {
      text-decoration: underline;
    }

    .badge {
      padding: 0 var(--cco-space-4);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-sm);
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .badge[data-compute="gpu"] {
      border-color: var(--cco-canvas-edge);
      color: var(--cco-canvas-edge);
    }

    .shape,
    .line,
    .word {
      margin: 0;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .word {
      color: var(--cco-text);
    }

    :host([data-state="free"]) .word {
      color: var(--cco-canvas-free);
    }

    :host([data-state="leaving"]) .word,
    :host([data-state="arriving"]) .word {
      color: var(--cco-canvas-edge);
    }

    .capacity {
      margin: var(--cco-space-4) 0 0;
      font-family: var(--cco-font-mono);
      font-variant-numeric: tabular-nums;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .key {
      letter-spacing: 0.08em;
      text-transform: uppercase;
    }

    /* Eight slots to a row, so a wide machine grows in area rather than running
       off the page, and still reads as bigger than a narrow one. */
    .slots {
      display: flex;
      flex-wrap: wrap;
      gap: 2px;
      max-width: calc(var(--cco-canvas-slot) * 8 + 14px);
      margin: var(--cco-space-4) 0;
      padding: 0;
      list-style: none;
    }

    .slot {
      /* Declared here with the single-slot default and overridden per slot by a
         style binding. A var() fallback would say the same thing while leaving
         the property undeclared in the cascade. */
      --cco-canvas-span: 1;

      display: flex;
      flex-direction: column;
      justify-content: center;
      flex: 0 0 calc(var(--cco-canvas-slot) * var(--cco-canvas-span));
      min-height: 2.5rem;
      padding: var(--cco-space-4);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-xs);
      background: var(--cco-canvas-ground);
      font-family: var(--cco-font-mono);
      font-size: var(--cco-text-label);
      overflow: hidden;
    }

    .slot-label {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      color: var(--cco-text);
    }

    .slot-span {
      color: var(--cco-text-secondary);
    }

    .slot[data-kind="occupied"] {
      border-color: color-mix(in srgb, var(--cco-canvas-busy) 55%, transparent);
      background: color-mix(in srgb, var(--cco-canvas-busy) 14%, var(--cco-canvas-card));
    }

    .slot[data-kind="free"],
    .slot[data-kind="would"] {
      border-style: dashed;
      border-color: var(--cco-canvas-free);
      background: transparent;
    }

    .slot[data-kind="free"] .slot-label,
    .slot[data-kind="would"] .slot-label {
      color: var(--cco-canvas-free);
    }

    /* Hatched, bordered, and carrying its word, never a status mark. Those mean
       instance liveness in this console, so a triangle here would read at a
       glance as failed instances on a machine that has none. */
    .slot[data-kind="unbacked"] {
      border-color: var(--cco-error);
      background: repeating-linear-gradient(
        45deg,
        transparent,
        transparent 3px,
        color-mix(in srgb, var(--cco-error) 18%, transparent) 3px,
        color-mix(in srgb, var(--cco-error) 18%, transparent) 6px
      );
    }

    .slot[data-kind="unbacked"] .slot-label {
      color: var(--cco-error);
    }

    /* A dead row in a real slot. Distinct from unbacked on purpose, because
       that is a claim about the machine's image and this is one about a
       create. */
    .slot[data-kind="error-held"] {
      border-color: var(--cco-canvas-edge);
      border-style: dotted;
      background: color-mix(in srgb, var(--cco-canvas-edge) 10%, var(--cco-canvas-card));
    }

    .slot[data-kind="error-held"] .slot-label {
      color: var(--cco-canvas-edge);
    }

    .slot:focus-visible {
      outline: 2px solid var(--cco-accent);
      outline-offset: 1px;
    }

    .group {
      display: flex;
      flex-direction: column;
      justify-content: center;
      min-height: 2.5rem;
      margin: var(--cco-space-4) 0;
      padding: var(--cco-space-8);
      border: 1px solid color-mix(in srgb, var(--cco-canvas-busy) 55%, transparent);
      border-radius: var(--cco-radius-xs);
      background: color-mix(in srgb, var(--cco-canvas-busy) 14%, var(--cco-canvas-card));
      font-size: var(--cco-text-label);
    }

    .group-name {
      font-family: var(--cco-font-mono);
      color: var(--cco-text);
    }

    .group-note {
      color: var(--cco-text-secondary);
    }

    /* Past sixteen machines the card stops encoding capacity and the words carry
       it. Still one card per machine, still focusable. */
    :host(.compact) {
      max-width: 20rem;
    }
  `,
})
export class TopologyMachineComponent {
  readonly card = input.required<MachineCard>();
  readonly compact = input<boolean>(false);
}
