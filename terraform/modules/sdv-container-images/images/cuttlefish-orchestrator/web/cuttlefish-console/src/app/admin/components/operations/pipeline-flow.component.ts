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
import type {EdgeActivity} from '../../store/control-plane.signal-store';

/** Everything the flow diagram renders, precomputed by the container. */
export interface PipelineModel {
  pending: number;
  oldestPendingAgeSeconds: number;
  inFlight: number;
  workersPerInstance: number;
  knownInstances: number;
  retrying: number;
  deadLettered: number;
  /** Whether the one background-plane lease is currently held by an instance. */
  planeLed: boolean;
  activity: EdgeActivity;
}

/** Sections of the page a node click navigates to. */
export type PipelineSection = 'queue' | 'workers' | 'reconcilers' | 'dlq';

/**
 * The control-plane pipeline: intent and reconcilers feed a durable queue,
 * workers drain it, outcomes complete / retry / dead-letter. Fixed topology,
 * hand-laid SVG; edges animate only when the last poll saw work move along
 * them.
 */
@Component({
  selector: 'app-pipeline-flow',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <!-- A group rather than an img. Three of the nodes below are buttons, and
         an image role collapses the whole diagram into one leaf that cannot
         hold them, which left the controls unreachable to a screen reader
         while they were still in the tab order. -->
    <svg viewBox="0 0 960 292" role="group"
      aria-label="Control-plane pipeline: intent and reconcilers enqueue tasks, workers claim them, tasks complete, retry, or dead-letter">
      <defs>
        <marker id="arrow" viewBox="0 0 8 8" refX="7" refY="4"
          markerWidth="7" markerHeight="7" orient="auto-start-reverse">
          <path d="M 0 0 L 8 4 L 0 8 z" fill="context-stroke" />
        </marker>
      </defs>

      <!-- Edges under the nodes -->
      <path class="edge" [class.active]="m().activity.intent"
        d="M 180 106 C 220 106, 220 122, 258 126" marker-end="url(#arrow)">
        <title>User intent: the API writes the resource row and its task in one Spanner transaction (transactional outbox)</title>
      </path>
      <path class="edge" [class.active]="m().activity.reconcile"
        d="M 180 202 C 220 202, 220 182, 258 172" marker-end="url(#arrow)">
        <title>Detectors enqueue keyed reconcile tasks, at most one pending per resource; duplicates merge</title>
      </path>
      <path class="edge" [class.active]="m().activity.claim"
        d="M 442 148 L 516 148" marker-end="url(#arrow)">
        <title>Claim: serializable SELECT … FOR UPDATE; any instance may claim, leases fence the outcome</title>
      </path>
      <path class="edge" [class.active]="m().activity.complete"
        d="M 702 122 C 740 110, 740 84, 776 74" marker-end="url(#arrow)">
        <title>Success deletes the row, so the queue only holds live work</title>
      </path>
      <path class="edge warn" [class.active]="m().activity.retry"
        d="M 702 148 L 776 148" marker-end="url(#arrow)">
        <title>A failed attempt goes back to pending with exponential backoff (10 attempts by default)</title>
      </path>
      <path class="edge danger" [class.active]="m().activity.dead"
        d="M 702 174 C 740 186, 740 212, 776 222" marker-end="url(#arrow)">
        <title>Attempts exhausted: dead-lettered for operator review below</title>
      </path>
      <path class="edge warn loop" [class.active]="m().activity.retry"
        d="M 830 128 C 790 44, 460 30, 356 100" marker-end="url(#arrow)">
        <title>Backoff elapses and the task becomes claimable again</title>
      </path>

      <!-- Sources -->
      <g class="node passive">
        <rect x="24" y="74" width="156" height="60" rx="10" />
        <text class="node-title" x="102" y="99">User intent</text>
        <text class="node-sub" x="102" y="118">create · delete · retry</text>
        <title>API requests enqueue transition tasks in the same transaction that writes the resource</title>
      </g>
      <g class="node" [class.warn]="!m().planeLed"
        role="button" tabindex="0" (click)="sectionClick.emit('reconcilers')"
        (keydown.enter)="sectionClick.emit('reconcilers')">
        <rect x="24" y="170" width="156" height="60" rx="10" />
        <text class="node-title" x="102" y="195">Reconcilers</text>
        <text class="node-sub" x="102" y="214">{{ m().planeLed ? 'leading' : 'no leader' }}</text>
        <title>Periodic loops, all gated by one leadership: they detect drift and enqueue corrective tasks, with details below</title>
      </g>

      <!-- Queue -->
      <g class="node" [class.warn]="queueWarn()"
        role="button" tabindex="0" (click)="sectionClick.emit('queue')"
        (keydown.enter)="sectionClick.emit('queue')">
        <rect x="258" y="96" width="184" height="104" rx="10" />
        <text class="node-title" x="350" y="122">Queue</text>
        <text class="node-stat" x="350" y="156">{{ m().pending }}</text>
        <text class="node-sub" x="350" y="182">{{ queueSub() }}</text>
        <title>Durable task queue in Spanner: one row per task, claimed by shard, delete-on-complete</title>
      </g>

      <!-- Workers -->
      <g class="node"
        role="button" tabindex="0" (click)="sectionClick.emit('workers')"
        (keydown.enter)="sectionClick.emit('workers')">
        <rect x="518" y="96" width="184" height="104" rx="10" />
        <text class="node-title" x="610" y="122">Workers</text>
        <text class="node-stat" x="610" y="156">{{ m().inFlight }}</text>
        <text class="node-sub" x="610" y="182">{{ workersSub() }}</text>
        <title>Worker pool on every instance; a 35-minute lease fences each delivery, the sweeper reclaims crashes</title>
      </g>

      <!-- Outcomes -->
      <g class="node outcome ok passive">
        <rect x="778" y="40" width="158" height="52" rx="10" />
        <text class="node-title" x="857" y="62">✓ Completed</text>
        <text class="node-sub" x="857" y="80">gone from the queue · see activity</text>
        <title>Success deletes the task row, so there is nothing to count here; the Activity page is the durable record</title>
      </g>
      <g class="node outcome passive" [class.warn]="m().retrying > 0">
        <rect x="778" y="122" width="158" height="52" rx="10" />
        <text class="node-title" x="857" y="144">↻ Retrying</text>
        <text class="node-sub" x="857" y="162">{{ m().retrying }} waiting out backoff</text>
        <title>Failed attempts waiting for their next try; the error and countdown are listed below</title>
      </g>
      <g class="node outcome" [class.danger]="m().deadLettered > 0"
        role="button" tabindex="0" (click)="sectionClick.emit('dlq')"
        (keydown.enter)="sectionClick.emit('dlq')">
        <rect x="778" y="196" width="158" height="52" rx="10" />
        <text class="node-title" x="857" y="218">✕ Dead-letter</text>
        <text class="node-sub" x="857" y="236">{{ dlqSub() }}</text>
        <title>Tasks that exhausted their retries; retry or delete them in the table below</title>
      </g>
    </svg>
    <p class="legend">
      Moving dashes mark edges that carried work in the last {{ pollSeconds() }}s ·
      amber is the retry path · red is the dead-letter path · click a node for details
    </p>
  `,
  styles: `
    :host {
      display: block;
    }

    svg {
      width: 88%;
      max-width: 960px;
      height: auto;
      display: block;
      margin: 0 auto;
    }

    .edge {
      fill: none;
      stroke: var(--cco-border);
      stroke-width: 1.5;
    }

    .edge.warn {
      stroke-dasharray: 7 7;
    }

    .edge.danger {
      stroke-dasharray: 7 7;
    }

    .edge.active {
      stroke: var(--cco-accent);
      stroke-dasharray: 7 7;
      animation: cco-flow 0.9s linear infinite;
    }

    .edge.warn.active {
      stroke: var(--cco-warning);
    }

    .edge.danger.active {
      stroke: var(--cco-error);
    }

    @keyframes cco-flow {
      to {
        stroke-dashoffset: -14;
      }
    }

    @media (prefers-reduced-motion: reduce) {
      .edge.active {
        animation: none;
      }
    }

    .node rect {
      fill: var(--cco-surface-variant);
      stroke: var(--cco-border);
      stroke-width: 1;
    }

    .node:not(.passive) {
      cursor: pointer;
    }

    .node:not(.passive):hover rect,
    .node:not(.passive):focus-visible rect {
      stroke: var(--cco-text-secondary);
    }

    .node:focus-visible {
      outline: none;
    }

    .node.warn rect {
      stroke: var(--cco-warning);
    }

    .node.danger rect {
      stroke: var(--cco-error);
    }

    .node.ok rect {
      stroke: var(--cco-border);
    }

    text {
      text-anchor: middle;
      fill: var(--cco-text);
    }

    .node-title {
      font-size: 14px;
      font-weight: 500;
    }

    .node-stat {
      font-size: 24px;
      font-weight: 600;
    }

    .node-sub {
      font-size: 11px;
      fill: var(--cco-text-secondary);
    }

    .node.warn .node-sub {
      fill: var(--cco-warning);
    }

    .node.danger .node-sub {
      fill: var(--cco-error);
    }

    .legend {
      margin: 4px 0 0;
      font-size: 11px;
      color: var(--cco-text-secondary);
      text-align: center;
    }
  `,
})
export class PipelineFlowComponent {
  readonly m = input.required<PipelineModel>();
  readonly pollSeconds = input.required<number>();

  readonly sectionClick = output<PipelineSection>();

  protected readonly queueWarn = computed(() => this.m().oldestPendingAgeSeconds > 120);

  protected readonly queueSub = computed(() => {
    const m = this.m();
    if (m.pending === 0) {
      return 'drained';
    }
    return `oldest waiting ${formatAge(m.oldestPendingAgeSeconds)}`;
  });

  protected readonly workersSub = computed(() => {
    const m = this.m();
    const slots = m.workersPerInstance * Math.max(1, m.knownInstances);
    if (slots > 0) {
      return `of up to ${slots} slots (${m.workersPerInstance}/instance)`;
    }
    return 'pool disabled';
  });

  protected readonly dlqSub = computed(() => {
    const n = this.m().deadLettered;
    return n === 0 ? 'empty' : `${n} need attention`;
  });
}

/** "42s" / "3m 12s" / "2h 5m". */
function formatAge(seconds: number): string {
  if (seconds < 60) {
    return `${seconds}s`;
  }
  if (seconds < 3600) {
    return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
  }
  return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`;
}
