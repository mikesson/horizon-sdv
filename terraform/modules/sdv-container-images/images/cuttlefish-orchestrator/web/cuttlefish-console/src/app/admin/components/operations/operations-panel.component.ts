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
import {MatIconModule} from '@angular/material/icon';
import {MatTooltipModule} from '@angular/material/tooltip';
import {RouterModule} from '@angular/router';
import type {Operation} from '../../../devices/models/operation.model';
import {operationVerbLabel} from '../../../devices/models/operation.model';
import {EmptyStateComponent} from '../../../shared/components/empty-state/empty-state.component';

interface OperationRow {
  id: string;
  verb: string;
  resourceId: string;
  /** Router link to the resource, or null when the type has no detail page. */
  link: string[] | null;
  attempt: string;
  lastError: string;
  ago: string;
}

/**
 * The fleet's in-flight async verbs (restarts, powerwashes, snapshots,
 * bugreports) which were otherwise visible only one device at a time.
 *
 * Live operations only. A settled one is answered by its resource's own
 * detail page, and mixing recently-finished rows in would make the panel a
 * second, worse activity log next to the one already on this page.
 */
@Component({
  selector: 'app-operations-panel',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [RouterModule, MatIconModule, MatTooltipModule, EmptyStateComponent],
  template: `
    <div class="panel">
      <h3>
        In-flight operations
        @if (rows().length > 0) {
          <span class="count">{{ rows().length }}</span>
        }
      </h3>

      @if (error(); as message) {
        <p class="panel-error">
          <mat-icon>error_outline</mat-icon>
          {{ message }}
        </p>
      } @else if (rows().length === 0) {
        <app-empty-state variant="compact" icon="done_all" message="Nothing running right now" />
      } @else {
        @for (row of rows(); track row.id) {
          <div class="row" [class.retrying]="row.attempt">
            <div class="row-head">
              <span class="verb">{{ row.verb }}</span>
              @if (row.link; as link) {
                <a class="resource" [routerLink]="link">{{ row.resourceId }}</a>
              } @else {
                <span class="resource plain">{{ row.resourceId }}</span>
              }
              <span class="ago">{{ row.ago }}</span>
            </div>
            @if (row.attempt) {
              <div class="row-detail">
                <mat-icon [matTooltip]="row.lastError || 'Retrying'">replay</mat-icon>
                <span>{{ row.attempt }}</span>
                @if (row.lastError) {
                  <span class="last-error">{{ row.lastError }}</span>
                }
              </div>
            }
          </div>
        }
      }
    </div>
  `,
  styles: `
    .panel {
      background: var(--cco-surface);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-lg);
      padding: var(--cco-space-16);
    }

    h3 {
      display: flex;
      align-items: center;
      gap: var(--cco-space-8);
      margin: 0 0 var(--cco-space-12);
      font-size: var(--cco-text-body);
      font-weight: 500;
    }

    .count {
      font-size: var(--cco-text-label);
      padding: 2px var(--cco-space-8);
      border-radius: var(--cco-radius-pill);
      background: var(--cco-accent-surface);
      color: var(--cco-accent);
    }

    .panel-error {
      display: flex;
      align-items: center;
      gap: var(--cco-space-8);
      margin: 0;
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
    }

    .panel-error mat-icon {
      color: var(--cco-warning);
      font-size: var(--cco-icon-md);
      width: var(--cco-icon-md);
      height: var(--cco-icon-md);
    }

    .row {
      padding: var(--cco-space-8) 0;
      border-top: 1px solid var(--cco-border);
    }

    .row:first-of-type {
      border-top: none;
    }

    .row-head {
      display: flex;
      align-items: baseline;
      gap: var(--cco-space-8);
      font-size: var(--cco-text-sm);
    }

    .verb {
      font-weight: 500;
    }

    .resource {
      flex: 1;
      font-family: monospace;
      font-size: var(--cco-text-label);
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .resource.plain {
      color: var(--cco-text-secondary);
    }

    .ago {
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-label);
      white-space: nowrap;
    }

    .row-detail {
      display: flex;
      align-items: center;
      gap: var(--cco-space-4);
      margin-top: var(--cco-space-4);
      font-size: var(--cco-text-label);
      color: var(--cco-warning);
    }

    .row-detail mat-icon {
      font-size: var(--cco-icon-xs);
      width: var(--cco-icon-xs);
      height: var(--cco-icon-xs);
    }

    .last-error {
      color: var(--cco-text-secondary);
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
  `,
})
export class OperationsPanelComponent {
  readonly operations = input.required<Operation[]>();
  /** Server time of the snapshot; ages are computed against it, not the client. */
  readonly serverNow = input.required<Date>();
  /** Set when the operations leg of the poll failed; the rest of the page is fine. */
  readonly error = input<string | null>(null);

  protected readonly rows = computed<OperationRow[]>(() => {
    const nowMs = this.serverNow().getTime();
    return this.operations().map(op => ({
      id: op.id,
      verb: operationVerbLabel(op),
      resourceId: op.metadata.resourceId,
      link: resourceLink(op.metadata.resourceType, op.metadata.resourceId),
      // attempt 1 is the first delivery, so it is not yet a retry.
      attempt: (op.metadata.attempt ?? 0) > 1 ? `attempt ${op.metadata.attempt}` : '',
      lastError: op.metadata.lastError ?? '',
      ago: formatAgo(nowMs - op.metadata.createTime.getTime()),
    }));
  });
}

/** Only devices and hosts have detail pages worth deep-linking to. */
function resourceLink(resourceType: string, resourceId: string): string[] | null {
  if (resourceType === 'device') {
    return ['/devices', resourceId];
  }
  if (resourceType === 'host') {
    return ['/hosts', resourceId];
  }
  return null;
}

/** "just now" / "42s ago" / "5m ago" / "3h ago" / "2d ago". */
function formatAgo(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 10) {
    return 'just now';
  }
  if (s < 60) {
    return `${s}s ago`;
  }
  if (s < 3600) {
    return `${Math.floor(s / 60)}m ago`;
  }
  if (s < 86400) {
    return `${Math.floor(s / 3600)}h ago`;
  }
  return `${Math.floor(s / 86400)}d ago`;
}
