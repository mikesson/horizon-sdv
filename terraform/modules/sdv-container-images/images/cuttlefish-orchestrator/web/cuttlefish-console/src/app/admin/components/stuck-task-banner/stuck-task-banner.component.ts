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

import {ChangeDetectionStrategy, Component, computed, inject, input, resource} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatIconModule} from '@angular/material/icon';
import {MatSnackBar} from '@angular/material/snack-bar';
import {RouterModule} from '@angular/router';
import {firstValueFrom} from 'rxjs';
import {fail} from '../../../shared/store/entity-ops';
import {TaskAdminApiService} from '../../services/task-admin-api.service';

/**
 * Inline stuck-cause banner for device/host detail pages. When a background
 * task for this resource dead-lettered, the resource is silently stuck, so this
 * says why and offers the retry right where the problem is visible.
 */
@Component({
  selector: 'app-stuck-task-banner',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatButtonModule, MatIconModule, RouterModule],
  template: `
    @if (failedTask(); as task) {
      <div class="stuck-banner">
        <mat-icon>report_problem</mat-icon>
        <div class="stuck-text">
          <span class="stuck-title">
            Background task failed: {{ task.type }}, attempt {{ task.attempts }}/{{ task.maxAttempts }}
          </span>
          <span class="stuck-error">{{ task.lastError }}</span>
        </div>
        <button type="button" mat-stroked-button (click)="retry()">
          <mat-icon>replay</mat-icon>
          Retry
        </button>
        <a mat-button routerLink="/admin/control-plane">Control Plane</a>
      </div>
    }
  `,
  styles: `
    .stuck-banner {
      display: flex;
      align-items: center;
      gap: 12px;
      padding: 12px 16px;
      margin-bottom: 16px;
      background: var(--cco-error-surface);
      border: 1px solid var(--cco-error-surface);
      border-radius: 8px;

      > mat-icon {
        color: var(--cco-error);
        flex-shrink: 0;
      }
    }

    .stuck-text {
      flex: 1;
      min-width: 0;
      display: flex;
      flex-direction: column;
      gap: 2px;
    }

    .stuck-title {
      font-size: 12px;
      font-weight: 500;
      color: var(--cco-error);
    }

    .stuck-error {
      font-size: 12px;
      color: var(--cco-text-secondary);
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
  `,
})
export class StuckTaskBannerComponent {
  private readonly taskAdminApi = inject(TaskAdminApiService);
  private readonly snackBar = inject(MatSnackBar);

  readonly resourceId = input.required<string>();

  private readonly failedTasks = resource({
    params: () => this.resourceId(),
    loader: ({params}) => firstValueFrom(this.taskAdminApi.listFailed(5, params)),
  });

  protected readonly failedTask = computed(() => {
    const tasks = this.failedTasks.hasValue() ? this.failedTasks.value() : [];
    return tasks.length > 0 ? tasks[0] : null;
  });

  protected retry(): void {
    const task = this.failedTask();
    if (!task) {
      return;
    }
    this.taskAdminApi.retry(task.id).subscribe({
      next: () => {
        this.snackBar.open(`Requeued ${task.type} task`, 'Close', {duration: 3000});
        this.failedTasks.reload();
      },
      error: err => fail(this.snackBar, 'Failed to retry task', err),
    });
  }
}
