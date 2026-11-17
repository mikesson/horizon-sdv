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

import {DatePipe, JsonPipe, SlicePipe} from '@angular/common';
import {ChangeDetectionStrategy, Component, input, signal} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatChipsModule} from '@angular/material/chips';
import {MatIconModule} from '@angular/material/icon';
import {MatTableModule} from '@angular/material/table';
import {MatTooltipModule} from '@angular/material/tooltip';
import {RouterModule} from '@angular/router';
import type {AuditEntry} from '../../models/audit.model';
import {auditResourceRoute} from '../../models/audit.model';

/**
 * Presentational audit-entry table with per-row expandable changes/metadata
 * (Material multiTemplateDataRows detail-row pattern).
 */
@Component({
  selector: 'app-activity-table',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    DatePipe,
    JsonPipe,
    SlicePipe,
    MatButtonModule,
    MatChipsModule,
    MatIconModule,
    MatTableModule,
    MatTooltipModule,
    RouterModule,
  ],
  template: `
    <table mat-table [dataSource]="entries()" multiTemplateDataRows>
      <ng-container matColumnDef="time">
        <th mat-header-cell *matHeaderCellDef>Time</th>
        <td mat-cell *matCellDef="let entry">{{ entry.timestamp | date: 'MMM d, HH:mm:ss' }}</td>
      </ng-container>

      <ng-container matColumnDef="actor">
        <th mat-header-cell *matHeaderCellDef>Actor</th>
        <td mat-cell *matCellDef="let entry">{{ entry.actorEmail || 'system' }}</td>
      </ng-container>

      <ng-container matColumnDef="action">
        <th mat-header-cell *matHeaderCellDef>Action</th>
        <td mat-cell *matCellDef="let entry">
          <mat-chip class="action-chip">{{ entry.action }}</mat-chip>
        </td>
      </ng-container>

      <ng-container matColumnDef="resource">
        <th mat-header-cell *matHeaderCellDef>Resource</th>
        <td mat-cell *matCellDef="let entry">
          @if (resourceRoute(entry); as link) {
            <a [routerLink]="link" [matTooltip]="entry.resourceId">
              {{ entry.resourceId | slice: 0 : 8 }}
            </a>
          } @else if (entry.resourceId) {
            <span [matTooltip]="entry.resourceId">{{ entry.resourceId | slice: 0 : 8 }}</span>
          } @else {
            <span class="muted">{{ entry.resourceType }}</span>
          }
        </td>
      </ng-container>

      <ng-container matColumnDef="expand">
        <th mat-header-cell *matHeaderCellDef aria-label="row details"></th>
        <td mat-cell *matCellDef="let entry">
          @if (hasDetails(entry)) {
            <button type="button"
              mat-icon-button
              [attr.aria-label]="expandedId() === entry.id ? 'Hide details' : 'Show details'"
              (click)="toggle(entry)">
              <mat-icon>{{ expandedId() === entry.id ? 'expand_less' : 'expand_more' }}</mat-icon>
            </button>
          }
        </td>
      </ng-container>

      <ng-container matColumnDef="details">
        <td mat-cell *matCellDef="let entry" [attr.colspan]="displayedColumns.length">
          @if (expandedId() === entry.id) {
            <div class="details-body">
              @if (objectKeys(entry.changes).length > 0) {
                <div class="details-block">
                  <span class="details-label">Changes</span>
                  <pre>{{ entry.changes | json }}</pre>
                </div>
              }
              @if (objectKeys(entry.metadata).length > 0) {
                <div class="details-block">
                  <span class="details-label">Metadata</span>
                  <pre>{{ entry.metadata | json }}</pre>
                </div>
              }
            </div>
          }
        </td>
      </ng-container>

      <tr mat-header-row *matHeaderRowDef="displayedColumns"></tr>
      <tr mat-row *matRowDef="let row; columns: displayedColumns" class="entry-row"></tr>
      <tr
        mat-row
        *matRowDef="let row; columns: ['details']"
        class="details-row"
        [class.collapsed]="expandedId() !== row.id"></tr>
    </table>
  `,
  styles: `
    table {
      width: 100%;
      background: transparent;
    }

    .action-chip {
      font-family: 'Roboto Mono', monospace;
      font-size: 11px;
    }

    .muted {
      color: var(--cco-text-secondary);
    }

    /* MDC gives every row a 52px min height; a collapsed detail row must
       vanish entirely, not render as a blank band under its entry. */
    tr.details-row.collapsed {
      height: 0;
    }

    .details-row.collapsed td {
      border-bottom-width: 0;
      padding: 0;
    }

    .details-body {
      display: flex;
      gap: 24px;
      padding: 8px 16px 16px;
    }

    .details-block {
      flex: 1;
      min-width: 0;
    }

    .details-label {
      font-size: 11px;
      font-weight: 500;
      text-transform: uppercase;
      color: var(--cco-text-secondary);
    }

    pre {
      margin: 4px 0 0;
      padding: 8px 12px;
      background: var(--cco-surface-variant);
      border-radius: 4px;
      font-size: 12px;
      overflow-x: auto;
    }
  `,
})
export class ActivityTableComponent {
  readonly entries = input.required<AuditEntry[]>();

  protected readonly expandedId = signal<string | null>(null);
  protected readonly displayedColumns = ['time', 'actor', 'action', 'resource', 'expand'];

  protected toggle(entry: AuditEntry): void {
    this.expandedId.update(id => (id === entry.id ? null : entry.id));
  }

  protected hasDetails(entry: AuditEntry): boolean {
    return Object.keys(entry.changes).length > 0 || Object.keys(entry.metadata).length > 0;
  }

  protected objectKeys(value: Record<string, unknown>): string[] {
    return Object.keys(value);
  }

  protected resourceRoute(entry: AuditEntry): string[] | null {
    return auditResourceRoute(entry);
  }
}
