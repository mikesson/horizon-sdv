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

import {ChangeDetectionStrategy, Component, input, signal} from '@angular/core';
import {MatIconModule} from '@angular/material/icon';

import type {InstanceReachability} from '../../models/reachability';

/**
 * What can address the selected instance, and where each connection ends.
 *
 * Three blocks, of which only Reachability expands. Its expanded state is the
 * containment drawing, which is why the drawing gets no section of its own.
 * Collapsed it is a list of channels; expanded it is the picture that explains
 * on the same wall. Drawing the same facts from the same selection twice would
 * be one question with two homes.
 *
 * Depth in the drawing is containment and nothing else. Horizontal distance
 * already means group-private scope everywhere else in this view, and a second
 * meaning for it on the same page would undo the grammar the rest of it rests
 * on.
 */
@Component({
  selector: 'app-reachability-panel',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatIconModule],
  template: `
    <section class="panel" aria-live="polite">
      <div class="block">
        <h3>Identity</h3>
        <dl>
          @for (row of reachability().identity; track row.label) {
            <div class="row">
              <dt>{{ row.label }}</dt>
              <dd>{{ row.value }}</dd>
            </div>
          }
        </dl>
      </div>

      <div class="block">
        <h3>
          <button
            type="button"
            class="block-toggle"
            [attr.aria-expanded]="expanded()"
            aria-controls="reachability-drawing"
            (click)="expanded.set(!expanded())"
          >
            <mat-icon class="chevron" [class.open]="expanded()">chevron_right</mat-icon>
            Reachability
          </button>
        </h3>

        <dl>
          @for (row of reachability().reach; track row.channel) {
            <div class="row">
              <dt>{{ row.channel }}</dt>
              <dd>{{ row.detail }}</dd>
            </div>
            @if (row.ridesInside; as tunnel) {
              <div class="row nested">
                <dt>inside</dt>
                <dd>{{ tunnel }}</dd>
              </div>
            }
          }
        </dl>

        <!-- The drawing is decoration: every fact in it is already a row above,
             which is what makes hiding it from assistive technology lossless
             rather than a shortcut. -->
        <div id="reachability-drawing">
          @if (expanded()) {
            <div class="drawing" aria-hidden="true">
              <div class="box">
                <span class="box-label">
                  <span class="box-kind">host</span>
                  <span class="box-name">{{ reachability().hostName }}</span>
                </span>

                <p class="terminates">
                  <span class="channel">CCO control plane</span>
                  <span class="dot"></span>
                  <span class="lead"></span>
                  <span class="wall">terminates here</span>
                </p>

                <div class="box instance-box">
                  <span class="box-label">
                    <span class="box-kind">instance</span>
                    <span class="box-name">{{ reachability().boxLabel }}</span>
                    @if (reachability().slotLabel; as slot) {
                      <span class="box-slot">{{ slot }}</span>
                    }
                  </span>

                  @for (row of reachability().reach; track row.channel) {
                    <p class="terminates">
                      <span class="channel">{{ row.channel }}</span>
                      <span class="dot"></span>
                      <span class="lead"></span>
                      <span class="wall">terminates here</span>
                    </p>

                    @if (row.ridesInside; as tunnel) {
                      <p class="inside">{{ tunnel }} rides inside this connection</p>
                    }

                    @if (row.tracksInside; as tracks) {
                      <p class="inside">display tracks, multiplexed in one connection:</p>
                      <ul class="tracks">
                        @for (screen of tracks; track screen.label) {
                          <li>
                            <span class="track-label">{{ screen.label }}</span>
                            <span>{{ screen.size }}</span>
                          </li>
                        }
                      </ul>
                    }
                  }
                </div>
              </div>
            </div>
          }
        </div>
      </div>

      <div class="block">
        <h3>Shares</h3>
        <dl class="shares">
          @for (row of reachability().shares; track row.label) {
            <div class="row">
              <dt>{{ row.label }}</dt>
              <dd>
                <span class="share-value">{{ row.value }}</span>
                <span class="share-with">{{ row.with }}</span>
              </dd>
            </div>
          }
        </dl>
      </div>
    </section>
  `,
  styles: `
    :host {
      display: block;
    }

    .panel {
      display: flex;
      flex-direction: column;
      gap: var(--cco-space-16);
      border: 1px solid var(--cco-card-border);
      border-radius: var(--cco-radius-lg);
      background: var(--cco-surface);
      padding: var(--cco-space-16);
    }

    h3 {
      margin: 0;
      font-size: var(--cco-text-label);
      font-weight: 500;
      letter-spacing: 0.06em;
      text-transform: uppercase;
      color: var(--cco-text-secondary);
    }

    .block-toggle {
      display: flex;
      align-items: center;
      gap: var(--cco-space-4);
      border: 0;
      background: none;
      padding: 0;
      font: inherit;
      letter-spacing: inherit;
      text-transform: inherit;
      cursor: pointer;
      color: inherit;
    }

    .chevron {
      transition: transform var(--cco-duration-fast);
      color: var(--cco-text-secondary);
    }

    .chevron.open {
      transform: rotate(90deg);
    }

    @media (prefers-reduced-motion: reduce) {
      .chevron {
        transition: none;
      }
    }

    dl {
      margin: var(--cco-space-8) 0 0;
    }

    .row {
      display: flex;
      gap: var(--cco-space-8);
      padding-block: 2px;
    }

    dt {
      flex: 0 0 6.5rem;
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
    }

    dd {
      margin: 0;
      font-family: var(--cco-font-mono);
      font-variant-numeric: tabular-nums;
      font-size: var(--cco-text-sm);
      color: var(--cco-text);
    }

    /* A tunnel is not a second channel, so it is indented under the one it
       rides in rather than aligned beside it. */
    .row.nested dd {
      padding-inline-start: var(--cco-space-16);
      color: var(--cco-text-secondary);
    }

    /* Identifiers are mono everywhere in this view; the company a medium keeps
       is prose, so it takes the page's own face rather than a second identifier
       font. */
    .shares dd {
      font-family: inherit;
    }

    .share-value {
      font-family: var(--cco-font-mono);
      font-variant-numeric: tabular-nums;
    }

    .share-with {
      display: block;
      color: var(--cco-text-secondary);
    }

    .drawing {
      margin-block-start: var(--cco-space-8);
    }

    /* Nesting IS the message: one box inside another says the inner thing is
       contained by the outer one, which no arrow between them could claim as
       cheaply. */
    .box {
      position: relative;
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-sm);
      padding: var(--cco-space-16) var(--cco-space-8) var(--cco-space-8);
    }

    .instance-box {
      margin: var(--cco-space-8) 0 0 var(--cco-space-16);
    }

    .box-label {
      position: absolute;
      inset-block-start: calc(-1 * var(--cco-space-8));
      inset-inline-start: var(--cco-space-8);
      display: flex;
      align-items: baseline;
      gap: var(--cco-space-8);
      background: var(--cco-surface);
      padding-inline: var(--cco-space-4);
    }

    .box-kind {
      font-size: var(--cco-text-label);
      letter-spacing: 0.06em;
      text-transform: uppercase;
      color: var(--cco-text-secondary);
    }

    .box-name,
    .box-slot {
      font-family: var(--cco-font-mono);
      font-size: var(--cco-text-sm);
      color: var(--cco-text);
    }

    .box-slot {
      color: var(--cco-text-secondary);
    }

    /* adb and WebRTC end at the same wall. There is one peer connection per
       session, with displays as tracks inside it, so both terminators sit at
       one depth and neither reaches further than the other. */
    .terminates {
      display: flex;
      align-items: center;
      gap: var(--cco-space-8);
      margin: var(--cco-space-8) 0 0;
      font-size: var(--cco-text-sm);
    }

    .dot {
      flex: 0 0 auto;
      width: 7px;
      height: 7px;
      border-radius: 50%;
      background: var(--cco-text-secondary);
    }

    .channel {
      font-family: var(--cco-font-mono);
      color: var(--cco-text);
    }

    .lead {
      flex: 1;
      align-self: center;
      border-block-start: 1px dashed var(--cco-border);
    }

    .wall {
      flex: 0 0 auto;
      color: var(--cco-text-secondary);
    }

    .inside {
      margin: 2px 0 0 var(--cco-space-24);
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
    }

    .tracks {
      margin: 2px 0 0 var(--cco-space-24);
      padding-inline-start: var(--cco-space-16);
      list-style: none;
    }

    .tracks li {
      display: flex;
      gap: var(--cco-space-8);
      font-family: var(--cco-font-mono);
      font-variant-numeric: tabular-nums;
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
    }

    .track-label {
      flex: 0 0 5.5rem;
    }
  `,
})
export class ReachabilityPanelComponent {
  readonly reachability = input.required<InstanceReachability>();

  protected readonly expanded = signal(false);
}
