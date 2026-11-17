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
import {toSignal} from '@angular/core/rxjs-interop';
import {MatIconModule} from '@angular/material/icon';
import {MatTooltipModule} from '@angular/material/tooltip';
import {RouterModule} from '@angular/router';
import {interval, map} from 'rxjs';
import type {LiveTask} from '../../models/control-plane.model';
import {isResync, isRetrying} from '../../models/control-plane.model';
import {taskResourceRoute} from '../../models/task-admin.model';

interface WorkRow {
  task: LiveTask;
  typeLabel: string;
  route: string[] | null;
  /** "in 42s" countdown, "ready now", or lease time left for in-flight rows. */
  timing: string;
  retrying: boolean;
  resync: boolean;
}

/**
 * The queue's current working set: tasks held by workers, and pending tasks
 * with their eligibility countdowns. Countdowns tick locally between polls,
 * against server time (the store's clock offset).
 */
@Component({
  selector: 'app-live-work',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatIconModule, MatTooltipModule, RouterModule],
  template: `
    <div class="columns">
      <section class="panel">
        <h3><mat-icon>bolt</mat-icon> In flight ({{ inFlightRows().length }})</h3>
        @if (inFlightRows().length === 0) {
          <p class="empty">No task is being worked on right now.</p>
        } @else {
          @for (row of inFlightRows(); track row.task.id) {
            <div class="row">
              <span class="type" [matTooltip]="row.task.type">{{ row.typeLabel }}</span>
              @if (row.route; as link) {
                <a class="resource" [routerLink]="link" [matTooltip]="row.task.resourceId">
                  {{ row.task.resourceId.slice(0, 8) }}
                </a>
              } @else {
                <span class="resource plain">{{ row.task.resourceId.slice(0, 8) || '-' }}</span>
              }
              @if (row.task.attempts > 1) {
                <span class="chip warn"
                  [matTooltip]="row.task.lastError || 'earlier attempts failed'">
                  attempt {{ row.task.attempts }}/{{ row.task.maxAttempts }}
                </span>
              }
              @if (row.resync) {
                <span class="chip muted" matTooltip="Reconciler-enqueued; claims behind user intent">resync</span>
              }
              <span class="timing">{{ row.timing }}</span>
            </div>
          }
        }
      </section>

      <section class="panel">
        <h3><mat-icon>schedule</mat-icon> Waiting ({{ waitingRows().length }})</h3>
        @if (waitingRows().length === 0) {
          <p class="empty">Nothing is waiting. The queue is drained.</p>
        } @else {
          @for (row of waitingRows(); track row.task.id) {
            <div class="row">
              <span class="type" [matTooltip]="row.task.type">{{ row.typeLabel }}</span>
              @if (row.route; as link) {
                <a class="resource" [routerLink]="link" [matTooltip]="row.task.resourceId">
                  {{ row.task.resourceId.slice(0, 8) }}
                </a>
              } @else {
                <span class="resource plain">{{ row.task.resourceId.slice(0, 8) || '-' }}</span>
              }
              @if (row.retrying) {
                <span class="chip warn"
                  [matTooltip]="row.task.lastError || 'earlier attempts failed'">
                  retry {{ row.task.attempts }}/{{ row.task.maxAttempts }}
                </span>
              }
              @if (row.resync) {
                <span class="chip muted" matTooltip="Reconciler-enqueued; claims behind user intent">resync</span>
              }
              <span class="timing">{{ row.timing }}</span>
            </div>
          }
        }
      </section>
    </div>
  `,
  styles: `
    .columns {
      display: grid;
      grid-template-columns: 1fr 1fr;
      gap: 16px;
    }

    @container page (max-width: 900px) {
      .columns {
        grid-template-columns: 1fr;
      }
    }

    .panel {
      background: var(--cco-surface);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-lg);
      padding: 16px 16px;
    }

    h3 {
      display: flex;
      align-items: center;
      gap: 8px;
      margin: 0 0 12px;
      font-size: 12px;
      font-weight: 500;
      color: var(--cco-text-secondary);

      mat-icon {
        font-size: 16px;
        width: 16px;
        height: 16px;
      }
    }

    .empty {
      margin: 0;
      font-size: 12px;
      color: var(--cco-text-secondary);
    }

    .row {
      display: flex;
      align-items: center;
      gap: 12px;
      padding: 8px 0;
      font-size: 12px;
      border-top: 1px solid var(--cco-divider);
    }

    .type {
      font-family: monospace;
      background: var(--cco-surface-variant);
      border-radius: var(--cco-radius-pill);
      padding: 2px 12px;
      white-space: nowrap;
    }

    .resource {
      font-family: monospace;
      color: var(--cco-accent);
      text-decoration: none;

      &:hover {
        text-decoration: underline;
      }
    }

    .resource.plain {
      color: var(--cco-text-secondary);
    }

    .chip {
      border-radius: var(--cco-radius-pill);
      padding: 1px 8px;
      font-size: 11px;
      white-space: nowrap;
    }

    .chip.warn {
      background: var(--cco-warning-surface);
      color: var(--cco-warning);
    }

    .chip.muted {
      background: var(--cco-surface-variant);
      color: var(--cco-text-secondary);
    }

    .timing {
      margin-left: auto;
      color: var(--cco-text-secondary);
      white-space: nowrap;
      font-variant-numeric: tabular-nums;
    }
  `,
})
export class LiveWorkComponent {
  readonly inFlight = input.required<LiveTask[]>();
  readonly waiting = input.required<LiveTask[]>();
  /** serverNow − clientNow, so countdowns follow server time. */
  readonly clockOffsetMs = input.required<number>();

  // Re-derives the row view-models every second so countdowns tick between
  // polls; the lists are tiny (worker slots × instances at most).
  private readonly tick = toSignal(interval(1000).pipe(map(i => i)), {initialValue: 0});

  protected readonly inFlightRows = computed<WorkRow[]>(() => {
    this.tick();
    const now = Date.now() + this.clockOffsetMs();
    return this.inFlight().map(task => ({
      ...baseRow(task),
      timing: task.leaseExpiry ? `lease ${formatSpan(task.leaseExpiry.getTime() - now)} left` : '',
    }));
  });

  protected readonly waitingRows = computed<WorkRow[]>(() => {
    this.tick();
    const now = Date.now() + this.clockOffsetMs();
    return this.waiting().map(task => {
      const wait = task.availableAt.getTime() - now;
      return {
        ...baseRow(task),
        timing: wait > 500 ? `ready in ${formatSpan(wait)}` : 'ready now',
      };
    });
  });
}

function baseRow(task: LiveTask): WorkRow {
  return {
    task,
    typeLabel: task.type.replace(/^(device|host)\./, '').replace(/_/g, ' '),
    route: taskResourceRoute(task),
    timing: '',
    retrying: isRetrying(task),
    resync: isResync(task),
  };
}

/** "42s" / "3m" / "2h 5m" from a millisecond span (clamped at zero). */
function formatSpan(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) {
    return `${s}s`;
  }
  if (s < 3600) {
    return `${Math.floor(s / 60)}m`;
  }
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
}
