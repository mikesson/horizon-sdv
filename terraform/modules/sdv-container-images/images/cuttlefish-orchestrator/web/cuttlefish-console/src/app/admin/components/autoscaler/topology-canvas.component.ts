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

import {ChangeDetectionStrategy, Component, computed, input, output} from '@angular/core';
import type {AutoscalerSnapshot} from '../../models/autoscaler.model';
import {DemandColumnComponent} from './demand-column.component';
import {PoolRegionComponent} from './pool-region.component';
import {buildCanvas} from './topology-canvas.model';
import {TopologyMachineComponent} from './topology-machine.component';

/** Where a jump from the canvas lands. */
export type CanvasSection = 'controls' | 'window';

/**
 * One drawing: the machines CCO manages, what is inside them, who owns them,
 * and what is arriving or being given back.
 *
 * Three reading zones, left to right (what could not be placed, what stands,
 * what is changing) with the autoscaler in the middle and a rail to the
 * machines it owns and to nothing else. Ownership is a drawn line rather than a
 * heading, because a line between two objects states it without words.
 *
 * The rail carries one stub per pool region and one for the engine, so the
 * connectors grow with the pools and never with the machines. The engine stays
 * SINGULAR, one keeper and one loop, and holds the facts that are one per
 * deployment beside a gap line per pool; each region carries what is that pool's
 * own. Facts drawn on the thing they are about.
 *
 * The layout is fixed grid areas and a rail drawn as a border, so nothing is
 * measured and a refresh cannot move anything. An auto-layout that settles
 * differently makes an unchanged snapshot look like a change.
 */
@Component({
  selector: 'app-topology-canvas',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [DemandColumnComponent, PoolRegionComponent, TopologyMachineComponent],
  template: `
    <div class="canvas" [class.mode-off]="!engine().provisions">
      <section class="unplaced" aria-label="Requests that found no machine">
        <app-demand-column
          [split]="snapshot().fleet.status.demand"
          [horizonSeconds]="snapshot().demandHorizonSec"
          [pools]="canvas().unplaced" />
        <button type="button" class="jump" (click)="jump.emit('window')">
          What the window measured
        </button>
      </section>

      <div class="owned">
        <section class="engine" [class.acting]="engine().provisions">
          <span class="ring" aria-hidden="true"></span>
          <h3>Autoscaler</h3>
          <!-- Split per PERMISSION rather than per mode. provision_only buys and
               renews while lowering no lease, so one "is it on" line would say
               the shortener may run when it may not. -->
          <ul class="perms">
            <li [class.granted]="engine().provisions">
              provisioning {{ engine().provisions ? 'permitted' : 'not permitted' }}
            </li>
            <li [class.granted]="engine().lowers">
              lowering a lease {{ engine().lowers ? 'permitted' : 'not permitted' }}
            </li>
          </ul>
          <!-- One line per pool, and the same fact its own region states. The
               loop is one, and what it is about to do differs by pool. -->
          <ul class="gaps">
            @for (gap of engine().gaps; track gap.pool) {
              <li>
                <span class="gap-pool">{{ gap.pool }}</span>
                <span class="gap-line" [class.short]="gap.short">{{ gap.line }}</span>
              </li>
            }
          </ul>
          <p class="cause">{{ engine().cause }}</p>
          <p class="activity">{{ engine().activity }}</p>
          <button type="button" class="jump" (click)="jump.emit('controls')">
            Change what is permitted
          </button>
        </section>

        @for (region of canvas().regions; track region.key) {
          <app-pool-region [region]="region" />
        }
      </div>

      <!-- Present so the totals reconcile against the hosts page, unconnected
           and dimmed so nobody reads them as capacity the target accounts for. -->
      <section class="strip" aria-label="Machines that are not the autoscaler’s">
        <h4>Not the autoscaler’s</h4>
        @for (group of canvas().strip; track group.hostClass) {
          <p class="strip-line"><strong>{{ group.label }}</strong> · {{ group.line }}</p>
          @if (group.cards.length > 0) {
            <ol class="machines">
              @for (card of group.cards; track card.key) {
                <li><app-topology-machine [card]="card" [compact]="true" /></li>
              }
            </ol>
          }
        }
        @if (canvas().strip.length === 0) {
          <p class="strip-line">Every machine standing is the autoscaler’s.</p>
        }
      </section>
    </div>

    @for (line of canvas().totals; track line) {
      <p class="totals">{{ line }}</p>
    }
    <p class="primer">
      A machine is one Compute Engine VM. Each has a fixed number of instance
      slots, and a device takes one slot per instance it runs. Each machine
      belongs to one pool, which is the set of bounds it is counted against.
    </p>
  `,
  styles: `
    :host {
      display: block;

      /* One token per SLOT, fixed across the canvas. This is what makes card
         area mean capacity. A per-machine width would make a 16-slot machine
         and a 3-slot one the same size and delete the point. */
      --cco-canvas-slot: 4.5rem;

      /* The palette, declared once here and inherited by the regions, the cards
         and the clusters. Every role carries two values and the 4.5:1 floor
         holds per scheme. An emerald that reads on near-black washes out on
         white, so the light value is its own decision rather than a tint of the
         dark one. */
      --cco-canvas-ground: var(--cco-surface-dim);
      --cco-canvas-card: var(--cco-surface);
      --cco-canvas-free: var(--cco-success);
      --cco-canvas-edge: var(--cco-warning);
      /* The one role the console palette has no counterpart for. It is a fill
         tint under full ink, never ink itself. */
      --cco-canvas-busy: light-dark(rgb(103 58 183), rgb(179 136 255));
      --cco-canvas-rail: var(--cco-text-disabled);
      /* Dropped entirely in light, because on a light ground a glow reads as a
         smear rather than a light source. */
      --cco-canvas-glow: light-dark(transparent, rgb(138 180 248 / 30%));
    }

    .canvas {
      display: grid;
      grid-template-columns: minmax(14rem, 22rem) 1fr;
      grid-template-areas:
        "unplaced owned"
        "strip    strip";
      gap: var(--cco-space-16);
      padding: var(--cco-space-16);
      border-radius: var(--cco-radius-lg);
      background: var(--cco-canvas-ground);
    }

    .unplaced {
      grid-area: unplaced;
      display: flex;
      flex-direction: column;
      align-items: start;
      gap: var(--cco-space-8);
    }

    /* The rail. One line for the whole drawing, with the engine and each pool
       region drawing a stub onto it. Ownership is drawn with a border instead
       of a measured path, nothing recomputes on resize, and the number of
       connectors grows with the pool count only. */
    .owned {
      grid-area: owned;
      display: grid;
      gap: var(--cco-space-24);
      padding-inline-start: var(--cco-space-24);
      border-inline-start: 2px solid var(--cco-canvas-rail);
    }

    .engine,
    app-pool-region {
      position: relative;
    }

    /* The stub onto the rail. */
    .engine::before,
    app-pool-region::before {
      content: "";
      position: absolute;
      inset-inline-start: calc(-1 * var(--cco-space-24));
      top: var(--cco-space-12);
      width: var(--cco-space-24);
      border-top: 2px solid var(--cco-canvas-rail);
    }

    .engine::before {
      border-top-color: var(--cco-accent);
    }

    /* Off is a configuration and not a fault, so the line breaks rather than
       reddening. A break reads as "not connected" before a word is read. */
    .canvas.mode-off .owned {
      border-inline-start-style: dashed;
    }

    .canvas.mode-off .engine::before,
    .canvas.mode-off app-pool-region::before {
      border-top-style: dashed;
    }

    .engine {
      align-self: start;
      justify-self: start;
      max-width: 32rem;
      padding: var(--cco-space-12) var(--cco-space-16);
      border: 1px solid var(--cco-accent);
      border-radius: var(--cco-radius-lg);
      background: var(--cco-canvas-card);
      box-shadow: 0 0 24px var(--cco-canvas-glow);
    }

    /* The single animation on the canvas, and the only thing that moves. A slow
       ring while the mode permits action. Compositor-only, and dead where the
       reader asked for less motion. */
    .ring {
      position: absolute;
      inset: -5px;
      border: 2px solid transparent;
      border-top-color: var(--cco-accent);
      border-radius: var(--cco-radius-lg);
      opacity: 0.6;
    }

    @media (prefers-reduced-motion: no-preference) {
      .engine.acting .ring {
        animation: cco-canvas-ring 9s linear infinite;
      }
    }

    @keyframes cco-canvas-ring {
      to {
        transform: rotate(1turn);
      }
    }

    h3 {
      margin: 0;
      font-size: var(--cco-text-title);
      font-weight: 600;
    }

    h4 {
      margin: 0 0 var(--cco-space-8);
      font-size: var(--cco-text-label);
      font-weight: 600;
      letter-spacing: 0.08em;
      text-transform: uppercase;
      color: var(--cco-text-secondary);
    }

    /* Never a mark without its word. The permission is spelled out and colour
       only agrees with it. */
    .perms {
      margin: var(--cco-space-8) 0;
      padding-inline-start: var(--cco-space-16);
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .perms .granted {
      color: var(--cco-text);
    }

    .gaps {
      margin: var(--cco-space-8) 0;
      padding: 0;
      list-style: none;
      font-family: var(--cco-font-mono);
      font-variant-numeric: tabular-nums;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .gaps li {
      display: flex;
      gap: var(--cco-space-8);
    }

    .gap-pool {
      min-width: 6rem;
      color: var(--cco-text);
    }

    .gap-line.short {
      color: var(--cco-canvas-edge);
    }

    .cause,
    .activity,
    .strip-line,
    .primer {
      margin: var(--cco-space-4) 0 0;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .cause {
      max-width: 60ch;
    }

    .activity,
    .totals {
      font-family: var(--cco-font-mono);
      font-variant-numeric: tabular-nums;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .jump {
      margin-top: var(--cco-space-8);
      padding: var(--cco-space-4) var(--cco-space-8);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-sm);
      background: none;
      font: inherit;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
      cursor: pointer;
    }

    .jump:hover,
    .jump:focus-visible {
      border-color: var(--cco-accent);
      color: var(--cco-text);
    }

    .machines {
      display: flex;
      flex-wrap: wrap;
      align-items: stretch;
      gap: var(--cco-space-12);
      margin: 0;
      padding: 0;
      list-style: none;
    }

    /* Contained rather than content-visibility. Skipping takes the strip out of
       the rendered tree while it is off screen, which is exactly what the
       accessibility scan and the browser suite then cannot see. */
    .strip {
      grid-area: strip;
      padding-top: var(--cco-space-12);
      border-top: 1px solid var(--cco-border);
      contain: content;
    }

    /* Dimmed by ink and treatment, never by opacity. Opacity lowers the
       contrast of every label under it and the scan is right to fail that. */
    .strip app-topology-machine {
      border-style: dotted;
      background: none;
    }

    .strip-line strong {
      color: var(--cco-text);
    }

    .totals,
    .primer {
      max-width: 78ch;
      margin-top: var(--cco-space-12);
    }

    /* Under roughly 900px the drawing becomes one column in reading order:
       unplaced, the autoscaler, the machines it owns. */
    @container page (max-width: 900px) {
      .canvas {
        grid-template-columns: 1fr;
        grid-template-areas:
          "unplaced"
          "owned"
          "strip";
      }
    }
  `,
})
export class TopologyCanvasComponent {
  readonly snapshot = input.required<AutoscalerSnapshot>();
  readonly jump = output<CanvasSection>();

  protected readonly canvas = computed(() => buildCanvas(this.snapshot()));
  protected readonly engine = computed(() => this.canvas().engine);
}
