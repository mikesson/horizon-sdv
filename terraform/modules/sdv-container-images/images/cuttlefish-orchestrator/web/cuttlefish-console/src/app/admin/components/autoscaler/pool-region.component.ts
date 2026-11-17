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

import {ChangeDetectionStrategy, Component, computed, input, signal} from '@angular/core';
import type {PoolRegion} from './topology-canvas.model';
import {TopologyMachineComponent} from './topology-machine.component';

/**
 * One pool, drawn as a labelled region of the rail: what it is held to, what is
 * limiting it, the shapes it builds, and the machines standing in it.
 *
 * A region and never a tab. The fact pools create is a cross-pool one (one
 * starving while another idles) and a switcher shows one pool at a time, which
 * hides exactly that.
 *
 * Nothing here derives anything. The region arrives composed, so the rules that
 * cost defects stay assertable as arithmetic in the canvas model.
 */
@Component({
  selector: 'app-pool-region',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [TopologyMachineComponent],
  host: {
    '[attr.data-pool]': 'region().pool',
  },
  template: `
    <div class="head">
      <h4>{{ region().pool }}</h4>
      <span class="family">{{ region().family }}</span>
      <p class="target">
        <strong>{{ region().target }}</strong>
        <span>machines wanted, from {{ region().from }}</span>
      </p>
      <p class="contracts">{{ region().contracts }}</p>
      <p class="gap" [class.short]="region().short">{{ region().gap }}</p>
      @if (region().cause) {
        <p class="cause">{{ region().cause }}</p>
      }
      <!-- What this pool's machines are built as, when an order says. Struck AND
           dimmed for a shape with no zone left: a line through a name is a mark,
           and the sentence under it is what carries it in words. -->
      @if (region().ladder; as ladder) {
        <p class="shapes">
          @for (step of ladder.steps; track step.shape) {
            @if (!$first) {
              <span class="arrow" aria-hidden="true">→</span>
            }
            <span class="step" [class.out]="step.out" [class.building]="step.building">
              {{ step.shape }}
            </span>
          }
        </p>
        <p class="shapes-note">{{ ladder.sentence }}</p>
      }
    </div>

    @if (region().empty) {
      <p class="empty">{{ region().empty }}</p>
    }

    @for (bank of region().banks; track bank.key) {
      <section class="bank" [attr.data-bank]="bank.key" [attr.aria-label]="bank.label">
        <h5>{{ bank.heading }}</h5>
        @if (bank.cards.length === 0 && bank.clusters.length === 0) {
          <p class="none">none</p>
        }
        <ol class="machines">
          @for (card of bank.cards; track card.key) {
            <li>
              <app-topology-machine [card]="card" [compact]="compact()" />
            </li>
          }
          @for (cluster of bank.clusters; track cluster.key) {
            <li>
              <button
                type="button"
                class="cluster"
                [attr.aria-expanded]="open().includes(cluster.key)"
                (click)="toggle(cluster.key)">
                <span class="count">{{ cluster.count }}</span>
                <span class="word">{{ cluster.word }}</span>
                @if (cluster.detail) {
                  <span class="detail">{{ cluster.detail }}</span>
                }
              </button>
              @if (open().includes(cluster.key)) {
                <ol class="members">
                  @for (member of cluster.members; track member.key) {
                    <li><app-topology-machine [card]="member" /></li>
                  }
                </ol>
              }
            </li>
          }
        </ol>
      </section>
    }
  `,
  styles: `
    :host {
      display: block;
      padding: var(--cco-space-12) var(--cco-space-16) var(--cco-space-16);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-lg);
    }

    .head {
      display: grid;
      grid-template-columns: auto 1fr;
      gap: 2px var(--cco-space-8);
      margin-bottom: var(--cco-space-12);
    }

    h4 {
      margin: 0;
      font-family: var(--cco-font-mono);
      font-size: var(--cco-text-body);
      font-weight: 600;
      color: var(--cco-text);
    }

    h5 {
      margin: 0 0 var(--cco-space-8);
      font-size: var(--cco-text-label);
      font-weight: 600;
      letter-spacing: 0.08em;
      text-transform: uppercase;
      color: var(--cco-text-secondary);
    }

    .family {
      align-self: center;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .target,
    .contracts,
    .gap,
    .cause,
    .shapes,
    .shapes-note,
    .none,
    .empty {
      grid-column: 1 / -1;
      margin: var(--cco-space-4) 0 0;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .target {
      display: flex;
      align-items: baseline;
      gap: var(--cco-space-8);
      font-size: var(--cco-text-body);
    }

    .target strong {
      font-size: var(--cco-text-title);
      font-variant-numeric: tabular-nums;
      color: var(--cco-text);
    }

    .gap {
      font-family: var(--cco-font-mono);
      font-variant-numeric: tabular-nums;
      color: var(--cco-text);
    }

    /* A shortfall is what this region is about; the word carries it and the
       colour only agrees. */
    .gap.short {
      color: var(--cco-canvas-edge);
    }

    .cause,
    .empty {
      max-width: 60ch;
    }

    .shapes {
      display: flex;
      flex-wrap: wrap;
      align-items: baseline;
      gap: var(--cco-space-4);
      font-family: var(--cco-font-mono);
    }

    .step {
      color: var(--cco-text);
    }

    /* Dimmed by ink rather than by opacity: opacity lowers the contrast of the
       label under it and the scan is right to fail that. */
    .step.out {
      color: var(--cco-text-secondary);
      text-decoration: line-through;
    }

    .step.building {
      color: var(--cco-canvas-free);
    }

    .arrow {
      color: var(--cco-text-secondary);
    }

    .bank + .bank {
      margin-top: var(--cco-space-16);
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

    .machines li {
      display: flex;
      flex-direction: column;
      gap: var(--cco-space-8);
    }

    .cluster {
      display: flex;
      flex-direction: column;
      gap: 2px;
      padding: var(--cco-space-12);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-lg);
      background: var(--cco-canvas-card);
      font: inherit;
      text-align: start;
      color: var(--cco-text-secondary);
      cursor: pointer;
    }

    .cluster:hover,
    .cluster:focus-visible {
      border-color: var(--cco-accent);
    }

    .count {
      font-size: var(--cco-text-title-lg);
      font-weight: 600;
      font-variant-numeric: tabular-nums;
      line-height: 1.1;
      color: var(--cco-text);
    }

    .word,
    .detail {
      font-size: var(--cco-text-label);
    }

    .members {
      display: flex;
      flex-wrap: wrap;
      gap: var(--cco-space-12);
      margin: 0;
      padding: 0;
      list-style: none;
    }
  `,
})
export class PoolRegionComponent {
  readonly region = input.required<PoolRegion>();

  protected readonly compact = computed(() => this.region().tier === 'compact');

  /** Which clusters are expanded. Cleared by nothing: a poll re-renders in place. */
  protected readonly open = signal<readonly string[]>([]);

  protected toggle(key: string): void {
    this.open.update(keys => (keys.includes(key) ? keys.filter(k => k !== key) : [...keys, key]));
  }
}
