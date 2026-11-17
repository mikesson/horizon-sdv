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

import {DatePipe, SlicePipe} from '@angular/common';
import {ChangeDetectionStrategy, Component, input, output} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatChipsModule} from '@angular/material/chips';
import {MatIconModule} from '@angular/material/icon';
import {MatMenuModule} from '@angular/material/menu';
import {MatTableModule} from '@angular/material/table';
import {MatTooltipModule} from '@angular/material/tooltip';
import {RouterModule} from '@angular/router';
import type {FailedTask} from '../../models/task-admin.model';
import {taskResourceRoute} from '../../models/task-admin.model';

/** Presentational dead-letter table; row actions are emitted to the container. */
@Component({
  selector: 'app-task-table',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    DatePipe,
    SlicePipe,
    MatButtonModule,
    MatChipsModule,
    MatIconModule,
    MatMenuModule,
    MatTableModule,
    MatTooltipModule,
    RouterModule,
  ],
  template: `
    <table mat-table [dataSource]="tasks()">
      <ng-container matColumnDef="type">
        <th mat-header-cell *matHeaderCellDef>Type</th>
        <td mat-cell *matCellDef="let task">
          <mat-chip class="task-type">{{ task.type }}</mat-chip>
        </td>
      </ng-container>

      <ng-container matColumnDef="resource">
        <th mat-header-cell *matHeaderCellDef>Resource</th>
        <td mat-cell *matCellDef="let task">
          @if (resourceRoute(task); as link) {
            <a [routerLink]="link" [matTooltip]="task.resourceId">
              {{ task.resourceId | slice: 0 : 8 }}
            </a>
          } @else {
            <span [matTooltip]="task.resourceId">{{ task.resourceId | slice: 0 : 8 }}</span>
          }
        </td>
      </ng-container>

      <ng-container matColumnDef="attempts">
        <th mat-header-cell *matHeaderCellDef>Attempts</th>
        <td mat-cell *matCellDef="let task">{{ task.attempts }}</td>
      </ng-container>

      <ng-container matColumnDef="error">
        <th mat-header-cell *matHeaderCellDef>Last error</th>
        <td mat-cell *matCellDef="let task">
          <span class="error-text" [matTooltip]="task.lastError">{{ task.lastError }}</span>
        </td>
      </ng-container>

      <ng-container matColumnDef="updated">
        <th mat-header-cell *matHeaderCellDef>Updated</th>
        <td mat-cell *matCellDef="let task">{{ task.updatedAt | date: 'MMM d, HH:mm:ss' }}</td>
      </ng-container>

      <ng-container matColumnDef="actions">
        <th mat-header-cell *matHeaderCellDef></th>
        <td mat-cell *matCellDef="let task">
          <button type="button"
            mat-icon-button
            [matMenuTriggerFor]="menu"
            aria-label="Task actions">
            <mat-icon>more_vert</mat-icon>
          </button>
          <mat-menu #menu="matMenu">
            <button type="button" mat-menu-item (click)="retryTask.emit(task)">
              <mat-icon>replay</mat-icon>
              <span>Retry</span>
            </button>
            <button type="button" mat-menu-item class="warn-item" (click)="deleteTask.emit(task)">
              <mat-icon>delete</mat-icon>
              <span>Delete</span>
            </button>
          </mat-menu>
        </td>
      </ng-container>

      <tr mat-header-row *matHeaderRowDef="displayedColumns"></tr>
      <tr mat-row *matRowDef="let row; columns: displayedColumns"></tr>
    </table>
  `,
  styles: `
    table {
      width: 100%;
    }

    .task-type {
      font-family: monospace;
      font-size: 12px;
    }

    a {
      color: var(--cco-accent);
      text-decoration: none;
      font-family: monospace;

      &:hover {
        text-decoration: underline;
      }
    }

    .error-text {
      display: inline-block;
      max-width: 420px;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      vertical-align: bottom;
      color: var(--cco-error);
    }

    .warn-item mat-icon {
      color: var(--cco-error);
    }
  `,
})
export class TaskTableComponent {
  readonly tasks = input.required<FailedTask[]>();

  readonly retryTask = output<FailedTask>();
  readonly deleteTask = output<FailedTask>();

  protected readonly displayedColumns = [
    'type',
    'resource',
    'attempts',
    'error',
    'updated',
    'actions',
  ];

  protected readonly resourceRoute = taskResourceRoute;
}
